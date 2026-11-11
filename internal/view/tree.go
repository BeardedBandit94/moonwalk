// Package view arranges a graph into nested groups and produces collapsed
// views of it, so that an estate of any size can be shown a few hundred
// items at a time.
package view

import (
	"fmt"
	"slices"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfaddr"
)

// Dimension is a property of a resource that the tree can group by.
type Dimension string

const (
	BySource  Dimension = "source"
	ByAccount Dimension = "account"
	ByRegion  Dimension = "region"
	ByModule  Dimension = "module"
	// ByNetwork groups a resource under the network and subnet that
	// contain it, as recorded by the graph's containment edges.
	ByNetwork Dimension = "network"
)

// DefaultDimensions is the grouping used when none is asked for.
var DefaultDimensions = []Dimension{BySource, ByAccount, ByRegion, ByModule}

var allDimensions = []Dimension{BySource, ByAccount, ByRegion, ByModule, ByNetwork}

// AllDimensions returns every dimension a tree can be grouped by.
func AllDimensions() []Dimension { return slices.Clone(allDimensions) }

// ParseDimensions reads a comma-separated list such as "account,module".
func ParseDimensions(s string) ([]Dimension, error) {
	var dims []Dimension
	for _, part := range strings.Split(s, ",") {
		d := Dimension(strings.TrimSpace(part))
		if !slices.Contains(allDimensions, d) {
			return nil, fmt.Errorf("unknown grouping %q (want any of source, account, region, module, network)", part)
		}
		if slices.Contains(dims, d) {
			return nil, fmt.Errorf("grouping %q is given twice", d)
		}
		dims = append(dims, d)
	}
	return dims, nil
}

// Kind says what a group, or an item in a view, stands for.
type Kind string

const (
	KindSource   Kind = "source"
	KindAccount  Kind = "account"
	KindRegion   Kind = "region"
	KindModule   Kind = "module"          // A module call, such as module.app.
	KindInstance Kind = "module_instance" // One instance of a call with count or for_each.
	KindNetwork  Kind = "network"         // A network and what it contains.
	KindSubnet   Kind = "subnet"          // A subnet and what it contains.
	KindType     Kind = "type"            // Resources of one type, split out of a crowded group.
	KindBlock    Kind = "block"           // The instances of one resource block.
	KindRange    Kind = "range"           // A slice of a group that is still too crowded.
	KindResource Kind = "resource"
)

// MaxChildren is the most children any group has. A group that would have
// more gets intermediate groups, by resource type and then by range.
const MaxChildren = 100

// Group is a set of resources that share a property, and may hold further
// groups.
type Group struct {
	ID     string
	Kind   Kind
	Name   string
	Parent *Group // For a top-level group, the tree's unnamed root.
	Groups []*Group
	Nodes  []string // IDs of the resources directly inside, in display order.
	// Resources counts every resource inside, at any depth.
	Resources int
	// Depth is 1 for a top-level group and one more for each level below.
	Depth int

	resourceType string // For a block: the type of its resources.
}

// Options says how to arrange a graph.
type Options struct {
	// Dimensions are the levels to group by, outermost first.
	Dimensions []Dimension
	// Filter leaves out the resources it does not match.
	Filter Filter
	// ShowGlue keeps glue resources as items. By default they are left
	// out, and the links they stand for are shown in their place.
	ShowGlue bool
}

// Tree is a graph arranged into groups.
type Tree struct {
	g      *graph.Graph
	opts   Options
	root   *Group // Holds the top-level groups; never shown.
	groups map[string]*Group
	// byName finds a group by its parent, kind and name without building
	// its ID, which matters when placing a hundred thousand resources.
	byName map[childKey]*Group
	labels map[string]string // Node ID to the label shown for it.
	types  map[string]string // Node ID to resource type.
	// statuses holds the status of each resource that is not managed.
	statuses map[string]graph.Status
	// perDepth[d] counts the groups and resources at depth d.
	perDepth []int
	inGroup  map[string]*Group // Node ID to the group it sits in directly.
	total    int               // Resources in the graph, glue included.
	glue     int               // Glue resources left out.
}

