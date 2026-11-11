package tfstate

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// loadInto adds a testdata file to g under the given source name.
func loadInto(t *testing.T, g *graph.Graph, source, name string) {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Load(g, source, f); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, name string) graph.Document {
	t.Helper()
	g := graph.New()
	loadInto(t, g, "prod", name)
	return g.Document()
}

// sameDocument fails the test at the first node or edge where the graphs
// built from the two state formats differ.
func sameDocument(t *testing.T, raw, show graph.Document) {
	t.Helper()
	if len(raw.Nodes) != len(show.Nodes) {
		t.Fatalf("raw state gave %d nodes, show-json %d", len(raw.Nodes), len(show.Nodes))
	}
	for i := range raw.Nodes {
		if !reflect.DeepEqual(raw.Nodes[i], show.Nodes[i]) {
			t.Fatalf("node %d differs:\n raw  %+v\n show %+v", i, raw.Nodes[i], show.Nodes[i])
		}
	}
	if !reflect.DeepEqual(raw.Edges, show.Edges) {
		t.Fatalf("edges differ:\n raw  %+v\n show %+v", raw.Edges, show.Edges)
	}
}

const (
	az     = `data.aws_availability_zones.available`
	vpc    = `aws_vpc.main`
	sub0   = `module.net["a.b"].aws_subnet.private[0]`
	sub1   = `module.net["a.b"].aws_subnet.private[1]`
	assoc  = `module.net["a.b"].aws_route_table_association.private["x]y"]`
	awsSrc = "registry.terraform.io/hashicorp/aws"
)

func id(address string) string { return NodeID("prod", address) }

func TestLoadRawState(t *testing.T) {
	d := load(t, "raw.tfstate")

	var ids []string
	for _, n := range d.Nodes {
		ids = append(ids, n.ID)
	}
	// The deposed association object must not produce a second node.
	wantIDs := []string{id(vpc), id(az), id(assoc), id(sub0), id(sub1)}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("node IDs:\n got %q\nwant %q", ids, wantIDs)
	}

	wantNode := graph.Node{
		ID:            `prod::` + sub1,
		Source:        "prod",
		Address:       sub1,
		ConfigAddress: "module.net.aws_subnet.private",
		Account:       "123456789012",
		Region:        "us-east-1",
		CloudID:       "arn:aws:ec2:us-east-1:123456789012:subnet/subnet-1",
		Attributes:    map[string]any{"id": "subnet-1", "arn": "arn:aws:ec2:us-east-1:123456789012:subnet/subnet-1"},
		Module:        `module.net["a.b"]`,
		Mode:          graph.ModeManaged,
		Type:          "aws_subnet",
		Name:          "private",
		Provider:      awsSrc,
		Status:        graph.StatusManaged,
	}
	if got := d.Nodes[4]; !reflect.DeepEqual(got, wantNode) {
		t.Errorf("node:\n got %+v\nwant %+v", got, wantNode)
	}
	// The data source states neither account nor region, so it takes the
	// ones its provider's managed resources agree on.
	if n := d.Nodes[1]; n.Mode != graph.ModeData || n.Account != "123456789012" || !n.AccountInferred ||
		n.Region != "us-east-1" || !n.RegionInferred {
		t.Errorf("data source without an ARN: %+v", n)
	}
	if d.Nodes[0].RegionInferred {
		t.Error("a region stated by the resource is marked as inferred")
	}
	if d.Nodes[0].AccountInferred {
		t.Error("an account stated by the resource is marked as inferred")
	}

	// A dependency on a counted block fans out to every instance, and the
	// dependency on a resource missing from state is dropped.
	dep := graph.EdgeDependsOn
	wantEdges := []graph.Edge{
		{From: id(assoc), To: id(sub0), Kind: dep},
		{From: id(assoc), To: id(sub1), Kind: dep},
		{From: id(sub0), To: id(vpc), Kind: dep},
		{From: id(sub0), To: id(az), Kind: dep},
		{From: id(sub1), To: id(vpc), Kind: dep},
		{From: id(sub1), To: id(az), Kind: dep},
	}
	if !reflect.DeepEqual(d.Edges, wantEdges) {
		t.Errorf("edges:\n got %+v\nwant %+v", d.Edges, wantEdges)
	}
}

