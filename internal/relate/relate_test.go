package relate

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

// network loads a small AWS-shaped state: a network with two subnets, a
// route table associated with both, a machine in one subnet, a load
// balancer across both, and a role with a policy attached.
func network(t *testing.T) *graph.Graph {
	t.Helper()
	f, err := os.Open("../tfstate/testdata/network.tfstate")
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

func loadString(t *testing.T, g *graph.Graph, source, state string) {
	t.Helper()
	if err := tfstate.Load(g, source, strings.NewReader(state)); err != nil {
		t.Fatal(err)
	}
}

// edges lists the edges of one kind as "from > to", without the source.
func edges(g *graph.Graph, kind graph.EdgeKind) []string {
	var out []string
	for _, e := range g.Edges() {
		if e.Kind == kind {
			line := e.From + " > " + e.To
			if e.Via != "" {
				line += " via " + e.Via
			}
			out = append(out, strings.ReplaceAll(line, "net::", ""))
		}
	}
	return out
}

func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n  got  %s\n  want %s", what, strings.Join(got, "\n       "), strings.Join(want, "\n       "))
	}
}

func TestContainment(t *testing.T) {
	g := network(t)
	if added := Containment(g); added != 7 {
		t.Errorf("added %d edges, want 7", added)
	}
	same(t, "contains", edges(g, graph.EdgeContains), []string{
		// The machine names one subnet; the associations name one each.
		"aws_subnet.private[0] > aws_route_table_association.private[0]",
		"aws_subnet.private[1] > aws_instance.app",
		"aws_subnet.private[1] > aws_route_table_association.private[1]",
		// The load balancer spans both subnets, so it sits in their network.
		"aws_vpc.main > aws_lb.web",
		"aws_vpc.main > aws_route_table.private",
		"aws_vpc.main > aws_subnet.private[0]",
		"aws_vpc.main > aws_subnet.private[1]",
	})
}

func TestContainmentAcrossStatesAndDataSources(t *testing.T) {
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	g := graph.New()
	loadString(t, g, "network", `{"version":4,"resources":[
	  {"mode":"managed","type":"aws_vpc","name":"main",`+aws+`,"instances":[{"attributes":{"id":"vpc-1"}}]},
	  {"mode":"managed","type":"aws_vpc","name":"other",`+aws+`,"instances":[{"attributes":{"id":"vpc-2"}}]},
	  {"mode":"managed","type":"aws_subnet","name":"a",`+aws+`,"instances":[{"attributes":{"id":"subnet-a","vpc_id":"vpc-1"}}]},
	  {"mode":"managed","type":"aws_subnet","name":"b",`+aws+`,"instances":[{"attributes":{"id":"subnet-b","vpc_id":"vpc-2"}}]}]}`)
	loadString(t, g, "app", `{"version":4,"resources":[
	  {"mode":"data","type":"aws_vpc","name":"main",`+aws+`,"instances":[{"attributes":{"id":"vpc-1"}}]},
	  {"mode":"managed","type":"aws_security_group","name":"app",`+aws+`,"instances":[{"attributes":{"id":"sg-1","vpc_id":"vpc-1"}}]},
	  {"mode":"managed","type":"aws_lambda_function","name":"fn",`+aws+`,
	   "instances":[{"attributes":{"vpc_config":[{"subnet_ids":["subnet-a"],"vpc_id":"vpc-1"}]}}]},
	  {"mode":"managed","type":"aws_lb","name":"split",`+aws+`,"instances":[{"attributes":{"subnets":["subnet-a","subnet-b"]}}]},
	  {"mode":"managed","type":"aws_instance","name":"elsewhere",`+aws+`,"instances":[{"attributes":{"subnet_id":"subnet-zzz"}}]}]}`)
	// In a state of its own: a value marked sensitive is redacted wherever
	// that state repeats it, which would hide the references above too.
	loadString(t, g, "db", `{"version":4,"resources":[
	  {"mode":"managed","type":"aws_db_instance","name":"secret",`+aws+`,
	   "instances":[{"attributes":{"vpc_id":"vpc-1"},"sensitive_attributes":[[{"type":"get_attr","value":"vpc_id"}]]}]}]}`)
	Containment(g)
	same(t, "contains", edges(g, graph.EdgeContains), []string{
		// The managed network wins over the data source that reads it, and
		// the match crosses from one state into the other.
		"network::aws_subnet.a > app::aws_lambda_function.fn",
		"network::aws_vpc.main > app::aws_security_group.app",
		"network::aws_vpc.main > network::aws_subnet.a",
		"network::aws_vpc.other > network::aws_subnet.b",
		// Nothing for the load balancer across two networks, the machine in
		// a subnet no state holds, or the redacted reference.
	})
}

