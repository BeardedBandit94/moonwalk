package relate

import (
	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfaddr"
)

// NarrowDependencies removes dependency edges to instances of a resource
// block that the dependent resource evidently does not use, and returns how
// many it removed.
//
// State records a dependency on a block with count or for_each as a
// dependency on the block, so the graph starts with an edge to every
// instance. Where the dependent resource's attributes hold a value that
// belongs to only some of those instances, such as one subnet's ID, the
// edges to the others are dropped. If its attributes point at none of them,
// all edges stay: there is nothing to go by.
func NarrowDependencies(g *graph.Graph) int {
	type blockKey struct{ source, block string }
	keyOf := func(n graph.Node) (blockKey, bool) {
		block, keyed := tfaddr.Block(n.Address)
		return blockKey{n.Source, block}, keyed
	}

	members := map[blockKey][]graph.Node{}
	for n := range g.AllNodes() {
		if key, keyed := keyOf(n); keyed {
			members[key] = append(members[key], n)
		}
	}
	// marks[key][id] holds the attribute values that only that instance of
	// the block has. Computed on first use.
	marks := map[blockKey]map[string][]string{}
	marksOf := func(key blockKey) map[string][]string {
		if m, ok := marks[key]; ok {
			return m
		}
		holders := map[string]int{}
		values := map[string]map[string]struct{}{}
		for _, n := range members[key] {
			set := map[string]struct{}{}
			for _, v := range n.Attributes {
				// Top-level values only: an ID, an ARN, a name, an address.
				if s, ok := v.(string); ok && s != "" && s != redacted {
					set[s] = struct{}{}
				}
			}
			values[n.ID] = set
			for s := range set {
				holders[s]++
			}
		}
		m := map[string][]string{}
		for id, set := range values {
			for s := range set {
				if holders[s] == 1 {
					m[id] = append(m[id], s)
				}
			}
		}
		marks[key] = m
		return m
	}

	targets := map[string][]string{}
	for e := range g.AllEdges() {
		if e.Kind == graph.EdgeDependsOn {
			targets[e.From] = append(targets[e.From], e.To)
		}
	}

	removed := 0
	for from, tos := range targets {
		byBlock := map[blockKey][]string{}
		for _, to := range tos {
			if n, ok := g.Node(to); ok {
				if key, keyed := keyOf(n); keyed {
					byBlock[key] = append(byBlock[key], to)
				}
			}
		}
		var held map[string]struct{} // The dependent's own strings, if needed.
		for key, instances := range byBlock {
			if len(instances) < 2 {
				continue
			}
			if held == nil {
				held = map[string]struct{}{}
				if n, ok := g.Node(from); ok {
					collectStrings(n.Attributes, held)
				}
			}
			used := map[string]bool{}
			for _, to := range instances {
				for _, mark := range marksOf(key)[to] {
					if _, ok := held[mark]; ok {
						used[to] = true
						break
					}
				}
			}
			if len(used) == 0 || len(used) == len(instances) {
				continue
			}
			for _, to := range instances {
				if !used[to] {
					g.RemoveEdge(graph.Edge{From: from, To: to, Kind: graph.EdgeDependsOn})
					removed++
				}
			}
		}
	}
	return removed
}
