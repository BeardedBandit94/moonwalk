package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
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
	regions      []string // One list block per region; none means the provider's own.
	types        []string // Patterns of resource types to list; none means all.
	provider     string   // Local name of the one provider to write for; "" means all.
	identityOnly bool     // Leave out include_resource.
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
			required := false
			for _, a := range schema.Block.Attributes {
				required = required || a.Required
			}
			for _, bt := range schema.Block.BlockTypes {
				required = required || bt.MinItems > 0
			}
			if required {
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
		b.WriteByte('\n')
		for _, t := range listable {
			_, takesRegion := lists[t].Block.Attributes["region"]
			if len(o.regions) == 0 || !takesRegion {
				writeListBlock(&b, t, "all", name, "", o.identityOnly)
				written++
				continue
			}
			for _, region := range o.regions {
				writeListBlock(&b, t, label(region), name, region, o.identityOnly)
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

func writeListBlock(b *strings.Builder, resourceType, name, provider, region string, identityOnly bool) {
	fmt.Fprintf(b, "list %q %q {\n", resourceType, name)
	if identityOnly {
		fmt.Fprintf(b, "  provider = %s\n  limit    = %d\n", provider, listLimit)
	} else {
		fmt.Fprintf(b, "  provider         = %s\n  include_resource = true\n  limit            = %d\n", provider, listLimit)
	}
	if region != "" {
		fmt.Fprintf(b, "\n  config {\n    region = %q\n  }\n", region)
	}
	b.WriteString("}\n\n")
}

var notIdentifier = regexp.MustCompile(`[^A-Za-z0-9_]`)

// label turns a region into a valid block name.
func label(region string) string {
	return notIdentifier.ReplaceAllString(region, "_")
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
