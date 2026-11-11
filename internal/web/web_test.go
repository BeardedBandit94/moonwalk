package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/relate"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
	"github.com/BeardedBandit94/moonwalk/internal/view"
)

// serve starts a server over the AWS-shaped network fixture and a state
// that holds secrets, prepared as the serve command prepares them.
func serve(t *testing.T) *httptest.Server {
	t.Helper()
	return serveStates(t, map[string]string{"net": "network.tfstate", "lab": "secrets.tfstate"})
}

func serveStates(t *testing.T, states map[string]string) *httptest.Server {
	t.Helper()
	g := graph.New()
	for source, file := range states {
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
	relate.Enrich(g)
	g.ReduceTransitive(graph.EdgeDependsOn)
	s := New(g, nil)
	s.LocalOnly = true
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	res, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(body)
}

func getJSON(t *testing.T, ts *httptest.Server, path string, into any) {
	t.Helper()
	status, body := get(t, ts, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, status, body)
	}
	if err := json.Unmarshal([]byte(body), into); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

func labels(d view.Document, kind view.Kind) []string {
	var out []string
	for _, it := range d.Items {
		if it.Kind == kind {
			out = append(out, it.Label)
		}
	}
	return out
}

func TestMeta(t *testing.T) {
	var m metaResponse
	getJSON(t, serve(t), "/api/meta", &m)
	if m.Resources != 19 || strings.Join(m.Sources, ",") != "lab,net" || m.Budget != view.Budget || len(m.Dimensions) != 5 {
		t.Errorf("got %+v", m)
	}
}

func TestViewDefaultsToWhatFits(t *testing.T) {
	var d view.Document
	getJSON(t, serve(t), "/api/view", &d)
	// Small enough to show in full: 16 resources once the 3 glue ones fold.
	if d.Resources != 16 || d.Total != 19 || d.GlueFolded != 3 || d.Depth != d.MaxDepth {
		t.Errorf("resources %d, total %d, glue %d, depth %d of %d", d.Resources, d.Total, d.GlueFolded, d.Depth, d.MaxDepth)
	}
	if got := len(labels(d, view.KindResource)); got != 16 {
		t.Errorf("%d resources drawn, want 16", got)
	}
}

func TestViewDepthAndOpen(t *testing.T) {
	ts := serve(t)
	var top view.Document
	getJSON(t, ts, "/api/view?depth=1", &top)
	if len(top.Items) != 2 || top.Items[0].Expanded {
		t.Fatalf("depth 1: %+v", top.Items)
	}

	// Naming one deep group opens it and everything above it, and nothing
	// beside it.
	region := "source:net/account:123456789012/region:global"
	var d view.Document
	getJSON(t, ts, "/api/view?open="+url.QueryEscape(region)+"&open=no-such-group", &d)
	var open, closed []string
	for _, it := range d.Items {
		if it.Kind == view.KindResource {
			continue
		}
		if it.Expanded {
			open = append(open, it.Label)
		} else {
			closed = append(closed, it.Label)
		}
	}
	if strings.Join(open, ",") != "net,123456789012,global" || strings.Join(closed, ",") != "lab,us-east-1" {
		t.Errorf("open %q, closed %q", open, closed)
	}
	if got := strings.Join(labels(d, view.KindResource), ","); got != "aws_iam_policy.read,aws_iam_role.app" {
		t.Errorf("resources drawn: %s", got)
	}
	// The glue between the role and the policy shows as a link.
	if len(d.Links) != 1 || d.Links[0].Kind != graph.EdgeLinks {
		t.Errorf("links: %+v", d.Links)
	}
}

func TestViewGroupingAndFilters(t *testing.T) {
	ts := serve(t)
	var d view.Document
	getJSON(t, ts, "/api/view?group_by=source,network&source=net", &d)
	if got := strings.Join(labels(d, view.KindNetwork), ","); got != "aws_vpc.main" {
		t.Errorf("networks: %q", got)
	}
	if got := strings.Join(labels(d, view.KindSubnet), ","); got != "aws_subnet.private[0],aws_subnet.private[1]" {
		t.Errorf("subnets: %q", got)
	}

	getJSON(t, ts, "/api/view?type=aws_iam_*&glue=1", &d)
	if got := strings.Join(labels(d, view.KindResource), ","); got != "aws_iam_policy.read,aws_iam_role.app,aws_iam_role_policy_attachment.app" {
		t.Errorf("type filter with glue shown: %q", got)
	}

	getJSON(t, ts, "/api/view?search=LB.web", &d)
	if got := strings.Join(labels(d, view.KindResource), ","); got != "aws_lb.web" {
		t.Errorf("search: %q", got)
	}

	getJSON(t, ts, "/api/view?group_by=source&around="+url.QueryEscape("net::aws_instance.app"), &d)
	if got := strings.Join(labels(d, view.KindResource), ","); got != "aws_subnet.private[1],aws_instance.app" {
		t.Errorf("around: %q", got)
	}
}

func TestViewRejectsBadRequests(t *testing.T) {
	ts := serve(t)
	for _, path := range []string{
		"/api/view?group_by=zone",
		"/api/view?depth=0",
		"/api/view?depth=deep",
		"/api/view?around=net::aws_vpc.nope",
	} {
		if status, _ := get(t, ts, path); status != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", path, status)
		}
	}
}

