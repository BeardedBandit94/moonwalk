package relate

import (
	"slices"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/azureid"
	"github.com/BeardedBandit94/moonwalk/internal/googleid"
	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// CrossState adds a dependency from a resource to a resource held in
// another source wherever the first names the second by its identity, and
// returns how many it added.
//
// State records dependencies only within itself. Across states the link is
// in the values: a subnet ID passed in as a variable, an ARN in a policy,
// the outputs a terraform_remote_state data source has read. A value
// counts as naming a resource when it is that resource's ARN, self link or
// ID, no other resource shares it, and it appears where a reference would:
// under an argument named like one (ending in _id, _ids, _arn or _arns),
// anywhere if it is an ARN or a path, in the outputs of a remote state, or
// as the ID of a data source, which reads the resource it names.
func CrossState(g *graph.Graph) int {
	nodes := g.Nodes()

	// Identity to the one resource that has it. A data source shares the
	// identity of what it reads and owns none; two resources with one
	// identity, as when a state is loaded twice, make it useless.
	owner := map[string]graph.Node{}
	ambiguous := map[string]bool{}
	for _, n := range nodes {
		// What only joins two resources is not named by anything, and
		// may carry the ID of one of the two: Azure gives a subnet's
		// association with a security group the subnet's own ID.
		if n.Status == graph.StatusDeclared || n.Mode == graph.ModeData || glueType(n.Type) {
			continue
		}
		for _, id := range identities(n) {
			if _, taken := owner[id]; taken {
				ambiguous[id] = true
			}
			owner[id] = n
		}
	}

	added := 0
	for _, n := range nodes {
		if len(n.Attributes) == 0 {
			continue
		}
		own := map[string]bool{}
		for _, id := range identities(n) {
			own[id] = true
		}
		seen := map[string]bool{}
		visit := func(value string, anywhere bool) {
			value = canonical(value)
			target, ok := owner[value]
			if !ok || ambiguous[value] || target.Source == n.Source || seen[target.ID] {
				return
			}
			// A data source's own ID names what it reads; any other
			// resource's own identity names itself.
			if own[value] && n.Mode != graph.ModeData {
				return
			}
			if !anywhere && !pathLike(value) {
				return
			}
			seen[target.ID] = true
			e := graph.Edge{From: n.ID, To: target.ID, Kind: graph.EdgeDependsOn}
			if !g.HasEdge(e) && g.AddEdge(e) == nil {
				added++
			}
		}
		for key, v := range n.Attributes {
			remoteOutputs := n.Type == "terraform_remote_state" && key == "outputs"
			walkValues(v, key, func(under, value string) {
				visit(value, remoteOutputs || referenceKey(under) || (under == "id" && n.Mode == graph.ModeData))
			})
		}
	}
	return added
}

// canonical returns an identity in the one form it is compared by. Azure
// spells one ID in several cases, and Google writes one name with or
// without the address in front.
func canonical(v string) string {
	return googleid.Canonical(azureid.Canonical(v))
}

// identities returns the values by which another resource could name n.
func identities(n graph.Node) []string {
	var out []string
	add := func(v string) {
		v = canonical(v)
		if distinctive(v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	add(n.CloudID)
	for _, key := range []string{"id", "arn", "self_link"} {
		if v, ok := n.Attributes[key].(string); ok && v != redacted {
			add(v)
		}
	}
	return out
}

// distinctive reports whether a value is specific enough to stand for one
// resource: long enough, and not a plain word such as "default".
func distinctive(v string) bool {
	return len(v) >= 6 && strings.ContainsAny(v, "0123456789-/:")
}

// pathLike reports whether a value is an ARN or a path, which name one
// resource wherever they are written.
func pathLike(v string) bool {
	return strings.HasPrefix(v, "arn:") || strings.Contains(v, "/")
}

// referenceKey reports whether an argument is named like a reference.
func referenceKey(key string) bool {
	for _, suffix := range []string{"_id", "_ids", "_arn", "_arns"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

// walkValues calls visit for every string in v with the name of the
// nearest argument above it: an element of a list goes by the list's name.
func walkValues(v any, under string, visit func(under, value string)) {
	switch t := v.(type) {
	case string:
		if t != "" && t != redacted {
			visit(under, t)
		}
	case map[string]any:
		for k, e := range t {
			walkValues(e, k, visit)
		}
	case []any:
		for _, e := range t {
			walkValues(e, under, visit)
		}
	}
}
