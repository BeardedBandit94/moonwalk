package tfstate

import (
	"encoding/json"
	"fmt"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// parseShowJSON reads the output of `terraform show -json` for a state.
func parseShowJSON(data []byte) (parsed, error) {
	var st tfjson.State
	st.UseJSONNumber(true) // Keep numbers exact, as the raw parser does.
	if err := json.Unmarshal(data, &st); err != nil {
		return parsed{}, fmt.Errorf("parse show-json state: %w", err)
	}
	p := parsed{secrets: secretSet{}}
	if st.Values != nil && st.Values.RootModule != nil { // An empty state has no values.
		p.walkModule(st.Values.RootModule)
	}
	return p, nil
}

func (p *parsed) walkModule(m *tfjson.StateModule) {
	for _, r := range m.Resources {
		if r.DeposedKey != "" {
			continue // A deposed object shares its address with the current one.
		}
		p.resources = append(p.resources, resource{
			node: graph.Node{
				Address:    r.Address,
				Module:     m.Address,
				Mode:       graph.Mode(r.Mode),
				Type:       r.Type,
				Name:       r.Name,
				Provider:   r.ProviderName,
				Attributes: p.redactShowValues(r.AttributeValues, r.SensitiveValues),
			},
			// This format does not say which provider alias was used.
			providerKey: r.ProviderName,
			dependsOn:   r.DependsOn,
		})
	}
	for _, child := range m.ChildModules {
		p.walkModule(child)
	}
}

// redactShowValues applies a resource's sensitive_values mask. Attributes
// are dropped entirely if the mask cannot be read.
func (p *parsed) redactShowValues(values map[string]any, rawMask json.RawMessage) map[string]any {
	if len(values) == 0 {
		return nil
	}
	if len(rawMask) == 0 {
		return values
	}
	var mask any
	if err := json.Unmarshal(rawMask, &mask); err != nil {
		return nil
	}
	redacted, ok := p.secrets.applyMask(values, mask).(map[string]any)
	if !ok {
		return nil // The mask covered the whole resource.
	}
	return redacted
}
