package relate

import (
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/googleid"
	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// The Google Cloud resource types that contain others, and the names under
// which a resource refers to them.
const (
	googleNetworkType = "google_compute_network"
	googleSubnetType  = "google_compute_subnetwork"
)

var (
	googleNetworkAttrs = []string{"network", "private_network", "authorized_network"}
	googleSubnetAttrs  = []string{"subnetwork", "subnetworks"}
)

// googleContainment places Google Cloud resources.
//
// A subnetwork is in the network it names. A resource that names one
// subnetwork sits in it; one that names several of one network, or only a
// network, sits in the network. A database names its network two blocks
// down, under its settings, so the names are looked for at any depth.
//
// One network or subnetwork is referred to in several ways: by its self
// link, by the same without the address in front, or by its name alone,
// which then means the one in the referring resource's own project and,
// for a subnetwork, its region.
func googleContainment(g *graph.Graph, nodes []graph.Node) int {
	// By canonical name, and by project, region and name for the short
	// way. A managed resource wins over a data source that reads the same.
	byName := map[string]graph.Node{}
	add := func(key string, n graph.Node) {
		if cur, dup := byName[key]; !dup || (cur.Mode != graph.ModeManaged && n.Mode == graph.ModeManaged) {
			byName[key] = n
		}
	}
	for _, n := range nodes {
		if n.Type != googleNetworkType && n.Type != googleSubnetType {
			continue
		}
		for _, key := range []string{"self_link", "id"} {
			if v, _ := n.Attributes[key].(string); googleid.Is(v) {
				add(googleid.Canonical(v), n)
			}
		}
		if name, _ := n.Attributes["name"].(string); name != "" && name != redacted {
			add(short(n.Type, n.Account, n.Region, name), n)
		}
	}
	if len(byName) == 0 {
		return 0
	}
	// resolve finds the network or subnetwork that a resource means by a
	// value under one of its attributes.
	resolve := func(from graph.Node, value, resourceType string) (graph.Node, bool) {
		if value == "" || value == redacted {
			return graph.Node{}, false
		}
		if n, ok := byName[googleid.Canonical(value)]; ok && n.Type == resourceType {
			return n, true
		}
		if strings.Contains(value, "/") {
			return graph.Node{}, false // A full name of something not loaded.
		}
		n, ok := byName[short(resourceType, from.Account, from.Region, value)]
		return n, ok
	}
	networkOf := func(n graph.Node) (graph.Node, bool) {
		if n.Type == googleNetworkType {
			return n, true
		}
		for _, v := range deepReferences(n.Attributes, googleNetworkAttrs) {
			if network, ok := resolve(n, v, googleNetworkType); ok {
				return network, true
			}
		}
		return graph.Node{}, false
	}

	added := 0
	for _, n := range nodes {
		if !strings.HasPrefix(n.Type, "google_") || n.Type == googleNetworkType {
			continue
		}
		container, found := graph.Node{}, false
		if n.Type != googleSubnetType {
			subnets := map[string]graph.Node{}
			for _, v := range deepReferences(n.Attributes, googleSubnetAttrs) {
				if subnet, ok := resolve(n, v, googleSubnetType); ok {
					subnets[subnet.ID] = subnet
				}
			}
			if len(subnets) == 1 {
				for _, subnet := range subnets {
					container, found = subnet, true
				}
			} else {
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

// short is the key for a network or subnetwork referred to by name alone.
// A network belongs to a project; a subnetwork to a project and a region.
func short(resourceType, project, region, name string) string {
	if resourceType == googleNetworkType {
		region = ""
	}
	return resourceType + "\x00" + project + "\x00" + region + "\x00" + name
}
