package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/azurerbac"
)

// azureExamples is where the roles for everything moonwalk can list in
// Azure are kept, to be read without running anything.
const azureExamples = "../../docs/azure-rbac/"

const exampleSubscription = "00000000-0000-0000-0000-000000000000"

// The example roles are what the command writes today. Run the tests with
// -update after the permissions change, and read the difference.
func TestExampleAzureRoles(t *testing.T) {
	for file, args := range map[string][]string{
		"with-attributes.json": {"-subscription", exampleSubscription},
		"identities-only.json": {"-subscription", exampleSubscription, "-identity-only", "-name", "moonwalk discovery (identities)"},
	} {
		got, err := runCLI(t, "", append([]string{"azure-role"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		if *update {
			if err := os.WriteFile(azureExamples+file, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(azureExamples + file)
		if err != nil || got != string(want) {
			t.Errorf("%s differs from what the command writes (run `go test ./cmd/moonwalk -update`, and read the difference)", file)
		}
	}
}

// The rules every role moonwalk writes keeps to, checked on the examples.
func TestExampleAzureRolesKeepToTheRules(t *testing.T) {
	// Azure compares these without regard to case, and Microsoft's own
	// list does not always capitalise them the same way.
	read := regexp.MustCompile(`(?i)^Microsoft\.[A-Za-z0-9.]+(/[A-Za-z0-9._@-]+)+/read$`)
	counts := map[string]int{}
	for _, file := range []string{"with-attributes.json", "identities-only.json"} {
		data, err := os.ReadFile(azureExamples + file)
		if err != nil {
			t.Fatal(err)
		}
		var role azurerbac.Role
		if err := json.Unmarshal(data, &role); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if !role.IsCustom || role.Name == "" || role.Description == "" {
			t.Errorf("%s: %+v", file, role)
		}
		if len(role.NotActions)+len(role.DataActions)+len(role.NotDataActions) != 0 {
			t.Errorf("%s has data actions or exclusions", file)
		}
		if len(role.AssignableScopes) != 1 || role.AssignableScopes[0] != "/subscriptions/"+exampleSubscription {
			t.Errorf("%s can be assigned at %v", file, role.AssignableScopes)
		}
		for _, a := range role.Actions {
			if !read.MatchString(a) {
				t.Errorf("%s: %q is not a read named in full", file, a)
			}
			if lower := strings.ToLower(a); strings.Contains(lower, "listkeys") || strings.Contains(lower, "sharedkeys") {
				t.Errorf("%s asks for keys with %s", file, a)
			}
		}
		counts[file] = len(role.Actions)
	}
	if counts["identities-only.json"] > counts["with-attributes.json"] || counts["identities-only.json"] < 50 {
		t.Errorf("permissions: %v", counts)
	}
}

const azureSchema = `{"format_version":"1.0","provider_schemas":{"registry.terraform.io/hashicorp/azurerm":{"list_resource_schemas":{
  "azurerm_virtual_network":{"block":{"attributes":{"resource_group_name":{"type":"string","required":true}}}},
  "azurerm_network_security_group":{"block":{"attributes":{"subscription_id":{"type":"string","optional":true}}}},
  "azurerm_storage_account":{"block":{"attributes":{"subscription_id":{"type":"string","optional":true}}}},
  "azurerm_servicebus_namespace":{"block":{"attributes":{"subscription_id":{"type":"string","optional":true}}}}}}}}`

func TestAzureRoleCommand(t *testing.T) {
	out, warnings, err := runCLIWithWarnings(t, azureSchema, "azure-role", "-subscription", exampleSubscription, "-")
	if err != nil {
		t.Fatal(err)
	}
	var role azurerbac.Role
	if err := json.Unmarshal([]byte(out), &role); err != nil {
		t.Fatalf("not a role definition: %v\n%s", err, out)
	}
	// The network needs a resource group named, so the query file leaves
	// it out, and so does the role. The namespace cannot be listed with
	// reads alone. The storage account is found, and no more.
	want := []string{"Microsoft.Network/networkSecurityGroups/read", "Microsoft.Storage/storageAccounts/read"}
	if strings.Join(role.Actions, ",") != strings.Join(want, ",") {
		t.Errorf("actions = %v", role.Actions)
	}
	for _, said := range []string{"left out", "azurerm_servicebus_namespace", "listed without attributes", "azurerm_storage_account", "keys"} {
		if !strings.Contains(warnings, said) {
			t.Errorf("warnings lack %q: %s", said, warnings)
		}
	}

	// The query file for the same schema asks for exactly that.
	query, err := runCLI(t, azureSchema, "query-file", "-")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, `list "azurerm_servicebus_namespace"`) || !strings.Contains(query, "azurerm_servicebus_namespace (") {
		t.Errorf("the namespace should be left out, and said to be:\n%s", query)
	}
	account := query[strings.Index(query, `list "azurerm_storage_account"`):]
	if strings.Contains(account[:strings.Index(account, "}")], "include_resource") {
		t.Errorf("the storage account should be listed by identity alone:\n%s", account)
	}
	group := query[strings.Index(query, `list "azurerm_network_security_group"`):]
	if !strings.Contains(group[:strings.Index(group, "}")], "include_resource = true") {
		t.Errorf("the security group should be listed with its attributes:\n%s", group)
	}

	// Without a schema: what the recorded provider can list.
	out, warnings, err = runCLIWithWarnings(t, "", "azure-role", "-subscription", exampleSubscription, "-type", "azurerm_network_*")
	if err != nil || !strings.Contains(out, `"Microsoft.Network/networkInterfaces/read"`) || strings.Contains(out, "Microsoft.Storage") || !strings.Contains(warnings, "no provider schema was given") {
		t.Errorf("without a schema (err %v, warnings %q):\n%s", err, warnings, out)
	}

	for name, args := range map[string][]string{
		"no subscription":            {"azure-role"},
		"a subscription that is not": {"azure-role", "-subscription", "prod"},
		"no type that matches":       {"azure-role", "-subscription", exampleSubscription, "-type", "aws_*"},
		"two schemas":                {"azure-role", "-subscription", exampleSubscription, "a.json", "b.json"},
		"unknown flag":               {"azure-role", "-allow", "x"},
	} {
		if _, err := runCLI(t, "", args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// Azure words a refusal its own way; the permission is named all the same.
func TestRefusedAzurePermissionsAreNamed(t *testing.T) {
	listing := `{"@level":"info","type":"version","terraform":"1.14.0"}` + "\n" +
		`{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"Error Listing Remote Resources","detail":"listing: unexpected status 403 (403 Forbidden) with error: AuthorizationFailed: The client 'aaaa' with object id 'bbbb' does not have authorization to perform action 'Microsoft.Network/natGateways/read' over scope '/subscriptions/00000000-0000-0000-0000-000000000000' or the scope is invalid."}}` + "\n"
	_, warnings, err := runCLIWithWarnings(t, listing, "graph", "cloud=-")
	if err != nil || !strings.Contains(warnings, "were refused 1 action: Microsoft.Network/natGateways/read.") {
		t.Errorf("err %v, warnings %q", err, warnings)
	}
}
