// Package relate derives relationships between resources that state does
// not record as such, from what the resources' attributes say.
//
// Everything here is inference. It reads only redacted attributes, and it
// leaves the graph as it was wherever the attributes do not settle a
// question.
package relate

import (
	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Result counts what Enrich changed.
type Result struct {
	Contained int // Containment edges added.
	Narrowed  int // Dependency edges removed as not meant.
	Glue      int // Resources marked as glue.
}

// Enrich applies every derivation in this package to g, in the order they
// build on each other.
func Enrich(g *graph.Graph) Result {
	return Result{
		Contained: Containment(g),
		Narrowed:  NarrowDependencies(g),
		Glue:      MarkGlue(g),
	}
}

// collectStrings gathers every non-empty, unredacted string in v into set.
func collectStrings(v any, set map[string]struct{}) {
	switch t := v.(type) {
	case string:
		if t != "" && t != redacted {
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

// redacted is the placeholder the state adapter leaves for a secret. It
// must never be taken for an identifier that two resources share.
const redacted = "(sensitive)"