func TestContainmentThroughSubnetGroups(t *testing.T) {
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	arn := func(region, rest string) string {
		return `"arn":"arn:aws:rds:` + region + `:111111111111:` + rest + `"`
	}
	g := graph.New()
	loadString(t, g, "db", `{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"aws_vpc","name":"main",`+aws+`,"instances":[{"attributes":{"id":"vpc-1"}}]},
	  {"mode":"managed","type":"aws_subnet","name":"a",`+aws+`,"instances":[{"attributes":{"id":"subnet-a","vpc_id":"vpc-1"}}]},
	  {"mode":"managed","type":"aws_subnet","name":"b",`+aws+`,"instances":[{"attributes":{"id":"subnet-b","vpc_id":"vpc-1"}}]},
	  {"mode":"managed","type":"aws_db_subnet_group","name":"east",`+aws+`,
	   "instances":[{"attributes":{"id":"data","name":"data","subnet_ids":["subnet-a","subnet-b"],`+arn("us-east-1", "subgrp:data")+`}}]},
	  {"mode":"managed","type":"aws_db_subnet_group","name":"west",`+aws+`,
	   "instances":[{"attributes":{"id":"data","name":"data","subnet_ids":["subnet-zzz"],`+arn("us-west-2", "subgrp:data")+`}}]},
	  {"mode":"managed","type":"aws_elasticache_subnet_group","name":"one",`+aws+`,
	   "instances":[{"attributes":{"id":"cache","name":"cache","subnet_ids":["subnet-b"]}}]},
	  {"mode":"managed","type":"aws_db_instance","name":"east",`+aws+`,
	   "instances":[{"attributes":{"db_subnet_group_name":"data",`+arn("us-east-1", "db:east")+`}}]},
	  {"mode":"managed","type":"aws_db_instance","name":"west",`+aws+`,
	   "instances":[{"attributes":{"db_subnet_group_name":"data",`+arn("us-west-2", "db:west")+`}}]},
	  {"mode":"managed","type":"aws_elasticache_cluster","name":"c",`+aws+`,
	   "instances":[{"attributes":{"subnet_group_name":"cache"}}]},
	  {"mode":"managed","type":"aws_db_instance","name":"default",`+aws+`,
	   "instances":[{"attributes":{"db_subnet_group_name":"default"}}]}]}`)
	Containment(g)
	same(t, "contains", strip(edges(g, graph.EdgeContains), "db::"), []string{
		// A group with one subnet puts its members in that subnet.
		"aws_subnet.b > aws_elasticache_cluster.c",
		"aws_subnet.b > aws_elasticache_subnet_group.one",
		// Two groups are named "data"; the database takes the one in its
		// own region, whose subnets share a network.
		"aws_vpc.main > aws_db_instance.east",
		"aws_vpc.main > aws_db_subnet_group.east",
		"aws_vpc.main > aws_subnet.a",
		"aws_vpc.main > aws_subnet.b",
		// Nothing for the western database, whose group's subnet is in no
		// state, or for the one in a group no state holds.
	})
}

func strip(lines []string, prefix string) []string {
	for i := range lines {
		lines[i] = strings.ReplaceAll(lines[i], prefix, "")
	}
	return lines
}

func TestSecurityGroupRulesAreGlue(t *testing.T) {
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	g := graph.New()
	loadString(t, g, "sg", `{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"aws_security_group","name":"web",`+aws+`,"instances":[{"attributes":{"id":"sg-web"}}]},
	  {"mode":"managed","type":"aws_security_group","name":"db",`+aws+`,"instances":[{"attributes":{"id":"sg-db"}}]},
	  {"mode":"managed","type":"aws_security_group_rule","name":"web_to_db",`+aws+`,
	   "instances":[{"attributes":{"security_group_id":"sg-db","source_security_group_id":"sg-web"},
	     "dependencies":["aws_security_group.db","aws_security_group.web"]}]},
	  {"mode":"managed","type":"aws_vpc_security_group_ingress_rule","name":"https",`+aws+`,
	   "instances":[{"attributes":{"security_group_id":"sg-web","cidr_ipv4":"0.0.0.0/0"},
	     "dependencies":["aws_security_group.web"]}]}]}`)
	if marked := MarkGlue(g); marked != 1 {
		t.Errorf("marked %d resources, want 1", marked)
	}
	// The rule between two groups joins them; the rule that only opens one
	// group to the internet joins nothing and stays a resource.
	same(t, "links", strip(edges(g, graph.EdgeLinks), "sg::"), []string{
		"aws_security_group.db > aws_security_group.web via aws_security_group_rule.web_to_db",
	})
}

