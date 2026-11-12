package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/azurerbac"
)

// runAzureRole handles the azure-role command: the Azure custom role that
// the query file for the same schema and options needs.
func runAzureRole(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("azure-role", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	types := fs.String("type", "", "")
	subscriptions := fs.String("subscription", "", "")
	name := fs.String("name", "", "")
	identityOnly := fs.Bool("identity-only", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("expected one schema file at most, or - for standard input\n\n%s", usage)
	}
	perms, err := azurerbac.Load()
	if err != nil {
		return err
	}

	// With a schema, the role is for what that provider can list. With
	// none, it is for what the recorded provider can.
	var listed []string
	if fs.NArg() == 1 {
		var schema []byte
		if fs.Arg(0) == "-" {
			schema, err = io.ReadAll(stdin)
		} else {
			schema, err = os.ReadFile(fs.Arg(0))
		}
		if err != nil {
			return err
		}
		if listed, _, err = providerListTypes(schema, "azurerm", list(*types)); err != nil {
			return err
		}
	} else {
		for t := range perms.Types {
			if *types == "" || matchesAnyPattern(list(*types), t) {
				listed = append(listed, t)
			}
		}
		if len(listed) == 0 {
			return fmt.Errorf("no resource type recorded from %s matches %s", perms.Provider, *types)
		}
		fmt.Fprintf(stderr, "moonwalk: no provider schema was given, so this is for what %s can list. "+
			"Give the schema of the provider in use to match its query file exactly.\n", perms.Provider)
	}

	role, plan, err := perms.Role(azurerbac.Options{
		Types: listed, IdentityOnly: *identityOnly, Subscriptions: list(*subscriptions), Name: *name,
	})
	if err != nil {
		return err
	}

	// Why some types are not covered in full, each reason once.
	say := func(types map[string]string, what string) {
		byReason := map[string][]string{}
		for t, reason := range types {
			byReason[reason] = append(byReason[reason], t)
		}
		reasons := make([]string, 0, len(byReason))
		for r := range byReason {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		for _, r := range reasons {
			sort.Strings(byReason[r])
			fmt.Fprintf(stderr, "moonwalk: %s, because that takes %s: %s.\n", what, r, strings.Join(byReason[r], ", "))
		}
	}
	say(plan.LeftOut, "left out")
	say(plan.IdentityBecause, "listed without attributes")
	if len(plan.Unknown) > 0 {
		fmt.Fprintf(stderr, "moonwalk: no permissions are recorded for %s that this provider can list and %s could not: %s. "+
			"The listing will say what it is refused.\n", plural(len(plan.Unknown), "type"), perms.Provider, strings.Join(plan.Unknown, ", "))
	}
	var unconfirmed []string
	for _, t := range append(slices.Clone(plan.Full), plan.Identity...) {
		if tp := perms.Types[t]; tp.ListUnconfirmed || (slices.Contains(plan.Full, t) && tp.ReadUnconfirmed) {
			unconfirmed = append(unconfirmed, t)
		}
	}
	sort.Strings(unconfirmed)
	if len(unconfirmed) > 0 {
		fmt.Fprintf(stderr, "moonwalk: for %d of the %d types the recording stopped early, so listing them may take permissions that are not in the role. "+
			"The listing will say what it is refused: %s\n", len(unconfirmed), len(listed), strings.Join(unconfirmed, ", "))
	}
	var unlisted []string
	for _, a := range perms.Unlisted {
		if slices.Contains(role.Actions, a) {
			unlisted = append(unlisted, a)
		}
	}
	if len(unlisted) > 0 {
		fmt.Fprintf(stderr, "moonwalk: Microsoft's published list of permissions, which trails the services, does not have %s. "+
			"They are spelled as the provider's own requests spell them.\n", strings.Join(unlisted, ", "))
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(role)
}
