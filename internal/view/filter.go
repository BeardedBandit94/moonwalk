package view

import (
	"path"
	"slices"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Filter chooses which resources a tree shows. A resource must satisfy
// every field that is set; the zero Filter matches everything.
type Filter struct {
	// Each of these matches a resource whose value is any of those listed.
	Sources, Accounts, Regions, Statuses []string
	// Types may also hold patterns such as "aws_iam_*".
	Types []string
	// Search matches a resource whose address or type contains it,
	// ignoring case.
	Search string
	// Around is a node ID. It matches that resource and every resource
	// joined to it by an edge of any kind, in either direction.
	Around string
}

// matcher returns the test the filter stands for on the given graph.
func (f Filter) matcher(g *graph.Graph) func(graph.Node) bool {
	var near map[string]bool
	if f.Around != "" {
		near = map[string]bool{f.Around: true}
		for e := range g.AllEdges() {
			switch f.Around {
			case e.From:
				near[e.To] = true
			case e.To:
				near[e.From] = true
			case e.Via:
				near[e.From], near[e.To] = true, true
			}
		}
	}
	search := strings.ToLower(f.Search)
	return func(n graph.Node) bool {
		switch {
		case near != nil && !near[n.ID],
			len(f.Sources) > 0 && !slices.Contains(f.Sources, n.Source),
			len(f.Accounts) > 0 && !slices.Contains(f.Accounts, n.Account),
			len(f.Regions) > 0 && !slices.Contains(f.Regions, n.Region),
			len(f.Statuses) > 0 && !slices.Contains(f.Statuses, string(n.Status)),
			len(f.Types) > 0 && !matchesAny(f.Types, n.Type),
			search != "" && !strings.Contains(strings.ToLower(n.Address), search) &&
				!strings.Contains(strings.ToLower(n.Type), search):
			return false
		}
		return true
	}
}

func matchesAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, s); err == nil && ok {
			return true
		}
	}
	return false
}
