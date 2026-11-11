package tfcode

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

func network(t *testing.T) *Config {
	t.Helper()
	c, err := Load("testdata/network")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func addresses(c *Config) []string {
	var out []string
	for addr := range c.Blocks {
		out = append(out, addr)
	}
	sort.Strings(out)
	return out
}

func TestLoadFindsEveryBlock(t *testing.T) {
	c := network(t)
	want := []string{
		"aws_cloudwatch_log_metric_filter.errors",
		"aws_iam_policy.read", // From the .tofu file.
		"aws_iam_role.app",
		"aws_iam_role_policy_attachment.app",
		"aws_instance.app",
		"aws_lb.web",
		"aws_route_table.private",
		"aws_route_table_association.private",
		"aws_security_group.app",
		"aws_security_group.lb",
		"aws_subnet.private",
		"aws_vpc.main",
		"data.aws_ami.linux",
		"module.logs.aws_cloudwatch_log_group.flow",
		"module.logs.aws_flow_log.vpc",
	}
	if got := addresses(c); !reflect.DeepEqual(got, want) {
		t.Errorf("blocks:\n got %q\nwant %q", got, want)
	}
	if len(c.Unresolved) != 0 {
		t.Errorf("unresolved modules: %q", c.Unresolved)
	}

	vpc := c.Blocks["aws_vpc.main"]
	if vpc.File != "main.tf" || vpc.Line != 9 || vpc.EndLine != 11 || vpc.Mode != graph.ModeManaged || vpc.Module != "" {
		t.Errorf("aws_vpc.main: %+v", vpc)
	}
	if want := "resource \"aws_vpc\" \"main\" {\n  cidr_block = \"10.0.0.0/16\"\n}"; vpc.Text != want {
		t.Errorf("text:\n%s\nwant:\n%s", vpc.Text, want)
	}
	flow := c.Blocks["module.logs.aws_flow_log.vpc"]
	if flow.File != "modules/logs/main.tf" || flow.Module != "module.logs" || flow.Type != "aws_flow_log" {
		t.Errorf("module block: %+v", flow)
	}
	if ami := c.Blocks["data.aws_ami.linux"]; ami.Mode != graph.ModeData || ami.Type != "aws_ami" {
		t.Errorf("data block: %+v", ami)
	}
}

func TestReferences(t *testing.T) {
	c := network(t)
	cases := map[string][]string{
		"aws_vpc.main":       {},
		"aws_subnet.private": {"aws_vpc.main"},
		// Through a local.
		"aws_route_table.private":             {"aws_vpc.main"},
		"aws_route_table_association.private": {"aws_route_table.private", "aws_subnet.private"},
		// local.name is a plain string and leads nowhere.
		"aws_instance.app": {"aws_security_group.app", "aws_subnet.private", "data.aws_ami.linux"},
		// A splat.
		"aws_lb.web": {"aws_subnet.private"},
		// Inside a dynamic block; "ingress.value" is not a resource.
		"aws_security_group.app": {"aws_security_group.lb", "aws_vpc.main"},
		// A variable leads to what the caller passes in.
		"module.logs.aws_flow_log.vpc": {"aws_vpc.main", "module.logs.aws_cloudwatch_log_group.flow"},
		// A module output leads to what it is computed from; depends_on counts too.
		"aws_cloudwatch_log_metric_filter.errors": {"aws_lb.web", "module.logs.aws_cloudwatch_log_group.flow"},
		"aws_iam_role_policy_attachment.app":      {"aws_iam_policy.read", "aws_iam_role.app"},
	}
	for addr, want := range cases {
		if got := c.Blocks[addr].DependsOn; !reflect.DeepEqual(got, want) {
			t.Errorf("%s depends on %q, want %q", addr, got, want)
		}
	}
}

func TestReferenceEdgeCases(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"main.tf": `
locals {
  a = local.b
  b = local.a           # Locals that refer to each other.
  ids = [t_thing.one.id]
}
resource "t_thing" "one" {}
resource "t_thing" "loop"  { x = local.a }
resource "t_thing" "self"  { x = t_thing.self.id }
resource "t_thing" "via"   { x = local.ids[0] }
resource "t_thing" "whole" { depends_on = [module.kids] }
resource "t_thing" "index" { x = module.kids["a"].out[0] }
resource "t_thing" "gone" {
  x = module.missing.out
  y = var.unset
  z = t_other.nope.id
}
module "kids" {
  source   = "./kids"
  for_each = toset(["a"])
  input    = t_thing.one.id
}
module "missing" { source = "example/registry/module" }
`,
		"kids/main.tf": `
variable "input" {}
resource "t_kid" "a" { x = var.input }
resource "t_kid" "b" {}
output "out" { value = [t_kid.a.id] }
module "deeper" { source = "../deeper" }
`,
		"deeper/main.tf": `resource "t_deep" "d" {}`,
	})
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"t_thing.loop": {},
		"t_thing.self": {},
		"t_thing.via":  {"t_thing.one"},
		// The whole module: everything in it, nested modules included.
		"t_thing.whole": {"module.kids.module.deeper.t_deep.d", "module.kids.t_kid.a", "module.kids.t_kid.b"},
		// An output, with instance keys and indexes in the reference.
		"t_thing.index":       {"module.kids.t_kid.a"},
		"t_thing.gone":        {},
		"module.kids.t_kid.a": {"t_thing.one"},
	}
	for addr, want := range cases {
		blk, ok := c.Blocks[addr]
		if !ok {
			t.Errorf("%s: missing", addr)
			continue
		}
		if !reflect.DeepEqual(blk.DependsOn, want) {
			t.Errorf("%s depends on %q, want %q", addr, blk.DependsOn, want)
		}
	}
	if !reflect.DeepEqual(c.Unresolved, []string{"module.missing"}) {
		t.Errorf("unresolved: %q", c.Unresolved)
	}
	// A module outside the root keeps a path that says so.
	if got := c.Blocks["module.kids.module.deeper.t_deep.d"].File; got != "deeper/main.tf" {
		t.Errorf("file of a module reached by ../: %q", got)
	}
}

