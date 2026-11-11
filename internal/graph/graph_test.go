package graph

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAddNodeRejectsDuplicatesAndEmptyID(t *testing.T) {
	g := New()
	if err := g.AddNode(Node{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode(Node{ID: "a"}); err == nil {
		t.Error("duplicate node accepted")
	}
	if err := g.AddNode(Node{}); err == nil {
		t.Error("empty ID accepted")
	}
}

func TestAddEdgeRequiresBothEndpoints(t *testing.T) {
	g := New()
	_ = g.AddNode(Node{ID: "a"})
	if err := g.AddEdge(Edge{From: "a", To: "b", Kind: EdgeDependsOn}); err == nil {
		t.Error("edge to unknown node accepted")
	}
	if err := g.AddEdge(Edge{From: "b", To: "a", Kind: EdgeDependsOn}); err == nil {
		t.Error("edge from unknown node accepted")
	}
}

func TestDocumentIsSortedAndDeduplicated(t *testing.T) {
	g := New()
	for _, id := range []string{"c", "a", "b"} {
		_ = g.AddNode(Node{ID: id})
	}
	for _, pair := range [][2]string{{"c", "a"}, {"b", "a"}, {"c", "a"}} {
		e := Edge{From: pair[0], To: pair[1], Kind: EdgeDependsOn}
		if err := g.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	d := g.Document()
	if len(d.Nodes) != 3 || d.Nodes[0].ID != "a" || d.Nodes[2].ID != "c" {
		t.Errorf("nodes not sorted: %+v", d.Nodes)
	}
	if len(d.Edges) != 2 || d.Edges[0].From != "b" || d.Edges[1].From != "c" {
		t.Errorf("edges not sorted or deduplicated: %+v", d.Edges)
	}
}

func edgeList(g *Graph) []string {
	var out []string
	for _, e := range g.Edges() {
		out = append(out, e.From+">"+e.To)
	}
	return out
}

func build(t *testing.T, edges ...string) *Graph {
	t.Helper()
	g := New()
	for _, e := range edges {
		from, to := e[:1], e[2:]
		for _, id := range []string{from, to} {
			if _, ok := g.Node(id); !ok {
				_ = g.AddNode(Node{ID: id})
			}
		}
		if err := g.AddEdge(Edge{From: from, To: to, Kind: EdgeDependsOn}); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func TestReduceTransitive(t *testing.T) {
	cases := []struct {
		name    string
		edges   []string
		want    []string
		removed int
	}{
		{"chain with shortcuts", []string{"a>b", "b>c", "c>d", "a>c", "a>d", "b>d"}, []string{"a>b", "b>c", "c>d"}, 3},
		{"diamond keeps both sides", []string{"a>b", "a>c", "b>d", "c>d"}, []string{"a>b", "a>c", "b>d", "c>d"}, 0},
		{"diamond with shortcut", []string{"a>b", "a>c", "b>d", "c>d", "a>d"}, []string{"a>b", "a>c", "b>d", "c>d"}, 1},
		{"cycle is left alone", []string{"a>b", "b>c", "c>a", "a>c"}, []string{"a>b", "a>c", "b>c", "c>a"}, 0},
		{"nothing to do", []string{"a>b"}, []string{"a>b"}, 0},
	}
	for _, c := range cases {
		g := build(t, c.edges...)
		removed := g.ReduceTransitive(EdgeDependsOn)
		got := edgeList(g)
		if removed != c.removed || len(got) != len(c.want) {
			t.Errorf("%s: removed %d, left %v; want %d, %v", c.name, removed, got, c.removed, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: left %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

// A node whose code says exactly what it refers to keeps a shortcut that
// would otherwise look implied.
func TestReduceTransitiveKeepsEdgesKnownToBeDirect(t *testing.T) {
	g := build(t, "a>b", "b>c", "a>c", "x>b", "x>c")
	g.Update("a", func(n *Node) { n.DependenciesDirect = true })
	if removed := g.ReduceTransitive(EdgeDependsOn); removed != 1 {
		t.Errorf("removed %d edges, want 1", removed)
	}
	got := strings.Join(edgeList(g), " ")
	if want := "a>b a>c b>c x>b"; got != want {
		t.Errorf("left %q, want %q", got, want)
	}
}

func TestReduceTransitiveIgnoresOtherKinds(t *testing.T) {
	g := build(t, "a>b", "b>c")
	if err := g.AddEdge(Edge{From: "a", To: "c", Kind: "other"}); err != nil {
		t.Fatal(err)
	}
	if removed := g.ReduceTransitive(EdgeDependsOn); removed != 0 {
		t.Errorf("removed %d edges of another kind", removed)
	}
}

func TestIterators(t *testing.T) {
	g := build(t, "a>b", "b>c")
	nodes, edges := 0, 0
	for range g.AllNodes() {
		nodes++
	}
	for range g.AllEdges() {
		edges++
	}
	wantNodes, wantEdges := g.Len()
	if nodes != 3 || edges != 2 || wantNodes != 3 || wantEdges != 2 {
		t.Errorf("iterated %d nodes and %d edges; Len says %d and %d", nodes, edges, wantNodes, wantEdges)
	}
}

func TestWithheldAttributesAreNeverSerialized(t *testing.T) {
	shown := Node{ID: "a", Attributes: map[string]any{"password": "hunter2"}}
	withheld := shown
	withheld.AttributesWithheld = true

	out, err := json.Marshal([]Node{shown, withheld})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(out), "hunter2"); got != 1 {
		t.Errorf("the attributes appear %d times, want once, for the node that shows them: %s", got, out)
	}
	if withheld.Attributes == nil {
		t.Error("serializing removed the attributes from the node itself")
	}
	var back []Node
	if err := json.Unmarshal(out, &back); err != nil || !back[1].AttributesWithheld || back[1].Attributes != nil {
		t.Errorf("round trip: %+v, %v", back, err)
	}
}

// WriteJSON spells the layout out by hand, so it is checked against what
// the standard encoder makes of the same document.
func TestWriteJSONMatchesTheStandardEncoder(t *testing.T) {
	full := build(t, "a>b", "b>c")
	full.Update("a", func(n *Node) {
		n.Attributes = map[string]any{"name": "<a & b>", "tags": map[string]any{"k": []any{1, "two"}}}
	})
	if err := full.AddEdge(Edge{From: "a", To: "c", Kind: EdgeLinks, Via: "b"}); err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]*Graph{"full": full, "empty": New()} {
		d := g.Document()
		var want, got bytes.Buffer
		enc := json.NewEncoder(&want)
		enc.SetIndent("", "  ")
		if err := enc.Encode(d); err != nil {
			t.Fatal(err)
		}
		if err := d.WriteJSON(&got); err != nil {
			t.Fatal(err)
		}
		if got.String() != want.String() {
			t.Errorf("%s:\n--- WriteJSON\n%s--- encoding/json\n%s", name, got.String(), want.String())
		}
	}
}
