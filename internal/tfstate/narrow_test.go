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