// `terraform init` records where it put each module; that is the only way
// to find one that does not come from a relative path.
func TestModulesFromTheInitManifest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"main.tf": `module "vpc" { source = "terraform-aws-modules/vpc/aws" }`,
		".terraform/modules/modules.json": `{"Modules":[{"Key":"","Source":"","Dir":"."},
		  {"Key":"vpc","Source":"registry.terraform.io/terraform-aws-modules/vpc/aws","Dir":".terraform/modules/vpc"},
		  {"Key":"vpc.inner","Source":"./inner","Dir":".terraform/modules/vpc/inner"}]}`,
		".terraform/modules/vpc/main.tf": `
resource "aws_vpc" "this" {}
module "inner" { source = "./inner" }`,
		".terraform/modules/vpc/inner/main.tf": `resource "aws_subnet" "this" { vpc_id = "x" }`,
	})
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"module.vpc.aws_vpc.this", "module.vpc.module.inner.aws_subnet.this"}
	if got := addresses(c); !reflect.DeepEqual(got, want) || len(c.Unresolved) != 0 {
		t.Errorf("blocks %q, unresolved %q", got, c.Unresolved)
	}
	if got := c.Blocks["module.vpc.aws_vpc.this"].File; got != ".terraform/modules/vpc/main.tf" {
		t.Errorf("file: %q", got)
	}
}

