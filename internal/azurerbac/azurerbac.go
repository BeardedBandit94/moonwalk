// Package azurerbac writes the Azure custom role that reading a
// subscription with moonwalk needs, and nothing beyond it.
//
// As for AWS (see internal/awsiam), what listing each resource type takes
// is recorded: the Terraform azurerm provider was run against a stand-in
// for Azure Resource Manager that notes every request, once for each type
// it can list, and each request was turned into the permission that
// authorizes it and checked against the permissions Microsoft publishes.
// The result is permissions.json; scripts/azure-permissions makes it
// again for a newer provider.
//
// The role keeps to these rules. Every permission is named in full: no
// wildcard. Every one ends in /read, which authorizes a GET and nothing
// else. There are no data actions, so nothing a service stores can be
// read. And the role can be assigned only at the subscriptions it names.
package azurerbac

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

//go:embed permissions.json
var embedded []byte

// Permissions says which Azure permissions listing each resource type takes.
type Permissions struct {
	// Provider is the provider and version the requests were recorded from.
	Provider string `json:"provider"`
	// Catalog is where the permissions were checked.
	Catalog string                     `json:"catalog"`
	Types   map[string]TypePermissions `json:"types"`
	// Unlisted holds the permissions in use that Microsoft's published
	// list does not have. The list trails the services; these are
	// spelled as the provider's requests spell them.
	Unlisted []string `json:"unlisted,omitempty"`
}

// TypePermissions is what one resource type takes.
type TypePermissions struct {
	// List holds the permissions that finding the resources takes.
	List []string `json:"list"`
	// Read holds the further permissions that reading each one's
	// attributes takes, which is what a list block with include_resource
	// does. For most types there are none: the listing already holds them.
	Read []string `json:"read,omitempty"`
	// Beyond says what reading the attributes takes beyond reading: a
	// request that hands over keys, or one to the service's own endpoint
	// for what it stores. No role from here allows that, so such a type is
	// listed without its attributes.
	Beyond string `json:"beyond,omitempty"`
	// ListBeyond says the same of finding the resources at all. Such a
	// type is left out.
	ListBeyond string `json:"list_beyond,omitempty"`
	// ListUnconfirmed and ReadUnconfirmed are true where the recording
	// did not run to the end, because the provider did not accept the
	// stand-in's made-up answer. A real subscription may show more.
	ListUnconfirmed bool `json:"list_unconfirmed,omitempty"`
	ReadUnconfirmed bool `json:"read_unconfirmed,omitempty"`
}

// Load returns the permissions that ship with moonwalk.
func Load() (*Permissions, error) {
	var p Permissions
	if err := json.Unmarshal(embedded, &p); err != nil {
		return nil, fmt.Errorf("azurerbac: %w", err)
	}
	return &p, nil
}

// Role is an Azure custom role definition, in the form that
// 'az role definition create --role-definition' takes.
type Role struct {
	Name             string   `json:"Name"`
	IsCustom         bool     `json:"IsCustom"`
	Description      string   `json:"Description"`
	Actions          []string `json:"Actions"`
	NotActions       []string `json:"NotActions"`
	DataActions      []string `json:"DataActions"`
	NotDataActions   []string `json:"NotDataActions"`
	AssignableScopes []string `json:"AssignableScopes"`
}

// Options says what the role is to cover.
type Options struct {
	// Types are the resource types to be listed.
	Types []string
	// IdentityOnly leaves out what reading each resource's attributes
	// takes, for list blocks without include_resource.
	IdentityOnly bool
	// Subscriptions are the IDs of the subscriptions the role may be
	// assigned at. There must be at least one.
	Subscriptions []string
	// Name is the role's name; "moonwalk discovery" if empty.
	Name string
}

