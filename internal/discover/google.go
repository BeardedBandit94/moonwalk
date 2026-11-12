package discover

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/googleid"
	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// googleAsset is one entry of what Google Cloud's asset inventory lists,
// for those without `terraform query`:
//
//	gcloud asset search-all-resources --scope=projects/<project> --format=json
type googleAsset struct {
	// Name is the resource's full name: "//" and its service, then its
	// own name, which for most resources starts with its project.
	Name        string `json:"name"`
	AssetType   string `json:"assetType"`
	DisplayName string `json:"displayName"`
	Location    string `json:"location"`
	// Project is the project by number, "projects/123456789012", where
	// the name usually has it by ID.
	Project string `json:"project"`
	// AdditionalAttributes holds a few more facts for some types, such
	// as a service account's email.
	AdditionalAttributes map[string]any `json:"additionalAttributes"`
}

// IsGoogleListing reports whether data looks like the output of the gcloud
// command that LoadGoogle reads. It looks at the first asset only.
func IsGoogleListing(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	if first, err := dec.Token(); err != nil || first != json.Delim('[') {
		return false
	}
	var a googleAsset
	return dec.More() && dec.Decode(&a) == nil && strings.HasPrefix(a.Name, "//") && strings.Contains(a.AssetType, ".googleapis.com/")
}

// numbered is a name that has its project by number.
var numbered = regexp.MustCompile(`^projects/(\d+)(/|$)`)

// LoadGoogle reads a listing of Google Cloud assets from r and joins it to
// g under the given source name, as Load does for query output. A listed
// resource is matched to a resource in state by its name, whichever way
// each side writes it. Nothing is known about a listed resource but its
// name, type and location, so it cannot be placed in a network.
func LoadGoogle(g *graph.Graph, source string, r io.Reader) (Result, error) {
	var res Result
	if source == "" {
		return res, fmt.Errorf("discover: source name is empty")
	}
	var listing []googleAsset
	if err := json.NewDecoder(r).Decode(&listing); err != nil {
		return res, fmt.Errorf("discover: %s: not a gcloud asset listing: %w", source, err)
	}

	// Some services name a resource by its project's number where state
	// has the project's ID. The listing itself says which number goes
	// with which ID: every asset carries the number, and most names the ID.
	ids := map[string]string{}
	for _, a := range listing {
		number := strings.TrimPrefix(a.Project, "projects/")
		if id := googleid.Part(a.Name, "projects"); id != "" && id != number && number != "" {
			ids[number] = id
		}
	}
	// name returns an asset's name as state would write it.
	name := func(a googleAsset) string {
		_, rest, _ := strings.Cut(strings.TrimPrefix(a.Name, "//"), "/")
		if m := numbered.FindStringSubmatch(rest); m != nil && ids[m[1]] != "" {
			rest = "projects/" + ids[m[1]] + rest[len("projects/"+m[1]):]
		}
		return rest
	}

	inState := map[string]bool{}
	for n := range g.AllNodes() {
		if n.Status == graph.StatusDeclared || n.Status == graph.StatusUnmanaged || !strings.HasPrefix(n.Type, "google_") {
			continue
		}
		inState[googleid.Canonical(n.CloudID)] = true
		if id, _ := n.Attributes["id"].(string); id != "" {
			inState[googleid.Canonical(id)] = true
		}
		// The inventory names a service account by its number.
		if unique, _ := n.Attributes["unique_id"].(string); unique != "" && n.Account != "" {
			inState["projects/"+n.Account+"/serviceAccounts/"+unique] = true
		}
	}
	delete(inState, "")

	seen := map[string]bool{}
	for _, a := range listing {
		if !strings.HasPrefix(a.Name, "//") || seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		res.Found++
		key := name(a)
		switch {
		case madeByGoogle(a, key):
			res.Defaults++
			continue
		case inState[key]:
			res.Matched++
			continue
		}
		account := googleid.Part(key, "projects")
		if account == "" {
			number := strings.TrimPrefix(a.Project, "projects/")
			if account = ids[number]; account == "" {
				account = number
			}
		}
		region := a.Location
		switch {
		case region == "global":
			region = graph.RegionGlobal
		case zone.MatchString(region):
			region = region[:strings.LastIndex(region, "-")]
		}
		resourceType := googleType(a.AssetType)
		label := googleid.Name(key)
		address := resourceType + "." + label
		n := graph.Node{
			// A name repeats across projects and places.
			ID:            source + "::" + address + "@" + account + "/" + a.Location,
			Source:        source,
			Address:       address,
			ConfigAddress: address,
			Mode:          graph.ModeManaged,
			Type:          resourceType,
			Name:          a.DisplayName,
			Status:        graph.StatusUnmanaged,
			Account:       account,
			Region:        region,
			CloudID:       key,
		}
		if n.Name == "" {
			n.Name = label
		}
		if err := g.AddNode(n); err != nil {
			return res, fmt.Errorf("discover: %s: %w", source, err)
		}
		res.Unmanaged++
	}
	return res, nil
}

