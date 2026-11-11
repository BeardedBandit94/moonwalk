package tfstate

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// rawState is the subset of a version 4 state file moonwalk reads.
type rawState struct {
	Resources []struct {
		Module    string `json:"module"`
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Instances []struct {
			IndexKey     any            `json:"index_key"`
			Deposed      string         `json:"deposed"`
			Attributes   map[string]any `json:"attributes"`
			Dependencies []string       `json:"dependencies"`
		} `json:"instances"`
	} `json:"resources"`
}

// parseRaw reads a raw version 4 state file.
func parseRaw(data []byte) ([]resource, error) {
	var st rawState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	var out []resource
	for _, r := range st.Resources {
		base := r.Type + "." + r.Name
		if r.Mode == string(graph.ModeData) {
			base = "data." + base
		}
		if r.Module != "" {
			base = r.Module + "." + base
		}
		for _, inst := range r.Instances {
			if inst.Deposed != "" {
				continue // A deposed object shares its address with the current one.
			}
			key, err := indexSuffix(inst.IndexKey)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", base, err)
			}
			out = append(out, resource{
				node: graph.Node{
					Address:  base + key,
					Module:   r.Module,
					Mode:     graph.Mode(r.Mode),
					Type:     r.Type,
					Name:     r.Name,
					Provider: providerSource(r.Provider),
				},
				attrs:     inst.Attributes,
				dependsOn: inst.Dependencies,
			})
		}
	}
	return out, nil
}

// indexSuffix renders an instance key the way Terraform prints addresses:
// nothing for a single instance, [0] for count, ["k"] for for_each.
func indexSuffix(key any) (string, error) {
	switch k := key.(type) {
	case nil:
		return "", nil
	case float64:
		return "[" + strconv.Itoa(int(k)) + "]", nil
	case string:
		return "[" + strconv.Quote(k) + "]", nil
	default:
		return "", fmt.Errorf("unexpected index_key of type %T", key)
	}
}
