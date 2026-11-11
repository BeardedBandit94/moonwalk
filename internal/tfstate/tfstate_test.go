package tfstate

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

func load(t *testing.T, name string) graph.Document {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
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

func TestLoadRawState(t *testing.T) {
	d := load(t, "raw.tfstate")

	var ids []string
	for _, n := range d.Nodes {
		ids = append(ids, n.ID)
	}
	// The deposed association object must not produce a second node.
	wantIDs := []string{vpc, az, assoc, sub0, sub1}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("node IDs:\n got %q\nwant %q", ids, wantIDs)
	}

	wantNode := graph.Node{
		ID:            sub1,
		ConfigAddress: "module.net.aws_subnet.private",
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
	if d.Nodes[1].Mode != graph.ModeData {
		t.Errorf("data source mode = %q", d.Nodes[1].Mode)
	}

	// A dependency on a counted block fans out to every instance, and the
	// dependency on a resource missing from state is dropped.
	dep := graph.EdgeDependsOn
	wantEdges := []graph.Edge{
		{From: assoc, To: sub0, Kind: dep},
		{From: assoc, To: sub1, Kind: dep},
		{From: sub0, To: vpc, Kind: dep},
		{From: sub0, To: az, Kind: dep},
		{From: sub1, To: vpc, Kind: dep},
		{From: sub1, To: az, Kind: dep},
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

func TestLoadRejectsUnsupportedInput(t *testing.T) {
	cases := map[string]string{
		"not JSON":      `resource "aws_vpc" "main" {}`,
		"old version":   `{"version": 3}`,
		"unrelated doc": `{"hello": "world"}`,
	}
	for name, in := range cases {
		if _, err := Load(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadEmptyState(t *testing.T) {
	for _, in := range []string{`{"format_version":"1.0"}`, `{"version":4,"resources":[]}`} {
		g, err := Load(strings.NewReader(in))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if n := len(g.Nodes()); n != 0 {
			t.Errorf("%s: %d nodes", in, n)
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