// Plan says how each of the types asked for can be listed with a role
// that only reads. A query file and its role are made from the same plan.
type Plan struct {
	// Full are listed with their attributes, and Identity without, either
	// because that was asked for or because reading the attributes takes
	// more than reading, which IdentityBecause explains.
	Full, Identity  []string
	IdentityBecause map[string]string
	// LeftOut cannot be listed at all with reads alone, for the reason given.
	LeftOut map[string]string
	// Unknown are types nothing is recorded about. They are listed, and
	// the listing reports what it is refused.
	Unknown []string
}

// Plan works out how the types in o can be listed.
func (p *Permissions) Plan(o Options) Plan {
	plan := Plan{IdentityBecause: map[string]string{}, LeftOut: map[string]string{}}
	types := slices.Clone(o.Types)
	sort.Strings(types)
	for _, t := range slices.Compact(types) {
		tp, ok := p.Types[t]
		switch {
		case !ok:
			plan.Unknown = append(plan.Unknown, t)
		case tp.ListBeyond != "":
			plan.LeftOut[t] = tp.ListBeyond
		case o.IdentityOnly:
			plan.Identity = append(plan.Identity, t)
		case tp.Beyond != "":
			plan.Identity = append(plan.Identity, t)
			plan.IdentityBecause[t] = tp.Beyond
		default:
			plan.Full = append(plan.Full, t)
		}
	}
	return plan
}

// subscriptionID is the form of an Azure subscription ID.
var subscriptionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// readOnly is the form of a permission this package will put in a role:
// named in full, and a read.
var readOnly = regexp.MustCompile(`^[A-Za-z0-9.]+(/[A-Za-z0-9._@-]+)+/[Rr]ead$`)

// Role returns the custom role that listing the given types takes, and
// the plan it follows.
func (p *Permissions) Role(o Options) (Role, Plan, error) {
	plan := p.Plan(o)
	if len(o.Subscriptions) == 0 {
		return Role{}, plan, fmt.Errorf("a role is made for the subscriptions it may be assigned at: name at least one")
	}
	role := Role{
		Name:           o.Name,
		IsCustom:       true,
		Actions:        []string{},
		NotActions:     []string{},
		DataActions:    []string{},
		NotDataActions: []string{},
	}
	if role.Name == "" {
		role.Name = "moonwalk discovery"
	}
	for _, id := range o.Subscriptions {
		if !subscriptionID.MatchString(id) {
			return Role{}, plan, fmt.Errorf("%q is not a subscription ID (8-4-4-4-12 hexadecimal digits)", id)
		}
		if scope := "/subscriptions/" + strings.ToLower(id); !slices.Contains(role.AssignableScopes, scope) {
			role.AssignableScopes = append(role.AssignableScopes, scope)
		}
	}
	add := func(actions []string) error {
		for _, a := range actions {
			if !readOnly.MatchString(a) {
				return fmt.Errorf("azurerbac: %q is not a read named in full, and may not be in a role", a)
			}
			if !slices.Contains(role.Actions, a) {
				role.Actions = append(role.Actions, a)
			}
		}
		return nil
	}
	for _, t := range plan.Identity {
		if err := add(p.Types[t].List); err != nil {
			return Role{}, plan, err
		}
	}
	for _, t := range plan.Full {
		if err := add(append(slices.Clone(p.Types[t].List), p.Types[t].Read...)); err != nil {
			return Role{}, plan, err
		}
	}
	if len(role.Actions) == 0 {
		return Role{}, plan, fmt.Errorf("nothing is recorded about the permissions of the types chosen; they were recorded from %s", p.Provider)
	}
	sort.Slice(role.Actions, func(i, j int) bool { return strings.ToLower(role.Actions[i]) < strings.ToLower(role.Actions[j]) })
	what := ""
	if len(plan.Full) == 0 {
		what = ", by name and ID only"
	}
	role.Description = fmt.Sprintf("Lets moonwalk list %d kinds of resource through Terraform%s. Read-only: every permission is a read named in full, with no data actions.",
		len(plan.Full)+len(plan.Identity), what)
	return role, plan, nil
}
