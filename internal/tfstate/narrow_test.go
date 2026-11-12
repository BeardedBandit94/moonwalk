package tfstate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// dependencies lists the dependency edges as "from > to", without the source.
func dependencies(g *graph.Graph) []string {
	var out []string
	for _, e := range g.Edges() {
		if e.Kind == graph.EdgeDependsOn {
			out = append(out, strings.ReplaceAll(e.From+" > "+e.To, "net::", ""))
		}
	}
	return out
}

func sameLines(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// The network fixture: two subnets from one block, a machine in the second,
// a load balancer across both, and a route table association per subnet.
func TestDependenciesAreNarrowedToTheInstancesMeant(t *testing.T) {
	g := graph.New()
	loadInto(t, g, "net", "network.tfstate")
	sameLines(t, dependencies(g), []string{
		"aws_iam_role_policy_attachment.app > aws_iam_policy.read",
		"aws_iam_role_policy_attachment.app > aws_iam_role.app",
		// The machine holds one subnet's ID, so only that edge is kept.
		"aws_instance.app > aws_subnet.private[1]",
		"aws_instance.app > aws_vpc.main",
		// The load balancer holds both, so both stay.
		"aws_lb.web > aws_subnet.private[0]",
		"aws_lb.web > aws_subnet.private[1]",
		"aws_lb.web > aws_vpc.main",
		"aws_route_table.private > aws_vpc.main",
		"aws_route_table_association.private[0] > aws_route_table.private",
		"aws_route_table_association.private[0] > aws_subnet.private[0]",
		"aws_route_table_association.private[0] > aws_vpc.main",
		"aws_route_table_association.private[1] > aws_route_table.private",
		"aws_route_table_association.private[1] > aws_subnet.private[1]",
		"aws_route_table_association.private[1] > aws_vpc.main",
		"aws_subnet.private[0] > aws_vpc.main",
		"aws_subnet.private[1] > aws_vpc.main",
	})
}

// With nothing in its attributes to tell the instances apart, a resource
// keeps its dependency on all of them.
func TestNarrowingKeepsWhatItCannotSettle(t *testing.T) {
	g := graph.New()
	err := Load(g, "net", strings.NewReader(`{"version":4,"resources":[
	  {"mode":"managed","type":"t","name":"pool","provider":"p","instances":[
	    {"index_key":0,"attributes":{"id":"a","zone":"z"}},
	    {"index_key":1,"attributes":{"id":"b","zone":"z"}}]},
	  {"mode":"managed","type":"t","name":"opaque","provider":"p",
	   "instances":[{"attributes":{"count":2},"dependencies":["t.pool"]}]},
	  {"mode":"managed","type":"t","name":"shared","provider":"p",
	   "instances":[{"attributes":{"zone":"z"},"dependencies":["t.pool"]}]},
	  {"mode":"managed","type":"t","name":"nested","provider":"p",
	   "instances":[{"attributes":{"targets":[{"ref":"b"}]},"dependencies":["t.pool"]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	sameLines(t, dependencies(g), []string{
		"t.nested > t.pool[1]", // Found by a value nested in a block.
		"t.opaque > t.pool[0]",
		"t.opaque > t.pool[1]",
		"t.shared > t.pool[0]", // "z" belongs to both instances, so it settles nothing.
		"t.shared > t.pool[1]",
	})
}

// State lists what a resource depends on through another next to what it
// depends on itself. A machine holds no subnet's ID, only its network
// interface's, so its dependency on the subnets is the interface's, and
// means the subnet the interface is in.
func TestOpenDependenciesFollowTheOthers(t *testing.T) {
	g := graph.New()
	err := Load(g, "net", strings.NewReader(`{"version":4,"resources":[
	  {"mode":"managed","type":"t","name":"subnet","provider":"p","instances":[
	    {"index_key":"a","attributes":{"id":"subnet-a"}},
	    {"index_key":"b","attributes":{"id":"subnet-b"}},
	    {"index_key":"c","attributes":{"id":"subnet-c"}}]},
	  {"mode":"managed","type":"t","name":"nic","provider":"p",
	   "instances":[{"attributes":{"id":"nic-1","subnet_id":"subnet-b"},"dependencies":["t.subnet"]}]},
	  {"mode":"managed","type":"t","name":"machine","provider":"p",
	   "instances":[{"attributes":{"id":"vm-1","nics":["nic-1"]},"dependencies":["t.nic","t.subnet"]}]},
	  {"mode":"managed","type":"t","name":"disk","provider":"p",
	   "instances":[{"attributes":{"id":"disk-1","machine":"vm-1"},"dependencies":["t.machine","t.nic","t.subnet"]}]},
	  {"mode":"managed","type":"t","name":"balancer","provider":"p",
	   "instances":[{"attributes":{"id":"lb-1","subnets":["subnet-a","subnet-b"]},"dependencies":["t.subnet"]}]},
	  {"mode":"managed","type":"t","name":"listener","provider":"p",
	   "instances":[{"attributes":{"id":"l-1","balancer":"lb-1"},"dependencies":["t.balancer","t.nic","t.subnet"]}]},
	  {"mode":"managed","type":"t","name":"report","provider":"p",
	   "instances":[{"attributes":{"id":"r-1"},"dependencies":["t.subnet"]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	sameLines(t, dependencies(g), []string{
		"t.balancer > t.subnet[\"a\"]",
		"t.balancer > t.subnet[\"b\"]",
		// Two steps away from the subnet, settled once the machine is.
		"t.disk > t.machine",
		"t.disk > t.nic",
		"t.disk > t.subnet[\"b\"]",
		// What the balancer and the interface depend on, taken together.
		"t.listener > t.balancer",
		"t.listener > t.nic",
		"t.listener > t.subnet[\"a\"]",
		"t.listener > t.subnet[\"b\"]",
		"t.machine > t.nic",
		"t.machine > t.subnet[\"b\"]",
		"t.nic > t.subnet[\"b\"]",
		// Nothing else it depends on says which: all three stay.
		"t.report > t.subnet[\"a\"]",
		"t.report > t.subnet[\"b\"]",
		"t.report > t.subnet[\"c\"]",
	})
}

// The Azure fixture: a network interface names its subnet with one
// segment of the ID in another case, as Azure returns it, and a machine
// names only the interface.
func TestAzureDependenciesAreNarrowed(t *testing.T) {
	g := graph.New()
	loadInto(t, g, "net", "azure.tfstate")
	var got []string
	for _, line := range dependencies(g) {
		if strings.Contains(line, " > azurerm_subnet.app") {
			got = append(got, line)
		}
	}
	sameLines(t, got, []string{
		// A cluster with nodes in one subnet and pods in the other.
		`azurerm_kubernetes_cluster.app > azurerm_subnet.app["data"]`,
		`azurerm_kubernetes_cluster.app > azurerm_subnet.app["web"]`,
		`azurerm_linux_virtual_machine.web > azurerm_subnet.app["web"]`,
		`azurerm_network_interface.web > azurerm_subnet.app["web"]`,
		`azurerm_private_endpoint.sql > azurerm_subnet.app["data"]`,
		`azurerm_subnet_network_security_group_association.web > azurerm_subnet.app["web"]`,
	})
}

func TestAzurePartition(t *testing.T) {
	g := graph.New()
	loadInto(t, g, "net", "azure.tfstate")
	const subscription = "11111111-2222-3333-4444-555555555555"
	want := map[string][2]string{ // Resource group and region.
		`azurerm_resource_group.app`:        {"rg-app", "westeurope"},
		`azurerm_virtual_network.hub`:       {"rg-shared", "northeurope"},
		`azurerm_subnet.app["web"]`:         {"rg-app", ""}, // A subnet states no location.
		`azurerm_mssql_database.app`:        {"rg-app", ""}, // Nor a group: it is in its ID.
		`azurerm_linux_virtual_machine.web`: {"rg-app", "westeurope"},
	}
	for address, w := range want {
		n, ok := g.Node("net::" + address)
		if !ok || n.Account != subscription || n.ResourceGroup != w[0] || n.Region != w[1] {
			t.Errorf("%s: account %q, group %q, region %q; want %q, %q, %q", address, n.Account, n.ResourceGroup, n.Region, subscription, w[0], w[1])
		}
	}
	// Other clouds have no such groups.
	other := graph.New()
	loadInto(t, other, "net", "network.tfstate")
	for n := range other.AllNodes() {
		if n.ResourceGroup != "" {
			t.Errorf("%s has the resource group %q", n.Address, n.ResourceGroup)
		}
	}
}
