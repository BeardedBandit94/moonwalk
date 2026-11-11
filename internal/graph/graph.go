// Package graph defines moonwalk's normalized resource graph.
//
// Every input (state, HCL, live discovery) is translated by an adapter into
// this one model, so the rest of the tool never depends on a source format.
package graph

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"sort"
)

// Mode distinguishes resources Terraform manages from data sources it reads.
type Mode string

const (
	ModeManaged Mode = "managed"
	ModeData    Mode = "data"
)

// Status says how a node relates to infrastructure as code.
type Status string

const (
	// StatusManaged means the resource is tracked in state.
	StatusManaged Status = "managed"
	// StatusUnmanaged means the resource exists in the cloud but not in state.
	StatusUnmanaged Status = "unmanaged"
	// StatusDrifted means the resource is tracked but differs from reality.
	StatusDrifted Status = "drifted"
)

// EdgeKind names the relationship an edge represents.
type EdgeKind string

const (
	// EdgeDependsOn means From was created using values from To.
	EdgeDependsOn EdgeKind = "depends_on"
	// EdgeContains means To sits inside From, as a subnet sits inside a
	// network. A node has at most one container.
	EdgeContains EdgeKind = "contains"
	// EdgeLinks means From and To are attached to each other by the glue
	// resource named in Via. The link has no direction; From is the smaller ID.
	EdgeLinks EdgeKind = "links"
)

// RegionGlobal is the Region of a resource that exists once per account
// rather than in a region, such as an IAM role.
const RegionGlobal = "global"

