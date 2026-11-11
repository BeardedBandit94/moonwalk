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
	Linked    int // Resources in the graph that were given a code location.
	Declared  int // Resources added because code declares them and state has none.
	NotInCode int // Managed resources that the code does not declare.
	Indirect  int // Recorded dependencies removed because the code shows them to be indirect.
}

// Apply joins the configuration to the resources that source holds in g.
//
// Every resource whose block is in the configuration is told where that
// block is written. A block with no resource in the graph is added as a
// resource with the status "declared", in the account and region that the
// state's other resources point to. A managed resource that the
// configuration does not declare is marked NotInCode.
//
// Dependencies follow the code where it can settle them. A declared
// resource gets them from its expressions, as do the resources that refer
// to it. A resource from state keeps what its state records, less the
// dependencies that the code shows to be indirect: the ones it does not
// refer to itself but that something it refers to leads to. A recorded
// dependency that the code does not explain at all is kept, since the code
// may have changed since it was applied.
func (c *Config) Apply(g *graph.Graph, source string) (Result, error) {
	var res Result
	byBlock := map[string][]string{}
	blockOf := map[string]string{} // Node ID to config address.
	where := newPlacer()
	var orphans []string
	for n := range g.AllNodes() {
		if n.Source != source {
			continue
		}
		byBlock[n.ConfigAddress] = append(byBlock[n.ConfigAddress], n.ID)
		blockOf[n.ID] = n.ConfigAddress
		where.learn(n)
		if _, inCode := c.Blocks[n.ConfigAddress]; !inCode && n.Mode == graph.ModeManaged &&
			n.Status == graph.StatusManaged && !c.unresolved(n.ConfigAddress) {
			orphans = append(orphans, n.ID)
		}
	}
	for _, id := range orphans {
		g.Update(id, func(n *graph.Node) { n.NotInCode = true })
	}
	res.NotInCode = len(orphans)

	addresses := make([]string, 0, len(c.Blocks))
	for addr := range c.Blocks {
		addresses = append(addresses, addr)
	}
	sort.Strings(addresses)

	// A block is "declared" when nothing has applied it: Apply adds its
	// resource here, or a plan added it as one to be created.
	declared := map[string]bool{}
	for _, addr := range addresses {
		blk := c.Blocks[addr]
		loc := &graph.CodeLocation{File: blk.File, Line: blk.Line, EndLine: blk.EndLine}
		if ids := byBlock[addr]; len(ids) > 0 {
			applied := false
			for _, id := range ids {
				g.Update(id, func(n *graph.Node) {
					n.Code = loc
					applied = applied || n.Status != graph.StatusDeclared
				})
				res.Linked++
			}
			declared[addr] = !applied
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
		blockOf[n.ID] = addr
		declared[addr] = true
		res.Declared++
	}

	// What each block leads to through any number of references.
	reach := map[string]map[string]bool{}
	var reachable func(addr string) map[string]bool
	reachable = func(addr string) map[string]bool {
		if r, ok := reach[addr]; ok {
			return r
		}
		r := map[string]bool{}
		reach[addr] = r // Set before recursing, so a loop ends.
		if blk := c.Blocks[addr]; blk != nil {
			for _, dep := range blk.DependsOn {
				r[dep] = true
				for further := range reachable(dep) {
					r[further] = true
				}
			}
		}
		return r
	}

	recorded := map[string][]string{} // Dependencies in the graph so far, by dependent.
	for e := range g.AllEdges() {
		if e.Kind == graph.EdgeDependsOn && blockOf[e.From] != "" {
			recorded[e.From] = append(recorded[e.From], e.To)
		}
	}
	for _, addr := range addresses {
		blk := c.Blocks[addr]
		direct := map[string]bool{}
		for _, dep := range blk.DependsOn {
			direct[dep] = true
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
		for _, from := range byBlock[addr] {
			for _, to := range recorded[from] {
				target, known := blockOf[to]
				if known && !direct[target] && reachable(addr)[target] {
					g.RemoveEdge(graph.Edge{From: from, To: to, Kind: graph.EdgeDependsOn})
					res.Indirect++
				}
			}
			g.Update(from, func(n *graph.Node) { n.DependenciesDirect = true })
		}
	}
	return res, nil
}

// unresolved reports whether an address lies in a module whose code could
// not be found, so that nothing can be said about what it declares.
func (c *Config) unresolved(configAddress string) bool {
	for _, module := range c.Unresolved {
		if strings.HasPrefix(configAddress, module+".") {
			return true
		}
	}
	return false
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
