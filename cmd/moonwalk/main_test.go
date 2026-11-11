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
	var out bytes.Buffer
	err := run(args, strings.NewReader(stdin), &out)
	return out.String(), err
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

func TestAttributesOmittedByDefault(t *testing.T) {
	out, err := runCLI(t, "", "graph", "-format", "json", fixtures+"secrets.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `"attributes"`) {
		t.Error("attributes printed without -attributes")
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
		"unknown command":  {"serve"},
		"no state":         {"graph"},
		"unknown format":   {"graph", "-format", "dot", fixtures + "raw.tfstate"},
		"unknown flag":     {"graph", "-nope", fixtures + "raw.tfstate"},
		"missing file":     {"graph", "does-not-exist.tfstate"},
		"same state twice": {"graph", fixtures + "raw.tfstate", fixtures + "raw.tfstate"},
		"same name twice":  {"graph", "a=" + fixtures + "raw.tfstate", "a=" + fixtures + "show.json"},
		"not a state":      {"graph", "main.go"},
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
