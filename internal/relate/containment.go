package relate

import (
	"slices"
	"sort"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Containment adds an EdgeContains edge from a network or subnet to each
// resource that sits in it, and returns how many it added.
//
// Where a resource sits is worked out from the IDs it names, by rules that
// differ between clouds; see awsContainment and azureContainment. A
// resource is matched across states, so a subnet can sit in a network that
// another state manages. A resource has at most one container.
func Containment(g *graph.Graph) int {
	nodes := g.Nodes()
	return awsContainment(g, nodes) + azureContainment(g, nodes)
}

// sharedNetwork returns the network that all the given subnets sit in, if
// there is exactly one.
func sharedNetwork(subnets map[string]graph.Node, networkOf func(graph.Node) (graph.Node, bool)) (graph.Node, bool) {
	var shared graph.Node
	first := true
	for _, subnet := range subnets {
		network, ok := networkOf(subnet)
		if !ok || (!first && network.ID != shared.ID) {
			return graph.Node{}, false
		}
		shared, first = network, false
	}
	return shared, !first
}

// references returns the strings held under any of the given attribute
// names, at the top level or one block down, where providers put settings
// such as vpc_config. A name may hold one string or a list of them.
func references(attrs map[string]any, names []string) []string {
	return referencesWhere(attrs, func(name string) bool { return slices.Contains(names, name) })
}

// referencesWhere is references for the attribute names that named accepts.
func referencesWhere(attrs map[string]any, named func(string) bool) []string {
	var out []string
	take := func(m map[string]any) {
		for name, value := range m {
			if !named(name) {
				continue
			}
			switch v := value.(type) {
			case string:
				out = append(out, v)
			case []any:
				for _, e := range v {
					if s, ok := e.(string); ok {
						out = append(out, s)
					}
				}
			}
		}
	}
	take(attrs)
	for _, v := range attrs {
		switch block := v.(type) {
		case map[string]any:
			take(block)
		case []any:
			for _, e := range block {
				if m, ok := e.(map[string]any); ok {
					take(m)
				}
			}
		}
	}
	sort.Strings(out) // Attributes come in no order; the result must not depend on it.
	return out
}
