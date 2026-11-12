package azurerbac

import (
	"reflect"
	"strings"
	"testing"
)

const subscription = "11111111-2222-3333-4444-555555555555"

// small is a made-up set of permissions that shows every case.
func small() *Permissions {
	return &Permissions{
		Provider: "test",
		Types: map[string]TypePermissions{
			"azurerm_virtual_network": {List: []string{"Microsoft.Network/virtualNetworks/read"}},
			"azurerm_private_endpoint": {List: []string{"Microsoft.Network/privateEndpoints/read"},
				Read: []string{"Microsoft.Network/privateEndpoints/privateDnsZoneGroups/read"}},
			"azurerm_storage_account": {List: []string{"Microsoft.Storage/storageAccounts/read"},
				Read:   []string{"Microsoft.Storage/storageAccounts/blobServices/read"},
				Beyond: "Microsoft.Storage/storageAccounts/listkeys/action, which asks for the resource's keys"},
			"azurerm_servicebus_namespace": {List: []string{"Microsoft.ServiceBus/namespaces/read"},
				ListBeyond: "Microsoft.ServiceBus/namespaces/authorizationRules/listkeys/action, which asks for the resource's keys"},
		},
	}
}

var all = []string{"azurerm_new_thing", "azurerm_private_endpoint", "azurerm_servicebus_namespace", "azurerm_storage_account", "azurerm_virtual_network"}

func TestRole(t *testing.T) {
	role, plan, err := small().Role(Options{Types: all, Subscriptions: []string{subscription, strings.ToUpper(subscription)}})
	if err != nil {
		t.Fatal(err)
	}
	want := Role{
		Name:     "moonwalk discovery",
		IsCustom: true,
		Description: "Lets moonwalk list 3 kinds of resource through Terraform. " +
			"Read-only: every permission is a read named in full, with no data actions.",
		Actions: []string{
			"Microsoft.Network/privateEndpoints/privateDnsZoneGroups/read",
			"Microsoft.Network/privateEndpoints/read",
			"Microsoft.Network/virtualNetworks/read",
			// Found, and no more: its attributes take its keys.
			"Microsoft.Storage/storageAccounts/read",
		},
		NotActions:     []string{},
		DataActions:    []string{},
		NotDataActions: []string{},
		// A subscription named twice, in whatever case, is one scope.
		AssignableScopes: []string{"/subscriptions/" + subscription},
	}
	if !reflect.DeepEqual(role, want) {
		t.Errorf("role = %+v", role)
	}
	wantPlan := Plan{
		Full:            []string{"azurerm_private_endpoint", "azurerm_virtual_network"},
		Identity:        []string{"azurerm_storage_account"},
		IdentityBecause: map[string]string{"azurerm_storage_account": "Microsoft.Storage/storageAccounts/listkeys/action, which asks for the resource's keys"},
		LeftOut:         map[string]string{"azurerm_servicebus_namespace": "Microsoft.ServiceBus/namespaces/authorizationRules/listkeys/action, which asks for the resource's keys"},
		Unknown:         []string{"azurerm_new_thing"},
	}
	if !reflect.DeepEqual(plan, wantPlan) {
		t.Errorf("plan = %+v", plan)
	}
}

func TestRoleForIdentitiesOnly(t *testing.T) {
	role, plan, err := small().Role(Options{Types: all, IdentityOnly: true, Subscriptions: []string{subscription}, Name: "listing"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Microsoft.Network/privateEndpoints/read", "Microsoft.Network/virtualNetworks/read", "Microsoft.Storage/storageAccounts/read"}
	if !reflect.DeepEqual(role.Actions, want) || role.Name != "listing" || !strings.Contains(role.Description, "by name and ID only") {
		t.Errorf("role = %+v", role)
	}
	if len(plan.Full) != 0 || len(plan.IdentityBecause) != 0 || len(plan.LeftOut) != 1 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestRoleErrors(t *testing.T) {
	p := small()
	cases := map[string]Options{
		"no subscription":            {Types: all},
		"a subscription that is not": {Types: all, Subscriptions: []string{"prod"}},
		"nothing recorded":           {Types: []string{"azurerm_new_thing"}, Subscriptions: []string{subscription}},
	}
	for name, o := range cases {
		if _, _, err := p.Role(o); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Whatever the data says, nothing but a read named in full gets into
	// a role.
	for _, bad := range []string{"Microsoft.Storage/storageAccounts/listkeys/action", "Microsoft.Storage/storageAccounts/write", "Microsoft.Storage/*/read", "*/read", "*"} {
		p.Types["azurerm_bad"] = TypePermissions{List: []string{bad}}
		if _, _, err := p.Role(Options{Types: []string{"azurerm_bad"}, Subscriptions: []string{subscription}}); err == nil {
			t.Errorf("%q was put in a role", bad)
		}
	}
}

// What ships with moonwalk keeps to the rules: every permission a read
// named in full, none of them one that hands over keys.
func TestShippedPermissionsKeepToTheRules(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Types) < 50 || p.Provider == "" || p.Catalog == "" {
		t.Fatalf("%d types from %q, checked against %q", len(p.Types), p.Provider, p.Catalog)
	}
	var types []string
	for name, tp := range p.Types {
		types = append(types, name)
		if len(tp.List) == 0 {
			t.Errorf("%s has no permission to list with", name)
		}
		for _, a := range append(append([]string{}, tp.List...), tp.Read...) {
			lower := strings.ToLower(a)
			if !readOnly.MatchString(a) || strings.Contains(a, "*") {
				t.Errorf("%s: %q is not a read named in full", name, a)
			}
			if strings.Contains(lower, "key") && !strings.Contains(lower, "keyvault") || strings.Contains(lower, "secret") || strings.Contains(lower, "registrationinformation") {
				t.Errorf("%s: %s looks like a read of keys or secrets", name, a)
			}
		}
	}
	role, plan, err := p.Role(Options{Types: types, Subscriptions: []string{subscription}})
	if err != nil {
		t.Fatal(err)
	}
	if len(role.NotActions)+len(role.DataActions)+len(role.NotDataActions) != 0 || len(role.Actions) < 50 {
		t.Errorf("role = %+v", role)
	}
	// The types known to take keys for their attributes are listed
	// without them.
	for _, name := range []string{"azurerm_storage_account", "azurerm_key_vault", "azurerm_cognitive_account"} {
		if plan.IdentityBecause[name] == "" {
			t.Errorf("%s is listed with its attributes", name)
		}
	}
}
