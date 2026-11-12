package discover

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/relate"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

// managed loads a state that holds one network and one of its subnets.
func managed(t *testing.T) *graph.Graph {
	t.Helper()
	f, err := os.Open("testdata/managed.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g := graph.New()
	if err := tfstate.Load(g, "prod", f); err != nil {
		t.Fatal(err)
	}
	return g
}

// The query fixture is real `terraform query -json` output from Terraform
// 1.14 and the AWS provider 6.73, run against a stand-in for the EC2 API
// that holds one network, three subnets and two default security groups.
func query(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open("testdata/query.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestLoad(t *testing.T) {
	g := managed(t)
	res, err := Load(g, "cloud", query(t))
	if err != nil {
		t.Fatal(err)
	}
	want := Result{Found: 6, Matched: 2, Unmanaged: 2, Defaults: 2}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result %+v, want %+v", res, want)
	}

	var unmanaged []string
	for _, n := range g.Nodes() {
		if n.Status == graph.StatusUnmanaged {
			unmanaged = append(unmanaged, n.ID)
		}
	}
	wantIDs := []string{"cloud::aws_subnet.subnet-0aa000000000000b2@123456789012/us-east-1", "cloud::aws_subnet.subnet-0aa000000000000c3@123456789012/us-east-1"}
	if !reflect.DeepEqual(unmanaged, wantIDs) {
		t.Errorf("unmanaged: %q, want %q", unmanaged, wantIDs)
	}

	n, _ := g.Node("cloud::aws_subnet.subnet-0aa000000000000b2@123456789012/us-east-1")
	if n.Type != "aws_subnet" || n.Account != "123456789012" || n.Region != "us-east-1" ||
		n.CloudID != "arn:aws:ec2:us-east-1:123456789012:subnet/subnet-0aa000000000000b2" || n.Mode != graph.ModeManaged {
		t.Errorf("unmanaged subnet: %+v", n)
	}
	// Its attributes are there for working out where it sits, but a
	// listing does not say which are secret, so they are never written out.
	if n.Attributes["vpc_id"] != "vpc-0a1b2c3d4e5f60001" || !n.AttributesWithheld {
		t.Errorf("attributes: withheld %v, vpc_id %v", n.AttributesWithheld, n.Attributes["vpc_id"])
	}
	out, err := json.Marshal(n)
	if err != nil || strings.Contains(string(out), "cidr_block") {
		t.Errorf("attributes were serialized: %s (%v)", out, err)
	}

	// The managed resources are untouched.
	for _, id := range []string{"prod::aws_vpc.main", "prod::aws_subnet.private[0]"} {
		if m, _ := g.Node(id); m.Status != graph.StatusManaged {
			t.Errorf("%s: status %q", id, m.Status)
		}
	}
}

// An unmanaged subnet is placed in the managed network it belongs to, even
// though the two come from different sources.
func TestUnmanagedResourcesAreContained(t *testing.T) {
	g := managed(t)
	if _, err := Load(g, "cloud", query(t)); err != nil {
		t.Fatal(err)
	}
	relate.Containment(g)
	if !g.HasEdge(graph.Edge{From: "prod::aws_vpc.main", To: "cloud::aws_subnet.subnet-0aa000000000000b2@123456789012/us-east-1", Kind: graph.EdgeContains}) {
		t.Error("the unmanaged subnet is not inside its network")
	}
	// The other one belongs to a network that neither source holds.
	for _, e := range g.Edges() {
		if e.To == "cloud::aws_subnet.subnet-0aa000000000000c3@123456789012/us-east-1" {
			t.Errorf("unexpected edge to the subnet of an unknown network: %+v", e)
		}
	}
}

// With no state loaded, everything that is not a cloud default is unmanaged.
func TestLoadWithoutState(t *testing.T) {
	g := graph.New()
	res, err := Load(g, "cloud", query(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Result{Found: 6, Unmanaged: 4, Defaults: 2}); !reflect.DeepEqual(res, want) {
		t.Errorf("result %+v, want %+v", res, want)
	}
	vpc, ok := g.Node("cloud::aws_vpc.vpc-0a1b2c3d4e5f60001@123456789012/us-east-1")
	if !ok || vpc.Name != "main (vpc-0a1b2c3d4e5f60001)" {
		t.Errorf("network: %+v (found %v)", vpc, ok)
	}
}

func line(resourceType, display string, identity, object map[string]any) string {
	found := map[string]any{"address": "list." + resourceType + ".all", "display_name": display, "identity": identity, "resource_type": resourceType}
	if object != nil {
		found["resource_object"] = object
	}
	out, _ := json.Marshal(map[string]any{"@level": "info", "type": "list_resource_found", "list_resource_found": found})
	return string(out) + "\n"
}

func TestMatching(t *testing.T) {
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	g := graph.New()
	err := tfstate.Load(g, "s", strings.NewReader(`{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"aws_s3_bucket","name":"logs",`+aws+`,
	   "instances":[{"attributes":{"id":"logs","bucket":"logs","region":"us-east-1","arn":"arn:aws:s3:::logs"}}]},
	  {"mode":"managed","type":"aws_instance","name":"east",`+aws+`,
	   "instances":[{"attributes":{"id":"i-1","arn":"arn:aws:ec2:us-east-1:111111111111:instance/i-1"}}]},
	  {"mode":"data","type":"aws_vpc","name":"read",`+aws+`,"instances":[{"attributes":{"id":"vpc-9"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	here := map[string]any{"account_id": "111111111111", "region": "us-east-1"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range here {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	input := line("aws_s3_bucket", "logs", with(map[string]any{"bucket": "logs"}), nil) + // Identified by name, not id.
		line("aws_s3_bucket", "other", with(map[string]any{"bucket": "other"}), nil) +
		line("aws_instance", "i-1", with(map[string]any{"id": "i-1"}), nil) +
		// The same ID in another region or account is another resource.
		line("aws_instance", "i-1", map[string]any{"account_id": "111111111111", "region": "eu-west-1", "id": "i-1"}, nil) +
		line("aws_instance", "i-1", map[string]any{"account_id": "222222222222", "region": "us-east-1", "id": "i-1"}, nil) +
		// A data source reads a resource; it does not manage it.
		line("aws_vpc", "vpc-9", with(map[string]any{"id": "vpc-9"}), nil) +
		// Listed twice, by two list blocks.
		line("aws_s3_bucket", "other", with(map[string]any{"bucket": "other"}), nil) +
		// Cloud defaults of three kinds.
		line("aws_default_vpc", "vpc-0", with(map[string]any{"id": "vpc-0"}), nil) +
		line("aws_security_group", "default", with(map[string]any{"id": "sg-0"}), nil) +
		line("aws_iam_role", "AWSServiceRoleForX", map[string]any{"account_id": "111111111111", "name": "AWSServiceRoleForX"},
			map[string]any{"path": "/aws-service-role/x.amazonaws.com/"}) +
		// A group that only happens to be shown as "default" keeps its place.
		line("aws_security_group", "default", with(map[string]any{"id": "sg-1"}), map[string]any{"name": "web"})
	res, err := Load(g, "cloud", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Result{Found: 11, Matched: 2, Unmanaged: 5, Defaults: 3}); !reflect.DeepEqual(res, want) {
		t.Errorf("result %+v, want %+v", res, want)
	}
	var unmanaged []string
	for _, n := range g.Nodes() {
		if n.Status == graph.StatusUnmanaged {
			unmanaged = append(unmanaged, n.Address+"@"+n.Account+"/"+n.Region)
		}
	}
	want := []string{
		"aws_instance.i-1@111111111111/eu-west-1",
		"aws_instance.i-1@222222222222/us-east-1",
		"aws_s3_bucket.other@111111111111/us-east-1",
		"aws_security_group.sg-1@111111111111/us-east-1",
		"aws_vpc.vpc-9@111111111111/us-east-1",
	}
	if !reflect.DeepEqual(unmanaged, want) {
		t.Errorf("unmanaged:\n got %q\nwant %q", unmanaged, want)
	}
}

func TestErrorsAndFormat(t *testing.T) {
	g := graph.New()
	res, err := Load(g, "cloud", strings.NewReader(
		`{"@level":"info","type":"version","terraform":"1.14.0"}`+"\n"+
			`{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"Error listing resources","detail":"AccessDenied: not allowed to list roles"}}`+"\n"+
			`{"@level":"warn","type":"diagnostic","diagnostic":{"severity":"warning","summary":"Deprecated","detail":"x"}}`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "AccessDenied") {
		t.Errorf("errors: %q", res.Errors)
	}
	if _, err := Load(g, "cloud", strings.NewReader("not json")); err == nil {
		t.Error("garbage was accepted")
	}
	if _, err := Load(g, "", strings.NewReader("")); err == nil {
		t.Error("an empty source name was accepted")
	}

	real, err := os.ReadFile("testdata/query.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile("testdata/managed.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	if !IsQueryOutput(real) || IsQueryOutput(state) || IsQueryOutput([]byte("")) || IsQueryOutput([]byte(`{"format_version":"1.0"}`)) {
		t.Error("IsQueryOutput misjudged its input")
	}
}

// A list block returns 100 resources unless it sets a limit, and does not
// say that it stopped. Exactly 100 is reported, and such a block is not
// taken to have listed everything.
func TestCappedListing(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"@level":"info","type":"list_start","list_start":{"address":"list.aws_subnet.all","resource_type":"aws_subnet"}}` + "\n")
	for i := 0; i < 100; i++ {
		b.WriteString(line("aws_subnet", "s", map[string]any{"account_id": "111111111111", "region": "us-east-1", "id": "subnet-" + strings.Repeat("0", 3) + string(rune('a'+i%26)) + string(rune('a'+i/26))}, nil))
	}
	b.WriteString(`{"@level":"info","type":"list_complete","list_complete":{"address":"list.aws_subnet.all","resource_type":"aws_subnet","total":100}}` + "\n")

	g := graph.New()
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	if err := tfstate.Load(g, "s", strings.NewReader(`{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"aws_subnet","name":"beyond",`+aws+`,
	   "instances":[{"attributes":{"id":"subnet-not-in-the-first-100","arn":"arn:aws:ec2:us-east-1:111111111111:subnet/x"}}]}]}`)); err != nil {
		t.Fatal(err)
	}
	res, err := Load(g, "cloud", strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Capped, []string{"list.aws_subnet.all"}) || res.NotListed != 0 {
		t.Errorf("capped %q, not listed %d", res.Capped, res.NotListed)
	}
}

func TestNotListed(t *testing.T) {
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	subnet := func(name, id, region, account string) string {
		return `{"mode":"managed","type":"aws_subnet","name":"` + name + `",` + aws + `,"instances":[{"attributes":{"id":"` + id +
			`","arn":"arn:aws:ec2:` + region + `:` + account + `:subnet/` + id + `"}}]}`
	}
	g := graph.New()
	err := tfstate.Load(g, "s", strings.NewReader(`{"version":4,"terraform_version":"1.9.5","resources":[`+
		subnet("found", "subnet-0aa000000000000a1", "us-east-1", "123456789012")+`,`+
		subnet("gone", "subnet-gone", "us-east-1", "123456789012")+`,`+
		subnet("other_region", "subnet-west", "us-west-2", "123456789012")+`,`+
		subnet("other_account", "subnet-other", "us-east-1", "999999999999")+`,
	  {"mode":"managed","type":"aws_instance","name":"not_a_listed_type",`+aws+`,
	   "instances":[{"attributes":{"id":"i-1","arn":"arn:aws:ec2:us-east-1:123456789012:instance/i-1"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// The real listing: subnets, networks and security groups in one
	// account, with no region set, all found in us-east-1.
	res, err := Load(g, "cloud", query(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.NotListed != 1 {
		t.Errorf("not listed: %d, want 1", res.NotListed)
	}
	for name, want := range map[string]bool{"found": false, "gone": true, "other_region": false, "other_account": false} {
		if n, _ := g.Node("s::aws_subnet." + name); n.NotListed != want {
			t.Errorf("aws_subnet.%s: not listed = %v, want %v", name, n.NotListed, want)
		}
	}
	if n, _ := g.Node("s::aws_instance.not_a_listed_type"); n.NotListed {
		t.Error("a resource of a type the listing did not cover was marked")
	}

	// With an error in the listing nothing is concluded.
	g2 := graph.New()
	if err := tfstate.Load(g2, "s", strings.NewReader(`{"version":4,"terraform_version":"1.9.5","resources":[`+
		subnet("gone", "subnet-gone", "us-east-1", "123456789012")+`]}`)); err != nil {
		t.Fatal(err)
	}
	real, _ := os.ReadFile("testdata/query.jsonl")
	failing := string(real) + `{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"Error","detail":"AccessDenied"}}` + "\n"
	if res, err := Load(g2, "cloud", strings.NewReader(failing)); err != nil || res.NotListed != 0 {
		t.Errorf("with an error: not listed %d, err %v", res.NotListed, err)
	}
}

// Azure and Google identify a resource by names, not an ID, and call the
// account a subscription or a project.
func TestMatchingOtherClouds(t *testing.T) {
	g := graph.New()
	err := tfstate.Load(g, "s", strings.NewReader(`{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"azurerm_virtual_network","name":"hub","provider":"provider[\"registry.terraform.io/hashicorp/azurerm\"]",
	   "instances":[{"attributes":{"id":"/subscriptions/sub-1/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/hub",
	     "name":"hub","resource_group_name":"net","location":"westeurope"}}]},
	  {"mode":"managed","type":"google_compute_instance","name":"vm","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/acme/zones/us-central1-a/instances/vm","name":"vm","project":"acme","zone":"us-central1-a"}}]},
	  {"mode":"managed","type":"aws_ebs_encryption_by_default","name":"on","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
	   "instances":[{"attributes":{"id":"x","enabled":true,"region":"us-east-1"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	input := line("azurerm_virtual_network", "hub", map[string]any{"subscription_id": "sub-1", "resource_group_name": "net", "name": "hub"}, nil) +
		line("azurerm_virtual_network", "hub", map[string]any{"subscription_id": "sub-1", "resource_group_name": "other", "name": "hub"}, nil) + // Same name, another group.
		line("azurerm_virtual_network", "hub", map[string]any{"subscription_id": "sub-2", "resource_group_name": "net", "name": "hub"}, nil) + // Another subscription.
		line("google_compute_instance", "vm", map[string]any{"project": "acme", "zone": "us-central1-a", "name": "vm"}, nil) +
		line("google_compute_instance", "vm", map[string]any{"project": "acme", "zone": "us-central1-b", "name": "vm"}, nil) + // Another zone.
		// A setting there is one of per account and region: nothing to name it by.
		line("aws_ebs_encryption_by_default", "us-east-1", map[string]any{"account_id": "111111111111", "region": "us-east-1"}, nil) +
		line("aws_ebs_encryption_by_default", "eu-west-1", map[string]any{"account_id": "111111111111", "region": "eu-west-1"}, nil)
	res, err := Load(g, "cloud", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 3 || res.Unmanaged != 4 {
		t.Errorf("matched %d, unmanaged %d; want 3 and 4", res.Matched, res.Unmanaged)
	}
	var placed []string
	for _, n := range g.Nodes() {
		if n.Status == graph.StatusUnmanaged {
			placed = append(placed, n.Type+"@"+n.Account+"/"+n.Region)
		}
	}
	want := []string{
		"aws_ebs_encryption_by_default@111111111111/eu-west-1",
		"azurerm_virtual_network@sub-1/",
		"azurerm_virtual_network@sub-2/",
		"google_compute_instance@acme/us-central1", // The zone names its region.
	}
	if !reflect.DeepEqual(placed, want) {
		t.Errorf("unmanaged placed at %q, want %q", placed, want)
	}
}

func TestLoadARNs(t *testing.T) {
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	g := graph.New()
	err := tfstate.Load(g, "s", strings.NewReader(`{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"aws_vpc","name":"main",`+aws+`,
	   "instances":[{"attributes":{"id":"vpc-1","arn":"arn:aws:ec2:us-east-1:111111111111:vpc/vpc-1"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"tagging-api.json", "resource-explorer.json"} {
		data, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if !IsARNListing(data) || IsQueryOutput(data) {
			t.Errorf("%s: not recognised as an ARN listing", file)
		}
	}
	// A state file has a "resources" key of its own and is not a listing.
	for _, file := range []string{"managed.tfstate", "query.jsonl"} {
		data, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if IsARNListing(data) {
			t.Errorf("%s was taken for an ARN listing", file)
		}
	}
	f, err := os.Open("testdata/tagging-api.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := LoadARNs(g, "cloud", f)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Result{Found: 6, Matched: 1, Unmanaged: 4, Defaults: 1}); !reflect.DeepEqual(res, want) {
		t.Errorf("result %+v, want %+v", res, want)
	}
	var got []string
	for _, n := range g.Nodes() {
		if n.Status == graph.StatusUnmanaged {
			got = append(got, n.Address+" ["+n.Name+"] @"+n.Account+"/"+n.Region)
		}
	}
	want := []string{
		"aws:glue:crawler.nightly [] @111111111111/us-east-1", // No Terraform type known for it.
		"aws_iam_role.deploy [] @111111111111/global",
		"aws_s3_bucket.acme-logs [logs] @/global", // A bucket's ARN names neither account nor region.
		"aws_subnet.subnet-9 [spare] @111111111111/us-east-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unmanaged:\n got %q\nwant %q", got, want)
	}

	// The other command's output gives the same answer for the same ARNs.
	g2 := graph.New()
	f2, err := os.Open("testdata/resource-explorer.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	res2, err := LoadARNs(g2, "cloud", f2)
	if err != nil || res2.Found != 3 || res2.Unmanaged != 3 {
		t.Errorf("resource explorer: %+v, %v", res2, err)
	}
	if _, err := LoadARNs(g2, "again", strings.NewReader("not json")); err == nil {
		t.Error("garbage was accepted")
	}
}