// zone is the form of a zone's name: a region's and a letter.
var zone = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+-[a-z]$`)

// madeByGoogle reports whether a listed resource is one that Google makes
// by itself in every project, which nobody would expect to find in code:
// the default network with its subnetworks, firewall rules and routes,
// and the service accounts of Google's own services.
func madeByGoogle(a googleAsset, name string) bool {
	last := googleid.Name(name)
	switch a.AssetType {
	case "compute.googleapis.com/Network", "compute.googleapis.com/Subnetwork":
		return last == "default"
	case "compute.googleapis.com/Firewall":
		return strings.HasPrefix(last, "default-allow-")
	case "compute.googleapis.com/Route":
		return strings.HasPrefix(last, "default-route-")
	case "iam.googleapis.com/ServiceAccount":
		// The inventory names an account by number; its email is beside it.
		email, _ := a.AdditionalAttributes["email"].(string)
		if strings.Contains(last, "@") {
			email = last
		}
		if a.DisplayName == "Compute Engine default service account" || a.DisplayName == "App Engine default service account" {
			return true
		}
		return strings.HasSuffix(email, "-compute@developer.gserviceaccount.com") ||
			strings.HasSuffix(email, "@appspot.gserviceaccount.com") ||
			strings.HasSuffix(email, "@cloudservices.gserviceaccount.com") ||
			strings.Contains(email, "@gcp-sa-")
	}
	return false
}

// googleType returns the Terraform resource type for an asset type, where
// it is one of the common ones, so that filters and groupings treat it
// like its managed neighbours. Otherwise it returns a name made from the
// asset type, such as "google:workflows.googleapis.com/Workflow".
func googleType(assetType string) string {
	if t, ok := googleTypes[assetType]; ok {
		return t
	}
	return "google:" + assetType
}

var googleTypes = map[string]string{
	"artifactregistry.googleapis.com/Repository": "google_artifact_registry_repository",
	"bigquery.googleapis.com/Dataset":            "google_bigquery_dataset",
	"bigquery.googleapis.com/Table":              "google_bigquery_table",
	"cloudfunctions.googleapis.com/Function":     "google_cloudfunctions2_function",
	"cloudkms.googleapis.com/CryptoKey":          "google_kms_crypto_key",
	"cloudkms.googleapis.com/KeyRing":            "google_kms_key_ring",
	"compute.googleapis.com/Address":             "google_compute_address",
	"compute.googleapis.com/Disk":                "google_compute_disk",
	"compute.googleapis.com/Firewall":            "google_compute_firewall",
	"compute.googleapis.com/ForwardingRule":      "google_compute_forwarding_rule",
	"compute.googleapis.com/Instance":            "google_compute_instance",
	"compute.googleapis.com/InstanceTemplate":    "google_compute_instance_template",
	"compute.googleapis.com/Network":             "google_compute_network",
	"compute.googleapis.com/Route":               "google_compute_route",
	"compute.googleapis.com/Router":              "google_compute_router",
	"compute.googleapis.com/Subnetwork":          "google_compute_subnetwork",
	"container.googleapis.com/Cluster":           "google_container_cluster",
	"dns.googleapis.com/ManagedZone":             "google_dns_managed_zone",
	"iam.googleapis.com/ServiceAccount":          "google_service_account",
	"pubsub.googleapis.com/Subscription":         "google_pubsub_subscription",
	"pubsub.googleapis.com/Topic":                "google_pubsub_topic",
	"redis.googleapis.com/Instance":              "google_redis_instance",
	"run.googleapis.com/Service":                 "google_cloud_run_v2_service",
	"secretmanager.googleapis.com/Secret":        "google_secret_manager_secret",
	"sqladmin.googleapis.com/Instance":           "google_sql_database_instance",
	"storage.googleapis.com/Bucket":              "google_storage_bucket",
}