func TestShowJSONMatchesRawState(t *testing.T) {
	sameDocument(t, load(t, "raw.tfstate"), load(t, "show.json"))
}

// The secrets fixtures are real OpenTofu 1.10 output for one configuration.
func TestSecretsFixtureFormatsAgree(t *testing.T) {
	sameDocument(t, load(t, "secrets.tfstate"), load(t, "secrets.show.json"))
}

func TestSensitiveValuesAreRedacted(t *testing.T) {
	for _, file := range []string{"secrets.tfstate", "secrets.show.json"} {
		g := graph.New()
		loadInto(t, g, "lab", file)

		db, _ := g.Node(NodeID("lab", "random_password.db"))
		if db.Attributes["result"] != Redacted || db.Attributes["bcrypt_hash"] != Redacted {
			t.Errorf("%s: schema-sensitive attributes not redacted: %v", file, db.Attributes)
		}
		if db.Attributes["length"] == Redacted || db.Attributes["length"] == nil {
			t.Errorf("%s: ordinary attribute lost: %v", file, db.Attributes["length"])
		}

		cfg, _ := g.Node(NodeID("lab", "terraform_data.cfg"))
		want := map[string]any{"user": "app", "password": Redacted, "list": []any{"a", Redacted}}
		if got := cfg.Attributes["input"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: input = %v, want %v", file, got, want)
		}
		// State does not mark this copy of the secret; it is caught by value.
		if got := cfg.Attributes["output"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: output = %v, want %v", file, got, want)
		}
	}
}

// Both instances of module.net hold a vpc and two subnets. State records
// each subnet's dependency only as "module.net.terraform_data.vpc".
func TestDependenciesStayInsideTheModuleInstance(t *testing.T) {
	d := load(t, "secrets.tfstate")
	var got []string
	for _, e := range d.Edges {
		got = append(got, strings.TrimPrefix(e.From, "prod::")+" -> "+strings.TrimPrefix(e.To, "prod::"))
	}
	want := []string{
		`module.net["a"].terraform_data.subnet[0] -> module.net["a"].terraform_data.vpc`,
		`module.net["a"].terraform_data.subnet[1] -> module.net["a"].terraform_data.vpc`,
		`module.net["b"].terraform_data.subnet[0] -> module.net["b"].terraform_data.vpc`,
		`module.net["b"].terraform_data.subnet[1] -> module.net["b"].terraform_data.vpc`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges:\n got %q\nwant %q", got, want)
	}
}

func TestAccountInference(t *testing.T) {
	const state = `{"version": 4, "resources": [
	  {"mode":"managed","type":"aws_iam_role","name":"a","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
	   "instances":[{"attributes":{"arn":"arn:aws:iam::111111111111:role/a"}}]},
	  {"mode":"managed","type":"aws_s3_bucket","name":"a","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
	   "instances":[{"attributes":{"arn":"arn:aws:s3:::a","region":"us-east-1"}}]},
	  {"mode":"data","type":"aws_ami","name":"public","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
	   "instances":[{"attributes":{"owner_id":"999999999999"}}]},
	  {"mode":"managed","type":"aws_iam_role","name":"b1","provider":"provider[\"registry.terraform.io/hashicorp/aws\"].other",
	   "instances":[{"attributes":{"arn":"arn:aws:iam::222222222222:role/b1"}}]},
	  {"mode":"managed","type":"aws_iam_role","name":"b2","provider":"provider[\"registry.terraform.io/hashicorp/aws\"].other",
	   "instances":[{"attributes":{"arn":"arn:aws:iam::333333333333:role/b2"}}]},
	  {"mode":"managed","type":"aws_s3_bucket","name":"b","provider":"provider[\"registry.terraform.io/hashicorp/aws\"].other",
	   "instances":[{"attributes":{"arn":"arn:aws:s3:::b"}}]},
	  {"mode":"managed","type":"terraform_data","name":"x","provider":"provider[\"terraform.io/builtin/terraform\"]",
	   "instances":[{"attributes":{"id":"x"}}]}
	]}`
	g := graph.New()
	if err := Load(g, "s", strings.NewReader(state)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		address  string
		account  string
		inferred bool
	}{
		{"aws_iam_role.a", "111111111111", false},
		// The bucket's ARN has no account; its provider's only account is used.
		// The data source's foreign owner does not get in the way.
		{"aws_s3_bucket.a", "111111111111", true},
		{"data.aws_ami.public", "999999999999", false},
		// This provider alias reaches two accounts, so nothing is inferred.
		{"aws_s3_bucket.b", "", false},
		{"terraform_data.x", "", false},
	}
	for _, c := range cases {
		n, ok := g.Node(NodeID("s", c.address))
		if !ok {
			t.Fatalf("%s: missing", c.address)
		}
		if n.Account != c.account || n.AccountInferred != c.inferred {
			t.Errorf("%s: account %q inferred %v, want %q %v", c.address, n.Account, n.AccountInferred, c.account, c.inferred)
		}
	}
}

