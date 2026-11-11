package tfstate

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// rawState is the subset of a version 4 state file moonwalk reads, plus the
// top-level fields that identify the other encodings.
type rawState struct {
	Version       *int            `json:"version"`
	FormatVersion string          `json:"format_version"` // Set by show-json output.
	EncryptedData json.RawMessage `json:"encrypted_data"` // Set by OpenTofu state encryption.

	Resources []struct {
		Module    string `json:"module"`
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Instances []struct {
			IndexKey            any            `json:"index_key"`
			Deposed             string         `json:"deposed"`
			Attributes          map[string]any `json:"attributes"`
			SensitiveAttributes [][]pathStep   `json:"sensitive_attributes"`
			Dependencies        []string       `json:"dependencies"`
		} `json:"instances"`
	} `json:"resources"`
}

// parseRaw reads a decoded raw version 4 state file.
func parseRaw(st rawState) (parsed, error) {
	p := parsed{secrets: secretSet{}}
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
				return parsed{}, fmt.Errorf("%s: %w", base, err)
			}
			var attrs map[string]any
			if len(inst.Attributes) > 0 {
				v := unwrapDynamic(inst.Attributes)
				for _, path := range inst.SensitiveAttributes {
					v = p.secrets.applyPath(v, path)
				}
				p.secrets.redactByName(v)
				attrs, _ = v.(map[string]any) // Not a map if the whole resource was marked.
			}
			p.resources = append(p.resources, resource{
				node: graph.Node{
					Address:    base + key,
					Module:     r.Module,
					Mode:       graph.Mode(r.Mode),
					Type:       r.Type,
					Name:       r.Name,
					Provider:   providerSource(r.Provider),
					Attributes: attrs,
				},
				providerKey: r.Provider,
				dependsOn:   inst.Dependencies,
			})
		}
	}
	return p, nil
}

// indexSuffix renders an instance key the way Terraform prints addresses:
// nothing for a single instance, [0] for count, ["k"] for for_each.
func indexSuffix(key any) (string, error) {
	switch k := key.(type) {
	case nil:
		return "", nil
	case json.Number:
		if _, err := strconv.Atoi(k.String()); err != nil {
			return "", fmt.Errorf("index_key %s is not an integer", k)
		}
		return "[" + k.String() + "]", nil
	case string:
		return "[" + strconv.Quote(k) + "]", nil
	default:
		return "", fmt.Errorf("unexpected index_key of type %T", key)
	}
}
