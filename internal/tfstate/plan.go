package tfstate

import (
	"encoding/json"
	"fmt"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfaddr"
)

// parsePlan reads the output of `terraform show -json <planfile>`.
//
// A plan carries the state it was made against, which is loaded as any
// state is, the changes it intends, and what its refresh found to have
// drifted: changed or deleted outside Terraform. A plan made with
// -refresh-only holds just the drift. Each resource that changes is
// marked with what will happen to it, and a resource the plan will create
// is added, with dependencies taken from the configuration the plan
// embeds. The values a plan intends to set are not read: they can hold
// secrets, and variables in particular are stored in the clear.
func parsePlan(data []byte) (parsed, error) {
	var plan tfjson.Plan
	plan.UseJSONNumber(true)
	if err := json.Unmarshal(data, &plan); err != nil {
		return parsed{}, fmt.Errorf("parse plan: %w", err)
	}
	// See parseShowJSON for the version.
	p := parsed{secrets: secretSet{}, withheld: !versionAtLeast(plan.TerraformVersion, 1, 5)}
	if st := plan.PriorState; st != nil && st.Values != nil && st.Values.RootModule != nil {
		p.walkModule(st.Values.RootModule)
	}
	known := map[string]int{}
	for i, r := range p.resources {
		known[r.node.Address] = i
	}

	var root *planScope
	if plan.Config != nil && plan.Config.RootModule != nil {
		root = &planScope{module: plan.Config.RootModule}
	}
	for _, rc := range plan.ResourceChanges {
		if rc.DeposedKey != "" || rc.Change == nil {
			continue // A deposed object shares its address with the current one.
		}
		change := changeName(rc.Change.Actions)
		if change == "" {
			continue
		}
		if i, ok := known[rc.Address]; ok {
			p.resources[i].node.Change = change
			continue
		}
		if change != "create" {
			continue // Only a creation can concern a resource the state lacks.
		}
		r := resource{
			node: graph.Node{
				Address:  rc.Address,
				Module:   rc.ModuleAddress,
				Mode:     graph.Mode(rc.Mode),
				Type:     rc.Type,
				Name:     rc.Name,
				Provider: rc.ProviderName,
				Change:   change,
			},
			providerKey: rc.ProviderName,
			declared:    true,
		}
		if root != nil {
			r.dependsOn = root.dependencies(tfaddr.ConfigAddress(rc.Address))
		}
		known[rc.Address] = len(p.resources)
		p.resources = append(p.resources, r)
	}
	// What a refresh found to differ from the state, outside Terraform.
	for _, rc := range plan.ResourceDrift {
		if rc.DeposedKey != "" || rc.Change == nil {
			continue
		}
		drift := ""
		switch {
		case rc.Change.Actions.Delete():
			drift = "deleted"
		case rc.Change.Actions.Update():
			drift = "changed"
		default:
			continue
		}
		if i, ok := known[rc.Address]; ok {
			p.resources[i].drift = drift
			continue
		}
		// The state a plan carries is the state after its refresh, which
		// has already dropped a resource found to be gone. It is put back
		// here so that the loss shows, without the values it used to have.
		known[rc.Address] = len(p.resources)
		p.resources = append(p.resources, resource{
			node: graph.Node{
				Address:  rc.Address,
				Module:   rc.ModuleAddress,
				Mode:     graph.Mode(rc.Mode),
				Type:     rc.Type,
				Name:     rc.Name,
				Provider: rc.ProviderName,
			},
			providerKey: rc.ProviderName,
			drift:       drift,
		})
	}
	return p, nil
}

// changeName names what a plan's list of actions amounts to, or "" for a
// resource the plan leaves alone or merely reads.
func changeName(actions tfjson.Actions) string {
	switch {
	case actions.Replace():
		return "replace"
	case actions.Create():
		return "create"
	case actions.Update():
		return "update"
	case actions.Delete():
		return "delete"
	}
	return ""
}

// planScope is one module of the configuration a plan embeds, as called
// from one place.
type planScope struct {
	path   string // Config module path, "" for the root.
	module *tfjson.ConfigModule
	parent *planScope
	call   *tfjson.ModuleCall // How the parent calls it.
}

// child returns the scope of a module call, or nil if it is not known.
func (s *planScope) child(name string) *planScope {
	call := s.module.ModuleCalls[name]
	if call == nil || call.Module == nil {
		return nil
	}
	path := "module." + name
	if s.path != "" {
		path = s.path + "." + path
	}
	return &planScope{path: path, module: call.Module, parent: s, call: call}
}

// dependencies returns the config addresses of the resources that the
// resource at configAddress refers to. The plan's configuration lists
// references but not locals, so a reference made through a local is not
// found here; loading the code with -code finds it.
func (s *planScope) dependencies(configAddress string) []string {
	scope := s
	rest := configAddress
	for strings.HasPrefix(rest, "module.") {
		name, after, ok := strings.Cut(strings.TrimPrefix(rest, "module."), ".")
		if !ok {
			return nil
		}
		if scope = scope.child(name); scope == nil {
			return nil
		}
		rest = after
	}
	for _, r := range scope.module.Resources {
		if r.Address != rest {
			continue
		}
		found := map[string]bool{}
		for _, e := range r.Expressions {
			scope.follow(e, found, 0)
		}
		scope.follow(r.CountExpression, found, 0)
		scope.follow(r.ForEachExpression, found, 0)
		scope.followAll(r.DependsOn, found, 0)
		delete(found, configAddress)
		out := make([]string, 0, len(found))
		for addr := range found {
			out = append(out, addr)
		}
		return out
	}
	return nil
}

// maxPlanDepth bounds how far references are followed through variables
// and outputs, in case a plan's configuration refers to itself.
const maxPlanDepth = 30

func (s *planScope) follow(e *tfjson.Expression, found map[string]bool, depth int) {
	if e == nil || e.ExpressionData == nil {
		return
	}
	s.followAll(e.References, found, depth)
	for _, block := range e.NestedBlocks {
		for _, inner := range block {
			s.follow(inner, found, depth)
		}
	}
}

func (s *planScope) followAll(refs []string, found map[string]bool, depth int) {
	if depth > maxPlanDepth {
		return
	}
	for _, ref := range refs {
		parts := strings.Split(tfaddr.ConfigAddress(ref), ".")
		switch {
		case parts[0] == "var" && len(parts) > 1:
			// A variable's value comes from the call, written in the parent.
			if s.parent != nil {
				s.parent.follow(s.call.Expressions[parts[1]], found, depth+1)
			}
		case parts[0] == "module" && len(parts) > 2:
			if child := s.child(parts[1]); child != nil {
				if out := child.module.Outputs[parts[2]]; out != nil {
					child.follow(out.Expression, found, depth+1)
				}
			}
		case parts[0] == "data" && len(parts) > 2:
			s.add("data."+parts[1]+"."+parts[2], found)
		case len(parts) > 1:
			s.add(parts[0]+"."+parts[1], found) // No match for local, each, count and the like.
		}
	}
}

// add records a resource of this module if it has one by that name.
func (s *planScope) add(local string, found map[string]bool) {
	for _, r := range s.module.Resources {
		if r.Address == local {
			if s.path != "" {
				local = s.path + "." + local
			}
			found[local] = true
			return
		}
	}
}
