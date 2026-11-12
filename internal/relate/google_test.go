package relate

import (
	"os"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

// google loads a Google Cloud state: two networks with a peering between
// them; two subnetworks from one block; a firewall; two machines, one
// naming its subnetwork by self link and one the short way; a cluster; a
// database that names its network two blocks down; a router; a bucket and
// a service account.
func google(t *testing.T) *graph.Graph {
	t.Helper()
	f, err := os.Open("../tfstate/testdata/google.tfstate")
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

func TestGoogleContainment(t *testing.T) {
	g := google(t)
	if added := Containment(g); added != 9 {
		t.Errorf("added %d containment edges, want 9", added)
	}
	same(t, "contains", edges(g, graph.EdgeContains), []string{
		`google_compute_network.main > google_compute_firewall.web`,
		// A peering belongs to the network it starts from.
		`google_compute_network.main > google_compute_network_peering.main_to_shared`,
		`google_compute_network.main > google_compute_router.nat`,
		`google_compute_network.main > google_compute_subnetwork.app["data"]`,
		`google_compute_network.main > google_compute_subnetwork.app["web"]`,
		// A database names its network under settings, then ip_configuration.
		`google_compute_network.main > google_sql_database_instance.db`,
		// Named without the address in front; it is the same subnetwork.
		`google_compute_subnetwork.app["data"] > google_compute_instance.batch`,
		`google_compute_subnetwork.app["web"] > google_compute_instance.web`,
		// A cluster names a network and a subnetwork, and sits in the subnetwork.
		`google_compute_subnetwork.app["web"] > google_container_cluster.main`,
	})
	if again := Containment(g); again != 9 || len(edges(g, graph.EdgeContains)) != 9 {
		t.Errorf("a second run changed something: %d", again)
	}
}

func TestGoogleGlue(t *testing.T) {
	g := google(t)
	if n := MarkGlue(g); n != 1 {
		t.Errorf("marked %d, want 1", n)
	}
	same(t, "links", edges(g, graph.EdgeLinks), []string{
		`google_compute_network.main > google_compute_network.shared via google_compute_network_peering.main_to_shared`,
	})
}

// A network or subnetwork named by its name alone is the one in the same
// project, and for a subnetwork the same region. With nothing to say
// which project, or with the name in another one, nothing is placed.
func TestGoogleShortNames(t *testing.T) {
	g := graph.New()
	loadString(t, g, "net", `{"version":4,"terraform_version":"1.14.0","resources":[
	  {"mode":"managed","type":"google_compute_network","name":"main","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/acme/global/networks/main","name":"main","project":"acme"}}]},
	  {"mode":"managed","type":"google_compute_network","name":"other","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/other/global/networks/main","name":"main","project":"other"}}]},
	  {"mode":"managed","type":"google_compute_subnetwork","name":"web","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/acme/regions/europe-west1/subnetworks/web","name":"web","project":"acme","region":"europe-west1","network":"main"}}]},
	  {"mode":"managed","type":"google_compute_address","name":"here","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/acme/regions/europe-west1/addresses/a","name":"a","project":"acme","region":"europe-west1","subnetwork":"web"}}]},
	  {"mode":"managed","type":"google_compute_address","name":"elsewhere","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/acme/regions/us-east1/addresses/b","name":"b","project":"acme","region":"us-east1","subnetwork":"web"}}]},
	  {"mode":"managed","type":"google_compute_firewall","name":"theirs","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/other/global/firewalls/f","name":"f","project":"other","network":"main"}}]},
	  {"mode":"managed","type":"google_compute_firewall","name":"gone","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/acme/global/firewalls/g","name":"g","project":"acme","network":"projects/acme/global/networks/deleted"}}]}]}`)
	Containment(g)
	same(t, "contains", edges(g, graph.EdgeContains), []string{
		`google_compute_network.main > google_compute_subnetwork.web`,
		// The network called main in the firewall's own project, not the other.
		`google_compute_network.other > google_compute_firewall.theirs`,
		// The subnetwork of that name in the address's own region; the
		// address in another region names one that is not loaded.
		`google_compute_subnetwork.web > google_compute_address.here`,
	})
}

// A resource in another state that names a Google resource is linked to
// it whichever way the name is written.
func TestGoogleCrossState(t *testing.T) {
	g := google(t)
	loadString(t, g, "app", `{"version":4,"terraform_version":"1.14.0","resources":[
	  {"mode":"managed","type":"google_compute_instance","name":"worker","provider":"provider[\"registry.terraform.io/hashicorp/google\"]",
	   "instances":[{"attributes":{"id":"projects/acme-prod/zones/europe-west1-c/instances/worker","name":"worker","project":"acme-prod","zone":"europe-west1-c",
	     "network_interface":[{"name":"nic0","subnetwork":"projects/acme-prod/regions/europe-west1/subnetworks/web"}]}}]}]}`)
	res := Enrich(g)
	if res.CrossState != 1 {
		t.Errorf("cross-state links = %d, want 1", res.CrossState)
	}
	// The other state holds the subnetwork's self link; this one names it
	// without the address in front.
	if !g.HasEdge(graph.Edge{From: "app::google_compute_instance.worker", To: `net::google_compute_subnetwork.app["web"]`, Kind: graph.EdgeDependsOn}) {
		t.Error("the machine in the other state is not linked to the subnetwork it names")
	}
	if !g.HasEdge(graph.Edge{From: `net::google_compute_subnetwork.app["web"]`, To: "app::google_compute_instance.worker", Kind: graph.EdgeContains}) {
		t.Error("the machine in the other state is not placed in the subnetwork")
	}
}