func TestMarkGlue(t *testing.T) {
	g := network(t)
	if marked := MarkGlue(g); marked != 3 {
		t.Errorf("marked %d resources, want 3", marked)
	}
	same(t, "links", edges(g, graph.EdgeLinks), []string{
		"aws_iam_policy.read > aws_iam_role.app via aws_iam_role_policy_attachment.app",
		"aws_route_table.private > aws_subnet.private[0] via aws_route_table_association.private[0]",
		"aws_route_table.private > aws_subnet.private[1] via aws_route_table_association.private[1]",
	})
	var glue []string
	for _, n := range g.Nodes() {
		if n.Glue {
			glue = append(glue, n.Address)
		}
	}
	same(t, "glue", glue, []string{
		"aws_iam_role_policy_attachment.app",
		"aws_route_table_association.private[0]",
		"aws_route_table_association.private[1]",
	})
}

func TestMarkGlueLeavesUnclearCasesAlone(t *testing.T) {
	g := graph.New()
	loadString(t, g, "net", `{"version":4,"resources":[
	  {"mode":"managed","type":"t_thing","name":"a","provider":"p","instances":[{}]},
	  {"mode":"managed","type":"t_thing","name":"b","provider":"p","instances":[{}]},
	  {"mode":"managed","type":"t_thing","name":"c","provider":"p","instances":[{}]},
	  {"mode":"managed","type":"t_thing_attachment","name":"one","provider":"p",
	   "instances":[{"dependencies":["t_thing.a"]}]},
	  {"mode":"managed","type":"t_thing_attachment","name":"three","provider":"p",
	   "instances":[{"dependencies":["t_thing.a","t_thing.b","t_thing.c"]}]},
	  {"mode":"managed","type":"t_thing_attachment","name":"needed","provider":"p",
	   "instances":[{"dependencies":["t_thing.a","t_thing.b"]}]},
	  {"mode":"managed","type":"t_thing","name":"after","provider":"p",
	   "instances":[{"dependencies":["t_thing_attachment.needed"]}]},
	  {"mode":"managed","type":"t_thing_joiner","name":"other","provider":"p",
	   "instances":[{"dependencies":["t_thing.a","t_thing.b"]}]},
	  {"mode":"data","type":"t_thing_attachment","name":"read","provider":"p",
	   "instances":[{"dependencies":["t_thing.a","t_thing.b"]}]},
	  {"mode":"managed","type":"t_thing","name":"pool","provider":"p","instances":[{"index_key":0},{"index_key":1}]},
	  {"mode":"managed","type":"t_thing_attachment","name":"same_block","provider":"p",
	   "instances":[{"dependencies":["t_thing.pool"]}]}]}`)
	if marked := MarkGlue(g); marked != 0 {
		t.Errorf("marked %d resources, want none: %v", marked, edges(g, graph.EdgeLinks))
	}
}

