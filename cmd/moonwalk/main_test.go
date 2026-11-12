package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files with current output")

const fixtures = "../../internal/tfstate/testdata/"

// code is a configuration that matches the network fixture and declares a
// few resources more.
const code = "../../internal/tfcode/testdata/network"

// found holds real `terraform query -json` output and a state that manages
// part of what it lists.
const found = "../../internal/discover/testdata/"

// planCode is the configuration the plan fixture was made from.
const planCode = "../../internal/tfcode/testdata/plan"

func runCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	out, _, err := runCLIWithWarnings(t, stdin, args...)
	return out, err
}

func runCLIWithWarnings(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, warnings bytes.Buffer
	err = run(args, strings.NewReader(stdin), &out, &warnings)
	return out.String(), warnings.String(), err
}

// golden compares got with testdata/<name>, or rewrites it under -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./cmd/moonwalk -update` to create it)", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s:\n%s", path, got)
	}
}

func TestGraphGolden(t *testing.T) {
	cases := []struct {
		golden string
		args   []string
	}{
		{"text.golden", []string{"graph", "prod=" + fixtures + "raw.tfstate", "lab=" + fixtures + "secrets.tfstate"}},
		{"json.golden", []string{"graph", "-format", "json", "prod=" + fixtures + "show.json"}},
		{"attributes.golden", []string{"graph", "-format", "json", "-attributes", "lab=" + fixtures + "secrets.show.json"}},
		{"view.golden", []string{"view", "prod=" + fixtures + "raw.tfstate", "lab=" + fixtures + "secrets.tfstate"}},
		{"view-depth.golden", []string{"view", "-depth", "3", "prod=" + fixtures + "raw.tfstate", "lab=" + fixtures + "secrets.tfstate"}},
		{"view-by-module.golden", []string{"view", "-group-by", "module", "prod=" + fixtures + "raw.tfstate", "lab=" + fixtures + "secrets.tfstate"}},
		{"view-json.golden", []string{"view", "-format", "json", "-depth", "3", "prod=" + fixtures + "show.json"}},
		{"network-graph.golden", []string{"graph", "net=" + fixtures + "network.tfstate"}},
		{"network-view.golden", []string{"view", "net=" + fixtures + "network.tfstate"}},
		{"network-view-by-network.golden", []string{"view", "-group-by", "source,region,network", "-glue", "net=" + fixtures + "network.tfstate"}},
		{"network-view-around.golden", []string{"view", "-group-by", "source", "-around", "aws_route_table.private", "net=" + fixtures + "network.tfstate"}},
		{"code-view.golden", []string{"view", "-code", code, "net=" + fixtures + "network.tfstate"}},
		{"code-declared.golden", []string{"view", "-format", "json", "-status", "declared", "-group-by", "source", "-code", "net=" + code, "net=" + fixtures + "network.tfstate"}},
		{"code-only.golden", []string{"graph", "-code", "plan=" + code}},
		{"plan-view.golden", []string{"view", "-group-by", "source", "p=" + fixtures + "plan.json"}},
		{"plan-with-code.golden", []string{"view", "-code", planCode, "p=" + fixtures + "plan.json"}},
		{"plan-changes.golden", []string{"graph", "-format", "json", "-direct", "p=" + fixtures + "plan.json"}},
		{"discover-view.golden", []string{"view", "-group-by", "account,region,network", "prod=" + found + "managed.tfstate", "cloud=" + found + "query.jsonl"}},
		{"discover-graph.golden", []string{"graph", "-format", "json", "-attributes", "cloud=" + found + "query.jsonl", "prod=" + found + "managed.tfstate"}},
		{"drift-view.golden", []string{"view", "-group-by", "source", "d=" + fixtures + "drift.json"}},
		{"network-view-filter.golden", []string{"view", "-format", "json", "-type", "aws_iam_*,aws_vpc", "-region", "global", "net=" + fixtures + "network.tfstate"}},
		{"azure-view.golden", []string{"view", "az=" + fixtures + "azure.tfstate"}},
		{"azure-view-by-group.golden", []string{"view", "-group-by", "account,resource_group,network", "az=" + fixtures + "azure.tfstate"}},
		{"azure-graph.golden", []string{"graph", "-format", "json", "az=" + fixtures + "azure.tfstate"}},
		{"azure-discover.golden", []string{"view", "-group-by", "resource_group", "-status", "unmanaged", "az=" + fixtures + "azure.tfstate",
			"cloud=" + found + "az-resource-list.json", "groups=" + found + "az-group-list.json"}},
	}
	for _, c := range cases {
		got, err := runCLI(t, "", c.args...)
		if err != nil {
			t.Errorf("%s: %v", c.golden, err)
			continue
		}
		golden(t, c.golden, got)
	}
}

