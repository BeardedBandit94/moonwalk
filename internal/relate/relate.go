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
	CrossState int // Dependencies added between resources in different sources.
	Contained  int // Containment edges added.
	Regions    int // Resources given the region of the resource they are part of.
	Glue       int // Resources marked as glue.
}

// Enrich applies every derivation in this package to g.
func Enrich(g *graph.Graph) Result {
	return Result{
		CrossState: CrossState(g),
		Contained:  Containment(g),
		Regions:    InheritRegions(g),
		Glue:       MarkGlue(g),
	}
}

// redacted is the placeholder the state adapter leaves for a secret. It
// must never be taken for an identifier that two resources share.
const redacted = "(sensitive)"
