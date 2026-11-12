package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// gcpExample is where the role for everything moonwalk can list in Google
// Cloud is kept, to be read without running anything.
const gcpExample = "../../docs/gcp-iam/moonwalk-discovery.yaml"

// The example role is what the command writes today. Run the tests with
// -update after the permissions change, and read the difference.
func TestExampleGCPRole(t *testing.T) {
	got, err := runCLI(t, "", "gcp-role")
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(gcpExample, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(gcpExample)
	if err != nil || got != string(want) {
		t.Errorf("%s differs from what the command writes (run `go test ./cmd/moonwalk -update`, and read the difference)", gcpExample)
	}
	// With identities only it is the same role: for Google Cloud, the
	// listing already holds the attributes.
	identities, err := runCLI(t, "", "gcp-role", "-identity-only")
	if err != nil || strings.Join(strings.Split(identities, "\n")[2:], "\n") != strings.Join(strings.Split(got, "\n")[2:], "\n") {
		t.Errorf("identities only (err %v) differs in more than its description", err)
	}
}

// The rules every role moonwalk writes keeps to, checked on the example.
func TestExampleGCPRoleKeepsToTheRules(t *testing.T) {
	data, err := os.ReadFile(gcpExample)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if !strings.HasPrefix(lines[0], `title: "`) || !strings.HasPrefix(lines[1], `description: "`) || lines[2] != "stage: GA" || lines[3] != "includedPermissions:" {
		t.Fatalf("the file does not start as a role definition:\n%s", strings.Join(lines[:4], "\n"))
	}
	if len(lines[1]) > len(`description: ""`)+300 {
		t.Errorf("the description is over 300 bytes")
	}
	read := regexp.MustCompile(`^- [a-z][a-z0-9]*\.[A-Za-z][A-Za-z0-9]*\.(list|get)$`)
	for _, line := range lines[4:] {
		if !read.MatchString(line) {
			t.Errorf("%q is not a list or a get named in full", line)
		}
	}
	if n := len(lines) - 4; n < 50 || n > 3000 {
		t.Errorf("%d permissions", n)
	}
}

const googleSchema = `{"format_version":"1.0","provider_schemas":{"registry.terraform.io/hashicorp/google":{"list_resource_schemas":{
  "google_compute_subnetwork":{"block":{"attributes":{"project":{"type":"string","optional":true},"region":{"type":"string","optional":true}}}},
  "google_compute_instance":{"block":{"attributes":{"project":{"type":"string","optional":true},"zone":{"type":"string","optional":true}}}},
  "google_storage_bucket":{"block":{"attributes":{"project":{"type":"string","optional":true}}}},
  "google_cloud_scheduler_job":{"block":{"attributes":{"project":{"type":"string","optional":true},"location":{"type":"string","optional":true}}}},
  "google_compute_region_backend_service":{"block":{"attributes":{"project":{"type":"string","optional":true},"region":{"type":"string","required":true}}}}}}}}`

func TestGCPRoleCommand(t *testing.T) {
	out, _, err := runCLIWithWarnings(t, googleSchema, "gcp-role", "-")
	if err != nil {
		t.Fatal(err)
	}
	// The backend service needs a region named, so the query file leaves
	// it out, and so does the role.
	for _, want := range []string{"- compute.subnetworks.list\n", "- compute.instances.list\n", "- storage.buckets.list\n", "- cloudscheduler.jobs.list\n", "stage: GA\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", strings.TrimSpace(want), out)
		}
	}
	if strings.Contains(out, "backendServices") || strings.Count(out, "\n- ") != 4 {
		t.Errorf("the role holds more than the four types take:\n%s", out)
	}

	out, warnings, err := runCLIWithWarnings(t, "", "gcp-role", "-type", "google_pubsub_*", "-title", "pubsub listing")
	if err != nil || !strings.Contains(out, `title: "pubsub listing"`) || !strings.Contains(out, "- pubsub.topics.list") || strings.Contains(out, "compute.") || !strings.Contains(warnings, "no provider schema was given") {
		t.Errorf("without a schema (err %v, warnings %q):\n%s", err, warnings, out)
	}

	for name, args := range map[string][]string{
		"no type that matches": {"gcp-role", "-type", "aws_*"},
		"two schemas":          {"gcp-role", "a.json", "b.json"},
		"unknown flag":         {"gcp-role", "-subscription", "x"},
	} {
		if _, err := runCLI(t, "", args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A Google list block covers one project and one region, zone or
// location. Given several of each, there is a block for every pair.
func TestQueryFileForGoogle(t *testing.T) {
	out, err := runCLI(t, googleSchema, "query-file", "-project", "acme-prod,acme-dev", "-region", "europe-west1", "-zone", "europe-west1-b,europe-west1-c", "-")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`list "google_compute_subnetwork" "acme_prod_europe_west1"`,
		`list "google_compute_subnetwork" "acme_dev_europe_west1"`,
		// By zone, where the type is listed by zone.
		`list "google_compute_instance" "acme_prod_europe_west1_b"`,
		`list "google_compute_instance" "acme_dev_europe_west1_c"`,
		// A bucket is listed by project alone.
		`list "google_storage_bucket" "acme_prod"`,
		// What Google calls a location is, here, a region.
		`list "google_cloud_scheduler_job" "acme_prod_europe_west1"`,
		"    project  = \"acme-prod\"\n    location = \"europe-west1\"\n",
		"    project = \"acme-dev\"\n    zone    = \"europe-west1-c\"\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "\nlist "); n != 2+4+2+2 {
		t.Errorf("%d list blocks, want 10", n)
	}
	// Without any of them, each type once, wherever the provider is set.
	out, err = runCLI(t, googleSchema, "query-file", "-")
	if err != nil || strings.Count(out, "\nlist ") != 4 || strings.Contains(out, "config {") {
		t.Errorf("no places: %v\n%s", err, out)
	}
}

// Google words a refusal two ways; the permission is named in both.
func TestRefusedGooglePermissionsAreNamed(t *testing.T) {
	listing := `{"@level":"info","type":"version","terraform":"1.14.0"}` + "\n" +
		`{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"API Error","detail":"Error when reading list: googleapi: Error 403: Required 'compute.subnetworks.list' permission for 'projects/acme-prod', forbidden"}}` + "\n" +
		`{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"API Error","detail":"googleapi: Error 403: Permission 'pubsub.topics.list' denied on resource '//cloudresourcemanager.googleapis.com/projects/acme-prod' (or it may not exist)."}}` + "\n"
	_, warnings, err := runCLIWithWarnings(t, listing, "graph", "cloud=-")
	if err != nil || !strings.Contains(warnings, "were refused 2 actions: compute.subnetworks.list, pubsub.topics.list.") {
		t.Errorf("err %v, warnings %q", err, warnings)
	}
}
