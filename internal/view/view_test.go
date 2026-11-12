package view

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

func node(source, account, region, module, resource, typ string) graph.Node {
	address := resource
	if module != "" {
		address = module + "." + resource
	}
	return graph.Node{
		ID: source + "::" + address, Source: source, Address: address,
		Account: account, Region: region, Module: module, Type: typ,
	}
}

func mustAdd(t testing.TB, g *graph.Graph, nodes ...graph.Node) {
	t.Helper()
	for _, n := range nodes {
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
}

func depend(t testing.TB, g *graph.Graph, from, to string) {
	t.Helper()
	if err := g.AddEdge(graph.Edge{From: from, To: to, Kind: graph.EdgeDependsOn}); err != nil {
		t.Fatal(err)
	}
}

// small is one state: a network at the root, a module with two instances
// that each hold two subnets, and a role with no region.
func small(t *testing.T) *graph.Graph {
	g := graph.New()
	const acct, region = "111111111111", "us-east-1"
	mustAdd(t, g,
		node("prod", acct, region, "", "aws_vpc.main", "aws_vpc"),
		node("prod", acct, "", "", "aws_iam_role.app", "aws_iam_role"),
		node("prod", "", "", "", "random_id.suffix", "random_id"),
	)
	for _, key := range []string{`["a"]`, `["b"]`} {
		for i := 0; i < 2; i++ {
			n := node("prod", acct, region, "module.net"+key, fmt.Sprintf("aws_subnet.private[%d]", i), "aws_subnet")
			mustAdd(t, g, n)
			depend(t, g, n.ID, "prod::aws_vpc.main")
		}
	}
	return g
}

func outline(v View) []string {
	var out []string
	for _, it := range v.Items {
		line := strings.Repeat("  ", it.Depth-1) + it.Label + " (" + string(it.Kind)
		if it.Kind != KindResource {
			line += fmt.Sprintf(" %d", it.Resources)
			if !it.Expanded {
				line += " collapsed"
			}
		}
		out = append(out, line+")")
	}
	return out
}

func TestTreeAndViews(t *testing.T) {
	tree := Build(small(t), Options{Dimensions: DefaultDimensions})
	if tree.Resources() != 7 || tree.Depth() != 7 {
		t.Fatalf("resources %d, depth %d; want 7 and 7", tree.Resources(), tree.Depth())
	}

	full := tree.AtDepth(tree.Depth())
	want := []string{
		`prod (source 7)`,
		`  111111111111 (account 6)`,
		`    us-east-1 (region 5)`,
		`      module.net (module 4)`,
		`        module.net["a"] (module_instance 2)`,
		`          aws_subnet.private (block 2)`,
		`            aws_subnet.private[0] (resource)`,
		`            aws_subnet.private[1] (resource)`,
		`        module.net["b"] (module_instance 2)`,
		`          aws_subnet.private (block 2)`,
		`            aws_subnet.private[0] (resource)`,
		`            aws_subnet.private[1] (resource)`,
		`      aws_vpc.main (resource)`,
		`    aws_iam_role.app (resource)`,
		`  random_id.suffix (resource)`,
	}
	if got := outline(full); !reflect.DeepEqual(got, want) {
		t.Errorf("full view:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(full.Links) != 4 {
		t.Errorf("full view has %d links, want one per subnet", len(full.Links))
	}

	// At depth 4 the module is one collapsed item, and its four dependencies
	// on the network become a single link.
	collapsed := tree.AtDepth(4)
	want = []string{
		`prod (source 7)`,
		`  111111111111 (account 6)`,
		`    us-east-1 (region 5)`,
		`      module.net (module 4 collapsed)`,
		`      aws_vpc.main (resource)`,
		`    aws_iam_role.app (resource)`,
		`  random_id.suffix (resource)`,
	}
	if got := outline(collapsed); !reflect.DeepEqual(got, want) {
		t.Errorf("depth 4:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	wantLinks := []Link{{
		From:  `source:prod/account:111111111111/region:us-east-1/module:module.net`,
		To:    `prod::aws_vpc.main`,
		Kind:  graph.EdgeDependsOn,
		Count: 4,
	}}
	if !reflect.DeepEqual(collapsed.Links, wantLinks) {
		t.Errorf("depth 4 links: %+v, want %+v", collapsed.Links, wantLinks)
	}

	// With everything in one item there is nothing to link.
	top := tree.AtDepth(1)
	if len(top.Items) != 1 || len(top.Links) != 0 {
		t.Errorf("depth 1: %d items, %d links", len(top.Items), len(top.Links))
	}

	for d := 1; d <= tree.Depth(); d++ {
		if got, want := tree.ItemsAtDepth(d), len(tree.AtDepth(d).Items); got != want {
			t.Errorf("ItemsAtDepth(%d) = %d, but the view has %d", d, got, want)
		}
	}
}

func TestViewExpandsOnlyWhatIsAsked(t *testing.T) {
	tree := Build(small(t), Options{Dimensions: DefaultDimensions})
	path := map[string]bool{
		`source:prod`:                      true,
		`source:prod/account:111111111111`: true,
		// Inside a collapsed region, so it must not show.
		`source:prod/account:111111111111/region:us-east-1/module:module.net`: true,
	}
	v := tree.View(func(grp *Group) bool { return path[grp.ID] })
	want := []string{
		`prod (source 7)`,
		`  111111111111 (account 6)`,
		`    us-east-1 (region 5 collapsed)`,
		`    aws_iam_role.app (resource)`,
		`  random_id.suffix (resource)`,
	}
	if got := outline(v); !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGroupingOrderAndLabels(t *testing.T) {
	g := small(t)
	mustAdd(t, g, node("staging", "222222222222", "us-east-1", "", "aws_vpc.main", "aws_vpc"))

	// Without a source level, the outermost module carries the source, so
	// two states' modules of the same name stay apart.
	byModule := Build(g, Options{Dimensions: []Dimension{ByModule}})
	var tops []string
	for _, grp := range byModule.Top() {
		tops = append(tops, grp.Name)
	}
	if want := []string{"prod::module.net"}; !reflect.DeepEqual(tops, want) {
		t.Errorf("top-level module groups: %q, want %q", tops, want)
	}
	if got := byModule.Label(`prod::module.net["a"].aws_subnet.private[0]`); got != "aws_subnet.private[0]" {
		t.Errorf("label inside a module = %q", got)
	}
	if got := byModule.Label("staging::aws_vpc.main"); got != "staging::aws_vpc.main" {
		t.Errorf("label outside any module = %q", got)
	}

	// Without modules in the grouping the label carries the module path,
	// and without sources it carries the source, so labels stay distinct.
	tree := Build(g, Options{Dimensions: []Dimension{ByRegion}})
	var labels []string
	for _, it := range tree.AtDepth(tree.Depth()).Items {
		if it.Kind == KindResource {
			labels = append(labels, it.Label)
		}
	}
	want := []string{
		`prod::module.net["a"].aws_subnet.private[0]`,
		`prod::module.net["a"].aws_subnet.private[1]`,
		`prod::module.net["b"].aws_subnet.private[0]`,
		`prod::module.net["b"].aws_subnet.private[1]`,
		`prod::aws_vpc.main`,
		`staging::aws_vpc.main`,
		`prod::aws_iam_role.app`,
		`prod::random_id.suffix`,
	}
	if !reflect.DeepEqual(labels, want) {
		t.Errorf("labels:\n%s\nwant:\n%s", strings.Join(labels, "\n"), strings.Join(want, "\n"))
	}
}

// related is a network with two subnets, a machine in the second, a route
// table attached to the first by a glue resource, and a bucket outside.
func related(t *testing.T) *graph.Graph {
	g := graph.New()
	n := func(resource, typ string) graph.Node {
		return node("prod", "111111111111", "us-east-1", "", resource, typ)
	}
	glue := n("aws_route_table_association.a", "aws_route_table_association")
	glue.Glue = true
	bucket := n("aws_s3_bucket.logs", "aws_s3_bucket")
	bucket.Region = "eu-west-1"
	mustAdd(t, g,
		n("aws_vpc.main", "aws_vpc"),
		n("aws_subnet.s[0]", "aws_subnet"),
		n("aws_subnet.s[1]", "aws_subnet"),
		n("aws_instance.app", "aws_instance"),
		n("aws_route_table.rt", "aws_route_table"),
		glue, bucket,
	)
	add := func(from, to string, kind graph.EdgeKind, via string) {
		e := graph.Edge{From: "prod::" + from, To: "prod::" + to, Kind: kind}
		if via != "" {
			e.Via = "prod::" + via
		}
		if err := g.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	add("aws_vpc.main", "aws_subnet.s[0]", graph.EdgeContains, "")
	add("aws_vpc.main", "aws_subnet.s[1]", graph.EdgeContains, "")
	add("aws_vpc.main", "aws_route_table.rt", graph.EdgeContains, "")
	add("aws_subnet.s[1]", "aws_instance.app", graph.EdgeContains, "")
	add("aws_subnet.s[0]", "aws_route_table_association.a", graph.EdgeContains, "")
	add("aws_instance.app", "aws_subnet.s[1]", graph.EdgeDependsOn, "")
	add("aws_route_table_association.a", "aws_subnet.s[0]", graph.EdgeDependsOn, "")
	add("aws_route_table_association.a", "aws_route_table.rt", graph.EdgeDependsOn, "")
	add("aws_route_table.rt", "aws_subnet.s[0]", graph.EdgeLinks, "aws_route_table_association.a")
	return g
}

func TestNetworkLevel(t *testing.T) {
	tree := Build(related(t), Options{Dimensions: []Dimension{BySource, ByNetwork}})
	want := []string{
		`prod (source 6)`,
		`  aws_vpc.main (network 5)`,
		`    aws_subnet.s[0] (subnet 1)`,
		`      aws_subnet.s[0] (resource)`,
		`    aws_subnet.s[1] (subnet 2)`,
		`      aws_instance.app (resource)`,
		`      aws_subnet.s[1] (resource)`,
		`    aws_route_table.rt (resource)`,
		`    aws_vpc.main (resource)`,
		`  aws_s3_bucket.logs (resource)`,
	}
	if got := outline(tree.AtDepth(tree.Depth())); !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGlueIsFoldedIntoLinks(t *testing.T) {
	g := related(t)
	dims := []Dimension{BySource}

	folded := Build(g, Options{Dimensions: dims})
	if folded.Resources() != 6 || folded.Total() != 7 || folded.GlueFolded() != 1 {
		t.Errorf("resources %d, total %d, glue %d; want 6, 7, 1", folded.Resources(), folded.Total(), folded.GlueFolded())
	}
	wantLinks := []Link{
		{From: "prod::aws_instance.app", To: "prod::aws_subnet.s[1]", Kind: graph.EdgeDependsOn, Count: 1},
		{From: "prod::aws_route_table.rt", To: "prod::aws_subnet.s[0]", Kind: graph.EdgeLinks, Count: 1},
	}
	if got := folded.AtDepth(folded.Depth()).Links; !reflect.DeepEqual(got, wantLinks) {
		t.Errorf("folded links: %+v\nwant %+v", got, wantLinks)
	}

	// Shown, the glue resource carries its own dependencies and the link
	// that stood for it is gone.
	shown := Build(g, Options{Dimensions: dims, ShowGlue: true})
	wantLinks = []Link{
		{From: "prod::aws_instance.app", To: "prod::aws_subnet.s[1]", Kind: graph.EdgeDependsOn, Count: 1},
		{From: "prod::aws_route_table_association.a", To: "prod::aws_route_table.rt", Kind: graph.EdgeDependsOn, Count: 1},
		{From: "prod::aws_route_table_association.a", To: "prod::aws_subnet.s[0]", Kind: graph.EdgeDependsOn, Count: 1},
	}
	if got := shown.AtDepth(shown.Depth()).Links; shown.Resources() != 7 || !reflect.DeepEqual(got, wantLinks) {
		t.Errorf("shown: %d resources, links %+v\nwant 7, %+v", shown.Resources(), got, wantLinks)
	}

	// A link between two resources inside one collapsed group disappears,
	// like any other.
	if got := folded.AtDepth(1).Links; len(got) != 0 {
		t.Errorf("collapsed view has links: %+v", got)
	}
}

func TestFilter(t *testing.T) {
	g := related(t)
	cases := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"type", Filter{Types: []string{"aws_subnet"}}, []string{"aws_subnet.s[0]", "aws_subnet.s[1]"}},
		{"type pattern", Filter{Types: []string{"aws_s*"}}, []string{"aws_s3_bucket.logs", "aws_subnet.s[0]", "aws_subnet.s[1]"}},
		{"several types", Filter{Types: []string{"aws_vpc", "aws_instance"}}, []string{"aws_instance.app", "aws_vpc.main"}},
		{"region", Filter{Regions: []string{"eu-west-1"}}, []string{"aws_s3_bucket.logs"}},
		{"account and region", Filter{Accounts: []string{"111111111111"}, Regions: []string{"nowhere"}}, nil},
		{"source", Filter{Sources: []string{"staging"}}, nil},
		{"status", Filter{Statuses: []string{"unmanaged"}}, nil},
		{"search ignores case", Filter{Search: "ROUTE"}, []string{"aws_route_table.rt"}},
		// The machine, the subnet it depends on and sits in; nothing further.
		{"around", Filter{Around: "prod::aws_instance.app"}, []string{"aws_instance.app", "aws_subnet.s[1]"}},
		// The table, its network, and the subnet its glue attaches it to.
		{"around follows links", Filter{Around: "prod::aws_route_table.rt"}, []string{"aws_route_table.rt", "aws_subnet.s[0]", "aws_vpc.main"}},
		// A folded glue resource is not shown, but what it joins is.
		{"around glue", Filter{Around: "prod::aws_route_table_association.a"}, []string{"aws_route_table.rt", "aws_subnet.s[0]"}},
		{"around and type", Filter{Around: "prod::aws_vpc.main", Types: []string{"aws_subnet"}}, []string{"aws_subnet.s[0]", "aws_subnet.s[1]"}},
	}
	for _, c := range cases {
		tree := Build(g, Options{Dimensions: []Dimension{BySource}, Filter: c.filter})
		var got []string
		for _, it := range tree.AtDepth(tree.Depth()).Items {
			if it.Kind == KindResource {
				got = append(got, it.Label)
			}
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if tree.Resources() != len(c.want) || tree.Total() != 7 {
			t.Errorf("%s: resources %d, total %d", c.name, tree.Resources(), tree.Total())
		}
	}
}

func TestParseDimensions(t *testing.T) {
	got, err := ParseDimensions("module, account")
	if err != nil || !reflect.DeepEqual(got, []Dimension{ByModule, ByAccount}) {
		t.Errorf("got %v, %v", got, err)
	}
	got, err = ParseDimensions("account,resource_group,network")
	if err != nil || !reflect.DeepEqual(got, []Dimension{ByAccount, ByResourceGroup, ByNetwork}) {
		t.Errorf("got %v, %v", got, err)
	}
	for _, bad := range []string{"", "zone", "account,account", "account,"} {
		if _, err := ParseDimensions(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNaturalCompare(t *testing.T) {
	sorted := []string{"a", "a[2]", "a[09]", "a[10]", "a[10]b", "b[1]", "b[1].c[3]", "b[1].c[20]"}
	for i := range sorted {
		for j := range sorted {
			got := naturalCompare(sorted[i], sorted[j])
			if (got < 0) != (i < j) || (got == 0) != (i == j) {
				t.Errorf("naturalCompare(%q, %q) = %d", sorted[i], sorted[j], got)
			}
		}
	}
}

func TestCrowdedGroupIsSplitByType(t *testing.T) {
	g := graph.New()
	for i := 0; i < 50; i++ {
		for _, typ := range []string{"aws_instance", "aws_s3_bucket", "aws_sqs_queue"} {
			mustAdd(t, g, node("prod", "", "", "", fmt.Sprintf("%s.r%d", typ, i), typ))
		}
	}
	tree := Build(g, Options{Dimensions: []Dimension{BySource}})
	prod := tree.Top()[0]
	var kinds []string
	for _, c := range prod.Groups {
		kinds = append(kinds, fmt.Sprintf("%s %s %d", c.Kind, c.Name, c.Resources))
	}
	want := []string{"type aws_instance 50", "type aws_s3_bucket 50", "type aws_sqs_queue 50"}
	if !reflect.DeepEqual(kinds, want) || len(prod.Nodes) != 0 {
		t.Errorf("got %v with %d loose resources, want %v", kinds, len(prod.Nodes), want)
	}
}

func TestCrowdedGroupIsSplitByRange(t *testing.T) {
	g := graph.New()
	for i := 0; i < 250; i++ {
		mustAdd(t, g, node("prod", "", "", "", fmt.Sprintf("aws_instance.w[%d]", i), "aws_instance"))
	}
	tree := Build(g, Options{Dimensions: []Dimension{BySource}})
	block := tree.Top()[0].Groups[0]
	var names []string
	for _, c := range block.Groups {
		names = append(names, fmt.Sprintf("%s %d", c.Name, c.Resources))
	}
	want := []string{
		"aws_instance.w[0] … aws_instance.w[99] 100",
		"aws_instance.w[100] … aws_instance.w[199] 100",
		"aws_instance.w[200] … aws_instance.w[249] 50",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}
}

// Identical labels must not make ranges collapse into one another.
func TestRangesWithIdenticalLabels(t *testing.T) {
	g := graph.New()
	for i := 0; i < 250; i++ {
		n := node("prod", "", "", "", "aws_vpc.main", "aws_vpc")
		n.ID = fmt.Sprintf("prod-%d::aws_vpc.main", i)
		mustAdd(t, g, n)
	}
	tree := Build(g, Options{Dimensions: []Dimension{BySource}})
	if got := len(tree.Top()[0].Groups); got != 3 {
		t.Errorf("got %d ranges, want 3", got)
	}
	checkBounded(t, tree)
}

func checkBounded(t *testing.T, tree *Tree) {
	t.Helper()
	var walk func(grp *Group)
	walk = func(grp *Group) {
		if n := len(grp.Groups) + len(grp.Nodes); n > MaxChildren {
			t.Errorf("group %q has %d children, over the limit of %d", grp.ID, n, MaxChildren)
		}
		for _, c := range grp.Groups {
			walk(c)
		}
	}
	for _, top := range tree.Top() {
		walk(top)
	}
	if len(tree.Top()) > MaxChildren {
		t.Errorf("%d top-level groups, over the limit of %d", len(tree.Top()), MaxChildren)
	}
}

// estate builds modules×100 resources: each module instance holds a network,
// 9 subnets that depend on it, and 90 machines that each depend on one
// subnet. The modules are spread over 20 accounts and 2 regions.
func estate(t testing.TB, modules int) *graph.Graph {
	g := graph.New()
	for m := 0; m < modules; m++ {
		account := fmt.Sprintf("%012d", 100000000000+m%20)
		region := fmt.Sprintf("us-east-%d", m%2+1)
		module := fmt.Sprintf("module.app[%d]", m)
		vpc := node("live", account, region, module, "aws_vpc.main", "aws_vpc")
		mustAdd(t, g, vpc)
		var subnets []string
		for i := 0; i < 9; i++ {
			n := node("live", account, region, module, fmt.Sprintf("aws_subnet.private[%d]", i), "aws_subnet")
			mustAdd(t, g, n)
			depend(t, g, n.ID, vpc.ID)
			subnets = append(subnets, n.ID)
		}
		for i := 0; i < 90; i++ {
			n := node("live", account, region, module, fmt.Sprintf("aws_instance.worker[%d]", i), "aws_instance")
			mustAdd(t, g, n)
			depend(t, g, n.ID, subnets[i%9])
		}
	}
	return g
}

// The budget: on an estate of 100,000 resources no group is crowded, the
// default view is readable, and so is every step of drilling down to a
// single resource.
func TestBudgetOnLargeEstate(t *testing.T) {
	g := estate(t, 1000)
	for _, dims := range [][]Dimension{DefaultDimensions, {ByModule}, {ByAccount}, {BySource}} {
		tree := Build(g, Options{Dimensions: dims})
		if tree.Resources() != 100000 {
			t.Fatalf("%v: %d resources", dims, tree.Resources())
		}
		checkBounded(t, tree)

		depth := tree.AutoDepth(Budget)
		if n := len(tree.AtDepth(depth).Items); n > Budget {
			t.Errorf("%v: default view at depth %d has %d items, over the budget of %d", dims, depth, n, Budget)
		}

		// Open one group per level, always the first, down to a resource.
		open := map[string]bool{}
		for grp := tree.Top()[0]; ; grp = grp.Groups[0] {
			open[grp.ID] = true
			v := tree.View(func(g *Group) bool { return open[g.ID] })
			if len(v.Items) > Budget {
				t.Errorf("%v: drilling into %q shows %d items, over the budget of %d", dims, grp.ID, len(v.Items), Budget)
			}
			if len(grp.Groups) == 0 {
				break
			}
		}
	}
}

func BenchmarkBuildAndView100k(b *testing.B) {
	g := estate(b, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree := Build(g, Options{Dimensions: DefaultDimensions})
		tree.AtDepth(tree.AutoDepth(Budget))
	}
}

// Resources with a resource group sit in a group named after it; the rest
// stay where the levels above put them.
func TestResourceGroupLevel(t *testing.T) {
	g := graph.New()
	for _, n := range []graph.Node{
		{ID: "s::a", Source: "s", Address: "azurerm_storage_account.a", Type: "azurerm_storage_account", Account: "sub-1", ResourceGroup: "rg-app"},
		{ID: "s::b", Source: "s", Address: "azurerm_storage_account.b", Type: "azurerm_storage_account", Account: "sub-1", ResourceGroup: "rg-app"},
		{ID: "s::c", Source: "s", Address: "azurerm_storage_account.c", Type: "azurerm_storage_account", Account: "sub-1", ResourceGroup: "rg-data"},
		{ID: "s::d", Source: "s", Address: "aws_s3_bucket.d", Type: "aws_s3_bucket", Account: "123456789012"},
	} {
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	tree := Build(g, Options{Dimensions: []Dimension{ByAccount, ByResourceGroup}})
	d := tree.Document(8)
	var lines []string
	for _, it := range d.Items {
		lines = append(lines, strings.Repeat("  ", it.Depth-1)+string(it.Kind)+" "+it.Label)
	}
	want := []string{
		"account 123456789012",
		"  resource s::aws_s3_bucket.d",
		"account sub-1",
		"  resource_group rg-app",
		"    resource s::azurerm_storage_account.a",
		"    resource s::azurerm_storage_account.b",
		"  resource_group rg-data",
		"    resource s::azurerm_storage_account.c",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
