package gcprole

import (
	"reflect"
	"strings"
	"testing"
)

// small is a made-up set of permissions that shows every case.
func small() *Permissions {
	return &Permissions{
		Provider: "test",
		Types: map[string]TypePermissions{
			"google_compute_subnetwork": {List: []string{"compute.subnetworks.list"}},
			"google_compute_instance":   {List: []string{"compute.instances.list"}, Read: []string{"compute.instances.get"}},
			"google_secret_manager_secret": {List: []string{"secretmanager.secrets.list"},
				Beyond: "secretmanager.versions.access, which returns a secret's value"},
			"google_made_up_thing":  {List: []string{"madeup.things.list"}, ListBeyond: "madeup.things.export, which is more than a read"},
			"google_unplaced_thing": {List: []string{}, ListUnknown: "a permission that Google's published list does not name"},
		},
	}
}

var all = []string{"google_compute_instance", "google_compute_subnetwork", "google_made_up_thing", "google_new_thing", "google_secret_manager_secret", "google_unplaced_thing"}

func TestRole(t *testing.T) {
	role, plan, err := small().Role(Options{Types: all})
	if err != nil {
		t.Fatal(err)
	}
	want := Role{
		Title:       "moonwalk discovery",
		Description: "Lets moonwalk list 3 kinds of resource through Terraform. Read-only: every permission is a list or a get, named in full.",
		Stage:       "GA",
		IncludedPermissions: []string{
			"compute.instances.get",
			"compute.instances.list",
			"compute.subnetworks.list",
			// Found, and no more: its attributes take the secret itself.
			"secretmanager.secrets.list",
		},
	}
	if !reflect.DeepEqual(role, want) {
		t.Errorf("role = %+v", role)
	}
	wantPlan := Plan{
		Full:            []string{"google_compute_instance", "google_compute_subnetwork"},
		Identity:        []string{"google_secret_manager_secret"},
		IdentityBecause: map[string]string{"google_secret_manager_secret": "secretmanager.versions.access, which returns a secret's value"},
		LeftOut: map[string]string{
			"google_made_up_thing":  "madeup.things.export, which is more than a read",
			"google_unplaced_thing": "a permission that Google's published list does not name",
		},
		Unknown: []string{"google_new_thing"},
	}
	if !reflect.DeepEqual(plan, wantPlan) {
		t.Errorf("plan = %+v", plan)
	}
	// The file gcloud takes.
	wantYAML := `title: "moonwalk discovery"
description: "Lets moonwalk list 3 kinds of resource through Terraform. Read-only: every permission is a list or a get, named in full."
stage: GA
includedPermissions:
- compute.instances.get
- compute.instances.list
- compute.subnetworks.list
- secretmanager.secrets.list
`
	if role.YAML() != wantYAML {
		t.Errorf("YAML =\n%s", role.YAML())
	}
}

func TestRoleForIdentitiesOnly(t *testing.T) {
	role, plan, err := small().Role(Options{Types: all, IdentityOnly: true, Title: "listing"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"compute.instances.list", "compute.subnetworks.list", "secretmanager.secrets.list"}
	if !reflect.DeepEqual(role.IncludedPermissions, want) || role.Title != "listing" || !strings.Contains(role.Description, "by name only") {
		t.Errorf("role = %+v", role)
	}
	if len(plan.Full) != 0 || len(plan.IdentityBecause) != 0 || len(plan.LeftOut) != 2 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestRoleErrors(t *testing.T) {
	p := small()
	if _, _, err := p.Role(Options{Types: []string{"google_new_thing"}}); err == nil {
		t.Error("nothing recorded: no error")
	}
	if _, _, err := p.Role(Options{Types: all, Title: strings.Repeat("long ", 30)}); err == nil {
		t.Error("a title over 100 characters was accepted")
	}
	// Whatever the data says, nothing but a list or a get named in full
	// gets into a role.
	for _, bad := range []string{"secretmanager.versions.access", "compute.instances.setMetadata", "compute.instances.*", "compute.*", "*", "storage.objects.delete", "iam.serviceAccounts.getAccessToken"} {
		p.Types["google_bad"] = TypePermissions{List: []string{bad}}
		if _, _, err := p.Role(Options{Types: []string{"google_bad"}}); err == nil {
			t.Errorf("%q was put in a role", bad)
		}
	}
}

// What ships with moonwalk keeps to the rules: every permission a list or
// a get named in full, and none a read of what a service stores.
func TestShippedPermissionsKeepToTheRules(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Types) < 50 || p.Provider == "" || p.Catalog == "" {
		t.Fatalf("%d types from %q, checked against %q", len(p.Types), p.Provider, p.Catalog)
	}
	stored := []string{"secretmanager.versions.", "storage.objects.", "getSerialPortOutput", "getScreenshot", "getGuestAttributes", "serviceAccountKeys", "getAccessToken", "signBlob", "signJwt"}
	var types []string
	for name, tp := range p.Types {
		types = append(types, name)
		if (len(tp.List) == 0) != (tp.ListUnknown != "") {
			t.Errorf("%s: %d permissions to list with, and what it takes is unknown because %q", name, len(tp.List), tp.ListUnknown)
		}
		for _, a := range append(append([]string{}, tp.List...), tp.Read...) {
			if !readOnly.MatchString(a) {
				t.Errorf("%s: %q is not a list or a get named in full", name, a)
			}
			for _, s := range stored {
				if strings.Contains(a, s) {
					t.Errorf("%s: %s reads what a service stores, or its keys", name, a)
				}
			}
		}
	}
	role, _, err := p.Role(Options{Types: types})
	if err != nil {
		t.Fatal(err)
	}
	if len(role.IncludedPermissions) < 50 || role.Stage != "GA" {
		t.Errorf("role = %+v", role)
	}
}