func TestResource(t *testing.T) {
	ts := serve(t)
	names := func(r related) string {
		var out []string
		for _, it := range r.Items {
			name := it.Address
			if it.Via != nil {
				name += " by " + it.Via.Address
			}
			out = append(out, name)
		}
		return strings.Join(out, ",")
	}

	var r resourceResponse
	getJSON(t, ts, "/api/resource?id="+url.QueryEscape("net::aws_subnet.private[1]"), &r)
	if !r.Shown || r.Group != "source:net/account:123456789012/region:us-east-1/block:aws_subnet.private" {
		t.Errorf("shown %v in %q", r.Shown, r.Group)
	}
	if r.Container == nil || r.Container.Address != "aws_vpc.main" {
		t.Errorf("container: %+v", r.Container)
	}
	if got := names(r.DependsOn); got != "aws_vpc.main" {
		t.Errorf("depends on: %q", got)
	}
	// The association also depends on the subnet, but it is glue: it shows
	// up as what attaches the subnet to the route table.
	if got := names(r.DependedOnBy); got != "aws_instance.app,aws_lb.web,aws_route_table_association.private[1]" {
		t.Errorf("depended on by: %q", got)
	}
	if got := names(r.LinkedTo); got != "aws_route_table.private by aws_route_table_association.private[1]" {
		t.Errorf("linked to: %q", got)
	}
	if got := names(r.Contains); got != "aws_instance.app,aws_route_table_association.private[1]" || r.Contains.Total != 2 {
		t.Errorf("contains: %q (%d)", got, r.Contains.Total)
	}

	// A folded glue resource is not drawn, and says what it joins.
	getJSON(t, ts, "/api/resource?id="+url.QueryEscape("net::aws_iam_role_policy_attachment.app"), &r)
	if r.Shown || !r.Node.Glue || names(r.Joins) != "aws_iam_policy.read,aws_iam_role.app" {
		t.Errorf("glue: shown %v, glue %v, joins %q", r.Shown, r.Node.Glue, names(r.Joins))
	}
	getJSON(t, ts, "/api/resource?glue=1&id="+url.QueryEscape("net::aws_iam_role_policy_attachment.app"), &r)
	if !r.Shown {
		t.Error("glue resource not shown with glue=1")
	}

	if status, _ := get(t, ts, "/api/resource?id=net::nope"); status != http.StatusNotFound {
		t.Errorf("unknown resource: status %d", status)
	}
}

// The explorer shows attributes, so it must show them redacted.
func TestResourceAttributesAreRedacted(t *testing.T) {
	ts := serve(t)
	for _, id := range []string{"lab::random_password.db", "lab::terraform_data.cfg"} {
		_, body := get(t, ts, "/api/resource?id="+url.QueryEscape(id))
		for _, secret := range []string{"hunter2", "4@Sze931N+:r", "$2a$10$INNvf"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s: response contains the secret %q", id, secret)
			}
		}
		if !strings.Contains(body, "(sensitive)") {
			t.Errorf("%s: no redaction marker in the response", id)
		}
	}
}

// A state from a version that does not mark every secret cannot be
// redacted, so the explorer must not be sent its attributes at all.
func TestResourceAttributesOfUnmarkedStatesAreWithheld(t *testing.T) {
	ts := serveStates(t, map[string]string{"old": "unmarked.tfstate"})
	status, body := get(t, ts, "/api/resource?id="+url.QueryEscape("old::random_password.db"))
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	for _, secret := range []string{"8%exVpqbOs%l", "$2a$10$Yh49MOe"} {
		if strings.Contains(body, secret) {
			t.Errorf("response contains the secret %q", secret)
		}
	}
	if strings.Contains(body, `"attributes":`) || !strings.Contains(body, `"attributes_withheld":true`) {
		t.Errorf("attributes should be withheld and marked as such: %s", body)
	}
}