// The secrets fixtures hold a generated password, its hash, and a sensitive
// variable that Terraform also copies, unmarked, into another attribute.
func TestSecretsNeverPrinted(t *testing.T) {
	secrets := []string{"hunter2", "4@Sze931N+:r", "$2a$10$INNvf"}
	for _, file := range []string{"secrets.tfstate", "secrets.show.json"} {
		for _, format := range []string{"text", "json"} {
			out, err := runCLI(t, "", "graph", "-format", format, "-attributes", fixtures+file)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Errorf("%s as %s: output contains the secret %q", file, format, s)
				}
			}
			if format == "json" && !strings.Contains(out, `"(sensitive)"`) {
				t.Errorf("%s: no redaction marker in output", file)
			}
		}
	}
}

// The unmarked fixtures are real output of Terraform 1.5.7 (the state file)
// and 1.3.10 (show -json). Neither marks the generated password as
// sensitive, so moonwalk cannot redact it and must not print any attribute
// of those states.
func TestUnmarkedStatesWithholdAttributes(t *testing.T) {
	secrets := map[string][]string{
		"unmarked.tfstate":   {"8%exVpqbOs%l", "$2a$10$Yh49MOe", "hunter2"},
		"unmarked.show.json": {">p6)+4Lqi@J?", "$2a$10$NOV6SnY", "hunter2"},
	}
	for file, values := range secrets {
		out, warnings, err := runCLIWithWarnings(t, "", "graph", "-format", "json", "-attributes", "old="+fixtures+file)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range values {
			if strings.Contains(out, s) {
				t.Errorf("%s: output contains the secret %q", file, s)
			}
		}
		if strings.Contains(out, `"attributes":`) || !strings.Contains(out, `"attributes_withheld": true`) {
			t.Errorf("%s: attributes should be withheld and marked as such:\n%s", file, out)
		}
		if !strings.Contains(warnings, "old") || !strings.Contains(warnings, "show -json") {
			t.Errorf("%s: warning should name the source and the remedy, got %q", file, warnings)
		}
	}

	// A state that does mark its secrets raises no warning.
	_, warnings, err := runCLIWithWarnings(t, "", "graph", "-format", "json", "-attributes", fixtures+"secrets.tfstate")
	if err != nil || warnings != "" {
		t.Errorf("marked state: err %v, warnings %q", err, warnings)
	}
}

func TestAttributesOmittedByDefault(t *testing.T) {
	out, err := runCLI(t, "", "graph", "-format", "json", fixtures+"secrets.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `"attributes`) {
		t.Error("attributes printed without -attributes")
	}
	out, warnings, err := runCLIWithWarnings(t, "", "graph", "-format", "json", fixtures+"unmarked.tfstate")
	if err != nil || strings.Contains(out, `"attributes`) || warnings != "" {
		t.Errorf("unmarked state without -attributes: err %v, warnings %q", err, warnings)
	}
}

// The subnets in the fixture depend on the network directly, and the
// association depends on the subnets; nothing there is implied by the rest.
// A state with a shortcut loses it under -direct.
func TestDirect(t *testing.T) {
	const state = `{"version":4,"resources":[
	  {"mode":"managed","type":"t","name":"a","provider":"p","instances":[{}]},
	  {"mode":"managed","type":"t","name":"b","provider":"p","instances":[{"dependencies":["t.a"]}]},
	  {"mode":"managed","type":"t","name":"c","provider":"p","instances":[{"dependencies":["t.a","t.b"]}]}]}`
	all, err := runCLI(t, state, "graph", "-")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := runCLI(t, state, "graph", "-direct", "-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all, "3 resources, 3 dependencies") || !strings.Contains(direct, "3 resources, 2 dependencies") {
		t.Errorf("want 3 dependencies without -direct and 2 with it:\n%s\n%s", all, direct)
	}
}

