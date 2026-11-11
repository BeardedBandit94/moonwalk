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
// they reach the graph; see redact.go for the rules. Older versions of
// Terraform and OpenTofu did not record every sensitive attribute; nodes
// from such a state are marked so that their attributes are never shown.
package tfstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfaddr"
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
	// withheld is true when the state comes from a version of Terraform or
	// OpenTofu that did not record every sensitive attribute. Redaction is
	// then incomplete, and the attributes must not be shown.
	withheld bool
}

// versionAtLeast reports whether version, such as "1.9.5" or "1.10.0-rc1",
// is at least major.minor. A version that cannot be read is not.
func versionAtLeast(version string, major, minor int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	gotMajor, err1 := strconv.Atoi(parts[0])
	gotMinor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
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

	// Accounts and regions stated by managed resources, per provider
	// configuration. Data sources are not evidence: they often describe
	// things owned by someone else, such as a public machine image.
	accounts, regions := map[string]map[string]struct{}{}, map[string]map[string]struct{}{}
	note := func(seen map[string]map[string]struct{}, key, value string) {
		if seen[key] == nil {
			seen[key] = map[string]struct{}{}
		}
		seen[key][value] = struct{}{}
	}
	for i := range rs {
		n := &rs[i].node
		n.Source = source
		n.ID = NodeID(source, n.Address)
		n.ConfigAddress = tfaddr.ConfigAddress(n.Address)
		n.Status = graph.StatusManaged
		n.AttributesWithheld = p.withheld && len(n.Attributes) > 0
		p.secrets.scrub(n.Attributes)
		n.Account, n.Region = partition(n.Provider, n.Type, n.Attributes)
		if n.Mode != graph.ModeManaged {
			continue
		}
		if n.Account != "" {
			note(accounts, rs[i].providerKey, n.Account)
		}
		if n.Region != "" && n.Region != graph.RegionGlobal {
			note(regions, rs[i].providerKey, n.Region)
		}
	}
	// only returns the single value seen for a provider configuration, or ""
	// if there were none or several.
	only := func(seen map[string]map[string]struct{}, key string) string {
		if len(seen[key]) != 1 {
			return ""
		}
		for v := range seen[key] {
			return v
		}
		return ""
	}

	// Targets are indexed by config address plus the instance keys of their
	// first k module calls, for every k, so a dependency finds its candidates
	// with one lookup instead of scanning every instance of the block.
	targets := map[string][]string{}
	for i := range rs {
		n := &rs[i].node
		// A provider configuration signs in to one account, so when its
		// managed resources all state the same one, the rest are in it too.
		if a := only(accounts, rs[i].providerKey); n.Account == "" && a != "" {
			n.Account, n.AccountInferred = a, true
		}
		if r := only(regions, rs[i].providerKey); n.Region == "" && r != "" && infersRegion(n.Provider) {
			n.Region, n.RegionInferred = r, true
		}
		if err := g.AddNode(*n); err != nil {
			return err
		}
		steps := tfaddr.ModuleSteps(n.Module)
		for k := 0; k <= len(steps); k++ {
			key := targetKey(n.ConfigAddress, steps[:k])
			targets[key] = append(targets[key], n.ID)
		}
	}

	for i := range rs {
		from := rs[i].node
		steps := tfaddr.ModuleSteps(from.Module)
		for _, dep := range rs[i].dependsOn {
			// State records a dependency as a resource block, not an instance.
			// A block can be reached only from inside the module instances the
			// two sides share, so targets in other instances of those modules
			// are ruled out. Among the rest state does not say which instance
			// is meant, so an edge goes to each. Dependencies on resources
			// absent from state are dropped.
			cfg := tfaddr.ConfigAddress(dep)
			shared := sharedModules(steps, tfaddr.ModuleNames(cfg))
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
func targetKey(configAddr string, steps []tfaddr.ModuleStep) string {
	var b strings.Builder
	b.WriteString(configAddr)
	for _, s := range steps {
		b.WriteByte(0)
		b.WriteString(s.Key)
	}
	return b.String()
}

// sharedModules counts the leading module calls that a resource's module
// path has in common with the module names of one of its dependencies.
func sharedModules(steps []tfaddr.ModuleStep, names []string) int {
	n := 0
	for n < len(steps) && n < len(names) && steps[n].Name == names[n] {
		n++
	}
	return n
}
