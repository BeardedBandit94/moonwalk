package discover

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

// azureState loads the Azure state fixture: two resource groups, a network
// with subnets, a machine, a database server and a storage account.
func azureState(t *testing.T) *graph.Graph {
	t.Helper()
	f, err := os.Open("../tfstate/testdata/azure.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g := graph.New()
	if err := tfstate.Load(g, "az", f); err != nil {
		t.Fatal(err)
	}
	return g
}

func loadAzure(t *testing.T, g *graph.Graph, file string) Result {
	t.Helper()
	f, err := os.Open("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := LoadAzure(g, "cloud", f)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// unmanaged lists the unmanaged resources as "type name @account/group/region".
func unmanaged(g *graph.Graph) []string {
	var out []string
	for _, n := range g.Nodes() {
		if n.Status == graph.StatusUnmanaged {
			out = append(out, n.Type+" "+n.Name+" @"+n.Account+"/"+n.ResourceGroup+"/"+n.Region)
		}
	}
	return out
}

const subscription = "11111111-2222-3333-4444-555555555555"

func TestLoadAzureResourceList(t *testing.T) {
	g := azureState(t)
	res := loadAzure(t, g, "az-resource-list.json")
	// Nine resources, one of them listed twice. Three are in the state,
	// one of those under an ID spelled in another case; two are Azure's own.
	want := Result{Found: 9, Matched: 3, Unmanaged: 4, Defaults: 2}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	got := unmanaged(g)
	wantNodes := []string{
		// A type with no Terraform name of its own keeps Azure's.
		"azure:Microsoft.Maps/accounts maps-demo @" + subscription + "/rg-sandbox/global",
		// A site's kind says which of four Terraform types it is.
		"azurerm_linux_function_app fn-report @" + subscription + "/rg-sandbox/westeurope",
		"azurerm_public_ip pip-test @" + subscription + "/rg-sandbox/westeurope",
		"azurerm_storage_account stmanual01 @" + subscription + "/rg-app/westeurope",
	}
	if !reflect.DeepEqual(got, wantNodes) {
		t.Errorf("unmanaged:\n  got  %s\n  want %s", strings.Join(got, "\n       "), strings.Join(wantNodes, "\n       "))
	}
	n, ok := g.Node("cloud::azurerm_storage_account.stmanual01@" + subscription + "/rg-app")
	if !ok || n.CloudID != "/subscriptions/"+subscription+"/resourceGroups/rg-app/providers/Microsoft.Storage/storageAccounts/stmanual01" {
		t.Errorf("unmanaged storage account = %+v, %v", n, ok)
	}
}

func TestLoadAzureGroupList(t *testing.T) {
	g := azureState(t)
	res := loadAzure(t, g, "az-group-list.json")
	// Two groups are in the state; one is a cluster's own, as its
	// managedBy says, and one is the network watchers'.
	want := Result{Found: 5, Matched: 2, Unmanaged: 1, Defaults: 2}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	if got := unmanaged(g); !reflect.DeepEqual(got, []string{"azurerm_resource_group rg-sandbox @" + subscription + "/rg-sandbox/westeurope"}) {
		t.Errorf("unmanaged = %q", got)
	}
}

// Resource Graph wraps the list in an object and writes types in lower
// case; what it finds is the same.
func TestLoadAzureGraphQuery(t *testing.T) {
	g := azureState(t)
	res := loadAzure(t, g, "az-graph-query.json")
	if want := (Result{Found: 7, Matched: 3, Unmanaged: 4}); !reflect.DeepEqual(res, want) {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	got := unmanaged(g)
	if len(got) != 4 || got[0] != "azure:microsoft.maps/accounts maps-demo @"+subscription+"/rg-sandbox/global" || !strings.HasPrefix(got[3], "azurerm_storage_account stmanual01") {
		t.Errorf("unmanaged = %q", got)
	}
}

// Without a state, everything listed that Azure did not make is unmanaged.
func TestLoadAzureWithoutState(t *testing.T) {
	g := graph.New()
	res := loadAzure(t, g, "az-resource-list.json")
	if res.Matched != 0 || res.Unmanaged != 7 || res.Defaults != 2 {
		t.Errorf("result = %+v", res)
	}
	// A machine's listing does not say which system it runs.
	if _, ok := g.Node("cloud::azurerm_virtual_machine.vm-web@" + subscription + "/rg-app"); !ok {
		t.Errorf("no node for the listed machine among %q", unmanaged(g))
	}
}

func TestIsAzureListing(t *testing.T) {
	read := func(path string) []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, file := range []string{"az-resource-list.json", "az-group-list.json", "az-graph-query.json"} {
		data := read("testdata/" + file)
		if !IsAzureListing(data) {
			t.Errorf("%s is not recognised", file)
		}
		if IsARNListing(data) || IsQueryOutput(data) {
			t.Errorf("%s is taken for another kind of listing", file)
		}
	}
	others := map[string][]byte{
		"a state":                read("../tfstate/testdata/azure.tfstate"),
		"query output":           read("testdata/query.jsonl"),
		"an AWS listing":         read("testdata/tagging-api.json"),
		"a Resource Explorer":    read("testdata/resource-explorer.json"),
		"an empty list":          []byte(`[]`),
		"a list of other things": []byte(`[{"id": "vpc-1", "type": "x"}]`),
		"IDs without types":      []byte(`[{"id": "/subscriptions/1111/resourceGroups/rg"}]`),
		"data that is no list":   []byte(`{"data": {"id": "/subscriptions/1111/resourceGroups/rg", "type": "x"}}`),
		"not JSON":               []byte(`/subscriptions/1111`),
	}
	for name, data := range others {
		if IsAzureListing(data) {
			t.Errorf("%s is taken for an Azure listing", name)
		}
	}
}

func TestLoadAzureErrors(t *testing.T) {
	if _, err := LoadAzure(graph.New(), "", strings.NewReader(`[]`)); err == nil {
		t.Error("empty source name: no error")
	}
	if _, err := LoadAzure(graph.New(), "cloud", strings.NewReader(`"text"`)); err == nil {
		t.Error("not a listing: no error")
	}
	// Entries that are not Azure resources are passed over.
	res, err := LoadAzure(graph.New(), "cloud", strings.NewReader(`[{"id": "vpc-1", "type": "x", "name": "n"}, {"id": "/subscriptions/1111/resourceGroups/rg", "type": "Microsoft.Resources/resourceGroups", "name": "rg", "location": "westeurope"}]`))
	if err != nil || res.Found != 1 || res.Unmanaged != 1 {
		t.Errorf("mixed entries: %+v, %v", res, err)
	}
}

// A resource found by `terraform query` carries its resource group, and
// two of one name in different groups are two resources.
func TestQueryOutputKeepsAzureResourceGroups(t *testing.T) {
	g := graph.New()
	input := line("azurerm_storage_account", "logs", map[string]any{"subscription_id": "sub-1", "resource_group_name": "rg-a", "name": "logs"}, nil) +
		line("azurerm_storage_account", "logs", map[string]any{"subscription_id": "sub-1", "resource_group_name": "rg-b", "name": "logs"},
			map[string]any{"id": "/subscriptions/sub-1/resourceGroups/rg-b/providers/Microsoft.Storage/storageAccounts/logs", "name": "logs", "resource_group_name": "rg-b", "location": "westeurope"})
	res, err := Load(g, "cloud", strings.NewReader(input))
	if err != nil || res.Unmanaged != 2 {
		t.Fatalf("result %+v, err %v", res, err)
	}
	got := unmanaged(g)
	want := []string{"azurerm_storage_account logs @sub-1/rg-a/", "azurerm_storage_account logs @sub-1/rg-b/westeurope"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unmanaged = %q, want %q", got, want)
	}
}
