package azureid

import "testing"

const (
	network = "/subscriptions/1111-22/resourceGroups/rg-app/providers/Microsoft.Network/virtualNetworks/vnet"
	subnet  = network + "/subnets/web"
)

func TestCanonical(t *testing.T) {
	other := "/SUBSCRIPTIONS/1111-22/resourcegroups/RG-APP/providers/microsoft.network/virtualnetworks/VNET"
	if Canonical(network) != Canonical(other) {
		t.Error("two spellings of one ID have different canonical forms")
	}
	for _, s := range []string{"vpc-0A1b", "arn:aws:iam::123456789012:role/App", "", "/subscriptions/", "https://vault.azure.net/keys/K"} {
		if Is(s) || Canonical(s) != s {
			t.Errorf("%q is not an Azure ID and must be left alone", s)
		}
	}
}

func TestPart(t *testing.T) {
	cases := []struct{ id, after, want string }{
		{subnet, "subscriptions", "1111-22"},
		{subnet, "resourceGroups", "rg-app"},
		{"/subscriptions/1111-22/resourcegroups/rg-app", "resourceGroups", "rg-app"},
		{subnet, "subnets", "web"},
		{"/subscriptions/1111-22", "resourceGroups", ""},
		{"https://vault.azure.net/resourceGroups/x", "resourceGroups", ""},
	}
	for _, c := range cases {
		if got := Part(c.id, c.after); got != c.want {
			t.Errorf("Part(%q, %q) = %q, want %q", c.id, c.after, got, c.want)
		}
	}
}

func TestParent(t *testing.T) {
	cases := []struct {
		id, want string
		ok       bool
	}{
		{subnet, network, true},
		{subnet + "/providers/Microsoft.Authorization/locks/keep", "", false}, // Not a part: something attached.
		{network, "", false},
		{"/subscriptions/1111-22/resourceGroups/rg-app", "", false},
		{"/subscriptions/1111-22/resourceGroups/rg/providers/Microsoft.Sql/servers/s/databases/d", "/subscriptions/1111-22/resourceGroups/rg/providers/Microsoft.Sql/servers/s", true},
		{"/subscriptions/1111-22/resourceGroups/rg/providers/Microsoft.Sql/servers/s/databases/d/extra", "", false},
		{"vpc-1", "", false},
	}
	for _, c := range cases {
		got, ok := Parent(c.id)
		if got != c.want || ok != c.ok {
			t.Errorf("Parent(%q) = %q, %v; want %q, %v", c.id, got, ok, c.want, c.ok)
		}
	}
}