func TestEnrich(t *testing.T) {
	got := Enrich(network(t))
	if want := (Result{Contained: 7, Glue: 3}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// The fixtures are real OpenTofu states: one that makes a network and two
// subnets and publishes their IDs as outputs, and one that reads those
// outputs through terraform_remote_state and uses the second subnet.
func TestCrossStateThroughRemoteState(t *testing.T) {
	g := graph.New()
	for source, file := range map[string]string{"network": "remote-network.tfstate", "app": "remote-app.tfstate"} {
		f, err := os.Open("../tfstate/testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		err = tfstate.Load(g, source, f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if added := CrossState(g); added != 4 {
		t.Errorf("added %d edges, want 4", added)
	}
	var crossing []string
	for _, e := range g.Edges() {
		if strings.HasPrefix(e.From, "app::") && strings.HasPrefix(e.To, "network::") {
			crossing = append(crossing, e.From+" > "+e.To)
		}
	}
	same(t, "edges from app to network", crossing, []string{
		// The data source read three IDs from the other state's outputs.
		"app::data.terraform_remote_state.network > network::terraform_data.subnet[0]",
		"app::data.terraform_remote_state.network > network::terraform_data.subnet[1]",
		"app::data.terraform_remote_state.network > network::terraform_data.vpc",
		// The server holds one of them under an argument named subnet_id.
		"app::terraform_data.server > network::terraform_data.subnet[1]",
	})
	// Doing it again adds nothing.
	if again := CrossState(g); again != 0 {
		t.Errorf("a second pass added %d edges", again)
	}
}

func TestCrossStateRules(t *testing.T) {
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	g := graph.New()
	loadString(t, g, "net", `{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"aws_vpc","name":"main",`+aws+`,
	   "instances":[{"attributes":{"id":"vpc-0123456789","arn":"arn:aws:ec2:us-east-1:111111111111:vpc/vpc-0123456789"}}]},
	  {"mode":"managed","type":"aws_s3_bucket","name":"logs",`+aws+`,
	   "instances":[{"attributes":{"id":"default","bucket":"default","arn":"arn:aws:s3:::default"}}]},
	  {"mode":"managed","type":"aws_kms_key","name":"a",`+aws+`,"instances":[{"attributes":{"id":"key-shared-1"}}]},
	  {"mode":"managed","type":"aws_kms_key","name":"b",`+aws+`,"instances":[{"attributes":{"id":"key-shared-1"}}]},
	  {"mode":"managed","type":"aws_subnet","name":"inside",`+aws+`,
	   "instances":[{"attributes":{"id":"subnet-0000000001","vpc_id":"vpc-0123456789"},"dependencies":["aws_vpc.main"]}]}]}`)
	loadString(t, g, "app", `{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"data","type":"aws_vpc","name":"read",`+aws+`,"instances":[{"attributes":{"id":"vpc-0123456789"}}]},
	  {"mode":"managed","type":"aws_security_group","name":"by_key",`+aws+`,
	   "instances":[{"attributes":{"id":"sg-0000000001","vpc_id":"vpc-0123456789"}}]},
	  {"mode":"managed","type":"aws_iam_policy","name":"by_arn",`+aws+`,
	   "instances":[{"attributes":{"id":"p-0000000001","policy":"arn:aws:ec2:us-east-1:111111111111:vpc/vpc-0123456789"}}]},
	  {"mode":"managed","type":"aws_instance","name":"mentions",`+aws+`,
	   "instances":[{"attributes":{"id":"i-0000000001","tags":{"note":"vpc-0123456789"},"user_data":"default","key_id":"key-shared-1"}}]},
	  {"mode":"managed","type":"aws_lb","name":"listed",`+aws+`,
	   "instances":[{"attributes":{"id":"lb-0000000001","subnet_ids":["subnet-0000000001"]}}]}]}`)
	// In a state of its own, since a value marked sensitive is redacted
	// wherever that state repeats it.
	loadString(t, g, "db", `{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"aws_db_instance","name":"secret",`+aws+`,
	   "instances":[{"attributes":{"id":"db-0000000001","vpc_id":"vpc-0123456789"},"sensitive_attributes":[[{"type":"get_attr","value":"vpc_id"}]]}]}]}`)
	CrossState(g)
	var crossing []string
	for _, e := range g.Edges() {
		if !strings.HasPrefix(e.From, "net::") {
			crossing = append(crossing, e.From+" > "+e.To)
		}
	}
	same(t, "edges out of app", crossing, []string{
		// An ARN names its resource wherever it is written.
		"app::aws_iam_policy.by_arn > net::aws_vpc.main",
		// An element of a list goes by the list's name.
		"app::aws_lb.listed > net::aws_subnet.inside",
		// A plain ID counts under an argument named like a reference.
		"app::aws_security_group.by_key > net::aws_vpc.main",
		// A data source reads the resource whose ID it has; the managed
		// resource, not the data source, owns that ID.
		"app::data.aws_vpc.read > net::aws_vpc.main",
		// Nothing for the instance: a tag is not a reference, "default" is
		// too plain to stand for a bucket, and two keys share the third
		// value. Nothing for the database, whose reference is redacted.
	})
}