// Build arranges g as the options say. Below the chosen dimensions,
// instances of one resource block are always kept together. A resource that
// lacks a property, such as a region, skips that level.
func Build(g *graph.Graph, o Options) *Tree {
	t := &Tree{
		g:        g,
		opts:     o,
		root:     &Group{},
		groups:   map[string]*Group{},
		byName:   map[childKey]*Group{},
		labels:   map[string]string{},
		types:    map[string]string{},
		statuses: map[string]graph.Status{},
		inGroup:  map[string]*Group{},
	}
	dims := o.Dimensions
	byModule, bySource := slices.Contains(dims, ByModule), slices.Contains(dims, BySource)

	// What contains what, for the network level and for Filter.Around.
	containerOf, isContainer := map[string]string{}, map[string]bool{}
	if slices.Contains(dims, ByNetwork) {
		for e := range g.AllEdges() {
			if e.Kind == graph.EdgeContains {
				containerOf[e.To] = e.From
				isContainer[e.From] = true
			}
		}
	}
	match := o.Filter.matcher(g)

	for _, n := range g.Nodes() {
		t.total++
		if n.Glue && !o.ShowGlue {
			t.glue++
			continue
		}
		if !match(n) {
			continue
		}
		grp := t.root
		for _, d := range dims {
			switch d {
			case BySource:
				grp = t.child(grp, KindSource, n.Source)
			case ByAccount:
				if n.Account != "" {
					grp = t.child(grp, KindAccount, n.Account)
				}
			case ByRegion:
				if n.Region != "" {
					grp = t.child(grp, KindRegion, n.Region)
				}
			case ByModule:
				// Without a source level, two states' modules of the same
				// name would merge; the outermost module carries the source.
				prefix := ""
				if !bySource {
					prefix = n.Source + "::"
				}
				for _, step := range tfaddr.ModuleSteps(n.Module) {
					grp = t.child(grp, KindModule, prefix+"module."+step.Name)
					if step.Key != "" {
						grp = t.child(grp, KindInstance, prefix+"module."+step.Name+step.Key)
					}
					prefix = ""
				}
			case ByNetwork:
				// The containers of n, outermost first. A container sits in
				// the group named after itself, next to what it contains.
				var chain []string
				if isContainer[n.ID] {
					chain = append(chain, n.ID)
				}
				for id, ok := containerOf[n.ID]; ok && len(chain) < 8; id, ok = containerOf[id] {
					chain = append(chain, id)
				}
				slices.Reverse(chain)
				for i, id := range chain {
					name := id // A node ID already carries the source.
					if c, ok := g.Node(id); ok && bySource {
						name = c.Address
					}
					kind := KindSubnet
					if i == 0 {
						kind = KindNetwork
					}
					grp = t.child(grp, kind, name)
				}
			}
		}
		label := n.Address
		inModule := byModule && n.Module != ""
		if inModule {
			// The module is already in the path above.
			label = strings.TrimPrefix(label, n.Module+".")
		}
		if !bySource && !inModule {
			// Two states can hold the same address; keep the labels apart.
			label = n.Source + "::" + label
		}
		// A container already has a group of its own to sit in.
		if block, keyed := tfaddr.Block(label); keyed && !isContainer[n.ID] {
			grp = t.child(grp, KindBlock, block)
			grp.resourceType = n.Type
		}
		grp.Nodes = append(grp.Nodes, n.ID)
		t.labels[n.ID] = label
		t.types[n.ID] = n.Type
		if n.Status != graph.StatusManaged && n.Status != "" {
			t.statuses[n.ID] = n.Status
		}
	}
	t.bound(t.root)
	t.finish(t.root, 0)
	return t
}

type childKey struct {
	parent *Group
	kind   Kind
	name   string
}

