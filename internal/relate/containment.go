package relate

import (
	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// The resource types that contain others, and the attributes by which a
// resource names the one it sits in. Only AWS is covered so far.
const (
	networkType = "aws_vpc"
	subnetType  = "aws_subnet"
)

var (
	networkAttrs = []string{"vpc_id"}
	subnetAttrs  = []string{"subnet_id", "subnet_ids", "subnets"}
)

// Containment adds an EdgeContains edge from a network or subnet to each
// resource that names it by ID, and returns how many it added.
//
// A resource in exactly one subnet is contained by that subnet. One that
// spans several subnets of the same network, such as a load balancer, is
// contained by the network. A resource is matched across states, so a
// subnet can sit in a network that another state manages.
func Containment(g *graph.Graph) int {
	nodes := g.Nodes()
	// Cloud ID to node, per container type. Sorted iteration makes the
	// choice between duplicates stable; a managed resource wins over a data
	// source that merely reads the same thing.
	byID := map[string]map[string]graph.Node{networkType: {}, subnetType: {}}
	for _, n := range nodes {
		index, ok := byID[n.Type]
		if !ok {
			continue
		}
		id, _ := n.Attributes["id"].(string)
		if id == "" || id == redacted {
			continue
		}
		if cur, dup := index[id]; !dup || (cur.Mode != graph.ModeManaged && n.Mode == graph.ModeManaged) {
			index[id] = n
		}
	}
	networkOf := func(n graph.Node) (graph.Node, bool) {
		for _, id := range references(n.Attributes, networkAttrs) {
			if network, ok := byID[networkType][id]; ok {
				return network, true
			}
		}
		return graph.Node{}, false
	}

	added := 0
	for _, n := range nodes {
		if n.Type == networkType {
			continue
		}
		container, found := graph.Node{}, false
		if n.Type != subnetType {
			subnets := map[string]graph.Node{}
			for _, id := range references(n.Attributes, subnetAttrs) {
				if subnet, ok := byID[subnetType][id]; ok {
					subnets[subnet.ID] = subnet
				}
			}
			switch {
			case len(subnets) == 1:
				for _, subnet := range subnets {
					container, found = subnet, true
				}
			case len(subnets) > 1:
				container, found = sharedNetwork(subnets, networkOf)
			}
		}
		if !found {
			container, found = networkOf(n)
		}
		if !found || container.ID == n.ID {
			continue
		}
		if err := g.AddEdge(graph.Edge{From: container.ID, To: n.ID, Kind: graph.EdgeContains}); err == nil {
			added++
		}
	}
	return added
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
	var out []string
	take := func(m map[string]any) {
		for _, name := range names {
			switch v := m[name].(type) {
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
	return out
}