func TestWhichFilesAreRead(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"both.tf":          `resource "t" "from_tf" {}`,
		"both.tofu":        `resource "t" "from_tofu" {}`, // OpenTofu reads this one instead.
		"plain.tf":         `resource "t" "plain" {}`,
		"override.tf":      `resource "t" "plain" { x = 1 }`,
		"more_override.tf": `resource "t" "plain" { x = 2 }`,
		"notes.txt":        `resource "t" "not_code" {}`,
		"vars.tfvars":      `x = 1`,
		".hidden.tf":       `resource "t" "hidden" {}`,
		"sub/main.tf":      `resource "t" "not_called" {}`,
	})
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := addresses(c), []string{"t.from_tofu", "t.plain"}; !reflect.DeepEqual(got, want) {
		t.Errorf("blocks %q, want %q", got, want)
	}
}

// Syntax that OpenTofu accepts and Terraform does not. Only structure is
// read, so none of it should get in the way.
func TestOpenTofuOnlySyntax(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"main.tofu": `
terraform {
  encryption {
    key_provider "pbkdf2" "k" {
      passphrase = var.passphrase
    }
    method "aes_gcm" "m" {
      keys = key_provider.pbkdf2.k
    }
    state {
      method = method.aes_gcm.m
    }
  }
}

variable "regions" {
  type = set(string)
}

provider "aws" {
  alias    = "by_region"
  for_each = var.regions
  region   = each.value
}

resource "aws_vpc" "main" {
  for_each = var.regions
  provider = aws.by_region[each.key]
  cidr_block = "10.0.0.0/16"

  lifecycle {
    enabled = each.key != "us-west-2"
  }
}

resource "aws_subnet" "a" {
  for_each = aws_vpc.main
  vpc_id   = each.value.id
}

module "regional" {
  source   = "./regional"
  for_each = var.regions
  providers = {
    aws = aws.by_region[each.key]
  }
}
`, "regional/main.tofu": `resource "aws_s3_bucket" "b" {}`})
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"aws_subnet.a", "aws_vpc.main", "module.regional.aws_s3_bucket.b"}
	if got := addresses(c); !reflect.DeepEqual(got, want) {
		t.Errorf("blocks %q, want %q", got, want)
	}
	if got := c.Blocks["aws_subnet.a"].DependsOn; !reflect.DeepEqual(got, []string{"aws_vpc.main"}) {
		t.Errorf("aws_subnet.a depends on %q", got)
	}
	// A provider reference is not a resource reference.
	if got := c.Blocks["aws_vpc.main"].DependsOn; len(got) != 0 {
		t.Errorf("aws_vpc.main depends on %q", got)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("an empty directory was accepted")
	}
	if _, err := Load("testdata/does-not-exist"); err == nil {
		t.Error("a missing directory was accepted")
	}
	dir := t.TempDir()
	write(t, dir, map[string]string{"main.tf": "resource \"t\" \"broken\" {\n  x = \n}\n"})
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "main.tf:2") {
		t.Errorf("a syntax error should name the file and line, got %v", err)
	}
}

