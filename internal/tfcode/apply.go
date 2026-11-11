package tfcode

import (
	"sort"
	"strings"

	"github.com/zclconf/go-cty/cty"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

var ctyString = cty.String

// Result counts what Apply did.
type Result struct {
	Linked   int // Resources in the graph that were given a code location.
	Declared int // Resources added because code declares them and state has none.
}

// Apply joins the configuration to the resources that source holds in g.
//
// Every resource whose block is in the configuration is told where that
// block is written. A block with no resource in the graph is added as a
// resource with the status "declared", in the account and region that the
// state's other resources point to, and gets dependencies worked out from
// its expressions, as do the resources that refer to it. Resources
// that both sides know keep the dependencies their state records.
func (c *Config) Apply(g *graph.Graph, source string) (Result, error) {
	var res Result
	byBlock := map[string][]string{}
	where := newPlacer()
	for n := range g.AllNodes() {
		if n.Source == source {
			byBlock[n.ConfigAddress] = append(byBlock[n.ConfigAddress], n.ID)
			where.learn(n)
		}
	}

	addresses := make([]string, 0, len(c.Blocks))
	for addr := range c.Blocks {
		addresses = append(addresses, addr)
	}
	sort.Strings(addresses)

	declared := map[string]bool{}
	for _, addr := range addresses {
		blk := c.Blocks[addr]
		loc := &graph.CodeLocation{File: blk.File, Line: blk.Line, EndLine: blk.EndLine}
		if ids := byBlock[addr]; len(ids) > 0 {
			for _, id := range ids {
				g.Update(id, func(n *graph.Node) { n.Code = loc })
				res.Linked++
			}
			continue
		}
		n := graph.Node{
			ID:            source + "::" + addr,
			Source:        source,
			Address:       addr,
			ConfigAddress: addr,
			Module:        blk.Module,
			Mode:          blk.Mode,
			Type:          blk.Type,
			Name:          blk.Name,
			Status:        graph.StatusDeclared,
			Code:          loc,
		}
		where.place(&n)
		if err := g.AddNode(n); err != nil {
			return res, err
		}
		byBlock[addr] = []string{n.ID}
		declared[addr] = true
		res.Declared++
	}

	for _, addr := range addresses {
		for _, dep := range c.Blocks[addr].DependsOn {
			if !declared[addr] && !declared[dep] {
				continue // Both are in state, which has recorded this already.
			}
			for _, from := range byBlock[addr] {
				for _, to := range byBlock[dep] {
					if err := g.AddEdge(graph.Edge{From: from, To: to, Kind: graph.EdgeDependsOn}); err != nil {
						return res, err
					}
				}
			}
		}
	}
	return res, nil
}

// placer guesses the account and region of a resource that exists only in
// code, from the resources of the same state that do exist.
type placer struct {
	// Accounts and regions seen, by resource type and by provider prefix
	// (the part of the type before the first underscore).
	accounts, regions map[string]map[string]bool
}

func newPlacer() *placer {
	return &placer{accounts: map[string]map[string]bool{}, regions: map[string]map[string]bool{}}
}

func providerPrefix(resourceType string) string {
	prefix, _, _ := strings.Cut(resourceType, "_")
	return "provider:" + prefix
}

func (p *placer) learn(n graph.Node) {
	if n.Mode != graph.ModeManaged {
		return
	}
	note := func(seen map[string]map[string]bool, key, value string) {
		if value == "" {
			return
		}
		if seen[key] == nil {
			seen[key] = map[string]bool{}
		}
		seen[key][value] = true
	}
	note(p.accounts, n.Type, n.Account)
	note(p.accounts, providerPrefix(n.Type), n.Account)
	note(p.regions, n.Type, n.Region)
	// A global resource says nothing about where the provider's regional
	// resources go.
	if n.Region != graph.RegionGlobal {
		note(p.regions, providerPrefix(n.Type), n.Region)
	}
}

// place fills in an account and a region where the evidence is unanimous:
// first from resources of the same type, which also tells a global service
// from a regional one, then from all resources of the same provider.
func (p *placer) place(n *graph.Node) {
	only := func(seen map[string]map[string]bool) string {
		for _, key := range []string{n.Type, providerPrefix(n.Type)} {
			if len(seen[key]) == 1 {
				for v := range seen[key] {
					return v
				}
			}
			if len(seen[key]) > 1 {
				return ""
			}
		}
		return ""
	}
	if a := only(p.accounts); a != "" {
		n.Account, n.AccountInferred = a, true
	}
	if r := only(p.regions); r != "" {
		n.Region, n.RegionInferred = r, true
	}
}
