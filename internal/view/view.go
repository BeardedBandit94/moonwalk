package view

import (
	"slices"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Budget is the number of items a single view should stay within to remain
// readable.
const Budget = 300

// SchemaVersion identifies the layout of Document. It increases whenever a
// field is removed or changes meaning; adding a field does not change it.
const SchemaVersion = 1

// Item is one thing shown in a view: a group, expanded or collapsed, or a
// single resource.
type Item struct {
	// ID is the group's ID, or for a resource its node ID in the graph.
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	Label string `json:"label"`
	// Parent is the ID of the expanded group this item sits in, or empty at
	// the top level.
	Parent string `json:"parent,omitempty"`
	Depth  int    `json:"depth"`
	// Resources is how many resources the item stands for: 1 for a resource.
	Resources int `json:"resources"`
	// Expanded is true for a group whose contents are also in the view.
	Expanded bool `json:"expanded,omitempty"`
	// Status is set for a resource that is anything but managed, such as
	// one declared in code and not applied yet.
	Status graph.Status `json:"status,omitempty"`
	// Drift says how a drifted resource differs from its state: "changed"
	// or "deleted".
	Drift string `json:"drift,omitempty"`
	// Unmanaged, Drifted and Declared count, for a group, the resources in
	// it with those statuses.
	Unmanaged int `json:"unmanaged,omitempty"`
	Drifted   int `json:"drifted,omitempty"`
	Declared  int `json:"declared,omitempty"`
	// Change is what a loaded plan intends for a resource: "create",
	// "update", "delete" or "replace".
	Change string `json:"change,omitempty"`
	// NotInCode is true for a resource in state that the loaded code no
	// longer declares.
	NotInCode bool `json:"not_in_code,omitempty"`
}

// Link joins two items that stand for resources in a view: single resources
// and collapsed groups. With kind depends_on, resources in From depend on
// resources in To. With kind links, glue resources attach the two sides to
// each other, in no direction; From is the smaller ID.
type Link struct {
	From string         `json:"from"`
	To   string         `json:"to"`
	Kind graph.EdgeKind `json:"kind"`
	// Count is the number of edges the link stands for.
	Count int `json:"count"`
}

// View is what is visible for one choice of expanded groups.
type View struct {
	Items []Item // In display order: each group is followed by its contents.
	Links []Link // Ordered by From, then To, then Kind.
}

// Document is the serializable form of a view.
type Document struct {
	SchemaVersion int         `json:"schema_version"`
	GroupBy       []Dimension `json:"group_by"`
	// Depth is how far the view is expanded; MaxDepth shows every resource.
	Depth    int `json:"depth"`
	MaxDepth int `json:"max_depth"`
	// Resources counts the resources the view stands for. Total counts all
	// those loaded; the difference is what a filter left out and the glue
	// resources folded into links, counted by GlueFolded.
	Resources  int    `json:"resources"`
	Total      int    `json:"total"`
	GlueFolded int    `json:"glue_folded"`
	Items      []Item `json:"items"`
	Links      []Link `json:"links"`
}

// AtDepth returns the view with every group above the given depth expanded:
// depth 1 shows only the top-level groups.
func (t *Tree) AtDepth(depth int) View {
	return t.View(func(grp *Group) bool { return grp.Depth < depth })
}

// Document returns the view at the given depth in serializable form.
func (t *Tree) Document(depth int) Document {
	return t.document(t.AtDepth(depth), depth)
}

// DocumentOpen returns the view in which the groups with the given IDs are
// expanded, along with every group they sit in, so that each of them is
// visible. IDs that name no group are ignored. Depth is 0 in the result.
func (t *Tree) DocumentOpen(ids []string) Document {
	open := map[*Group]bool{}
	for _, id := range ids {
		for grp := t.groups[id]; grp != nil && !open[grp]; grp = grp.Parent {
			open[grp] = true
		}
	}
	return t.document(t.View(func(grp *Group) bool { return open[grp] }), 0)
}

func (t *Tree) document(v View, depth int) Document {
	return Document{
		SchemaVersion: SchemaVersion,
		GroupBy:       t.opts.Dimensions,
		Depth:         depth,
		MaxDepth:      t.Depth(),
		Resources:     t.Resources(),
		Total:         t.total,
		GlueFolded:    t.glue,
		Items:         v.Items,
		Links:         v.Links,
	}
}

// View returns the items visible when exactly the groups for which expanded
// returns true are open, and the links between them. A group inside a
// collapsed group is not visible, whatever expanded says about it.
func (t *Tree) View(expanded func(*Group) bool) View {
	var v View
	// shownAs maps every resource to the visible item that stands for it.
	shownAs := make(map[string]string, t.Resources())

	var hide func(grp *Group, as string)
	hide = func(grp *Group, as string) {
		for _, id := range grp.Nodes {
			shownAs[id] = as
		}
		for _, c := range grp.Groups {
			hide(c, as)
		}
	}
	var show func(grp *Group)
	show = func(grp *Group) {
		for _, c := range grp.Groups {
			open := expanded(c)
			v.Items = append(v.Items, Item{
				ID: c.ID, Kind: c.Kind, Label: c.Name, Parent: grp.ID,
				Depth: c.Depth, Resources: c.Resources, Expanded: open,
				Unmanaged: c.Unmanaged, Drifted: c.Drifted, Declared: c.Declared,
			})
			if open {
				show(c)
			} else {
				hide(c, c.ID)
			}
		}
		for _, id := range grp.Nodes {
			v.Items = append(v.Items, Item{
				ID: id, Kind: KindResource, Label: t.labels[id], Parent: grp.ID,
				Depth: grp.Depth + 1, Resources: 1, Status: t.statuses[id],
				Change: t.changes[id], NotInCode: t.notInCode[id], Drift: t.drifts[id],
			})
			shownAs[id] = id
		}
	}
	show(t.root)

	counts := map[Link]int{}
	for e := range t.g.AllEdges() {
		// A link stands in for a glue resource; when the glue itself is
		// shown, its own dependencies say the same thing.
		if e.Kind != graph.EdgeDependsOn && (e.Kind != graph.EdgeLinks || t.opts.ShowGlue) {
			continue
		}
		// An end may be missing: filtered out, or folded glue.
		from, ok := shownAs[e.From]
		to, ok2 := shownAs[e.To]
		if !ok || !ok2 || from == to {
			continue
		}
		if e.Kind == graph.EdgeLinks && to < from {
			from, to = to, from
		}
		counts[Link{From: from, To: to, Kind: e.Kind}]++
	}
	v.Links = make([]Link, 0, len(counts))
	for l, n := range counts {
		l.Count = n
		v.Links = append(v.Links, l)
	}
	slices.SortFunc(v.Links, func(a, b Link) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		if c := strings.Compare(a.To, b.To); c != 0 {
			return c
		}
		return strings.Compare(string(a.Kind), string(b.Kind))
	})
	return v
}
