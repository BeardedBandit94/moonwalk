package snapshot

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Change says how one resource differs between two graphs.
type Change struct {
	ID string `json:"id"`
	// What says which fields differ and how, such as "status: managed →
	// drifted". Attribute values are not given, only their names.
	What []string `json:"what"`
}

// Diff is what differs between an older graph and a newer one.
type Diff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []Change `json:"changed"`
	// DependenciesAdded and DependenciesRemoved hold "from → to" pairs
	// between resources that both graphs have.
	DependenciesAdded   []string `json:"dependencies_added"`
	DependenciesRemoved []string `json:"dependencies_removed"`
}

// Compare returns what differs between older and newer.
//
// Resources are matched by ID, which starts with the name of the source a
// resource came from. Where each side has a single source and the two are
// named differently, as two state files compared by path are, the source
// is left out and resources are matched and reported by address alone.
func Compare(older, newer *graph.Graph) Diff {
	d := Diff{Added: []string{}, Removed: []string{}, Changed: []Change{}, DependenciesAdded: []string{}, DependenciesRemoved: []string{}}
	oldKey, newKey := keys(older, newer)

	before := map[string]graph.Node{}
	for n := range older.AllNodes() {
		before[oldKey(n.ID)] = n
	}
	after := map[string]bool{}
	for _, n := range newer.Nodes() {
		k := newKey(n.ID)
		after[k] = true
		o, ok := before[k]
		if !ok {
			d.Added = append(d.Added, k)
			continue
		}
		if what := differences(o, n); len(what) > 0 {
			d.Changed = append(d.Changed, Change{ID: k, What: what})
		}
	}
	for _, o := range older.Nodes() {
		if k := oldKey(o.ID); !after[k] {
			d.Removed = append(d.Removed, k)
		}
	}

	// Only among resources present on both sides: a dependency of a
	// resource that came or went says nothing more than that it did.
	type pair struct{ from, to string }
	onBoth := func(k string) bool {
		_, was := before[k]
		return was && after[k]
	}
	dependencies := func(g *graph.Graph, key func(string) string) map[pair]bool {
		set := map[pair]bool{}
		for e := range g.AllEdges() {
			p := pair{key(e.From), key(e.To)}
			if e.Kind == graph.EdgeDependsOn && onBoth(p.from) && onBoth(p.to) {
				set[p] = true
			}
		}
		return set
	}
	had, has := dependencies(older, oldKey), dependencies(newer, newKey)
	for p := range has {
		if !had[p] {
			d.DependenciesAdded = append(d.DependenciesAdded, p.from+" → "+p.to)
		}
	}
	for p := range had {
		if !has[p] {
			d.DependenciesRemoved = append(d.DependenciesRemoved, p.from+" → "+p.to)
		}
	}
	sort.Strings(d.DependenciesAdded)
	sort.Strings(d.DependenciesRemoved)
	return d
}

// Sources returns the names of the sources g's resources came from.
func Sources(g *graph.Graph) []string {
	seen := map[string]bool{}
	var names []string
	for n := range g.AllNodes() {
		if !seen[n.Source] {
			seen[n.Source] = true
			names = append(names, n.Source)
		}
	}
	sort.Strings(names)
	return names
}

// keys returns the functions that turn an ID of each side into what the
// two sides are matched by.
func keys(older, newer *graph.Graph) (oldKey, newKey func(string) string) {
	same := func(id string) string { return id }
	o, n := Sources(older), Sources(newer)
	if len(o) != 1 || len(n) != 1 || o[0] == n[0] {
		return same, same
	}
	without := func(source string) func(string) string {
		return func(id string) string { return strings.TrimPrefix(id, source+"::") }
	}
	return without(o[0]), without(n[0])
}

// Empty reports whether nothing differs.
func (d Diff) Empty() bool {
	return len(d.Added)+len(d.Removed)+len(d.Changed)+len(d.DependenciesAdded)+len(d.DependenciesRemoved) == 0
}

// differences lists how a resource changed between two graphs.
func differences(o, n graph.Node) []string {
	var what []string
	field := func(name string, before, after any) {
		if before != after {
			what = append(what, fmt.Sprintf("%s: %s → %s", name, show(before), show(after)))
		}
	}
	field("status", string(o.Status), string(n.Status))
	field("drift", o.Drift, n.Drift)
	field("planned change", o.Change, n.Change)
	field("account", o.Account, n.Account)
	field("region", o.Region, n.Region)
	field("in code", !o.NotInCode, !n.NotInCode)
	field("found in the cloud listing", !o.NotListed, !n.NotListed)

	// Attributes are compared only where both sides have them to show.
	if o.Attributes != nil && n.Attributes != nil && !o.AttributesWithheld && !n.AttributesWithheld {
		names := map[string]bool{}
		for k, v := range n.Attributes {
			if before, had := o.Attributes[k]; !had || !reflect.DeepEqual(before, v) {
				names[k] = true
			}
		}
		for k := range o.Attributes {
			if _, has := n.Attributes[k]; !has {
				names[k] = true
			}
		}
		if len(names) > 0 {
			sorted := make([]string, 0, len(names))
			for k := range names {
				sorted = append(sorted, k)
			}
			sort.Strings(sorted)
			what = append(what, "attributes: "+strings.Join(sorted, ", "))
		}
	}
	return what
}

func show(v any) string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return "none"
		}
		return t
	case bool:
		if t {
			return "yes"
		}
		return "no"
	}
	return fmt.Sprint(v)
}
