// Package tfstate turns Terraform and OpenTofu state into a graph.
//
// Two encodings are accepted and detected automatically:
//
//   - the output of `terraform show -json` / `tofu show -json`, the
//     documented, stable format;
//   - a raw version 4 state file (`terraform.tfstate`), which is what remote
//     backends actually store.
//
// Attribute values are deliberately not copied into the graph yet: state
// holds secrets in plain text, and nothing downstream redacts them so far.
package tfstate

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Load reads state from r and returns its resource graph.
func Load(r io.Reader) (*graph.Graph, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("tfstate: read: %w", err)
	}
	var probe struct {
		FormatVersion string `json:"format_version"`
		Version       *int   `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("tfstate: not valid JSON: %w", err)
	}

	var resources []resource
	switch {
	case probe.FormatVersion != "":
		resources, err = parseShowJSON(data)
	case probe.Version != nil && *probe.Version == 4:
		resources, err = parseRaw(data)
	case probe.Version != nil:
		return nil, fmt.Errorf("tfstate: unsupported state file version %d (only 4 is supported)", *probe.Version)
	default:
		return nil, fmt.Errorf("tfstate: input is neither `terraform show -json` output nor a state file")
	}
	if err != nil {
		return nil, err
	}
	return build(resources)
}

// resource is the format-independent form both parsers produce.
type resource struct {
	node graph.Node
	// dependsOn holds config addresses (no instance keys), as state records them.
	dependsOn []string
}

func build(resources []resource) (*graph.Graph, error) {
	g := graph.New()
	byConfig := map[string][]string{}
	for _, r := range resources {
		if err := g.AddNode(r.node); err != nil {
			return nil, fmt.Errorf("tfstate: %w", err)
		}
		byConfig[r.node.ConfigAddress] = append(byConfig[r.node.ConfigAddress], r.node.ID)
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
					return nil, fmt.Errorf("tfstate: %w", err)
				}
			}
		}
	}
	return g, nil
}
