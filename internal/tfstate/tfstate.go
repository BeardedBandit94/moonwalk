// Package tfstate turns Terraform and OpenTofu state into a graph.
//
// Two encodings are accepted and detected automatically:
//
//   - the output of `terraform show -json` / `tofu show -json`, the
//     documented, stable format;
//   - a raw version 4 state file (`terraform.tfstate`), which is what remote
//     backends actually store.
//
// State holds secrets in plain text. Attribute values are redacted before
// they reach the graph; see redact.go for the rules.
package tfstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Load reads one state from r and adds its resources to g under the given
// source name. Call it once per state to build a graph of several; source
// must be unique among them.
//
// Dependencies are resolved within the state only, since state does not
// record references to resources held in another state.
//
// If Load fails, g may hold part of the state and should be discarded.
func Load(g *graph.Graph, source string, r io.Reader) error {
	if source == "" {
		return fmt.Errorf("tfstate: source name is empty")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("tfstate: %s: read: %w", source, err)
	}
	// One pass decodes the file both to tell the formats apart and, for a
	// raw state, to read it; show-json output leaves the resource list empty.
	var st rawState
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // Keep numbers exact, as the show-json parser does.
	if err := dec.Decode(&st); err != nil {
		return fmt.Errorf("tfstate: %s: not a JSON state: %w", source, err)
	}
	if dec.More() {
		return fmt.Errorf("tfstate: %s: unexpected data after the JSON document", source)
	}

	var p parsed
	switch {
	case len(st.EncryptedData) > 0:
		err = fmt.Errorf("state is encrypted; decrypt it by piping `tofu show -json` into moonwalk instead")
	case st.FormatVersion != "":
		p, err = parseShowJSON(data)
	case st.Version != nil && *st.Version == 4:
		p, err = parseRaw(st)
	case st.Version != nil:
		err = fmt.Errorf("unsupported state file version %d (only 4 is supported)", *st.Version)
	default:
		err = fmt.Errorf("input is neither `terraform show -json` output nor a state file")
	}
	if err != nil {
		return fmt.Errorf("tfstate: %s: %w", source, err)
	}
	if err := add(g, source, p); err != nil {
		return fmt.Errorf("tfstate: %s: %w", source, err)
	}
	return nil
}

// NodeID returns the graph ID of the resource at address in the named source.
func NodeID(source, address string) string {
	return source + "::" + address
}

// parsed is the format-independent form both parsers produce.
type parsed struct {
	resources []resource
	// secrets holds the strings the parser redacted using the state's own
	// sensitivity marks.
	secrets secretSet
}

// resource is one resource instance. The parser fills every node field that
// comes from the state itself, with marked attributes already redacted; add
// fills the rest.
type resource struct {
	node graph.Node
	// providerKey identifies the provider configuration as precisely as the
	// format allows. Resources sharing it were created with one set of
	// credentials.
	providerKey string
	// dependsOn holds config addresses (no instance keys), as state records them.
	dependsOn []string
}

func add(g *graph.Graph, source string, p parsed) error {
	rs := p.resources

	// Accounts stated by managed resources, per provider configuration.
	// Data sources are not evidence: they often describe things owned by
	// someone else, such as a public machine image.
	stated := map[string]map[string]struct{}{}
	for i := range rs {
		n := &rs[i].node
		n.Source = source
		n.ID = NodeID(source, n.Address)
		n.ConfigAddress = configAddress(n.Address)
		n.Status = graph.StatusManaged
		p.secrets.scrub(n.Attributes)
		n.Account, n.Region = partition(n.Provider, n.Attributes)
		if n.Account != "" && n.Mode == graph.ModeManaged {
			key := rs[i].providerKey
			if stated[key] == nil {
				stated[key] = map[string]struct{}{}
			}
			stated[key][n.Account] = struct{}{}
		}
	}

	// Targets are indexed by config address plus the instance keys of their
	// first k module calls, for every k, so a dependency finds its candidates
	// with one lookup instead of scanning every instance of the block.
	targets := map[string][]string{}
	for i := range rs {
		n := &rs[i].node
		// A provider configuration signs in to one account, so when its
		// managed resources all state the same one, the rest are in it too.
		if accounts := stated[rs[i].providerKey]; n.Account == "" && len(accounts) == 1 {
			for a := range accounts {
				n.Account, n.AccountInferred = a, true
			}
		}
		if err := g.AddNode(*n); err != nil {
			return err
		}
		steps := moduleSteps(n.Module)
		for k := 0; k <= len(steps); k++ {
			key := targetKey(n.ConfigAddress, steps[:k])
			targets[key] = append(targets[key], n.ID)
		}
	}

	for i := range rs {
		from := rs[i].node
		steps := moduleSteps(from.Module)
		for _, dep := range rs[i].dependsOn {
			// State records a dependency as a resource block, not an instance.
			// A block can be reached only from inside the module instances the
			// two sides share, so targets in other instances of those modules
			// are ruled out. Among the rest state does not say which instance
			// is meant, so an edge goes to each. Dependencies on resources
			// absent from state are dropped.
			cfg := configAddress(dep)
			shared := sharedModules(steps, moduleNames(cfg))
			for _, to := range targets[targetKey(cfg, steps[:shared])] {
				if to == from.ID {
					continue
				}
				if err := g.AddEdge(graph.Edge{From: from.ID, To: to, Kind: graph.EdgeDependsOn}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// targetKey names the resources at a config address that sit inside the
// given module instances.
func targetKey(configAddr string, steps []moduleStep) string {
	var b strings.Builder
	b.WriteString(configAddr)
	for _, s := range steps {
		b.WriteByte(0)
		b.WriteString(s.key)
	}
	return b.String()
}

// sharedModules counts the leading module calls that a resource's module
// path has in common with the module names of one of its dependencies.
func sharedModules(steps []moduleStep, names []string) int {
	n := 0
	for n < len(steps) && n < len(names) && steps[n].name == names[n] {
		n++
	}
	return n
}
