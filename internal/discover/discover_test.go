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
