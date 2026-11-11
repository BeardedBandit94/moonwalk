package tfstate

import (
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
	// The data source states no account, so it takes the one its provider's
	// managed resources agree on. Its region stays unknown.
	if n := d.Nodes[1]; n.Mode != graph.ModeData || n.Account != "123456789012" || !n.AccountInferred || n.Region != "" {
		t.Errorf("data source without an ARN: %+v", n)
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
		attrs           map[string]any
		account, region string
	}{
		{"aws arn", aws, map[string]any{"arn": "arn:aws:ec2:eu-west-1:123456789012:vpc/vpc-1"}, "123456789012", "eu-west-1"},
		{"aws global arn", aws, map[string]any{"arn": "arn:aws:iam::123456789012:role/app"}, "123456789012", ""},
		{"aws s3 bucket", aws, map[string]any{"arn": "arn:aws:s3:::logs", "region": "us-west-2"}, "", "us-west-2"},
		{"aws managed policy", aws, map[string]any{"arn": "arn:aws:iam::aws:policy/ReadOnlyAccess"}, "", ""},
		{"aws owner_id", aws, map[string]any{"owner_id": "123456789012", "region": "us-east-1"}, "123456789012", "us-east-1"},
		{"aws resource in arn has colons", aws, map[string]any{"arn": "arn:aws:logs:us-east-1:123456789012:log-group:/a:*"}, "123456789012", "us-east-1"},
		{"azure", azure, map[string]any{"id": "/subscriptions/0000-11/resourceGroups/rg/providers/x", "location": "westeurope"}, "0000-11", "westeurope"},
		{"azure non-arm id", azure, map[string]any{"id": "https://vault.azure.net/keys/k"}, "", ""},
		{"google region", google, map[string]any{"project": "acme-prod", "region": "europe-west1"}, "acme-prod", "europe-west1"},
		{"google zone self link", google, map[string]any{"project": "acme-prod", "zone": "https://www.googleapis.com/compute/v1/projects/acme-prod/zones/us-central1-a"}, "acme-prod", "us-central1"},
		{"google location", google, map[string]any{"project": "acme-prod", "location": "US"}, "acme-prod", "US"},
		{"unknown provider", "terraform.io/builtin/terraform", map[string]any{"region": "x", "arn": "y"}, "", ""},
		{"no attributes", aws, nil, "", ""},
		{"non-string values", aws, map[string]any{"arn": 5, "region": []any{"a"}}, "", ""},
	}
	for _, c := range cases {
		account, region := partition(c.provider, c.attrs)
		if account != c.account || region != c.region {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, account, region, c.account, c.region)
		}
	}
}

func TestConfigAddress(t *testing.T) {
	cases := map[string]string{
		`aws_vpc.main`:                        `aws_vpc.main`,
		`aws_subnet.s[3]`:                     `aws_subnet.s`,
		`module.a["x"].module.b[0].aws_s3.b`:  `module.a.module.b.aws_s3.b`,
		`aws_s3.b["we]ird.\"key"]`:            `aws_s3.b`,
		`module.m["a.b"].data.aws_ami.u["k"]`: `module.m.data.aws_ami.u`,
	}
	for in, want := range cases {
		if got := configAddress(in); got != want {
			t.Errorf("configAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModuleSteps(t *testing.T) {
	cases := map[string][]moduleStep{
		``:                            nil,
		`module.a`:                    {{"a", ""}},
		`module.a["x.y"].module.b[0]`: {{"a", `["x.y"]`}, {"b", "[0]"}},
		`module.a["]\""].module.b`:    {{"a", `["]\""]`}, {"b", ""}},
	}
	for in, want := range cases {
		if got := moduleSteps(in); !reflect.DeepEqual(got, want) {
			t.Errorf("moduleSteps(%q) = %q, want %q", in, got, want)
		}
	}
	if got, want := moduleNames("module.a.module.b.data.aws_ami.u"), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("moduleNames = %q, want %q", got, want)
	}
	if got := moduleNames("aws_vpc.main"); got != nil {
		t.Errorf("moduleNames for a root resource = %q", got)
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