// Node is one resource instance.
type Node struct {
	// ID is unique within a graph. Adapters build it from Source and Address
	// so that the same address in two states stays distinct.
	ID string `json:"id"`
	// Source names the input the node came from, such as a state file. It is
	// the first partition: nodes from one source were loaded together.
	Source string `json:"source"`
	// Address is the instance address within Source, for example
	// `module.net.aws_subnet.private["a"]`.
	Address string `json:"address"`
	// ConfigAddress is Address with instance keys removed. All instances
	// created by one resource block share it.
	ConfigAddress string `json:"config_address"`
	// Account is the cloud account, subscription or project that owns the
	// resource, and Region is where it lives. Both are best effort: they are
	// empty when unknown, and Region is also empty for global resources.
	Account string `json:"account,omitempty"`
	Region  string `json:"region,omitempty"`
	// AccountInferred is true when Account was not stated by the resource
	// itself but taken from other resources that share its provider.
	AccountInferred bool `json:"account_inferred,omitempty"`
	// RegionInferred is the same for Region.
	RegionInferred bool `json:"region_inferred,omitempty"`
	// Glue is true for a resource that only attaches two others to each
	// other, such as a route table association. It is represented by an
	// EdgeLinks edge between the two, and views leave it out.
	Glue bool `json:"glue,omitempty"`
	// Module is the module instance path, or "" for the root module.
	Module   string `json:"module,omitempty"`
	Mode     Mode   `json:"mode"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Status   Status `json:"status"`
	// Attributes holds the resource's attribute values with secrets already
	// redacted by the adapter. It may be nil.
	Attributes map[string]any `json:"attributes,omitempty"`
	// AttributesWithheld is true when the adapter could not tell which
	// attributes are secret, so none may be shown. Attributes is still
	// filled in, for working out relationships, but is never serialized.
	AttributesWithheld bool `json:"attributes_withheld,omitempty"`
}

// MarshalJSON leaves out the attributes of a node that withholds them, so
// that no way of writing a node out can leak them.
func (n Node) MarshalJSON() ([]byte, error) {
	type plain Node // Without this method, to avoid recursing.
	p := plain(n)
	if p.AttributesWithheld {
		p.Attributes = nil
	}
	return json.Marshal(p)
}

// Edge is a directed relationship between two nodes, by ID.
type Edge struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Kind EdgeKind `json:"kind"`
	// Via is the ID of the glue resource behind an EdgeLinks edge.
	Via string `json:"via,omitempty"`
}

// Graph is a set of nodes and the edges between them.
// The zero value is not usable; call New.
type Graph struct {
	nodes map[string]*Node
	edges map[Edge]struct{}
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{nodes: map[string]*Node{}, edges: map[Edge]struct{}{}}
}

// AddNode inserts n. It is an error to add two nodes with the same ID.
func (g *Graph) AddNode(n Node) error {
	if n.ID == "" {
		return fmt.Errorf("graph: node has empty ID")
	}
	if _, dup := g.nodes[n.ID]; dup {
		return fmt.Errorf("graph: duplicate node %q", n.ID)
	}
	g.nodes[n.ID] = &n
	return nil
}

// AddEdge inserts e. Both endpoints must already exist. Adding the same edge
// twice is a no-op.
func (g *Graph) AddEdge(e Edge) error {
	if _, ok := g.nodes[e.From]; !ok {
		return fmt.Errorf("graph: edge from unknown node %q", e.From)
	}
	if _, ok := g.nodes[e.To]; !ok {
		return fmt.Errorf("graph: edge to unknown node %q", e.To)
	}
	g.edges[e] = struct{}{}
	return nil
}

// HasEdge reports whether e is present.
func (g *Graph) HasEdge(e Edge) bool {
	_, ok := g.edges[e]
	return ok
}

// RemoveEdge deletes e if it is present.
func (g *Graph) RemoveEdge(e Edge) {
	delete(g.edges, e)
}

// Update applies change to the node with the given ID. The ID itself must
// not be changed. It reports whether the node exists.
func (g *Graph) Update(id string, change func(*Node)) bool {
	n, ok := g.nodes[id]
	if ok {
		change(n)
	}
	return ok
}

// Node returns the node with the given ID.
func (g *Graph) Node(id string) (Node, bool) {
	n, ok := g.nodes[id]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// Nodes returns all nodes sorted by ID.
func (g *Graph) Nodes() []Node {
	out := make([]Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Edges returns all edges sorted by From, then To, then Kind, then Via.
func (g *Graph) Edges() []Edge {
	out := make([]Edge, 0, len(g.edges))
	for e := range g.edges {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Via < b.Via
	})
	return out
}

// SchemaVersion identifies the layout of Document. It increases whenever a
// field is removed or changes meaning; adding a field does not change it.
const SchemaVersion = 1

// AllNodes iterates over the nodes in no particular order, without the
// sorting and copying that Nodes does.
func (g *Graph) AllNodes() iter.Seq[Node] {
	return func(yield func(Node) bool) {
		for _, n := range g.nodes {
			if !yield(*n) {
				return
			}
		}
	}
}

// AllEdges iterates over the edges in no particular order.
func (g *Graph) AllEdges() iter.Seq[Edge] {
	return func(yield func(Edge) bool) {
		for e := range g.edges {
			if !yield(e) {
				return
			}
		}
	}
}

// Len returns the number of nodes and edges.
func (g *Graph) Len() (nodes, edges int) {
	return len(g.nodes), len(g.edges)
}

// ReduceTransitive removes every edge of the given kind that another path
// already implies: if a→b and b→c exist, a→c is dropped. What can be reached
// from each node stays the same. It returns the number of edges removed.
//
// Terraform state lists indirect dependencies alongside direct ones, so
// this leaves the edges that say something. If the edges of that kind form
// a cycle, nothing is removed, because the result would not be well defined.
func (g *Graph) ReduceTransitive(kind EdgeKind) int {
	next := map[string][]string{}
	incoming := map[string]int{}
	for e := range g.edges {
		if e.Kind == kind {
			next[e.From] = append(next[e.From], e.To)
			incoming[e.To]++
		}
	}
	if !acyclic(next, incoming) {
		return 0
	}

	removed := 0
	direct := map[string]struct{}{}
	for from, tos := range next {
		clear(direct)
		for _, to := range tos {
			direct[to] = struct{}{}
		}
		for _, via := range tos {
			for _, to := range next[via] {
				if _, ok := direct[to]; ok {
					delete(direct, to)
					delete(g.edges, Edge{From: from, To: to, Kind: kind})
					removed++
				}
			}
		}
	}
	return removed
}

// acyclic reports whether the edges in next contain no cycle, by repeatedly
// removing nodes that nothing points at. It consumes incoming.
func acyclic(next map[string][]string, incoming map[string]int) bool {
	var ready []string
	for from := range next {
		if incoming[from] == 0 {
			ready = append(ready, from)
		}
	}
	remaining := len(incoming)
	for len(ready) > 0 {
		from := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		for _, to := range next[from] {
			if incoming[to]--; incoming[to] == 0 {
				remaining--
				ready = append(ready, to)
			}
		}
	}
	return remaining == 0
}

// Document is the stable, serializable form of a graph.
type Document struct {
	SchemaVersion int    `json:"schema_version"`
	Nodes         []Node `json:"nodes"`
	Edges         []Edge `json:"edges"`
}

// WriteJSON writes the document as indented JSON, one node or edge at a
// time, so that a large graph is never held in memory a second time as
// text. The output is what encoding/json would produce for the whole
// document with a two-space indent.
func (d Document) WriteJSON(w io.Writer) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "{\n  \"schema_version\": %d,\n  \"nodes\": ", d.SchemaVersion)
	if err := writeArray(bw, d.Nodes); err != nil {
		return err
	}
	bw.WriteString(",\n  \"edges\": ")
	if err := writeArray(bw, d.Edges); err != nil {
		return err
	}
	bw.WriteString("\n}\n")
	return bw.Flush()
}

// writeArray writes items as a JSON array nested one level deep.
func writeArray[T any](bw *bufio.Writer, items []T) error {
	if len(items) == 0 {
		bw.WriteString("[]")
		return nil
	}
	bw.WriteString("[")
	for i, item := range items {
		text, err := json.MarshalIndent(item, "    ", "  ")
		if err != nil {
			return err
		}
		if i > 0 {
			bw.WriteString(",")
		}
		bw.WriteString("\n    ")
		bw.Write(text)
	}
	bw.WriteString("\n  ]")
	return nil
}

// Document returns the graph in deterministic order, ready to encode.
func (g *Graph) Document() Document {
	return Document{SchemaVersion: SchemaVersion, Nodes: g.Nodes(), Edges: g.Edges()}
}
