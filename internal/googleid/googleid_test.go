package googleid

import "testing"

const (
	link    = "https://www.googleapis.com/compute/v1/projects/acme/regions/europe-west1/subnetworks/web"
	id      = "projects/acme/regions/europe-west1/subnetworks/web"
	asset   = "//compute.googleapis.com/projects/acme/regions/europe-west1/subnetworks/web"
	network = "https://www.googleapis.com/compute/beta/projects/acme/global/networks/main"
)

func TestCanonical(t *testing.T) {
	for _, s := range []string{link, id, asset} {
		if got := Canonical(s); got != id {
			t.Errorf("Canonical(%q) = %q", s, got)
		}
		if !Is(s) {
			t.Errorf("%q is not taken for a name in a project", s)
		}
	}
	// What is not such a name is left alone.
	for _, s := range []string{"web", "", "vpc-0a1b2c3d", "arn:aws:iam::123456789012:role/app", "https://example.com/subprojects/x",
		"/subscriptions/1111/resourceGroups/rg", "//storage.googleapis.com/acme-logs"} {
		if Canonical(s) != s || Is(s) {
			t.Errorf("%q should be left alone", s)
		}
	}
}

func TestParts(t *testing.T) {
	cases := []struct{ s, after, want string }{
		{link, "projects", "acme"},
		{link, "regions", "europe-west1"},
		{asset, "subnetworks", "web"},
		{"projects/acme/zones/europe-west1-b/instances/vm", "zones", "europe-west1-b"},
		{network, "regions", ""},
		{"web", "projects", ""},
	}
	for _, c := range cases {
		if got := Part(c.s, c.after); got != c.want {
			t.Errorf("Part(%q, %q) = %q, want %q", c.s, c.after, got, c.want)
		}
	}
	if !Global(network) || Global(link) {
		t.Error("Global: the network is global and the subnetwork is not")
	}
	if Name(link) != "web" || Name("web") != "web" {
		t.Error("Name should give the last part")
	}
}
