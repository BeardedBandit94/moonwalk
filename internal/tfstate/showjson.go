package tfstate

import (
	"encoding/json"
	"fmt"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// parseShowJSON reads the output of `terraform show -json` for a state.
func parseShowJSON(data []byte) ([]resource, error) {
	var st tfjson.State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("tfstate: parse show-json state: %w", err)
	}
	if st.Values == nil || st.Values.RootModule == nil {
		return nil, nil // An empty state has no values.
	}
	var out []resource
	walkModule(st.Values.RootModule, &out)
	return out, nil
}

func walkModule(m *tfjson.StateModule, out *[]resource) {
	for _, r := range m.Resources {
		if r.DeposedKey != "" {
			continue // A deposed object shares its address with the current one.
		}
		*out = append(*out, resource{
			node: graph.Node{
				ID:            r.Address,
				ConfigAddress: configAddress(r.Address),
				Module:        m.Address,
				Mode:          graph.Mode(r.Mode),
				Type:          r.Type,
				Name:          r.Name,
				Provider:      r.ProviderName,
				Status:        graph.StatusManaged,
			},
			dependsOn: r.DependsOn,
		})
	}
	for _, child := range m.ChildModules {
		walkModule(child, out)
	}
}