// child returns the group of the given kind and name inside parent,
// creating it if needed.
func (t *Tree) child(parent *Group, kind Kind, name string) *Group {
	key := childKey{parent, kind, name}
	if grp, ok := t.byName[key]; ok {
		return grp
	}
	grp := &Group{ID: groupID(parent, kind, name), Kind: kind, Name: name, Parent: parent}
	t.byName[key] = grp
	t.groups[grp.ID] = grp
	parent.Groups = append(parent.Groups, grp)
	return grp
}

var escapeSegment = strings.NewReplacer("%", "%25", "/", "%2F")

// groupID extends the parent's ID with one "kind:name" segment. Slashes in
// the name are escaped so the segments stay separable.
func groupID(parent *Group, kind Kind, name string) string {
	seg := string(kind) + ":" + escapeSegment.Replace(name)
	if parent.ID == "" {
		return seg
	}
	return parent.ID + "/" + seg
}

// bound makes sure no group in the subtree has more than MaxChildren
// children, first by gathering resources of the same type, then by cutting
// what is left into ranges.
func (t *Tree) bound(grp *Group) {
	if len(grp.Groups)+len(grp.Nodes) > MaxChildren {
		t.splitByType(grp)
	}
	for len(grp.Groups)+len(grp.Nodes) > MaxChildren {
		t.splitByRange(grp)
	}
	for _, c := range grp.Groups {
		t.bound(c)
	}
}

// splitByType moves the resources and blocks directly inside grp into one
// group per resource type. It does nothing if they are all of one type.
func (t *Tree) splitByType(grp *Group) {
	seen := map[string]bool{}
	for _, id := range grp.Nodes {
		seen[t.types[id]] = true
	}
	for _, c := range grp.Groups {
		if c.Kind == KindBlock {
			seen[c.resourceType] = true
		}
	}
	if len(seen) < 2 {
		return
	}

	nodes, groups := grp.Nodes, grp.Groups
	grp.Nodes, grp.Groups = nil, nil
	for _, c := range groups {
		if c.Kind != KindBlock {
			grp.Groups = append(grp.Groups, c)
		}
	}
	for _, c := range groups {
		if c.Kind == KindBlock {
			byType := t.child(grp, KindType, c.resourceType)
			c.Parent = byType
			byType.Groups = append(byType.Groups, c)
		}
	}
	for _, id := range nodes {
		byType := t.child(grp, KindType, t.types[id])
		byType.Nodes = append(byType.Nodes, id)
	}
}

// splitByRange replaces the children of grp with ranges of at most
// MaxChildren of them each, in display order.
func (t *Tree) splitByRange(grp *Group) {
	t.sortChildren(grp)
	groups, nodes := grp.Groups, grp.Nodes
	grp.Groups, grp.Nodes = nil, nil

	label := func(i int) string {
		if i < len(groups) {
			return groups[i].Name
		}
		return t.labels[nodes[i-len(groups)]]
	}
	total := len(groups) + len(nodes)
	for start := 0; start < total; start += MaxChildren {
		end := min(start+MaxChildren, total)
		// The ID is built from positions, not labels, so two ranges can
		// never be mistaken for one another.
		r := &Group{
			ID:     groupID(grp, KindRange, fmt.Sprintf("%d-%d", start+1, end)),
			Kind:   KindRange,
			Name:   label(start) + " … " + label(end-1),
			Parent: grp,
		}
		t.groups[r.ID] = r
		grp.Groups = append(grp.Groups, r)
		for i := start; i < end; i++ {
			if i < len(groups) {
				groups[i].Parent = r
				r.Groups = append(r.Groups, groups[i])
			} else {
				r.Nodes = append(r.Nodes, nodes[i-len(groups)])
			}
		}
	}
}

