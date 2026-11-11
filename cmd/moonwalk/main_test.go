package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files with current output")

const fixtures = "../../internal/tfstate/testdata/"

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
		{"network-view-filter.golden", []string{"view", "-format", "json", "-type", "aws_iam_*,aws_vpc", "-region", "global", "net=" + fixtures + "network.tfstate"}},
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
