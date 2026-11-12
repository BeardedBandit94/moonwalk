// Package gcprole writes the Google Cloud custom role that reading a
// project with moonwalk needs, and nothing beyond it.
//
// As for AWS and Azure (see internal/awsiam and internal/azurerbac), what
// listing each resource type takes is recorded: the Terraform google
// provider was run against a stand-in for Google's APIs that notes every
// request, once for each type it can list, and each request was turned
// into the permission that authorizes it and checked against the
// permissions Google publishes. The result is permissions.json;
// scripts/google-permissions makes it again for a newer provider.
//
// The role keeps to these rules. Every permission is named in full. Every
// one is a list or a get, which authorize a GET and nothing else. And
// none is a read of what a service stores: no secret's value, no object.
package gcprole

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

// Permissions says which Google Cloud permissions listing each resource
// type takes.
type Permissions struct {
	// Provider is the provider and version the requests were recorded from.
	Provider string `json:"provider"`
	// Catalog is where the permissions were checked.
	Catalog string                     `json:"catalog"`
	Types   map[string]TypePermissions `json:"types"`
}

// TypePermissions is what one resource type takes.
type TypePermissions struct {
	// List holds the permissions that finding the resources takes.
	List []string `json:"list"`
	// Read holds the further permissions that reading each one's
	// attributes takes, which is what a list block with include_resource
	// does.
	Read []string `json:"read,omitempty"`
	// Beyond says what reading the attributes takes beyond reading how
	// the resource is set up. No role from here allows that, so such a
	// type is listed without its attributes.
	Beyond string `json:"beyond,omitempty"`
	// ListBeyond says the same of finding the resources at all. Such a
	// type is left out.
	ListBeyond string `json:"list_beyond,omitempty"`
	// ListUnknown says why what finding the resources takes could not be
	// worked out from the recording. Such a type is left out too: no role
	// can be said to cover it.
	ListUnknown string `json:"list_unknown,omitempty"`
	// ListUnconfirmed and ReadUnconfirmed are true where the recording
	// did not run to the end, because the provider did not accept the
	// stand-in's made-up answer. A real project may show more.
	ListUnconfirmed bool `json:"list_unconfirmed,omitempty"`
	ReadUnconfirmed bool `json:"read_unconfirmed,omitempty"`
}

// Load returns the permissions that ship with moonwalk.
func Load() (*Permissions, error) {
	var p Permissions
	if err := json.Unmarshal(embedded, &p); err != nil {
		return nil, fmt.Errorf("gcprole: %w", err)
	}
	return &p, nil
}

// Role is a Google Cloud custom role, in the form that the --file option
// of 'gcloud iam roles create' takes.
type Role struct {
	Title               string   `json:"title"`
	Description         string   `json:"description"`
	Stage               string   `json:"stage"`
	IncludedPermissions []string `json:"includedPermissions"`
}

// YAML returns the role as the YAML file that 'gcloud iam roles create
// --file' is documented to take.
func (r Role) YAML() string {
	quote := func(s string) string {
		b, _ := json.Marshal(s) // A JSON string is a YAML string.
		return string(b)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "title: %s\ndescription: %s\nstage: %s\nincludedPermissions:\n", quote(r.Title), quote(r.Description), r.Stage)
	for _, p := range r.IncludedPermissions {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	return b.String()
}

// Options says what the role is to cover.
type Options struct {
	// Types are the resource types to be listed.
	Types []string
	// IdentityOnly leaves out what reading each resource's attributes
	// takes, for list blocks without include_resource.
	IdentityOnly bool
	// Title is the role's title; "moonwalk discovery" if empty.
	Title string
}

// Plan says how each of the types asked for can be listed with a role
// that only reads. A query file and its role are made from the same plan.
type Plan struct {
	// Full are listed with their attributes, and Identity without, either
	// because that was asked for or because reading the attributes takes
	// more than reading, which IdentityBecause explains.
	Full, Identity  []string
	IdentityBecause map[string]string
	// LeftOut cannot be listed at all with reads alone, or it is not
	// known what listing them takes. Each has what it takes as its value.
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
		case tp.ListUnknown != "":
			plan.LeftOut[t] = tp.ListUnknown
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

// readOnly is the form of a permission this package will put in a role:
// a service, a kind of resource, and "list" or "get".
var readOnly = regexp.MustCompile(`^[a-z][a-z0-9]*\.[A-Za-z][A-Za-z0-9]*\.(list|get)$`)

// Role returns the custom role that listing the given types takes, and
// the plan it follows.
func (p *Permissions) Role(o Options) (Role, Plan, error) {
	plan := p.Plan(o)
	role := Role{Title: o.Title, Stage: "GA", IncludedPermissions: []string{}}
	if role.Title == "" {
		role.Title = "moonwalk discovery"
	}
	if len(role.Title) > 100 {
		return Role{}, plan, fmt.Errorf("a role's title is at most 100 characters; this one is %d", len(role.Title))
	}
	add := func(permissions []string) error {
		for _, name := range permissions {
			if !readOnly.MatchString(name) {
				return fmt.Errorf("gcprole: %q is not a list or a get named in full, and may not be in a role", name)
			}
			if !slices.Contains(role.IncludedPermissions, name) {
				role.IncludedPermissions = append(role.IncludedPermissions, name)
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
	if len(role.IncludedPermissions) == 0 {
		return Role{}, plan, fmt.Errorf("nothing is recorded about the permissions of the types chosen; they were recorded from %s", p.Provider)
	}
	sort.Strings(role.IncludedPermissions)
	what := ""
	if len(plan.Full) == 0 {
		what = ", by name only"
	}
	role.Description = fmt.Sprintf("Lets moonwalk list %d kinds of resource through Terraform%s. Read-only: every permission is a list or a get, named in full.",
		len(plan.Full)+len(plan.Identity), what)
	// The limits Google sets on a custom role.
	if len(role.IncludedPermissions) > 3000 || len(role.Description) > 300 {
		return Role{}, plan, fmt.Errorf("the role is beyond what Google accepts: %d permissions (3,000 at most), a description of %d bytes (300 at most)",
			len(role.IncludedPermissions), len(role.Description))
	}
	return role, plan, nil
}