func TestRegionInference(t *testing.T) {
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]`
	state := `{"version": 4, "resources": [
	  {"mode":"managed","type":"aws_vpc","name":"main",` + aws + `",
	   "instances":[{"attributes":{"arn":"arn:aws:ec2:eu-west-1:111111111111:vpc/vpc-1"}}]},
	  {"mode":"managed","type":"aws_route_table_association","name":"a",` + aws + `",
	   "instances":[{"attributes":{"id":"rtbassoc-1"}}]},
	  {"mode":"managed","type":"aws_iam_role","name":"app",` + aws + `",
	   "instances":[{"attributes":{"arn":"arn:aws:iam::111111111111:role/app"}}]},
	  {"mode":"managed","type":"aws_vpc","name":"x",` + aws + `.multi",
	   "instances":[{"attributes":{"arn":"arn:aws:ec2:us-east-1:111111111111:vpc/vpc-2"}}]},
	  {"mode":"managed","type":"aws_vpc","name":"y",` + aws + `.multi",
	   "instances":[{"attributes":{"arn":"arn:aws:ec2:us-west-2:111111111111:vpc/vpc-3"}}]},
	  {"mode":"managed","type":"aws_route_table_association","name":"b",` + aws + `.multi",
	   "instances":[{"attributes":{"id":"rtbassoc-2"}}]},
	  {"mode":"managed","type":"azurerm_resource_group","name":"rg","provider":"provider[\"registry.terraform.io/hashicorp/azurerm\"]",
	   "instances":[{"attributes":{"id":"/subscriptions/s-1/resourceGroups/rg","location":"westeurope"}}]},
	  {"mode":"managed","type":"azurerm_role_assignment","name":"ra","provider":"provider[\"registry.terraform.io/hashicorp/azurerm\"]",
	   "instances":[{"attributes":{"id":"/subscriptions/s-1/providers/x"}}]}
	]}`
	g := graph.New()
	if err := Load(g, "s", strings.NewReader(state)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		address  string
		region   string
		inferred bool
	}{
		{"aws_vpc.main", "eu-west-1", false},
		// The association states no region; its provider works in one.
		{"aws_route_table_association.a", "eu-west-1", true},
		// A global resource is known to be global, not given a region.
		{"aws_iam_role.app", "global", false},
		// This provider alias reaches two regions, so nothing is inferred.
		{"aws_route_table_association.b", "", false},
		// Only AWS ties a provider configuration to a region.
		{"azurerm_role_assignment.ra", "", false},
	}
	for _, c := range cases {
		n, ok := g.Node(NodeID("s", c.address))
		if !ok {
			t.Fatalf("%s: missing", c.address)
		}
		if n.Region != c.region || n.RegionInferred != c.inferred {
			t.Errorf("%s: region %q inferred %v, want %q %v", c.address, n.Region, n.RegionInferred, c.region, c.inferred)
		}
	}
}

// Attributes of a state that does not mark every secret stay available for
// working out relationships, but are flagged and never serialized.
func TestOldStatesWithholdAttributes(t *testing.T) {
	for _, file := range []string{"unmarked.tfstate", "unmarked.show.json"} {
		g := graph.New()
		loadInto(t, g, "old", file)
		n, ok := g.Node(NodeID("old", "random_password.db"))
		if !ok {
			t.Fatalf("%s: resource missing", file)
		}
		if !n.AttributesWithheld || n.Attributes["length"] == nil {
			t.Errorf("%s: withheld %v, attributes %v", file, n.AttributesWithheld, n.Attributes)
		}
		out, err := json.Marshal(g.Document())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), `"attributes":`) || strings.Contains(string(out), `"result"`) {
			t.Errorf("%s: attributes were serialized", file)
		}
	}
	for _, file := range []string{"secrets.tfstate", "secrets.show.json", "raw.tfstate", "show.json", "network.tfstate"} {
		for _, n := range load(t, file).Nodes {
			if n.AttributesWithheld {
				t.Errorf("%s: %s withholds its attributes", file, n.Address)
			}
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"1.8.0", true}, {"1.8.0-rc1", true}, {"1.10.6", true}, {"2.0.0", true}, {"1.12", true},
		{"1.7.5", false}, {"0.14.11", false}, {"1.5.7", false},
		{"", false}, {"1", false}, {"one.eight.zero", false}, {"1.x.0", false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.version, 1, 8); got != c.want {
			t.Errorf("versionAtLeast(%q, 1, 8) = %v, want %v", c.version, got, c.want)
		}
	}
}

func TestLoadRejectsEncryptedState(t *testing.T) {
	f, err := os.Open("testdata/encrypted.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = Load(graph.New(), "s", f)
	if err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("got %v, want an error saying the state is encrypted", err)
	}
}

func TestLoadSeveralStates(t *testing.T) {
	g := graph.New()
	loadInto(t, g, "prod", "raw.tfstate")
	loadInto(t, g, "staging", "show.json")
	d := g.Document()

	// The two states hold the same addresses; they must stay separate.
	if len(d.Nodes) != 10 || len(d.Edges) != 12 {
		t.Fatalf("got %d nodes and %d edges, want 10 and 12", len(d.Nodes), len(d.Edges))
	}
	perSource := map[string]int{}
	for _, n := range d.Nodes {
		perSource[n.Source]++
	}
	if perSource["prod"] != 5 || perSource["staging"] != 5 {
		t.Errorf("nodes per source: %v", perSource)
	}
	for _, e := range d.Edges {
		from, _ := g.Node(e.From)
		to, _ := g.Node(e.To)
		if from.Source != to.Source {
			t.Errorf("edge crosses sources: %+v", e)
		}
	}
}

func TestLoadRejectsReusedSource(t *testing.T) {
	g := graph.New()
	loadInto(t, g, "prod", "raw.tfstate")
	f, err := os.Open("testdata/show.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Load(g, "prod", f); err == nil {
		t.Error("same source name accepted twice")
	}
}

func TestLoadRejectsUnsupportedInput(t *testing.T) {
	cases := map[string]string{
		"not JSON":      `resource "aws_vpc" "main" {}`,
		"old version":   `{"version": 3}`,
		"unrelated doc": `{"hello": "world"}`,
	}
	for name, in := range cases {
		if err := Load(graph.New(), "s", strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := Load(graph.New(), "", strings.NewReader(`{"version":4}`)); err == nil {
		t.Error("empty source name accepted")
	}
}

func TestLoadEmptyState(t *testing.T) {
	for _, in := range []string{`{"format_version":"1.0"}`, `{"version":4,"resources":[]}`} {
		g := graph.New()
		if err := Load(g, "s", strings.NewReader(in)); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if n := len(g.Nodes()); n != 0 {
			t.Errorf("%s: %d nodes", in, n)
		}
	}
}

func TestPartition(t *testing.T) {
	const (
		aws    = "registry.terraform.io/hashicorp/aws"
		azure  = "registry.terraform.io/hashicorp/azurerm"
		google = "registry.opentofu.org/hashicorp/google"
	)
	cases := []struct {
		name            string
		provider        string
		typ             string
		attrs           map[string]any
		account, region string
	}{
		{"aws arn", aws, "aws_vpc", map[string]any{"arn": "arn:aws:ec2:eu-west-1:123456789012:vpc/vpc-1"}, "123456789012", "eu-west-1"},
		{"aws global arn", aws, "aws_iam_role", map[string]any{"arn": "arn:aws:iam::123456789012:role/app"}, "123456789012", "global"},
		{"aws s3 bucket", aws, "aws_s3_bucket", map[string]any{"arn": "arn:aws:s3:::logs", "region": "us-west-2"}, "", "us-west-2"},
		{"aws managed policy", aws, "aws_iam_policy", map[string]any{"arn": "arn:aws:iam::aws:policy/ReadOnlyAccess"}, "", "global"},
		{"aws owner_id", aws, "aws_security_group", map[string]any{"owner_id": "123456789012", "region": "us-east-1"}, "123456789012", "us-east-1"},
		{"aws resource in arn has colons", aws, "aws_cloudwatch_log_group", map[string]any{"arn": "arn:aws:logs:us-east-1:123456789012:log-group:/a:*"}, "123456789012", "us-east-1"},
		{"aws global service without arn", aws, "aws_iam_role_policy_attachment", map[string]any{"role": "app"}, "", "global"},
		{"aws route 53 zone", aws, "aws_route53_record", map[string]any{"name": "a.example.com"}, "", "global"},
		{"aws route 53 resolver is regional", aws, "aws_route53_resolver_endpoint", map[string]any{"name": "r"}, "", ""},
		{"aws regional without arn or region", aws, "aws_route_table_association", map[string]any{"id": "rtbassoc-1"}, "", ""},
		{"azure", azure, "azurerm_virtual_network", map[string]any{"id": "/subscriptions/0000-11/resourceGroups/rg/providers/x", "location": "westeurope"}, "0000-11", "westeurope"},
		{"azure non-arm id", azure, "azurerm_key_vault_key", map[string]any{"id": "https://vault.azure.net/keys/k"}, "", ""},
		{"google region", google, "google_compute_subnetwork", map[string]any{"project": "acme-prod", "region": "europe-west1"}, "acme-prod", "europe-west1"},
		{"google zone self link", google, "google_compute_instance", map[string]any{"project": "acme-prod", "zone": "https://www.googleapis.com/compute/v1/projects/acme-prod/zones/us-central1-a"}, "acme-prod", "us-central1"},
		{"google location", google, "google_storage_bucket", map[string]any{"project": "acme-prod", "location": "US"}, "acme-prod", "US"},
		{"unknown provider", "terraform.io/builtin/terraform", "terraform_data", map[string]any{"region": "x", "arn": "y"}, "", ""},
		{"no attributes", aws, "aws_vpc", nil, "", ""},
		{"non-string values", aws, "aws_vpc", map[string]any{"arn": 5, "region": []any{"a"}}, "", ""},
	}
	for _, c := range cases {
		account, region := Partition(c.provider, c.typ, c.attrs)
		if account != c.account || region != c.region {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, account, region, c.account, c.region)
		}
	}
}

func TestProviderSource(t *testing.T) {
	cases := map[string]string{
		`provider["registry.terraform.io/hashicorp/aws"]`:               awsSrc,
		`module.a.provider["registry.terraform.io/hashicorp/aws"].west`: awsSrc,
		`something-else`: `something-else`,
		`provider["]`:    `provider["]`,
		`provider[""]`:   ``,
	}
	for in, want := range cases {
		if got := providerSource(in); got != want {
			t.Errorf("providerSource(%q) = %q, want %q", in, got, want)
		}
	}
}