func TestResourceCode(t *testing.T) {
	g := graph.New()
	loc := &graph.CodeLocation{File: "main.tf", Line: 3, EndLine: 5}
	for _, n := range []graph.Node{
		{ID: "s::t.coded", Source: "s", Address: "t.coded", ConfigAddress: "t.coded", Status: graph.StatusDeclared, Code: loc},
		{ID: "s::t.plain", Source: "s", Address: "t.plain", ConfigAddress: "t.plain", Status: graph.StatusManaged},
	} {
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	lookup := func(source, address string) (CodeInfo, bool) {
		return CodeInfo{
			Text: "resource \"t\" \"coded\" {}  # " + source + " " + address,
			References: []CodeReference{
				{Attribute: "x", Written: "local.p", To: "t.plain"},
				{Attribute: "y", Written: "t.gone.id", To: "t.gone"}, // No such resource: left out.
			},
		}, true
	}
	ts := httptest.NewServer(New(g, lookup).Handler())
	t.Cleanup(ts.Close)

	var r resourceResponse
	getJSON(t, ts, "/api/resource?id=s::t.coded", &r)
	if r.Node.Code == nil || *r.Node.Code != *loc || r.CodeText != `resource "t" "coded" {}  # s t.coded` {
		t.Errorf("code %+v, text %q", r.Node.Code, r.CodeText)
	}
	if len(r.RefersTo) != 1 || r.RefersTo[0] != (reference{Attribute: "x", Written: "local.p", Address: "t.plain", ID: "s::t.plain", Instances: 1}) {
		t.Errorf("refers to: %+v", r.RefersTo)
	}
	// A resource with no known block is not looked up at all.
	var plain resourceResponse
	getJSON(t, ts, "/api/resource?id=s::t.plain", &plain)
	if plain.Node.Code != nil || plain.CodeText != "" {
		t.Errorf("resource without code: %+v, %q", plain.Node.Code, plain.CodeText)
	}

	var d view.Document
	getJSON(t, ts, "/api/view", &d)
	statuses := map[string]graph.Status{}
	for _, it := range d.Items {
		if it.Kind == view.KindResource {
			statuses[it.Label] = it.Status
		}
	}
	if statuses["t.coded"] != graph.StatusDeclared || statuses["t.plain"] != "" {
		t.Errorf("item statuses: %v", statuses)
	}
}

func TestPlanChanges(t *testing.T) {
	ts := serveStates(t, map[string]string{"p": "plan.json"})
	var m metaResponse
	getJSON(t, ts, "/api/meta", &m)
	if got := strings.Join(m.Changes, ","); got != "create,delete,replace,update" {
		t.Errorf("changes: %q", got)
	}
	if got := strings.Join(m.Statuses, ","); got != "declared,managed" {
		t.Errorf("statuses: %q", got)
	}

	var d view.Document
	getJSON(t, ts, "/api/view?group_by=source&change=any", &d)
	changes := map[string]string{}
	for _, it := range d.Items {
		if it.Kind == view.KindResource {
			changes[it.Label] = it.Change
		}
	}
	want := map[string]string{
		"terraform_data.create": "create", "terraform_data.update": "update", "terraform_data.replace": "replace",
		"terraform_data.delete": "delete", "module.net.terraform_data.route": "create", "module.net.terraform_data.subnet[2]": "create",
	}
	if len(changes) != len(want) {
		t.Errorf("items: %v", changes)
	}
	for label, change := range want {
		if changes[label] != change {
			t.Errorf("%s: change %q, want %q", label, changes[label], change)
		}
	}

	getJSON(t, ts, "/api/view?group_by=source&change=delete", &d)
	if got := strings.Join(labels(d, view.KindResource), ","); got != "terraform_data.delete" {
		t.Errorf("change=delete: %q", got)
	}
	// A server with no plan loaded offers no change filter.
	getJSON(t, serve(t), "/api/meta", &m)
	if len(m.Changes) != 0 {
		t.Errorf("changes without a plan: %q", m.Changes)
	}
}

func TestMetaListsFilterValues(t *testing.T) {
	var m metaResponse
	getJSON(t, serve(t), "/api/meta", &m)
	if got := strings.Join(m.Accounts, ","); got != "123456789012" {
		t.Errorf("accounts: %q", got)
	}
	if got := strings.Join(m.Regions, ","); got != "global,us-east-1" {
		t.Errorf("regions: %q", got)
	}
	if got := strings.Join(m.Statuses, ","); got != "managed" {
		t.Errorf("statuses: %q", got)
	}
}

func TestLink(t *testing.T) {
	ts := serve(t)
	pairs := func(r linkResponse) string {
		var out []string
		for _, p := range r.Pairs {
			line := p.From.Address + ">" + p.To.Address
			if p.Via != nil {
				line += " by " + p.Via.Address
			}
			out = append(out, line)
		}
		return strings.Join(out, ",")
	}
	q := func(from, to, more string) string {
		return "/api/link?group_by=source,region&from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to) + more
	}

	// Between two collapsed groups: every dependency that crosses.
	var r linkResponse
	getJSON(t, ts, q("source:net/region:us-east-1/block:aws_subnet.private", "net::aws_vpc.main", ""), &r)
	if r.Total != 2 || pairs(r) != "aws_subnet.private[0]>aws_vpc.main,aws_subnet.private[1]>aws_vpc.main" {
		t.Errorf("subnets to network: %d, %s", r.Total, pairs(r))
	}

	// A glue link is found from either end, and names its glue.
	want := "aws_route_table.private>aws_subnet.private[0] by aws_route_table_association.private[0]," +
		"aws_route_table.private>aws_subnet.private[1] by aws_route_table_association.private[1]"
	getJSON(t, ts, q("net::aws_route_table.private", "source:net/region:us-east-1/block:aws_subnet.private", ""), &r)
	if pairs(r) != want {
		t.Errorf("route table to subnets: %s", pairs(r))
	}
	getJSON(t, ts, q("source:net/region:us-east-1/block:aws_subnet.private", "net::aws_route_table.private", ""), &r)
	if pairs(r) != want {
		t.Errorf("subnets to route table: %s", pairs(r))
	}
	// With glue shown as resources, the link it stood for is not listed.
	getJSON(t, ts, q("net::aws_route_table.private", "source:net/region:us-east-1/block:aws_subnet.private", "&glue=1"), &r)
	if r.Total != 0 {
		t.Errorf("with glue shown: %s", pairs(r))
	}

	if status, _ := get(t, ts, q("net::aws_vpc.main", "nowhere", "")); status != http.StatusNotFound {
		t.Errorf("unknown item: status %d", status)
	}
}

// Replacing the graph changes the generation, which is how the page learns
// that a state file changed.
func TestReplace(t *testing.T) {
	g := graph.New()
	s := New(g, nil)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	var m metaResponse
	getJSON(t, ts, "/api/meta", &m)
	if m.Generation != 1 || m.Resources != 0 {
		t.Fatalf("before: %+v", m)
	}
	fresh := graph.New()
	if err := fresh.AddNode(graph.Node{ID: "s::a", Source: "s", Address: "a"}); err != nil {
		t.Fatal(err)
	}
	s.Replace(fresh, nil)
	res, err := http.Get(ts.URL + "/api/view")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Moonwalk-Generation"); got != "2" {
		t.Errorf("generation header: %q", got)
	}
	getJSON(t, ts, "/api/meta", &m)
	if m.Generation != 2 || m.Resources != 1 {
		t.Errorf("after: %+v", m)
	}
}

func TestFrontEndIsServed(t *testing.T) {
	ts := serve(t)
	for path, want := range map[string]string{
		"/":          "<title>moonwalk</title>",
		"/app.js":    "api/view",
		"/style.css": "--accent",
	} {
		status, body := get(t, ts, path)
		if status != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("GET %s: status %d, missing %q", path, status, want)
		}
	}
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy: %q", csp)
	}
}

// A page on another site can point a host name at 127.0.0.1; requests that
// arrive under such a name must be refused.
func TestLocalOnlyRefusesOtherHosts(t *testing.T) {
	ts := serve(t)
	for host, want := range map[string]int{
		"localhost:7878":    http.StatusOK,
		"127.0.0.1:7878":    http.StatusOK,
		"[::1]:7878":        http.StatusOK,
		"evil.example":      http.StatusForbidden,
		"evil.example:7878": http.StatusForbidden,
		"192.168.1.10:7878": http.StatusForbidden,
	} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/meta", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("Host %q: status %d, want %d", host, res.StatusCode, want)
		}
	}
}
