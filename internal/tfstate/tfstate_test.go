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
		Module:        `module.net["a.b"]`,
		Mode:          graph.ModeManaged,
		Type:          "aws_subnet",
		Name:          "private",
		Provider:      awsSrc,
		Status:        graph.StatusManaged,
	}
	if got := d.Nodes[4]; got != wantNode {
		t.Errorf("node:\n got %+v\nwant %+v", got, wantNode)
	}
	if n := d.Nodes[1]; n.Mode != graph.ModeData || n.Account != "" || n.Region != "" {
		t.Errorf("data source without an ARN: %+v", n)
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
	raw, show := load(t, "raw.tfstate"), load(t, "show.json")
	if !reflect.DeepEqual(raw, show) {
		t.Errorf("formats disagree:\n raw  %+v\n show %+v", raw, show)
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

func TestProviderSource(t *testing.T) {
	cases := map[string]string{
		`provider["registry.terraform.io/hashicorp/aws"]`:               awsSrc,
		`module.a.provider["registry.terraform.io/hashicorp/aws"].west`: awsSrc,
		`something-else`: `something-else`,
	}
	for in, want := range cases {
		if got := providerSource(in); got != want {
			t.Errorf("providerSource(%q) = %q, want %q", in, got, want)
		}
	}
}
