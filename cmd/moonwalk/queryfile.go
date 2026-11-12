package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/awsiam"
)

// listLimit is the limit written into every list block. Without one a
// block stops at 100 resources and does not say so.
const listLimit = 1000000

// providerSchemas is the part of `terraform providers schema -json` that
// says what each provider can list.
type providerSchemas struct {
	ProviderSchemas map[string]struct {
		ListResourceSchemas map[string]struct {
			Block struct {
				Attributes map[string]struct {
					Required bool `json:"required"`
				} `json:"attributes"`
				BlockTypes map[string]struct {
					MinItems int `json:"min_items"`
				} `json:"block_types"`
			} `json:"block"`
		} `json:"list_resource_schemas"`
	} `json:"provider_schemas"`
}

// queryFileOptions says what to put in a query file.
type queryFileOptions struct {
	regions []string // One list block per region; none means the provider's own.
	// subscriptions is the same for Azure subscriptions, which are to the
	// azurerm provider what regions are to the aws one: a list block
	// covers one, the provider's own unless it names another.
	subscriptions []string
	types         []string // Patterns of resource types to list; none means all.
	provider      string   // Local name of the one provider to write for; "" means all.
	identityOnly  bool     // Leave out include_resource.
	// allow names the AWS actions that are withheld from a policy by
	// default and wanted all the same; see awsiam.Withheld.
	allow []string
}

// writeQueryFile writes a .tfquery.hcl file that lists every resource type
// the providers in schema can list on their own.
func writeQueryFile(w io.Writer, schema []byte, o queryFileOptions) error {
	var s providerSchemas
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("not the output of 'terraform providers schema -json': %w", err)
	}
	addresses := make([]string, 0, len(s.ProviderSchemas))
	for addr := range s.ProviderSchemas {
		addresses = append(addresses, addr)
	}
	sort.Strings(addresses)

	var b strings.Builder
	written := 0
	for _, addr := range addresses {
		name := path.Base(addr)
		lists := s.ProviderSchemas[addr].ListResourceSchemas
		if len(lists) == 0 || (o.provider != "" && o.provider != name) {
			continue
		}
		var listable, needArgument []string
		for t, schema := range lists {
			if len(o.types) > 0 && !matchesAnyPattern(o.types, t) {
				continue
			}
			if needsArgument(schema.Block.Attributes, schema.Block.BlockTypes) {
				needArgument = append(needArgument, t)
			} else {
				listable = append(listable, t)
			}
		}
		sort.Strings(listable)
		sort.Strings(needArgument)
		if len(listable)+len(needArgument) == 0 {
			continue
		}
		// For AWS, the file asks for what the policy from 'moonwalk
		// iam-policy' allows and no more: a type that cannot be listed, or
		// not with its attributes, without an action that reads what is
		// stored is left out, or listed by identity alone.
		var plan awsiam.Plan
		if name == "aws" {
			perms, err := awsiam.Load()
			if err != nil {
				return err
			}
			if plan, err = perms.Plan(awsiam.Options{Types: listable, IdentityOnly: o.identityOnly, Allow: o.allow}); err != nil {
				return err
			}
			listable = slices.DeleteFunc(listable, func(t string) bool { return plan.LeftOut[t] != "" })
		}

		fmt.Fprintf(&b, "# Lists what exists through the %s provider: %d resource types.\n", name, len(listable))
		fmt.Fprintf(&b, "# Written by 'moonwalk query-file'. Put it next to the provider configuration and run:\n#\n")
		fmt.Fprintf(&b, "#   terraform query -json > found.jsonl\n#\n")
		fmt.Fprintf(&b, "# Each block sets a limit, because a list block stops at 100 resources without one.\n")
		fmt.Fprintf(&b, "# Check it first with 'terraform validate -query', and delete any block it rejects:\n")
		fmt.Fprintf(&b, "# a few types need an argument that the provider's schema does not mark as required.\n")
		if len(needArgument) > 0 {
			fmt.Fprintf(&b, "#\n# Left out, because each needs an argument that names a parent resource:\n")
			for _, line := range wrap(needArgument, 76) {
				fmt.Fprintf(&b, "#   %s\n", line)
			}
		}
		withheld := func(title string, types map[string]string) {
			if len(types) == 0 {
				return
			}
			var entries []string
			for t, action := range types {
				entries = append(entries, fmt.Sprintf("%s (%s)", t, action))
			}
			sort.Strings(entries)
			fmt.Fprintf(&b, "#\n# %s\n", title)
			for _, e := range entries {
				fmt.Fprintf(&b, "#   %s\n", e)
			}
		}
		withheld("Left out, because listing them takes an action that reads what is stored in the\n"+
			"# service, which the policy from 'moonwalk iam-policy' withholds. To include one, give\n"+
			"# both commands -allow with the action:", plan.LeftOut)
		withheld("Listed without attributes, for the same reason:", plan.IdentityBecause)
		b.WriteByte('\n')
		for _, t := range listable {
			// The argument that says where to list, and the places asked for.
			argument, places, prefix := "", []string(nil), ""
			if _, takes := lists[t].Block.Attributes["region"]; takes {
				argument, places = "region", o.regions
			} else if _, takes := lists[t].Block.Attributes["subscription_id"]; takes {
				// A subscription ID can start with a digit; a block name cannot.
				argument, places, prefix = "subscription_id", o.subscriptions, "sub_"
			}
			identityOnly := o.identityOnly || plan.IdentityBecause[t] != ""
			if len(places) == 0 {
				writeListBlock(&b, t, "all", name, "", "", identityOnly)
				written++
				continue
			}
			for _, place := range places {
				writeListBlock(&b, t, prefix+label(place), name, argument, place, identityOnly)
				written++
			}
		}
	}
	if written == 0 {
		return fmt.Errorf("the schema has no resource types to list; listing needs Terraform 1.14 or later and a provider that supports it")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// needsArgument reports whether a list block of a type cannot be written
// without naming something, usually a parent resource.
func needsArgument(attributes map[string]struct {
	Required bool `json:"required"`
}, blocks map[string]struct {
	MinItems int `json:"min_items"`
}) bool {
	for _, a := range attributes {
		if a.Required {
			return true
		}
	}
	for _, b := range blocks {
		if b.MinItems > 0 {
			return true
		}
	}
	return false
}

func writeListBlock(b *strings.Builder, resourceType, name, provider, argument, place string, identityOnly bool) {
	fmt.Fprintf(b, "list %q %q {\n", resourceType, name)
	if identityOnly {
		fmt.Fprintf(b, "  provider = %s\n  limit    = %d\n", provider, listLimit)
	} else {
		fmt.Fprintf(b, "  provider         = %s\n  include_resource = true\n  limit            = %d\n", provider, listLimit)
	}
	if argument != "" {
		fmt.Fprintf(b, "\n  config {\n    %s = %q\n  }\n", argument, place)
	}
	b.WriteString("}\n\n")
}

var notIdentifier = regexp.MustCompile(`[^A-Za-z0-9_]`)

// label turns a region or a subscription into part of a valid block name.
func label(place string) string {
	return notIdentifier.ReplaceAllString(place, "_")
}

func matchesAnyPattern(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, s); err == nil && ok {
			return true
		}
	}
	return false
}

// wrap joins words with ", " into lines of at most width characters.
func wrap(words []string, width int) []string {
	var lines []string
	line := ""
	for i, w := range words {
		if i < len(words)-1 {
			w += ","
		}
		if line != "" && len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
