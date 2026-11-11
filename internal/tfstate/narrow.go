package tfstate

import (
	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfaddr"
)

// narrower decides which instances of a resource block a dependency means.
//
// State records a dependency on a block with count or for_each as a
// dependency on the block, not on an instance. Where the dependent
// resource's attributes hold a value that belongs to only some of the
// instances, such as one subnet's ID, those are the ones meant. If its
// attributes point at none of them, every instance is kept: there is
// nothing to go by.
type narrower struct {
	nodes map[string]*graph.Node
	// blockOf names the block of each resource that is one of several
	// instances; members lists the instances of each block.
	blockOf map[string]string
	members map[string][]string
	// marks[block][id] holds the attribute values that only that instance
	// of the block has. Filled in on first use.
	marks map[string]map[string][]string
}

func newNarrower(rs []resource) *narrower {
	nw := &narrower{
		nodes:   make(map[string]*graph.Node, len(rs)),
		blockOf: map[string]string{},
		members: map[string][]string{},
		marks:   map[string]map[string][]string{},
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
		}
		kept = append(kept, used...)
	}
	return kept, held
}

// collectStrings gathers every non-empty, unredacted string in v into set.
func collectStrings(v any, set map[string]struct{}) {
	switch t := v.(type) {
	case string:
		if t != "" && t != Redacted {
			set[t] = struct{}{}
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
