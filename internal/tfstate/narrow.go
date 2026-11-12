package tfstate

import (
	"slices"
	"sort"

	"github.com/BeardedBandit94/moonwalk/internal/azureid"
	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfaddr"
)

// narrower decides which instances of a resource block a dependency means.
//
// State records a dependency on a block with count or for_each as a
// dependency on the block, not on an instance. Where the dependent
// resource's attributes hold a value that belongs to only some of the
// instances, such as one subnet's ID, those are the ones meant.
//
// If its attributes point at none of them, the dependency is most often
// one that state lists because something in between has it: a virtual
// machine holds no subnet's ID, only its network interface's, and state
// still lists the subnets. Then the instances meant are the ones that the
// resource's other dependencies depend on; see settle. If those do not say
// either, every instance is kept: there is nothing to go by.
type narrower struct {
	nodes map[string]*graph.Node
	// blockOf names the block of each resource that is one of several
	// instances; members lists the instances of each block.
	blockOf map[string]string
	members map[string][]string
	// marks[block][id] holds the attribute values that only that instance
	// of the block has. Filled in on first use.
	marks map[string]map[string][]string
	// open[from][block] lists the instances of a block that from was left
	// depending on because its attributes point at none of them.
	open map[string]map[string][]string
}

func newNarrower(rs []resource) *narrower {
	nw := &narrower{
		nodes:   make(map[string]*graph.Node, len(rs)),
		blockOf: map[string]string{},
		members: map[string][]string{},
		marks:   map[string]map[string][]string{},
		open:    map[string]map[string][]string{},
	}
	for i := range rs {
		n := &rs[i].node
		nw.nodes[n.ID] = n
		if block, keyed := tfaddr.Block(n.Address); keyed {
			nw.blockOf[n.ID] = block
			nw.members[block] = append(nw.members[block], n.ID)
		}
	}
	return nw
}

// marksOf returns, for each instance of a block, the top-level attribute
// values no other instance shares: an ID, an ARN, a name, an address.
func (nw *narrower) marksOf(block string) map[string][]string {
	if m, ok := nw.marks[block]; ok {
		return m
	}
	holders := map[string]int{}
	values := map[string][]string{}
	for _, id := range nw.members[block] {
		seen := map[string]struct{}{}
		for _, v := range nw.nodes[id].Attributes {
			if s, ok := v.(string); ok && s != "" && s != Redacted {
				s = azureid.Canonical(s)
				if _, dup := seen[s]; !dup {
					seen[s] = struct{}{}
					values[id] = append(values[id], s)
					holders[s]++
				}
			}
		}
	}
	m := map[string][]string{}
	for id, vs := range values {
		for _, s := range vs {
			if holders[s] == 1 {
				m[id] = append(m[id], s)
			}
		}
	}
	nw.marks[block] = m
	return m
}

// narrow returns the candidates that the resource from evidently depends
// on. held caches the strings in from's attributes; pass a nil map for each
// new resource and keep passing what comes back.
func (nw *narrower) narrow(from *graph.Node, candidates []string, held map[string]struct{}) ([]string, map[string]struct{}) {
	if len(candidates) < 2 {
		return candidates, held
	}
	// Candidates can span module instances; each block is judged alone.
	byBlock := map[string][]string{}
	var order []string
	for _, id := range candidates {
		block, keyed := nw.blockOf[id]
		if !keyed {
			block = id // A single instance: nothing to choose between.
		}
		if _, seen := byBlock[block]; !seen {
			order = append(order, block)
		}
		byBlock[block] = append(byBlock[block], id)
	}

	kept := make([]string, 0, len(candidates))
	for _, block := range order {
		instances := byBlock[block]
		if len(instances) < 2 {
			kept = append(kept, instances...)
			continue
		}
		if held == nil {
			held = map[string]struct{}{}
			collectStrings(from.Attributes, held)
		}
		marks := nw.marksOf(block)
		var used []string
		for _, id := range instances {
			for _, mark := range marks[id] {
				if _, ok := held[mark]; ok {
					used = append(used, id)
					break
				}
			}
		}
		if len(used) == 0 {
			used = instances
			if nw.open[from.ID] == nil {
				nw.open[from.ID] = map[string][]string{}
			}
			nw.open[from.ID][block] = instances
		}
		kept = append(kept, used...)
	}
	return kept, held
}

// settle narrows the dependencies that narrow left open, by what each
// resource's other dependencies depend on, and removes the edges that
// turn out not to be meant. deps holds every resource's dependencies and
// is kept in step.
//
// State lists a resource's indirect dependencies next to its direct ones.
// So when a resource depends on a whole block without pointing at any
// instance, and something else it depends on does point at some, the
// dependency on the block is that other one's, passed along, and means
// the same instances.
func (nw *narrower) settle(g *graph.Graph, deps map[string][]string) {
	if len(nw.open) == 0 {
		return
	}
	froms := make([]string, 0, len(nw.open))
	for from := range nw.open {
		froms = append(froms, from)
	}
	sort.Strings(froms) // The outcome must not depend on map order.

	// within[id][block] holds the dependencies of id that are instances
	// of block. Built on first use, and dropped when id's change.
	within := map[string]map[string][]string{}
	targetsIn := func(id, block string) []string {
		m, ok := within[id]
		if !ok {
			m = map[string][]string{}
			for _, to := range deps[id] {
				if b, keyed := nw.blockOf[to]; keyed {
					m[b] = append(m[b], to)
				}
			}
			within[id] = m
		}
		return m[block]
	}

	// A resource narrowed in one round can settle its dependents in the
	// next. Chains are short; the bound only guards against surprises.
	for round := 0; round < 16; round++ {
		changed := false
		for _, from := range froms {
			for block, instances := range nw.open[from] {
				candidate := make(map[string]bool, len(instances))
				for _, id := range instances {
					candidate[id] = true
				}
				reached := map[string]bool{}
				for _, via := range deps[from] {
					if candidate[via] {
						continue
					}
					for _, id := range targetsIn(via, block) {
						if candidate[id] {
							reached[id] = true
						}
					}
				}
				if len(reached) == 0 || len(reached) == len(instances) {
					continue
				}
				var kept []string
				for _, id := range instances {
					if reached[id] {
						kept = append(kept, id)
					} else {
						g.RemoveEdge(graph.Edge{From: from, To: id, Kind: graph.EdgeDependsOn})
					}
				}
				deps[from] = slices.DeleteFunc(deps[from], func(id string) bool { return candidate[id] && !reached[id] })
				delete(within, from)
				if len(kept) > 1 {
					nw.open[from][block] = kept
				} else {
					delete(nw.open[from], block)
				}
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

// collectStrings gathers every non-empty, unredacted string in v into set,
// each in the form it is compared by.
func collectStrings(v any, set map[string]struct{}) {
	switch t := v.(type) {
	case string:
		if t != "" && t != Redacted {
			set[azureid.Canonical(t)] = struct{}{}
		}
	case map[string]any:
		for _, e := range t {
			collectStrings(e, set)
		}
	case []any:
		for _, e := range t {
			collectStrings(e, set)
		}
	}
}
