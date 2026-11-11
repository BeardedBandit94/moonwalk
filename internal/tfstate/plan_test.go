package tfstate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// The plan fixture is real OpenTofu 1.10 output: against a state of six
// resources it keeps three, updates one, replaces one, deletes one and
// creates three, one of them a third instance of a counted block.
func TestLoadPlan(t *testing.T) {
	g := graph.New()
	loadInto(t, g, "p", "plan.json")

	type want struct {
		status graph.Status
		change string
	}
	cases := map[string]want{
		"terraform_data.keep":                 {graph.StatusManaged, ""},
		"terraform_data.update":               {graph.StatusManaged, "update"},
		"terraform_data.replace":              {graph.StatusManaged, "replace"},
		"terraform_data.delete":               {graph.StatusManaged, "delete"},
		"terraform_data.create":               {graph.StatusDeclared, "create"},
		"module.net.terraform_data.route":     {graph.StatusDeclared, "create"},
		"module.net.terraform_data.subnet[0]": {graph.StatusManaged, ""},
		"module.net.terraform_data.subnet[1]": {graph.StatusManaged, ""},
		"module.net.terraform_data.subnet[2]": {graph.StatusDeclared, "create"},
	}
	if nodes, _ := g.Len(); nodes != len(cases) {
		t.Errorf("%d resources, want %d", nodes, len(cases))
	}
	for addr, w := range cases {
		n, ok := g.Node(NodeID("p", addr))
		if !ok {
			t.Errorf("%s: missing", addr)
			continue
		}
		if n.Status != w.status || n.Change != w.change {
			t.Errorf("%s: status %q change %q, want %q %q", addr, n.Status, n.Change, w.status, w.change)
		}
	}

	// A resource to be created carries none of the values planned for it.
	created, _ := g.Node(NodeID("p", "terraform_data.create"))
	if created.Attributes != nil || created.Type != "terraform_data" || created.ConfigAddress != "terraform_data.create" {
		t.Errorf("created resource: %+v", created)
	}
	route, _ := g.Node(NodeID("p", "module.net.terraform_data.route"))
	if route.Module != "module.net" {
		t.Errorf("created resource in a module: %+v", route)
	}

	var deps []string
	for _, e := range g.Edges() {
		deps = append(deps, strings.ReplaceAll(e.From+" > "+e.To, "p::", ""))
	}
	wantDeps := []string{
		// From the state the plan was made against.
		"module.net.terraform_data.subnet[0] > terraform_data.keep",
		"module.net.terraform_data.subnet[1] > terraform_data.keep",
		// From the configuration in the plan: a sibling by index...
		"module.net.terraform_data.route > module.net.terraform_data.subnet[0]",
		"module.net.terraform_data.route > module.net.terraform_data.subnet[1]",
		"module.net.terraform_data.route > module.net.terraform_data.subnet[2]",
		// ...a variable, which leads to what the caller passes...
		"module.net.terraform_data.subnet[2] > terraform_data.keep",
		// ...a module output, a direct reference and depends_on.
		"terraform_data.create > module.net.terraform_data.subnet[0]",
		"terraform_data.create > module.net.terraform_data.subnet[1]",
		"terraform_data.create > module.net.terraform_data.subnet[2]",
		"terraform_data.create > terraform_data.keep",
		"terraform_data.create > terraform_data.update",
		"terraform_data.delete > terraform_data.keep",
	}
	sortStrings(wantDeps)
	if !reflect.DeepEqual(deps, wantDeps) {
		t.Errorf("dependencies:\n got %s\nwant %s", strings.Join(deps, "\n     "), strings.Join(wantDeps, "\n     "))
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// A plan stores variables, and the values it will set, in the clear. None
// of that may come out.
func TestPlanSecretsStayOut(t *testing.T) {
	g := graph.New()
	loadInto(t, g, "p", "plan.json")
	out, err := json.Marshal(g.Document())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "hunter2") {
		t.Error("the plan's sensitive variable is in the graph")
	}
}

func TestChangeName(t *testing.T) {
	g := graph.New()
	err := Load(g, "p", strings.NewReader(`{"format_version":"1.2","terraform_version":"1.9.5","planned_values":{},
	  "resource_changes":[
	    {"address":"t.read","mode":"data","type":"t","name":"read","change":{"actions":["read"]}},
	    {"address":"t.noop","mode":"managed","type":"t","name":"noop","change":{"actions":["no-op"]}},
	    {"address":"t.gone","mode":"managed","type":"t","name":"gone","change":{"actions":["delete"]}},
	    {"address":"t.cbd","mode":"managed","type":"t","name":"cbd","change":{"actions":["create","delete"]}},
	    {"address":"t.old","mode":"managed","type":"t","name":"old","deposed":"abc","change":{"actions":["delete"]}},
	    {"address":"t.new","mode":"managed","type":"t","name":"new","change":{"actions":["create"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// With no prior state, only the creation concerns a resource to show.
	// A replacement of something the state lacks cannot happen.
	var got []string
	for _, n := range g.Nodes() {
		got = append(got, n.Address+"="+n.Change)
	}
	if want := []string{"t.new=create"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The drift fixture is a real OpenTofu refresh-only plan, made after one of
// two files it manages was deleted by hand.
func TestLoadDrift(t *testing.T) {
	g := graph.New()
	loadInto(t, g, "d", "drift.json")

	removed, ok := g.Node(NodeID("d", "local_file.removed"))
	if !ok || removed.Status != graph.StatusDrifted || removed.Drift != "deleted" || removed.Type != "local_file" {
		t.Errorf("deleted resource: %+v (found %v)", removed, ok)
	}
	if removed.Attributes != nil {
		t.Error("a deleted resource carries its old values")
	}
	kept, _ := g.Node(NodeID("d", "local_file.kept"))
	if kept.Status != graph.StatusManaged || kept.Drift != "" {
		t.Errorf("untouched resource: status %q drift %q", kept.Status, kept.Drift)
	}
	// What depended on the deleted resource still points at it.
	if !g.HasEdge(graph.Edge{From: NodeID("d", "terraform_data.uses"), To: NodeID("d", "local_file.removed"), Kind: graph.EdgeDependsOn}) {
		t.Error("the dependency on the deleted resource is gone")
	}
}

func TestDriftKinds(t *testing.T) {
	g := graph.New()
	err := Load(g, "d", strings.NewReader(`{"format_version":"1.2","terraform_version":"1.9.5","planned_values":{},
	  "prior_state":{"format_version":"1.0","terraform_version":"1.9.5","values":{"root_module":{"resources":[
	    {"address":"t.changed","mode":"managed","type":"t","name":"changed","provider_name":"p","values":{"id":"1"}},
	    {"address":"t.same","mode":"managed","type":"t","name":"same","provider_name":"p","values":{"id":"2"}}]}}},
	  "resource_drift":[
	    {"address":"t.changed","mode":"managed","type":"t","name":"changed","change":{"actions":["update"]}},
	    {"address":"t.gone","mode":"managed","type":"t","name":"gone","change":{"actions":["delete"]}},
	    {"address":"t.same","mode":"managed","type":"t","name":"same","change":{"actions":["no-op"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range g.Nodes() {
		got = append(got, n.Address+"="+string(n.Status)+":"+n.Drift)
	}
	want := []string{"t.changed=drifted:changed", "t.gone=drifted:deleted", "t.same=managed:"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
