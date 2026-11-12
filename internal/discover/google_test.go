package discover

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

// googleState loads the Google Cloud state fixture.
func googleState(t *testing.T) *graph.Graph {
	t.Helper()
	f, err := os.Open("../tfstate/testdata/google.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g := graph.New()
	if err := tfstate.Load(g, "g", f); err != nil {
		t.Fatal(err)
	}
	return g
}

func loadGoogle(t *testing.T, g *graph.Graph) Result {
	t.Helper()
	f, err := os.Open("testdata/gcloud-assets.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := LoadGoogle(g, "cloud", f)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// placed lists the unmanaged resources as "type name @account/region".
func placed(g *graph.Graph) []string {
	var out []string
	for _, n := range g.Nodes() {
		if n.Status == graph.StatusUnmanaged {
			out = append(out, n.Type+" "+n.Name+" @"+n.Account+"/"+n.Region)
		}
	}
	return out
}

func TestLoadGoogleAssets(t *testing.T) {
	g := googleState(t)
	res := loadGoogle(t, g)
	// Fourteen assets, one of them listed twice. Five are in the state:
	// one named there by a self link, one by a plain ID, and a service
	// account that the inventory names by number. Four are Google's own.
	want := Result{Found: 14, Matched: 5, Unmanaged: 5, Defaults: 4}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	got := placed(g)
	wantNodes := []string{
		// A type with no Terraform name of its own keeps Google's.
		"google:workflows.googleapis.com/Workflow nightly @acme-prod/europe-west1",
		// A zone is in its region.
		"google_compute_instance scratch @acme-prod/europe-west1",
		"google_pubsub_topic manual @acme-prod/global",
		// Named by the project's number; the rest of the listing says
		// which project that is.
		"google_secret_manager_secret api-key @acme-prod/global",
		// A bucket's name says nothing of its project.
		"google_storage_bucket acme-scratch-data @acme-prod/us",
	}
	if !reflect.DeepEqual(got, wantNodes) {
		t.Errorf("unmanaged:\n  got  %s\n  want %s", strings.Join(got, "\n       "), strings.Join(wantNodes, "\n       "))
	}
	n, ok := g.Node("cloud::google_secret_manager_secret.api-key@acme-prod/global")
	if !ok || n.CloudID != "projects/acme-prod/secrets/api-key" {
		t.Errorf("the secret = %+v, %v", n, ok)
	}
}

// Without a state, everything listed that Google did not make is unmanaged.
func TestLoadGoogleAssetsWithoutState(t *testing.T) {
	g := graph.New()
	res := loadGoogle(t, g)
	if res.Matched != 0 || res.Unmanaged != 10 || res.Defaults != 4 {
		t.Errorf("result = %+v", res)
	}
}

func TestIsGoogleListing(t *testing.T) {
	read := func(path string) []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	data := read("testdata/gcloud-assets.json")
	if !IsGoogleListing(data) || IsAzureListing(data) || IsARNListing(data) || IsQueryOutput(data) {
		t.Error("the asset listing is not recognised, or is taken for another kind")
	}
	for name, other := range map[string][]byte{
		"a state":             read("../tfstate/testdata/google.tfstate"),
		"an Azure listing":    read("testdata/az-resource-list.json"),
		"an AWS listing":      read("testdata/tagging-api.json"),
		"query output":        read("testdata/query.jsonl"),
		"an empty list":       []byte(`[]`),
		"names without types": []byte(`[{"name": "//compute.googleapis.com/projects/p/global/networks/n"}]`),
		"not JSON":            []byte(`//compute.googleapis.com/`),
	} {
		if IsGoogleListing(other) {
			t.Errorf("%s is taken for a gcloud asset listing", name)
		}
	}
	if _, err := LoadGoogle(graph.New(), "", strings.NewReader(`[]`)); err == nil {
		t.Error("empty source name: no error")
	}
	if _, err := LoadGoogle(graph.New(), "cloud", strings.NewReader(`{"not": "a list"}`)); err == nil {
		t.Error("not a listing: no error")
	}
}

// Two machines of one name in two zones of a region, found by `terraform
// query`, are two resources.
func TestQueryOutputKeepsGoogleZonesApart(t *testing.T) {
	g := graph.New()
	input := line("google_compute_instance", "vm", map[string]any{"project": "acme", "zone": "europe-west1-b", "name": "vm"}, nil) +
		line("google_compute_instance", "vm", map[string]any{"project": "acme", "zone": "europe-west1-c", "name": "vm"}, nil)
	res, err := Load(g, "cloud", strings.NewReader(input))
	if err != nil || res.Unmanaged != 2 {
		t.Fatalf("result %+v, err %v", res, err)
	}
}
