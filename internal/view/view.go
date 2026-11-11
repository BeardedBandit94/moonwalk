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
}

// Link says that resources in one item depend on resources in another.
// Links join the items that stand for resources in a view: single resources
// and collapsed groups.
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Count is the number of dependencies the link stands for.
	Count int `json:"count"`
}

// View is what is visible for one choice of expanded groups.
type View struct {
	Items []Item // In display order: each group is followed by its contents.
	Links []Link // Ordered by From, then To.
}

// Document is the serializable form of a view.
type Document struct {
	SchemaVersion int         `json:"schema_version"`
	GroupBy       []Dimension `json:"group_by"`
	// Depth is how far the view is expanded; MaxDepth shows every resource.
	Depth     int    `json:"depth"`
	MaxDepth  int    `json:"max_depth"`
	Resources int    `json:"resources"`
	Items     []Item `json:"items"`
	Links     []Link `json:"links"`
}

// AtDepth returns the view with every group above the given depth expanded:
// depth 1 shows only the top-level groups.
func (t *Tree) AtDepth(depth int) View {
	return t.View(func(grp *Group) bool { return grp.Depth < depth })
}

// Document returns the view at the given depth in serializable form.
func (t *Tree) Document(depth int) Document {
	v := t.AtDepth(depth)
	return Document{
		SchemaVersion: SchemaVersion,
		GroupBy:       t.dims,
		Depth:         depth,
		MaxDepth:      t.Depth(),
		Resources:     t.Resources(),
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
				Depth: grp.Depth + 1, Resources: 1,
			})
			shownAs[id] = id
		}
	}
	show(t.root)

	counts := map[[2]string]int{}
	for e := range t.g.AllEdges() {
		if e.Kind != graph.EdgeDependsOn {
			continue
		}
		from, to := shownAs[e.From], shownAs[e.To]
		if from != to {
			counts[[2]string{from, to}]++
		}
	}
	v.Links = make([]Link, 0, len(counts))
	for pair, n := range counts {
		v.Links = append(v.Links, Link{From: pair[0], To: pair[1], Count: n})
	}
	slices.SortFunc(v.Links, func(a, b Link) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		return strings.Compare(a.To, b.To)
	})
	return v
}