// A depth beyond the deepest level shows everything rather than failing.
func TestViewDepthIsCapped(t *testing.T) {
	deep, err := runCLI(t, "", "view", "-depth", "99", fixtures+"raw.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(deep, "Expanded to depth") || !strings.Contains(deep, "aws_subnet.private[1]") {
		t.Errorf("view at depth 99 is not fully expanded:\n%s", deep)
	}
}

func TestStdin(t *testing.T) {
	data, err := os.ReadFile(fixtures + "raw.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, string(data), "graph", "-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nstdin\n") {
		t.Errorf("default name for standard input missing:\n%s", out)
	}
	out, err = runCLI(t, string(data), "graph", "piped=-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\npiped\n") {
		t.Errorf("chosen name for standard input missing:\n%s", out)
	}
}

func TestDirectory(t *testing.T) {
	data, err := os.ReadFile(fixtures + "raw.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"network/terraform.tfstate":        string(data),
		"app/terraform.tfstate":            string(data),
		"app/terraform.tfstate.backup":     "not loaded",
		"app/notes.json":                   "not loaded",
		"app/.terraform/terraform.tfstate": `{"version": 3}`,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runCLI(t, "", "graph", "live="+dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"10 resources, 12 dependencies, 2 sources", "\nlive/app/terraform.tfstate\n", "\nlive/network/terraform.tfstate\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	if _, err := runCLI(t, "", "graph", t.TempDir()); err == nil {
		t.Error("directory without state files accepted")
	}
}

// A path that exists is never split at "=", so such directories still load.
func TestExistingPathWithEqualsSign(t *testing.T) {
	data, err := os.ReadFile(fixtures + "raw.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "env=prod.tfstate")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "", "graph", path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\n"+path+"\n") {
		t.Errorf("path was not used as the source name:\n%s", out)
	}
}

func TestPlan(t *testing.T) {
	out, err := runCLI(t, "", "view", "-change", "any", "-group-by", "source", fixtures+"plan.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"6 resources", "3 resources left out by the filter",
		"terraform_data.create  (to create)", "terraform_data.update  (to update)",
		"terraform_data.replace  (to replace)", "terraform_data.delete  (to delete)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out, err = runCLI(t, "", "view", "-change", "delete,replace", "-group-by", "source", fixtures+"plan.json")
	if err != nil || !strings.Contains(out, "2 resources in") {
		t.Errorf("two kinds of change: %v\n%s", err, out)
	}
	// The plan holds a sensitive variable in the clear; it must not come out.
	for _, args := range [][]string{
		{"graph", "-format", "json", "-attributes", fixtures + "plan.json"},
		{"view", "-format", "json", fixtures + "plan.json"},
	} {
		if out, err := runCLI(t, "", args...); err != nil || strings.Contains(out, "hunter2") {
			t.Errorf("%v: err %v, or the secret is in the output", args, err)
		}
	}
	// With the code, a resource the plan deletes because its block is gone
	// is also marked as missing from the code.
	out, err = runCLI(t, "", "view", "-group-by", "source", "-code", planCode, fixtures+"plan.json")
	if err != nil || !strings.Contains(out, "terraform_data.delete  (to delete, not in code)") {
		t.Errorf("plan with code: %v\n%s", err, out)
	}
}

func TestDiscovery(t *testing.T) {
	// The order of the arguments does not matter: states are loaded first.
	for _, args := range [][]string{
		{"prod=" + found + "managed.tfstate", "cloud=" + found + "query.jsonl"},
		{"cloud=" + found + "query.jsonl", "prod=" + found + "managed.tfstate"},
	} {
		out, err := runCLI(t, "", append([]string{"view", "-status", "unmanaged", "-group-by", "source"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"2 resources in", "cloud  (source, 2 resources, 2 unmanaged)",
			"aws_subnet.subnet-0aa000000000000b2  (unmanaged)", "aws_subnet.subnet-0aa000000000000c3  (unmanaged)"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: missing %q in:\n%s", args, want, out)
			}
		}
	}
	// A listing's attributes are never printed: it does not say which are
	// secret. The warning says so, and does not blame an old Terraform.
	out, warnings, err := runCLIWithWarnings(t, "", "graph", "-format", "json", "-attributes", "cloud="+found+"query.jsonl")
	if err != nil || strings.Contains(out, "cidr_block") || !strings.Contains(out, `"attributes_withheld": true`) {
		t.Errorf("listing attributes: %v", err)
	}
	if !strings.Contains(warnings, "attribute values of cloud are withheld: a listing") || strings.Contains(warnings, "show -json") {
		t.Errorf("listing attributes: warning %q", warnings)
	}
	// With one state and one listing, code needs no name: it belongs to the state.
	out, err = runCLI(t, "", "view", "-group-by", "source", "-code", code, "prod="+found+"managed.tfstate", "cloud="+found+"query.jsonl")
	if err != nil || !strings.Contains(out, "prod  (source") || !strings.Contains(out, "(declared)") {
		t.Errorf("code with a state and a listing: %v\n%s", err, out)
	}
	// An error in the listing is passed on as a warning.
	listing := `{"@level":"info","type":"version","terraform":"1.14.0"}` + "\n" +
		`{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"Error listing","detail":"AccessDenied"}}` + "\n"
	_, warnings, err = runCLIWithWarnings(t, listing, "graph", "cloud=-")
	if err != nil || !strings.Contains(warnings, "incomplete") || !strings.Contains(warnings, "AccessDenied") {
		t.Errorf("listing error: err %v, warnings %q", err, warnings)
	}
}

// For those without `terraform query`: the AWS CLI's own listings.
func TestARNListing(t *testing.T) {
	out, err := runCLI(t, "", "view", "-group-by", "account,region", "prod="+found+"managed.tfstate", "cli="+found+"tagging-api.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cli::aws_subnet.subnet-9  (unmanaged)", "cli::aws_iam_role.deploy  (unmanaged)", "prod::aws_vpc.main"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A state file is still a state file: it has a "resources" key, as a
	// Resource Explorer search has "Resources".
	if strings.Contains(out, "prod::aws_vpc.main  (unmanaged)") {
		t.Errorf("the state was read as a listing:\n%s", out)
	}
}

func TestCappedListingWarns(t *testing.T) {
	var listing strings.Builder
	listing.WriteString(`{"@level":"info","type":"list_start","list_start":{"address":"list.aws_subnet.all","resource_type":"aws_subnet"}}` + "\n")
	listing.WriteString(`{"@level":"info","type":"list_complete","list_complete":{"address":"list.aws_subnet.all","resource_type":"aws_subnet","total":100}}` + "\n")
	_, warnings, err := runCLIWithWarnings(t, listing.String(), "graph", "cloud=-")
	if err != nil || !strings.Contains(warnings, "list.aws_subnet.all") || !strings.Contains(warnings, "limit = 1000000") {
		t.Errorf("err %v, warnings %q", err, warnings)
	}
}

// A subnet in state that the listing covered and did not find is marked.
func TestNotFoundInListing(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "s.tfstate")
	const aws = `"provider":"provider[\"registry.terraform.io/hashicorp/aws\"]"`
	if err := os.WriteFile(state, []byte(`{"version":4,"terraform_version":"1.9.5","resources":[
	  {"mode":"managed","type":"aws_subnet","name":"gone",`+aws+`,
	   "instances":[{"attributes":{"id":"subnet-gone","arn":"arn:aws:ec2:us-east-1:123456789012:subnet/subnet-gone"}}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "", "view", "-group-by", "source", "s="+state, "cloud="+found+"query.jsonl")
	if err != nil || !strings.Contains(out, "aws_subnet.gone  (not found in the cloud listing)") {
		t.Errorf("err %v\n%s", err, out)
	}
}

func TestDrift(t *testing.T) {
	out, err := runCLI(t, "", "view", "-status", "drifted", "-group-by", "source", fixtures+"drift.json")
	if err != nil || !strings.Contains(out, "local_file.removed  (drifted: deleted)") || !strings.Contains(out, "1 resource in") {
		t.Errorf("drift: %v\n%s", err, out)
	}
}

func TestCodeFlag(t *testing.T) {
	// With one state the code needs no name; with two it does.
	if _, err := runCLI(t, "", "view", "-code", code, "a="+fixtures+"network.tfstate", "b="+fixtures+"raw.tfstate"); err == nil {
		t.Error("code without a name was accepted alongside two states")
	}
	out, err := runCLI(t, "", "view", "-code", "a="+code, "a="+fixtures+"network.tfstate", "b="+fixtures+"raw.tfstate")
	if err != nil || !strings.Contains(out, "aws_security_group.app  (declared)") {
		t.Errorf("named code: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "", "view", "-code", "a="+code, "-code", "a="+code, "a="+fixtures+"network.tfstate"); err == nil {
		t.Error("two code directories for one state were accepted")
	}
	if _, err := runCLI(t, "", "view", "-code", "no-such-directory"); err == nil {
		t.Error("a missing code directory was accepted")
	}

	// The location is in the JSON, and a module that cannot be found is
	// reported without failing the command.
	out, err = runCLI(t, "", "graph", "-format", "json", "-code", code, fixtures+"network.tfstate")
	if err != nil || !strings.Contains(out, `"file": "modules/logs/main.tf"`) {
		t.Errorf("code location missing from JSON: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`module "vpc" { source = "example/vpc/aws" }`+"\n"+`resource "t" "x" {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, warnings, err := runCLIWithWarnings(t, "", "view", "-code", "c="+dir)
	if err != nil || !strings.Contains(out, "x  (declared)") || !strings.Contains(warnings, "module.vpc") || !strings.Contains(warnings, "terraform init") {
		t.Errorf("unresolved module: err %v, warnings %q\n%s", err, warnings, out)
	}
}

func TestFingerprintFollowsTheFiles(t *testing.T) {
	data, err := os.ReadFile(fixtures + "raw.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	first := filepath.Join(dir, "a.tfstate")
	if err := os.WriteFile(first, data, 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"live=" + dir}
	before, ok := fingerprint(args, nil)
	if !ok {
		t.Fatal("a directory should be watchable")
	}
	if again, _ := fingerprint(args, nil); again != before {
		t.Error("fingerprint changed although nothing did")
	}

	// A change of content, even to the same length, shows.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(first, later, later); err != nil {
		t.Fatal(err)
	}
	touched, _ := fingerprint(args, nil)
	if touched == before {
		t.Error("fingerprint missed a modified file")
	}
	// So does a new state in a watched directory.
	if err := os.WriteFile(filepath.Join(dir, "b.tfstate"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if added, _ := fingerprint(args, nil); added == touched {
		t.Error("fingerprint missed a new file")
	}
	// And a file that disappears, which leaves the directory unreadable as
	// a set of states only once it is empty.
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	removed, _ := fingerprint(args, nil)
	if removed == touched {
		t.Error("fingerprint missed a removed file")
	}

	// Code is watched too: its configuration files, at any depth.
	codeDir := t.TempDir()
	module := filepath.Join(codeDir, "modules", "m")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(codeDir, "main.tf"), filepath.Join(module, "main.tofu"), filepath.Join(codeDir, "notes.txt")} {
		if err := os.WriteFile(f, []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	withCode, _ := fingerprint(nil, []string{"c=" + codeDir})
	if err := os.Chtimes(filepath.Join(codeDir, "notes.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	if again, _ := fingerprint(nil, []string{"c=" + codeDir}); again != withCode {
		t.Error("a file that is not configuration changed the fingerprint")
	}
	if err := os.Chtimes(filepath.Join(module, "main.tofu"), later, later); err != nil {
		t.Fatal(err)
	}
	if again, _ := fingerprint(nil, []string{"c=" + codeDir}); again == withCode {
		t.Error("fingerprint missed a changed file in a module")
	}

	if _, ok := fingerprint([]string{filepath.Join(dir, "b.tfstate"), "-"}, nil); ok {
		t.Error("standard input cannot be watched")
	}
}

// at pins the time snapshots are stamped with.
func at(t *testing.T, when time.Time) {
	t.Helper()
	was := now
	now = func() time.Time { return when }
	t.Cleanup(func() { now = was })
}

// collect writes a snapshot of the given inputs to a file and returns its
// path.
func collect(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runCLI(t, "", append([]string{"collect"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCollect(t *testing.T) {
	at(t, time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC))
	out, err := runCLI(t, "", "collect", "-code", code, "net="+fixtures+"network.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "collect.golden", out)
}

// Whatever was loaded, a snapshot of it shows the same as it did, in every
// output, and from standard input as from a file.
func TestSnapshotShowsWhatWasCollected(t *testing.T) {
	inputs := map[string][]string{
		"states":    {"prod=" + fixtures + "raw.tfstate", "lab=" + fixtures + "secrets.tfstate"},
		"show":      {fixtures + "show.json"},
		"plan":      {"-code", planCode, "p=" + fixtures + "plan.json"},
		"drift":     {fixtures + "drift.json"},
		"code":      {"-code", code, "net=" + fixtures + "network.tfstate"},
		"listing":   {"net=" + found + "managed.tfstate", found + "query.jsonl"},
		"withheld":  {"old=" + fixtures + "unmarked.tfstate"},
		"code only": {"-code", "plan=" + code},
	}
	outputs := [][]string{
		{"graph"},
		{"graph", "-format", "json", "-attributes"},
		{"graph", "-direct"},
		{"view"},
		{"view", "-format", "json", "-group-by", "source,region,network", "-glue"},
	}
	for name, in := range inputs {
		snap := collect(t, in...)
		data, err := os.ReadFile(snap)
		if err != nil {
			t.Fatal(err)
		}
		for _, output := range outputs {
			want, err := runCLI(t, "", append(slices.Clone(output), in...)...)
			if err != nil {
				t.Fatal(err)
			}
			got, err := runCLI(t, "", append(slices.Clone(output), snap)...)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s as %v: the snapshot shows something else than what it was collected from:\n%s", name, output, got)
			}
			piped, err := runCLI(t, string(data), append(slices.Clone(output), "-")...)
			if err != nil || piped != want {
				t.Errorf("%s as %v: the snapshot on standard input shows something else (err %v)", name, output, err)
			}
		}
	}
}

// Two snapshots collected apart are linked when loaded together, and a
// snapshot loads next to a state.
func TestSnapshotsTogether(t *testing.T) {
	want, err := runCLI(t, "", "view", "network="+fixtures+"remote-network.tfstate", "app="+fixtures+"remote-app.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	network := collect(t, "network="+fixtures+"remote-network.tfstate")
	app := collect(t, "app="+fixtures+"remote-app.tfstate")
	for name, args := range map[string][]string{
		"two snapshots":        {network, app},
		"a snapshot and state": {network, "app=" + fixtures + "remote-app.tfstate"},
		// The name given to a snapshot's file goes unused.
		"a named snapshot": {"ignored=" + network, app},
	} {
		got, err := runCLI(t, "", append([]string{"view"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s:\n%s\nwant\n%s", name, got, want)
		}
	}
	if !strings.Contains(want, "-> network / ") {
		t.Errorf("the two states should be linked:\n%s", want)
	}

	// Compressed, a snapshot reads the same, and so does a state.
	zipped := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write(data)
		zw.Close()
		out := filepath.Join(t.TempDir(), "zipped.gz")
		if err := os.WriteFile(out, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got, err := runCLI(t, "", "view", zipped(network), "app="+zipped(fixtures+"remote-app.tfstate")); err != nil || got != want {
		t.Errorf("compressed inputs (err %v):\n%s", err, got)
	}
	cut := filepath.Join(t.TempDir(), "cut.gz")
	if data, _ := os.ReadFile(zipped(network)); os.WriteFile(cut, data[:len(data)/2], 0o644) != nil {
		t.Fatal("cannot write")
	}
	if _, err := runCLI(t, "", "view", cut); err == nil {
		t.Error("a compressed file cut short: no error")
	}

	// A snapshot of snapshots is the same as one of the states.
	both := collect(t, network, app)
	if got, _ := runCLI(t, "", "view", both); got != want {
		t.Errorf("snapshot of two snapshots:\n%s", got)
	}

	// The same source twice is refused, whichever way it arrives.
	for name, args := range map[string][]string{
		"same snapshot twice":         {network, network},
		"snapshot and its own state":  {network, "network=" + fixtures + "remote-network.tfstate"},
		"state and then its snapshot": {"network=" + fixtures + "remote-network.tfstate", network},
	} {
		if _, err := runCLI(t, "", append([]string{"view"}, args...)...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestCollectWarnsOfWithheldAttributes(t *testing.T) {
	out, warnings, err := runCLIWithWarnings(t, "", "collect", "old="+fixtures+"unmarked.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings, "old") || strings.Contains(out, `"attributes":`) {
		t.Errorf("warnings %q; a snapshot of an unmarked state must hold no attributes", warnings)
	}
	for _, s := range []string{"8%exVpqbOs%l", "$2a$10$Yh49MOe", "hunter2"} {
		if strings.Contains(out, s) {
			t.Errorf("the snapshot contains the secret %q", s)
		}
	}
}

func TestDiff(t *testing.T) {
	before, after := fixtures+"diff-before.tfstate", fixtures+"diff-after.tfstate"
	out, err := runCLI(t, "", "diff", before, after)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "diff.golden", out)
	out, err = runCLI(t, "", "diff", "-format", "json", "app="+before, "app="+after)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "diff-json.golden", out)

	// Snapshots compare as what they were collected from, with each other
	// and with a state.
	want, _ := runCLI(t, "", "diff", "app="+before, "app="+after)
	snapBefore, snapAfter := collect(t, "app="+before), collect(t, "app="+after)
	for name, args := range map[string][]string{
		"two snapshots":      {snapBefore, snapAfter},
		"snapshot and state": {snapBefore, "app=" + after},
		"state and snapshot": {"app=" + before, snapAfter},
	} {
		got, err := runCLI(t, "", append([]string{"diff"}, args...)...)
		if err != nil || got != want {
			t.Errorf("%s (err %v):\n%s\nwant\n%s", name, err, got, want)
		}
	}
	for name, args := range map[string][]string{
		"a state and itself":       {before, before},
		"a state and its snapshot": {"app=" + before, snapBefore},
	} {
		if got, err := runCLI(t, "", append([]string{"diff"}, args...)...); err != nil || got != "No differences\n" {
			t.Errorf("%s (err %v):\n%s", name, err, got)
		}
	}

	// What a cloud listing found that no state holds is a difference.
	out, err = runCLI(t, "", "diff", "net="+found+"managed.tfstate", collect(t, "net="+found+"managed.tfstate", "cloud="+found+"query.jsonl"))
	if err != nil || !strings.Contains(out, "+ cloud::aws_subnet.") {
		t.Errorf("listing against state (err %v):\n%s", err, out)
	}

	_, err = runCLI(t, "", "diff", collect(t, "a="+before, "b="+fixtures+"raw.tfstate"), collect(t, "c="+after, "d="+fixtures+"raw.tfstate"))
	if err == nil || !strings.Contains(err.Error(), "a, b") || !strings.Contains(err.Error(), "c, d") {
		t.Errorf("sides with no source in common: error should name the sources, got %v", err)
	}
	for name, args := range map[string][]string{
		"one input":      {before},
		"three inputs":   {before, after, after},
		"unknown format": {"-format", "dot", before, after},
		"unknown flag":   {"-depth", "2", before, after},
		"missing file":   {before, "does-not-exist.tfstate"},
	} {
		if _, err := runCLI(t, "", append([]string{"diff"}, args...)...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestErrors(t *testing.T) {
	cases := map[string][]string{
		"no command":       {},
		"unknown command":  {"explore"},
		"no state":         {"graph"},
		"unknown format":   {"graph", "-format", "dot", fixtures + "raw.tfstate"},
		"unknown flag":     {"graph", "-nope", fixtures + "raw.tfstate"},
		"missing file":     {"graph", "does-not-exist.tfstate"},
		"same state twice": {"graph", fixtures + "raw.tfstate", fixtures + "raw.tfstate"},
		"same name twice":  {"graph", "a=" + fixtures + "raw.tfstate", "a=" + fixtures + "show.json"},
		"not a state":      {"graph", "main.go"},
		"unknown grouping": {"view", "-group-by", "zone", fixtures + "raw.tfstate"},
		"around nothing":   {"view", "-around", "aws_vpc.nope", fixtures + "raw.tfstate"},
		"around ambiguous": {"view", "-around", "aws_vpc.main", "a=" + fixtures + "raw.tfstate", "b=" + fixtures + "show.json"},
		"negative depth":   {"view", "-depth", "-1", fixtures + "raw.tfstate"},
		"graph-only flag":  {"view", "-attributes", fixtures + "raw.tfstate"},
		"view-only flag":   {"graph", "-depth", "2", fixtures + "raw.tfstate"},
		"collect format":   {"collect", "-format", "json", fixtures + "raw.tfstate"},
		"collect nothing":  {"collect"},
	}
	for name, args := range cases {
		if _, err := runCLI(t, "", args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	_, err := runCLI(t, "", "graph", fixtures+"encrypted.tfstate")
	if err == nil || !strings.Contains(err.Error(), "tofu show -json") {
		t.Errorf("encrypted state: error should say how to decrypt, got %v", err)
	}
}

// Azure listings are told apart from states and from AWS listings, and
// what they find beyond the state shows as unmanaged, in its resource
// group, whichever of the three commands made the listing.
func TestAzureListings(t *testing.T) {
	for _, file := range []string{"az-resource-list.json", "az-graph-query.json"} {
		out, err := runCLI(t, "", "view", "-status", "unmanaged", "-group-by", "resource_group", "az="+fixtures+"azure.tfstate", "cloud="+found+file)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"4 resources in", "rg-sandbox  (resource group, 3 resources, 3 unmanaged)", "azurerm_storage_account.stmanual01  (unmanaged)", "azurerm_public_ip.pip-test  (unmanaged)"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: missing %q in:\n%s", file, want, out)
			}
		}
		// What the state holds is not listed again, however its ID is spelled.
		if strings.Contains(out, "vnet-app") || strings.Contains(out, "vm-web") {
			t.Errorf("%s: a managed resource shows as unmanaged:\n%s", file, out)
		}
	}
	// Compressed and from standard input, like any input.
	data, err := os.ReadFile(found + "az-group-list.json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, string(data), "graph", "cloud=-")
	if err != nil || !strings.Contains(out, "azurerm_resource_group.rg-sandbox") || strings.Contains(out, "NetworkWatcherRG") {
		t.Errorf("group list on standard input (err %v):\n%s", err, out)
	}
}

func TestQueryFileForAzure(t *testing.T) {
	const azure = `{"format_version":"1.0","provider_schemas":{"registry.terraform.io/hashicorp/azurerm":{"list_resource_schemas":{
	  "azurerm_storage_account":{"block":{"attributes":{"resource_group_name":{"type":"string","optional":true},"subscription_id":{"type":"string","optional":true}}}},
	  "azurerm_subnet":{"block":{"attributes":{"virtual_network_id":{"type":"string","required":true}}}}}}}}`
	out, err := runCLI(t, azure, "query-file", "-subscription", "11111111-2222-3333-4444-555555555555,aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "-region", "eu-west-1", "-")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		// A block name cannot start with a digit, as a subscription ID can.
		`list "azurerm_storage_account" "sub_11111111_2222_3333_4444_555555555555"`,
		`subscription_id = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"`,
		"azurerm_subnet", // Named as left out: it needs its network.
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "\nlist ") != 2 || strings.Contains(out, "region") {
		t.Errorf("want one block per subscription and no region:\n%s", out)
	}
	// Without subscriptions, once, in the provider's own.
	out, err = runCLI(t, azure, "query-file", "-")
	if err != nil || strings.Count(out, "\nlist ") != 1 || strings.Contains(out, "config {") {
		t.Errorf("no subscriptions: %v\n%s", err, out)
	}
}
