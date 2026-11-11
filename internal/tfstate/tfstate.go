// Package tfstate turns Terraform and OpenTofu state into a graph.
//
// Two encodings are accepted and detected automatically:
//
//   - the output of `terraform show -json` / `tofu show -json`, the
//     documented, stable format;
//   - a raw version 4 state file (`terraform.tfstate`), which is what remote
//     backends actually store.
//
// Attribute values are deliberately not copied into the graph: state holds
// secrets in plain text, and nothing downstream redacts them so far. The
// adapter reads them only to derive each node's account and region.
package tfstate

import (
	"encoding/json"
	"fmt"
	"io"

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
	var probe struct {
		FormatVersion string `json:"format_version"`
		Version       *int   `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("tfstate: %s: not valid JSON: %w", source, err)
	}

	var resources []resource
	switch {
	case probe.FormatVersion != "":
		resources, err = parseShowJSON(data)
	case probe.Version != nil && *probe.Version == 4:
		resources, err = parseRaw(data)
	case probe.Version != nil:
		err = fmt.Errorf("unsupported state file version %d (only 4 is supported)", *probe.Version)
	default:
		err = fmt.Errorf("input is neither `terraform show -json` output nor a state file")
	}
	if err != nil {
		return fmt.Errorf("tfstate: %s: %w", source, err)
	}
	if err := add(g, source, resources); err != nil {
		return fmt.Errorf("tfstate: %s: %w", source, err)
	}
	return nil
}

// NodeID returns the graph ID of the resource at address in the named source.
func NodeID(source, address string) string {
	return source + "::" + address
}

// resource is the format-independent form both parsers produce. The parsers
// fill every node field that comes from the state itself; add fills the rest.
type resource struct {
	node graph.Node
	// attrs is used to derive the partition and then dropped.
	attrs map[string]any
	// dependsOn holds config addresses (no instance keys), as state records them.
	dependsOn []string
}

func add(g *graph.Graph, source string, resources []resource) error {
	byConfig := map[string][]string{}
	for i := range resources {
		n := &resources[i].node
		n.Source = source
		n.ID = NodeID(source, n.Address)
		n.ConfigAddress = configAddress(n.Address)
		n.Status = graph.StatusManaged
		n.Account, n.Region = partition(n.Provider, resources[i].attrs)
		if err := g.AddNode(*n); err != nil {
			return err
		}
		byConfig[n.ConfigAddress] = append(byConfig[n.ConfigAddress], n.ID)
	}
	for _, r := range resources {
		for _, dep := range r.dependsOn {
			// State records dependencies per resource block, not per instance,
			// so an edge goes to every instance of the block. Dependencies on
			// resources absent from state are dropped.
			for _, target := range byConfig[configAddress(dep)] {
				if target == r.node.ID {
					continue
				}
				e := graph.Edge{From: r.node.ID, To: target, Kind: graph.EdgeDependsOn}
				if err := g.AddEdge(e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
