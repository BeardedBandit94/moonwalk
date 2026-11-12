package relate

import (
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// MarkGlue finds resources that exist only to attach two others to each
// other, marks them as glue, and adds an EdgeLinks edge between the two.
// It returns how many it marked.
//
// A resource counts as glue when its type is named as an association,
// attachment or membership, or is a security group rule, nothing depends
// on it, and it depends directly
// on exactly two resources from different blocks. Anything less clear-cut
// stays an ordinary resource.
func MarkGlue(g *graph.Graph) int {
	targets := map[string][]string{}
	dependedOn := map[string]bool{}
	for e := range g.AllEdges() {
		if e.Kind == graph.EdgeDependsOn {
			targets[e.From] = append(targets[e.From], e.To)
			dependedOn[e.To] = true
		}
	}

	marked := 0
	for _, n := range g.Nodes() {
		if n.Mode != graph.ModeManaged || !glueType(n.Type) || dependedOn[n.ID] {
			continue
		}
		// Leave out dependencies that another one implies, such as the
		// network behind a subnet.
		var direct []string
		for _, to := range targets[n.ID] {
			implied := false
			for _, via := range targets[n.ID] {
				if via != to && g.HasEdge(graph.Edge{From: via, To: to, Kind: graph.EdgeDependsOn}) {
					implied = true
					break
				}
			}
			if !implied {
				direct = append(direct, to)
			}
		}
		if len(direct) != 2 {
			continue
		}
		a, b := direct[0], direct[1]
		// Two instances of one block are a dependency on that block which
		// could not be narrowed down, not two things being joined.
		if na, _ := g.Node(a); true {
			if nb, _ := g.Node(b); na.Source == nb.Source && na.ConfigAddress == nb.ConfigAddress {
				continue
			}
		}
		if b < a {
			a, b = b, a
		}
		if err := g.AddEdge(graph.Edge{From: a, To: b, Kind: graph.EdgeLinks, Via: n.ID}); err != nil {
			continue
		}
		g.Update(n.ID, func(n *graph.Node) { n.Glue = true })
		marked++
	}
	return marked
}

// glueType reports whether a resource type is named as something that
// joins two others. The convention holds across providers. A few types do
// the same job under another name: a security group rule that names a
// second group joins the two, and a peering joins two networks.
func glueType(resourceType string) bool {
	for _, suffix := range []string{"_association", "_attachment", "_membership"} {
		if strings.HasSuffix(resourceType, suffix) {
			return true
		}
	}
	switch resourceType {
	case "aws_security_group_rule", "aws_vpc_security_group_ingress_rule", "aws_vpc_security_group_egress_rule":
		return true
	case "azurerm_virtual_network_peering":
		return true // Joins two networks, one resource for each direction.
	}
	return false
}