func TestApply(t *testing.T) {
	g := graph.New()
	f, err := os.Open("../tfstate/testdata/network.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := tfstate.Load(g, "net", f); err != nil {
		t.Fatal(err)
	}
	before := map[string]bool{}
	for _, e := range g.Edges() {
		before[e.From+" > "+e.To] = true
	}

	res, err := network(t).Apply(g, "net")
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{Linked: 11, Declared: 6}) {
		t.Errorf("result: %+v", res)
	}

	// Both instances of a counted block point at the one block.
	for _, addr := range []string{"aws_subnet.private[0]", "aws_subnet.private[1]"} {
		n, _ := g.Node("net::" + addr)
		if n.Status != graph.StatusManaged || n.Code == nil || *n.Code != (graph.CodeLocation{File: "main.tf", Line: 13, EndLine: 17}) {
			t.Errorf("%s: status %s, code %+v", addr, n.Status, n.Code)
		}
	}
	sg, ok := g.Node("net::aws_security_group.app")
	if !ok || sg.Status != graph.StatusDeclared || sg.Code == nil || sg.Type != "aws_security_group" || sg.ConfigAddress != "aws_security_group.app" {
		t.Errorf("declared resource: %+v", sg)
	}
	if flow, _ := g.Node("net::module.logs.aws_flow_log.vpc"); flow.Module != "module.logs" || flow.Status != graph.StatusDeclared {
		t.Errorf("declared resource in a module: %+v", flow)
	}
	// A declared resource goes where the state's other resources of its
	// provider are: here one account and one region.
	if sg.Account != "123456789012" || !sg.AccountInferred || sg.Region != "us-east-1" || !sg.RegionInferred {
		t.Errorf("declared resource placed in %q, %q", sg.Account, sg.Region)
	}

	// Only dependencies that touch a declared resource are added; the rest
	// are left as state recorded them.
	var added []string
	for _, e := range g.Edges() {
		if line := e.From + " > " + e.To; !before[line] {
			added = append(added, strings.ReplaceAll(line, "net::", ""))
		}
	}
	want := []string{
		"aws_cloudwatch_log_metric_filter.errors > aws_lb.web",
		"aws_cloudwatch_log_metric_filter.errors > module.logs.aws_cloudwatch_log_group.flow",
		"aws_instance.app > aws_security_group.app",
		"aws_instance.app > data.aws_ami.linux",
		"aws_security_group.app > aws_security_group.lb",
		"aws_security_group.app > aws_vpc.main",
		"aws_security_group.lb > aws_vpc.main",
		"module.logs.aws_flow_log.vpc > aws_vpc.main",
		"module.logs.aws_flow_log.vpc > module.logs.aws_cloudwatch_log_group.flow",
	}
	if !reflect.DeepEqual(added, want) {
		t.Errorf("edges added:\n got %s\nwant %s", strings.Join(added, "\n     "), strings.Join(want, "\n     "))
	}
}

// With no state at all, the configuration alone still makes a graph.
func TestApplyWithoutState(t *testing.T) {
	g := graph.New()
	res, err := network(t).Apply(g, "code")
	if err != nil {
		t.Fatal(err)
	}
	nodes, edges := g.Len()
	if res.Linked != 0 || res.Declared != 15 || nodes != 15 || edges != 17 {
		t.Errorf("result %+v, %d nodes, %d edges", res, nodes, edges)
	}
}

func TestPlacer(t *testing.T) {
	p := newPlacer()
	for _, n := range []graph.Node{
		{Type: "aws_vpc", Mode: graph.ModeManaged, Account: "111", Region: "us-east-1"},
		{Type: "aws_subnet", Mode: graph.ModeManaged, Account: "111", Region: "us-east-1"},
		{Type: "aws_iam_role", Mode: graph.ModeManaged, Account: "111", Region: graph.RegionGlobal},
		{Type: "azurerm_resource_group", Mode: graph.ModeManaged, Account: "sub-1", Region: "westeurope"},
		{Type: "azurerm_resource_group", Mode: graph.ModeManaged, Account: "sub-2", Region: "northeurope"},
		{Type: "aws_ami", Mode: graph.ModeData, Account: "999", Region: "eu-west-1"}, // Not evidence.
	} {
		p.learn(n)
	}
	cases := []struct{ typ, account, region string }{
		{"aws_instance", "111", "us-east-1"},   // From the provider's other resources.
		{"aws_iam_role", "111", "global"},      // From resources of the same type.
		{"aws_iam_policy", "111", "us-east-1"}, // Nothing says this type is global.
		{"azurerm_virtual_network", "", ""},    // Two subscriptions and regions: no guess.
		{"google_compute_instance", "", ""},    // Nothing known about the provider.
	}
	for _, c := range cases {
		n := graph.Node{Type: c.typ}
		p.place(&n)
		if n.Account != c.account || n.Region != c.region || n.AccountInferred != (c.account != "") || n.RegionInferred != (c.region != "") {
			t.Errorf("%s placed in %q, %q; want %q, %q", c.typ, n.Account, n.Region, c.account, c.region)
		}
	}
}
