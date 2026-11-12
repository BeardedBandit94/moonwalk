package discover

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/azureid"
	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// azureResource is one entry of an Azure CLI listing. These commands all
// print the same shape, a list of resources by ID:
//
//	az resource list
//	az group list
//	az graph query -q "Resources"
//
// The last wraps the list in an object, under "data".
type azureResource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Kind      string `json:"kind"`
	Location  string `json:"location"`
	ManagedBy string `json:"managedBy"`
}

// IsAzureListing reports whether data looks like the output of one of the
// Azure CLI commands that LoadAzure reads. It looks at the first resource
// only, so it costs the same however long the listing is.
func IsAzureListing(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil {
		return false
	}
	if first == json.Delim('{') {
		// A Resource Graph answer: a few numbers, and the list as "data".
		for {
			key, err := dec.Token()
			if err != nil {
				return false
			}
			value, err := dec.Token()
			if err != nil {
				return false
			}
			if _, nested := value.(json.Delim); nested {
				if key != "data" || value != json.Delim('[') {
					return false
				}
				break
			}
		}
	} else if first != json.Delim('[') {
		return false
	}
	var r azureResource
	return dec.More() && dec.Decode(&r) == nil && azureid.Is(r.ID) && r.Type != ""
}

// LoadAzure reads a listing of Azure resources from r and joins it to g
// under the given source name, as Load does for query output. A listed
// resource is matched to a resource in state by its ID, compared as Azure
// compares IDs. Nothing is known about a listed resource but its ID, name,
// type and location, so it cannot be placed in a network.
func LoadAzure(g *graph.Graph, source string, r io.Reader) (Result, error) {
	var res Result
	if source == "" {
		return res, fmt.Errorf("discover: source name is empty")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return res, fmt.Errorf("discover: %s: %w", source, err)
	}
	var listing []azureResource
	if err := json.Unmarshal(data, &listing); err != nil {
		var wrapped struct {
			Data []azureResource `json:"data"`
		}
		if err := json.Unmarshal(data, &wrapped); err != nil {
			return res, fmt.Errorf("discover: %s: not an Azure CLI resource listing: %w", source, err)
		}
		listing = wrapped.Data
	}

	inState := map[string]bool{}
	for n := range g.AllNodes() {
		if n.Status != graph.StatusDeclared && n.Status != graph.StatusUnmanaged && azureid.Is(n.CloudID) {
			inState[azureid.Canonical(n.CloudID)] = true
		}
	}
	seen := map[string]bool{}
	for _, r := range listing {
		key := azureid.Canonical(r.ID)
		if !azureid.Is(r.ID) || seen[key] {
			continue
		}
		seen[key] = true
		res.Found++
		group := azureid.Part(r.ID, "resourceGroups")
		switch {
		case madeByAzure(r, group):
			res.Defaults++
			continue
		case inState[key]:
			res.Matched++
			continue
		}
		resourceType := azureType(r)
		account := azureid.Part(r.ID, "subscriptions")
		address := resourceType + "." + r.Name
		n := graph.Node{
			// A name repeats across subscriptions and resource groups.
			ID:            source + "::" + address + "@" + account + "/" + group,
			Source:        source,
			Address:       address,
			ConfigAddress: address,
			Mode:          graph.ModeManaged,
			Type:          resourceType,
			Name:          r.Name,
			Status:        graph.StatusUnmanaged,
			Account:       account,
			Region:        r.Location,
			ResourceGroup: group,
			CloudID:       r.ID,
		}
		if err := g.AddNode(n); err != nil {
			return res, fmt.Errorf("discover: %s: %w", source, err)
		}
		res.Unmanaged++
	}
	return res, nil
}

// madeByAzure reports whether a listed resource is one that Azure makes by
// itself, or that another resource makes and looks after, which nobody
// would expect to find in code: the network watcher of each region, the
// resource group a Kubernetes cluster keeps its nodes in, and what is in
// such groups.
func madeByAzure(r azureResource, group string) bool {
	if strings.EqualFold(r.Type, "Microsoft.Resources/resourceGroups") && r.ManagedBy != "" {
		return true // Its managedBy names the resource that owns it.
	}
	lower := strings.ToLower(group)
	return lower == "networkwatcherrg" || strings.HasPrefix(lower, "mc_") || strings.HasPrefix(lower, "defaultresourcegroup-")
}

