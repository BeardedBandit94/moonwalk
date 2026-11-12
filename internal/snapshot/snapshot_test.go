package snapshot

import (
	"bytes"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/relate"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

const fixtures = "../tfstate/testdata/"

// load reads state fixtures into one graph, each named as given.
func load(t *testing.T, named ...string) *graph.Graph {
	t.Helper()
	g := graph.New()
	for i := 0; i < len(named); i += 2 {
		f, err := os.Open(fixtures + named[i+1])
		if err != nil {
			t.Fatal(err)
		}
		err = tfstate.Load(g, named[i], f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	relate.Enrich(g)
	return g
}

func write(t *testing.T, g *graph.Graph) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, g, time.Date(2026, 11, 12, 9, 30, 0, 0, time.FixedZone("", 3600))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRoundTrip(t *testing.T) {
	for _, file := range []string{"raw.tfstate", "show.json", "network.tfstate", "plan.json", "drift.json", "secrets.tfstate", "unmarked.tfstate"} {
		g := load(t, "s", file)
		data := write(t, g)
		if !Is(data) {
			t.Fatalf("%s: a snapshot is not recognised as one", file)
		}
		back := graph.New()
		info, err := Load(back, bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		// Withheld attributes are in memory and never in a snapshot, so
		// what is compared is what each graph has to show.
		if again := write(t, back); !bytes.Equal(data, again) {
			t.Errorf("%s: the graph read back differs from the one written", file)
		}
		if file != "unmarked.tfstate" && !reflect.DeepEqual(g.Document(), back.Document()) {
			t.Errorf("%s: the graph read back differs in memory from the one written", file)
		}
		nodes, _ := g.Len()
		if info.Resources != nodes || !info.Created.Equal(time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC)) {
			t.Errorf("%s: info = %+v", file, info)
		}
	}
}

// A state is not a snapshot, and neither is a graph printed as JSON.
func TestIs(t *testing.T) {
	state, err := os.ReadFile(fixtures + "raw.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	if err := load(t, "s", "raw.tfstate").Document().WriteJSON(&printed); err != nil {
		t.Fatal(err)
	}
	other := []byte(`{"kind": "something-else", "nodes": []}`)
	list := []byte(`[{"kind": "moonwalk-snapshot"}]`)
	for name, data := range map[string][]byte{"state": state, "graph": printed.Bytes(), "text": []byte("kind: moonwalk-snapshot"), "empty": nil, "other kind": other, "list": list} {
		if Is(data) {
			t.Errorf("%s taken for a snapshot", name)
		}
	}
}

// Snapshots taken apart come together in one graph, and what links them
// is found then: neither snapshot alone holds both ends.
func TestSeveralSnapshotsLinkUp(t *testing.T) {
	network := write(t, load(t, "network", "remote-network.tfstate"))
	app := write(t, load(t, "app", "remote-app.tfstate"))

	g := graph.New()
	for _, data := range [][]byte{network, app} {
		if _, err := Load(g, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	if _, edges := g.Len(); edges != 3 {
		t.Fatalf("edges before linking = %d, want the 3 the snapshots hold", edges)
	}
	if res := relate.Enrich(g); res.CrossState == 0 {
		t.Error("no link found between the two snapshots")
	}
	together := load(t, "network", "remote-network.tfstate", "app", "remote-app.tfstate")
	if !reflect.DeepEqual(g.Document(), together.Document()) {
		t.Error("two snapshots loaded together differ from the two states loaded together")
	}
}

func TestLoadErrors(t *testing.T) {
	snap := write(t, load(t, "s", "raw.tfstate"))

	g := graph.New()
	if _, err := Load(g, bytes.NewReader(snap)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(g, bytes.NewReader(snap)); err == nil || !strings.Contains(err.Error(), `source name "s"`) {
		t.Errorf("same snapshot twice: error should name the shared source, got %v", err)
	}

	cases := map[string]string{
		"newer schema":    strings.Replace(string(snap), `"schema_version":1`, `"schema_version":99`, 1),
		"cut short":       string(snap[:len(snap)/2]),
		"kind comes late": `{"schema_version": 1, "nodes": [], "edges": [], "kind": "moonwalk-snapshot"}`,
		"not a snapshot":  `{"schema_version": 1, "nodes": [], "edges": []}`,
		"not JSON":        `moonwalk-snapshot`,
		"dangling edge":   `{"schema_version": 1, "kind": "moonwalk-snapshot", "nodes": [], "edges": [{"from": "a", "to": "b", "kind": "depends_on"}]}`,
	}
	for name, data := range cases {
		if _, err := Load(graph.New(), strings.NewReader(data)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	_, err := Load(graph.New(), strings.NewReader(cases["newer schema"]))
	if err == nil || !strings.Contains(err.Error(), "newer moonwalk") {
		t.Errorf("newer schema: error should say why, got %v", err)
	}
}

// A snapshot holds attributes, so it must hold no secret: the redacted
// ones stay redacted and the ones that could not be redacted stay out.
func TestSnapshotHoldsNoSecrets(t *testing.T) {
	secrets := map[string][]string{
		"secrets.tfstate":    {"hunter2", "4@Sze931N+:r", "$2a$10$INNvf"},
		"secrets.show.json":  {"hunter2", "4@Sze931N+:r", "$2a$10$INNvf"},
		"unmarked.tfstate":   {"8%exVpqbOs%l", "$2a$10$Yh49MOe", "hunter2"},
		"unmarked.show.json": {">p6)+4Lqi@J?", "$2a$10$NOV6SnY", "hunter2"},
	}
	for file, values := range secrets {
		data := write(t, load(t, "s", file))
		for _, s := range values {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("%s: the snapshot contains the secret %q", file, s)
			}
		}
		// And nothing brings them back on the way in.
		back := graph.New()
		if _, err := Load(back, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		var again bytes.Buffer
		if err := back.Document().WriteJSON(&again); err != nil {
			t.Fatal(err)
		}
		for _, s := range values {
			if bytes.Contains(again.Bytes(), []byte(s)) {
				t.Errorf("%s: the secret %q came back from the snapshot", file, s)
			}
		}
	}
}

func TestCompare(t *testing.T) {
	// Real states of one configuration before and after a change that
	// removed the gateway, added a queue, resized the server and made it
	// depend on the network instead of the subnets.
	before := load(t, "before", "diff-before.tfstate")
	after := load(t, "after", "diff-after.tfstate")
	d := Compare(before, after)
	want := Diff{
		Added:               []string{"terraform_data.queue"},
		Removed:             []string{"terraform_data.gateway"},
		Changed:             []Change{{ID: "terraform_data.server", What: []string{"attributes: input, output"}}},
		DependenciesAdded:   []string{},
		DependenciesRemoved: []string{"terraform_data.server → terraform_data.subnet[0]", "terraform_data.server → terraform_data.subnet[1]"},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("Compare =\n%+v\nwant\n%+v", d, want)
	}

	// The other way round, what was removed is what gets added.
	back := Compare(after, before)
	if !reflect.DeepEqual(back.Added, want.Removed) || !reflect.DeepEqual(back.Removed, want.Added) || !reflect.DeepEqual(back.DependenciesAdded, want.DependenciesRemoved) {
		t.Errorf("reversed Compare = %+v", back)
	}

	if same := Compare(before, before); !same.Empty() {
		t.Errorf("a graph differs from itself: %+v", same)
	}
	if d.Empty() {
		t.Error("Empty reports no difference where there is one")
	}
}

// Sides with one source each are matched by address whatever the sources
// are called; with more than one, the names have to agree.
func TestCompareMatchesBySourceName(t *testing.T) {
	same := Compare(load(t, "x", "diff-before.tfstate"), load(t, "x", "diff-after.tfstate"))
	if !reflect.DeepEqual(same.Added, []string{"x::terraform_data.queue"}) {
		t.Errorf("same source name: added = %v, want the full ID", same.Added)
	}

	older := load(t, "network", "remote-network.tfstate", "app", "diff-before.tfstate")
	newer := load(t, "network", "remote-network.tfstate", "app", "diff-after.tfstate")
	d := Compare(older, newer)
	if !reflect.DeepEqual(d.Added, []string{"app::terraform_data.queue"}) || !reflect.DeepEqual(d.Removed, []string{"app::terraform_data.gateway"}) || len(d.Changed) != 1 {
		t.Errorf("two sources each: %+v", d)
	}

	renamed := load(t, "net", "remote-network.tfstate", "app", "diff-after.tfstate")
	if d := Compare(older, renamed); len(d.Removed) != 4 || len(d.Added) != 4 {
		t.Errorf("a renamed source among several should read as removed and added, got %+v", d)
	}
}

// What a resource is, apart from its attributes, is reported by field.
func TestCompareFields(t *testing.T) {
	node := func(change func(*graph.Node)) *graph.Graph {
		g := graph.New()
		n := graph.Node{ID: "s::aws_vpc.main", Source: "s", Address: "aws_vpc.main", Status: graph.StatusManaged, Account: "111111111111", Region: "eu-west-1"}
		if change != nil {
			change(&n)
		}
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
		return g
	}
	cases := []struct {
		change func(*graph.Node)
		want   string
	}{
		{func(n *graph.Node) { n.Status = graph.StatusDrifted }, "status: managed → drifted"},
		{func(n *graph.Node) { n.Region = "us-east-1" }, "region: eu-west-1 → us-east-1"},
		{func(n *graph.Node) { n.Account = "" }, "account: 111111111111 → none"},
		{func(n *graph.Node) { n.NotInCode = true }, "in code: yes → no"},
		{func(n *graph.Node) { n.NotListed = true }, "found in the cloud listing: yes → no"},
		{func(n *graph.Node) { n.Change = "update" }, "planned change: none → update"},
	}
	for _, c := range cases {
		d := Compare(node(nil), node(c.change))
		if len(d.Changed) != 1 || !reflect.DeepEqual(d.Changed[0].What, []string{c.want}) {
			t.Errorf("want %q, got %+v", c.want, d.Changed)
		}
	}

	// Attributes one side withholds cannot be compared, and are not
	// reported as changed.
	shown := node(func(n *graph.Node) { n.Attributes = map[string]any{"cidr_block": "10.0.0.0/16"} })
	withheld := node(func(n *graph.Node) { n.AttributesWithheld = true })
	if d := Compare(shown, withheld); !d.Empty() {
		t.Errorf("withheld attributes reported as a change: %+v", d)
	}
	// Both of these hold a random_password.db, and one withholds it.
	for _, c := range Compare(load(t, "a", "unmarked.tfstate"), load(t, "b", "secrets.tfstate")).Changed {
		if slices.ContainsFunc(c.What, func(w string) bool { return strings.HasPrefix(w, "attributes") }) {
			t.Errorf("attributes of a withholding state compared: %+v", c)
		}
	}
	// Values never appear, only names.
	other := node(func(n *graph.Node) {
		n.Attributes = map[string]any{"cidr_block": "10.1.0.0/16", "tags": map[string]any{}}
	})
	d := Compare(shown, other)
	if len(d.Changed) != 1 || d.Changed[0].What[0] != "attributes: cidr_block, tags" {
		t.Errorf("attribute names: %+v", d.Changed)
	}
}
