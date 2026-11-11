package relate

import (
	"slices"
	"strings"

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
	// A database, cache or similar names a subnet group instead of subnets.
	subnetGroupAttrs = []string{
		"db_subnet_group_name", "subnet_group_name", "cluster_subnet_group_name", "replication_subnet_group_id",
	}
)

// subnetGroupType reports whether a resource type is a named group of
// subnets, such as aws_db_subnet_group.
func subnetGroupType(resourceType string) bool {
	return strings.HasPrefix(resourceType, "aws_") && strings.HasSuffix(resourceType, "_subnet_group")
}

// Containment adds an EdgeContains edge from a network or subnet to each
// resource that names it by ID, and returns how many it added.
//
// A resource in exactly one subnet is contained by that subnet. One that
// spans several subnets of the same network, such as a load balancer, is
// contained by the network. One that names a subnet group, such as a
// database, sits where that group's subnets put it. A resource is matched
// across states, so a subnet can sit in a network that another state manages.
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
	// Subnet groups by name. Names repeat across accounts and regions.
	groups := map[string][]graph.Node{}
	for _, n := range nodes {
		if !subnetGroupType(n.Type) {
			continue
		}
		for _, key := range []string{"name", "id", "replication_subnet_group_id"} {
			if name, _ := n.Attributes[key].(string); name != "" && name != redacted {
				if !slices.ContainsFunc(groups[name], func(g graph.Node) bool { return g.ID == n.ID }) {
					groups[name] = append(groups[name], n)
				}
			}
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
	// bySubnets places a resource by the subnets it names: in the subnet if
	// there is one, in their network if there are several.
	bySubnets := func(n graph.Node) (graph.Node, bool) {
		subnets := map[string]graph.Node{}
		for _, id := range references(n.Attributes, subnetAttrs) {
			if subnet, ok := byID[subnetType][id]; ok {
				subnets[subnet.ID] = subnet
			}
		}
		if len(subnets) == 1 {
			for _, subnet := range subnets {
				return subnet, true
			}
		}
		return sharedNetwork(subnets, networkOf)
	}
	// byGroup places a resource where the subnet group it names is placed.
	// Of several groups with the name, the one in the resource's own account
	// and region is meant; if that does not single one out, none is used.
	byGroup := func(n graph.Node) (graph.Node, bool) {
		for _, name := range references(n.Attributes, subnetGroupAttrs) {
			candidates := groups[name]
			if len(candidates) > 1 {
				var same []graph.Node
				for _, c := range candidates {
					if c.Account == n.Account && c.Region == n.Region {
						same = append(same, c)
					}
				}
				candidates = same
			}
			if len(candidates) == 1 && candidates[0].ID != n.ID {
				if container, ok := bySubnets(candidates[0]); ok {
					return container, true
				}
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
			if container, found = bySubnets(n); !found {
				container, found = byGroup(n)
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