// azureType returns the Terraform resource type for a listed resource,
// where its Azure type is one of the common ones, so that filters and
// groupings treat it like its managed neighbours. Otherwise it returns a
// name made from the Azure type, such as "azure:Microsoft.Maps/accounts".
func azureType(r azureResource) string {
	key := strings.ToLower(r.Type)
	if key == "microsoft.web/sites" {
		// One Azure type, told apart by kind: "app,linux", "functionapp".
		kind := strings.ToLower(r.Kind)
		os := "windows"
		if strings.Contains(kind, "linux") {
			os = "linux"
		}
		if strings.Contains(kind, "functionapp") {
			return "azurerm_" + os + "_function_app"
		}
		return "azurerm_" + os + "_web_app"
	}
	if t, ok := azureTypes[key]; ok {
		return t
	}
	return "azure:" + r.Type
}

// azureTypes maps Azure resource types, in lower case, to Terraform's.
var azureTypes = map[string]string{
	"microsoft.cache/redis":              "azurerm_redis_cache",
	"microsoft.compute/availabilitysets": "azurerm_availability_set",
	"microsoft.compute/disks":            "azurerm_managed_disk",
	// A listing does not say which system a machine runs, and Terraform
	// has a type for each. This is the type that covers both.
	"microsoft.compute/virtualmachines":                "azurerm_virtual_machine",
	"microsoft.compute/virtualmachinescalesets":        "azurerm_virtual_machine_scale_set",
	"microsoft.containerregistry/registries":           "azurerm_container_registry",
	"microsoft.containerservice/managedclusters":       "azurerm_kubernetes_cluster",
	"microsoft.dbformysql/flexibleservers":             "azurerm_mysql_flexible_server",
	"microsoft.dbforpostgresql/flexibleservers":        "azurerm_postgresql_flexible_server",
	"microsoft.documentdb/databaseaccounts":            "azurerm_cosmosdb_account",
	"microsoft.eventhub/namespaces":                    "azurerm_eventhub_namespace",
	"microsoft.insights/components":                    "azurerm_application_insights",
	"microsoft.keyvault/vaults":                        "azurerm_key_vault",
	"microsoft.managedidentity/userassignedidentities": "azurerm_user_assigned_identity",
	"microsoft.network/applicationgateways":            "azurerm_application_gateway",
	"microsoft.network/azurefirewalls":                 "azurerm_firewall",
	"microsoft.network/dnszones":                       "azurerm_dns_zone",
	"microsoft.network/loadbalancers":                  "azurerm_lb",
	"microsoft.network/natgateways":                    "azurerm_nat_gateway",
	"microsoft.network/networkinterfaces":              "azurerm_network_interface",
	"microsoft.network/networksecuritygroups":          "azurerm_network_security_group",
	"microsoft.network/privatednszones":                "azurerm_private_dns_zone",
	"microsoft.network/privateendpoints":               "azurerm_private_endpoint",
	"microsoft.network/publicipaddresses":              "azurerm_public_ip",
	"microsoft.network/routetables":                    "azurerm_route_table",
	"microsoft.network/virtualnetworkgateways":         "azurerm_virtual_network_gateway",
	"microsoft.network/virtualnetworks":                "azurerm_virtual_network",
	"microsoft.operationalinsights/workspaces":         "azurerm_log_analytics_workspace",
	"microsoft.resources/resourcegroups":               "azurerm_resource_group",
	"microsoft.servicebus/namespaces":                  "azurerm_servicebus_namespace",
	"microsoft.sql/servers":                            "azurerm_mssql_server",
	"microsoft.sql/servers/databases":                  "azurerm_mssql_database",
	"microsoft.storage/storageaccounts":                "azurerm_storage_account",
	"microsoft.web/serverfarms":                        "azurerm_service_plan",
}
