package relate

import (
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/azureid"
	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// The Azure resource types that contain others.
const (
	azureNetworkType   = "azurerm_virtual_network"
	azureSubnetType    = "azurerm_subnet"
	azureInterfaceType = "azurerm_network_interface"
)

// azureSubnetAttr reports whether an attribute names a subnet by ID. The
// provider has a few dozen spellings (subnet_id, virtual_network_subnet_id,
// vnet_subnet_id, pod_subnet_id, delegated_subnet_id and so on), all with
// the same ending. What such an attribute holds is still only taken for a
// subnet if it is the ID of one.
func azureSubnetAttr(name string) bool {
	return name == "subnet_id" || name == "subnet_ids" || strings.HasSuffix(name, "_subnet_id") || strings.HasSuffix(name, "_subnet_ids")
}

var (
	azureNetworkAttrs   = []string{"virtual_network_id"}
	azureInterfaceAttrs = []string{"network_interface_id", "network_interface_ids"}
)

// azureContainment places Azure resources.
//
// A subnet is part of the network its ID starts with, as is anything else
// whose ID does, such as a peering. A resource that names one subnet sits
// in it; one that names several subnets of one network, such as a cluster
// with nodes and pods apart, sits in the network. A virtual machine names
// no subnet, only its network interfaces, and sits where they do.
//
// Azure IDs are compared without regard to case: the same subnet's ID
// reads "resourceGroups" in one resource and "resourcegroups" in another.
func azureContainment(g *graph.Graph, nodes []graph.Node) int {
	// Networks, subnets and interfaces by canonical ID. A managed resource
	// wins over a data source that reads the same thing.
	byID := map[string]graph.Node{}
	for _, n := range nodes {
		if n.Type != azureNetworkType && n.Type != azureSubnetType && n.Type != azureInterfaceType {
			continue
		}
		id, _ := n.Attributes["id"].(string)
		if !azureid.Is(id) {
			continue
		}
		key := azureid.Canonical(id)
		if cur, dup := byID[key]; !dup || (cur.Mode != graph.ModeManaged && n.Mode == graph.ModeManaged) {
			byID[key] = n
		}
	}
	if len(byID) == 0 {
		return 0
	}
	lookup := func(id, resourceType string) (graph.Node, bool) {
		n, ok := byID[azureid.Canonical(id)]
		return n, ok && n.Type == resourceType
	}
	// partOf returns the network or subnet that the ID is part of.
	partOf := func(id string) (graph.Node, bool) {
		for parent, ok := azureid.Parent(id); ok; parent, ok = azureid.Parent(parent) {
			if c, found := byID[azureid.Canonical(parent)]; found && c.Type != azureInterfaceType {
				return c, true
			}
		}
		return graph.Node{}, false
	}
	networkOf := func(n graph.Node) (graph.Node, bool) {
		if n.Type == azureNetworkType {
			return n, true
		}
		id, _ := n.Attributes["id"].(string)
		return partOf(id)
	}
	// among returns the one container in the set, or the network they
	// all sit in.
	among := func(containers map[string]graph.Node) (graph.Node, bool) {
		if len(containers) == 1 {
			for _, c := range containers {
				return c, true
			}
		}
		return sharedNetwork(containers, networkOf)
	}
	// byNames places a resource by the subnets it names, or failing
	// that the network.
	byNames := func(n graph.Node) (graph.Node, bool) {
		subnets := map[string]graph.Node{}
		for _, id := range referencesWhere(n.Attributes, azureSubnetAttr) {
			if subnet, ok := lookup(id, azureSubnetType); ok {
				subnets[subnet.ID] = subnet
			}
		}
		if c, ok := among(subnets); ok {
			return c, true
		}
		for _, id := range references(n.Attributes, azureNetworkAttrs) {
			if network, ok := lookup(id, azureNetworkType); ok {
				return network, true
			}
		}
		return graph.Node{}, false
	}
	// byInterfaces places a resource where the interfaces it names sit.
	byInterfaces := func(n graph.Node) (graph.Node, bool) {
		containers := map[string]graph.Node{}
		for _, id := range references(n.Attributes, azureInterfaceAttrs) {
			if nic, ok := lookup(id, azureInterfaceType); ok && nic.ID != n.ID {
				if c, ok := byNames(nic); ok {
					containers[c.ID] = c
				}
			}
		}
		return among(containers)
	}

	added := 0
	for _, n := range nodes {
		if !strings.HasPrefix(n.Type, "azurerm_") || n.Type == azureNetworkType {
			continue
		}
		container, found := graph.Node{}, false
		if n.Type != azureSubnetType {
			if container, found = byNames(n); !found {
				container, found = byInterfaces(n)
			}
		}
		if !found {
			id, _ := n.Attributes["id"].(string)
			container, found = partOf(id)
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

// InheritRegions gives a resource that states no region the region of the
// resource it is part of, and returns how many it changed.
//
// An Azure subnet has no location of its own, nor has a database on a
// server: they are where their parent is. A resource group is not a
// parent in this sense, since what it holds can be anywhere.
func InheritRegions(g *graph.Graph) int {
	regions := map[string]string{}
	var without []graph.Node
	for n := range g.AllNodes() {
		id, _ := n.Attributes["id"].(string)
		if !azureid.Is(id) || n.Status == graph.StatusDeclared {
			continue
		}
		switch {
		case n.Region == "":
			without = append(without, n)
		case !n.RegionInferred:
			regions[azureid.Canonical(id)] = n.Region
		}
	}
	at := func(id string) string { return regions[azureid.Canonical(id)] }
	changed := 0
	for _, n := range without {
		id, _ := n.Attributes["id"].(string)
		region := ""
		// What joins two resources carries the first one's ID before a
		// bar, and is where that one is.
		if first, _, joined := strings.Cut(id, "|"); joined {
			id, region = first, at(first)
		}
		for parent, ok := azureid.Parent(id); ok && region == ""; parent, ok = azureid.Parent(parent) {
			region = at(parent)
		}
		if region != "" {
			g.Update(n.ID, func(n *graph.Node) { n.Region, n.RegionInferred = region, true })
			changed++
		}
	}
	return changed
}