func (t *Tree) sortChildren(grp *Group) {
	slices.SortFunc(grp.Groups, func(a, b *Group) int {
		if c := naturalCompare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(grp.Nodes, func(a, b string) int {
		if c := naturalCompare(t.labels[a], t.labels[b]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
}

// finish orders the children of every group and fills in depths and counts.
func (t *Tree) finish(grp *Group, depth int) {
	grp.Depth = depth
	// Ranges were cut in display order and must keep it: sorted by name, a
	// range of groups could land after a range of resources.
	if len(grp.Groups) == 0 || grp.Groups[0].Kind != KindRange {
		t.sortChildren(grp)
	}
	grp.Resources = len(grp.Nodes)
	t.count(depth)
	for _, id := range grp.Nodes {
		t.count(depth + 1)
		t.inGroup[id] = grp
	}
	for _, c := range grp.Groups {
		t.finish(c, depth+1)
		grp.Resources += c.Resources
	}
}

func (t *Tree) count(depth int) {
	for len(t.perDepth) <= depth {
		t.perDepth = append(t.perDepth, 0)
	}
	t.perDepth[depth]++
}

// Dimensions returns the dimensions the tree was built with.
func (t *Tree) Dimensions() []Dimension { return t.opts.Dimensions }

// Total returns the number of resources in the graph, whether or not the
// tree shows them.
func (t *Tree) Total() int { return t.total }

// GlueFolded returns the number of glue resources left out of the tree.
func (t *Tree) GlueFolded() int { return t.glue }

// Top returns the top-level groups.
func (t *Tree) Top() []*Group { return t.root.Groups }

// Group returns the group with the given ID.
func (t *Tree) Group(id string) (*Group, bool) {
	grp, ok := t.groups[id]
	return grp, ok
}

// GroupOf returns the ID of the group a resource sits in directly. It is
// empty if the tree does not show the resource, or shows it at the top level.
func (t *Tree) GroupOf(nodeID string) string {
	if grp := t.inGroup[nodeID]; grp != nil {
		return grp.ID
	}
	return ""
}

// NodesUnder returns the IDs of the resources an item of a view stands for:
// every resource inside a group, or the resource itself. It returns nil for
// an ID the tree does not show.
func (t *Tree) NodesUnder(itemID string) []string {
	if grp, ok := t.groups[itemID]; ok {
		var ids []string
		var walk func(*Group)
		walk = func(g *Group) {
			ids = append(ids, g.Nodes...)
			for _, c := range g.Groups {
				walk(c)
			}
		}
		walk(grp)
		return ids
	}
	if t.Shows(itemID) {
		return []string{itemID}
	}
	return nil
}

// Shows reports whether the tree shows the resource.
func (t *Tree) Shows(nodeID string) bool {
	_, ok := t.inGroup[nodeID]
	return ok
}

// Label returns the name a resource is shown under: its address, without
// the module path when modules are part of the grouping.
func (t *Tree) Label(nodeID string) string { return t.labels[nodeID] }

// Resources returns the number of resources the tree shows.
func (t *Tree) Resources() int { return t.root.Resources }

// Depth returns the depth of the deepest item, which is the depth at which
// every resource is visible.
func (t *Tree) Depth() int { return len(t.perDepth) - 1 }

// ItemsAtDepth returns how many items a view expanded to the given depth
// shows.
func (t *Tree) ItemsAtDepth(depth int) int {
	n := 0
	for d := 1; d <= depth && d < len(t.perDepth); d++ {
		n += t.perDepth[d]
	}
	return n
}

// AutoDepth returns the deepest depth whose view stays within budget items,
// and at least 1.
func (t *Tree) AutoDepth(budget int) int {
	depth := 1
	for d := 2; d <= t.Depth() && t.ItemsAtDepth(d) <= budget; d++ {
		depth = d
	}
	return depth
}

// naturalCompare orders strings so that runs of digits compare as numbers:
// "a[9]" comes before "a[10]".
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			na, nb := digits(a), digits(b)
			ta, tb := strings.TrimLeft(a[:na], "0"), strings.TrimLeft(b[:nb], "0")
			if len(ta) != len(tb) {
				return len(ta) - len(tb)
			}
			if c := strings.Compare(ta, tb); c != 0 {
				return c
			}
			a, b = a[na:], b[nb:]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digits(s string) int {
	n := 0
	for n < len(s) && isDigit(s[n]) {
		n++
	}
	return n
}
