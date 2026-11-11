package graph

import "testing"

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
	for _, e := range []Edge{{"c", "a", EdgeDependsOn}, {"b", "a", EdgeDependsOn}, {"c", "a", EdgeDependsOn}} {
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
