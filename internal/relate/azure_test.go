package relate

import (
	"os"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

// azure loads an Azure-shaped state: two resource groups; a network with
// two subnets from one block and a peering to a second network; a machine
// with a network interface in one subnet; a private endpoint in the other;
// a cluster across both; a database on a server; and a storage account.
func azure(t *testing.T) *graph.Graph {
	t.Helper()
	f, err := os.Open("../tfstate/testdata/azure.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g := graph.New()
	if err := tfstate.Load(g, "net", f); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestAzureContainment(t *testing.T) {
	g := azure(t)
	if added := Containment(g); added != 8 {
		t.Errorf("added %d containment edges, want 8", added)
	}
	same(t, "contains", edges(g, graph.EdgeContains), []string{
		`azurerm_subnet.app["data"] > azurerm_private_endpoint.sql`,
		// A machine names no subnet. It sits where its interface does.
		`azurerm_subnet.app["web"] > azurerm_linux_virtual_machine.web`,
		// The interface names its subnet with "resourcegroups" in lower
		// case, as Azure returned it; it is the same subnet.
		`azurerm_subnet.app["web"] > azurerm_network_interface.web`,
		`azurerm_subnet.app["web"] > azurerm_subnet_network_security_group_association.web`,
		// Nodes in one subnet and pods in the other: the network.
		`azurerm_virtual_network.app > azurerm_kubernetes_cluster.app`,
		// A subnet is part of the network its ID starts with, and so is a peering.
		`azurerm_virtual_network.app > azurerm_subnet.app["data"]`,
		`azurerm_virtual_network.app > azurerm_subnet.app["web"]`,
		`azurerm_virtual_network.app > azurerm_virtual_network_peering.app_to_hub`,
	})
	// Running it again changes nothing.
	if again := Containment(g); again != 8 {
		t.Errorf("second run reported %d", again)
	}
	if got := edges(g, graph.EdgeContains); len(got) != 8 {
		t.Errorf("second run left %d edges", len(got))
	}
}

func TestAzureRegionsAreInherited(t *testing.T) {
	g := azure(t)
	if n := InheritRegions(g); n != 5 {
		t.Errorf("gave %d resources a region, want 5", n)
	}
	want := map[string]string{
		`azurerm_subnet.app["web"]`:                             "westeurope", // Its network's.
		`azurerm_subnet.app["data"]`:                            "westeurope",
		`azurerm_mssql_database.app`:                            "westeurope", // Its server's.
		`azurerm_virtual_network_peering.app_to_hub`:            "westeurope", // The network it starts from, not the one it reaches.
		`azurerm_subnet_network_security_group_association.web`: "westeurope", // The subnet's, which is itself inherited.
	}
	for address, region := range want {
		n, _ := g.Node("net::" + address)
		if n.Region != region || !n.RegionInferred {
			t.Errorf("%s: region %q (inferred %v), want %q inferred", address, n.Region, n.RegionInferred, region)
		}
	}
	// A resource group's location is not its contents': the storage
	// account says where it is, and nothing takes a group's region.
	for address, region := range map[string]string{"azurerm_storage_account.logs": "northeurope", "azurerm_virtual_network.app": "westeurope"} {
		if n, _ := g.Node("net::" + address); n.Region != region || n.RegionInferred {
			t.Errorf("%s: region %q (inferred %v), want %q as stated", address, n.Region, n.RegionInferred, region)
		}
	}
	if n := InheritRegions(g); n != 0 {
		t.Errorf("second run changed %d", n)
	}
}

func TestAzureGlue(t *testing.T) {
	g := azure(t)
	if n := MarkGlue(g); n != 2 {
		t.Errorf("marked %d, want 2", n)
	}
	same(t, "links", edges(g, graph.EdgeLinks), []string{
		`azurerm_network_security_group.web > azurerm_subnet.app["web"] via azurerm_subnet_network_security_group_association.web`,
		// A peering joins two networks.
		`azurerm_virtual_network.app > azurerm_virtual_network.hub via azurerm_virtual_network_peering.app_to_hub`,
	})
}

// A resource in another state that names an Azure resource is linked to
// it however the ID is spelled, and although the subnet's association
// with its security group carries the same ID as the subnet.
func TestAzureCrossState(t *testing.T) {
	g := azure(t)
	const subnet = "/subscriptions/11111111-2222-3333-4444-555555555555/RESOURCEGROUPS/rg-app/providers/Microsoft.Network/virtualNetworks/vnet-app/subnets/web"
	loadString(t, g, "app", `{"version":4,"terraform_version":"1.14.0","resources":[
	  {"mode":"managed","type":"azurerm_network_interface","name":"batch","provider":"provider[\"registry.terraform.io/hashicorp/azurerm\"]",
	   "instances":[{"attributes":{"id":"/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg-batch/providers/Microsoft.Network/networkInterfaces/nic-batch",
	     "name":"nic-batch","resource_group_name":"rg-batch","location":"westeurope",
	     "ip_configuration":[{"name":"internal","subnet_id":"`+subnet+`"}]}}]}]}`)
	res := Enrich(g)
	if res.CrossState != 1 {
		t.Errorf("cross-state links = %d, want 1", res.CrossState)
	}
	if !g.HasEdge(graph.Edge{From: "app::azurerm_network_interface.batch", To: `net::azurerm_subnet.app["web"]`, Kind: graph.EdgeDependsOn}) {
		t.Error("the interface in the other state is not linked to the subnet it names")
	}
	// And it sits in that subnet, which another state manages.
	if !g.HasEdge(graph.Edge{From: `net::azurerm_subnet.app["web"]`, To: "app::azurerm_network_interface.batch", Kind: graph.EdgeContains}) {
		t.Error("the interface in the other state is not placed in the subnet")
	}
}

// What is not Azure's is left alone by the Azure rules, and the other way
// round.
func TestCloudsDoNotMix(t *testing.T) {
	g := network(t)
	before := edges(g, graph.EdgeContains)
	if n := InheritRegions(g); n != 0 {
		t.Errorf("an AWS state had %d regions inherited", n)
	}
	Containment(g)
	aws := edges(g, graph.EdgeContains)
	if len(aws) == 0 || len(before) != 0 {
		t.Fatalf("containment on the AWS fixture: %d before, %d after", len(before), len(aws))
	}
	for _, e := range aws {
		if strings.Contains(e, "azurerm_") {
			t.Errorf("unexpected edge %s", e)
		}
	}
}
