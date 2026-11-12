package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/awsiam"
	"github.com/BeardedBandit94/moonwalk/internal/s3state"
)

// runIAMPolicy handles the iam-policy command: the IAM policies that the
// query file for the same schema and options needs, and the policy for
// reading states from S3.
func runIAMPolicy(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("iam-policy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	types := fs.String("type", "", "")
	regions := fs.String("region", "", "")
	account := fs.String("account", "", "")
	partition := fs.String("partition", "aws", "")
	identityOnly := fs.Bool("identity-only", false, "")
	allow := fs.String("allow", "", "")
	out := fs.String("out", "", "")
	var states, keys multiFlag
	fs.Var(&states, "state", "")
	fs.Var(&keys, "kms-key", "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if fs.NArg() > 1 || (fs.NArg() == 0 && len(states) == 0) {
		return fmt.Errorf("expected a schema file (or - for standard input), -state, or both\n\n%s", usage)
	}
	if len(keys) > 0 && len(states) == 0 {
		return fmt.Errorf("-kms-key goes with -state: it is the key the states are encrypted with")
	}

	type file struct {
		name string
		doc  awsiam.Document
	}
	var files []file

	if fs.NArg() == 1 {
		var schema []byte
		var err error
		if fs.Arg(0) == "-" {
			schema, err = io.ReadAll(stdin)
		} else {
			schema, err = os.ReadFile(fs.Arg(0))
		}
		if err != nil {
			return err
		}
		listed, global, err := awsListTypes(schema, list(*types))
		if err != nil {
			return err
		}
		perms, err := awsiam.Load()
		if err != nil {
			return err
		}
		docs, plan, err := perms.Policies(awsiam.Options{
			Types: listed, IdentityOnly: *identityOnly, Regions: list(*regions), Global: global,
			Account: *account, Partition: *partition, Allow: list(*allow),
		})
		if err != nil {
			return err
		}
		if len(docs) == 0 {
			return fmt.Errorf("nothing is known about the permissions of the %d types chosen; they were recorded from %s", len(listed), perms.Provider)
		}
		for i, d := range docs {
			files = append(files, file{fmt.Sprintf("moonwalk-discovery-%02d.json", i+1), d})
		}
		warnPlan(stderr, plan, perms)
		var unconfirmed []string
		for _, t := range append(slices.Clone(plan.Full), plan.Identity...) {
			if tp := perms.Types[t]; tp.ListUnconfirmed || (slices.Contains(plan.Full, t) && tp.ReadUnconfirmed) {
				unconfirmed = append(unconfirmed, t)
			}
		}
		sort.Strings(unconfirmed)
		if len(unconfirmed) > 0 {
			fmt.Fprintf(stderr, "moonwalk: for %d of the %d types the recording stopped early, so listing them may take actions that are not in the policy. "+
				"The listing will say what it is refused: %s\n", len(unconfirmed), len(listed), strings.Join(unconfirmed, ", "))
		}
	}

	if len(states) > 0 {
		var locations []awsiam.StateLocation
		for _, address := range states {
			_, address = splitName(address)
			loc, ok, err := s3state.Parse(address)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("-state takes the address of states in S3, such as s3://bucket/prefix/, not %q", address)
			}
			locations = append(locations, awsiam.StateLocation{Bucket: loc.Bucket, Key: loc.Key})
		}
		doc, err := awsiam.StatePolicy(locations, keys, *partition)
		if err != nil {
			return err
		}
		files = append(files, file{"moonwalk-state.json", doc})
	}

	encode := func(w io.Writer, d awsiam.Document) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(d)
	}
	if *out == "" {
		if len(files) != 1 {
			return fmt.Errorf("this takes %d policy documents, since IAM accepts %d characters in one; name a directory to write them to with -out", len(files), awsiam.MaxSize)
		}
		return encode(stdout, files[0].doc)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		w, err := os.Create(filepath.Join(*out, f.name))
		if err != nil {
			return err
		}
		err = encode(w, f.doc)
		if closeErr := w.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		actions := 0
		for _, s := range f.doc.Statement {
			actions += len(s.Action)
		}
		fmt.Fprintf(stdout, "%s  %s, %d of %d characters\n", filepath.Join(*out, f.name), plural(actions, "action"), awsiam.Size(f.doc), awsiam.MaxSize)
	}
	if len(files) > 10 {
		fmt.Fprintf(stderr, "moonwalk: a role takes 10 managed policies unless its quota is raised; these are %d. Narrow the types with -type, or ask for the quota.\n", len(files))
	}
	return nil
}

// warnPlan says which types a policy does not cover in full, and why.
func warnPlan(stderr io.Writer, plan awsiam.Plan, perms *awsiam.Permissions) {
	// By the action withheld, so that each is explained once.
	byAction := func(types map[string]string) map[string][]string {
		out := map[string][]string{}
		for t, a := range types {
			out[a] = append(out[a], t)
		}
		return out
	}
	say := func(types map[string]string, what string) {
		grouped := byAction(types)
		actions := make([]string, 0, len(grouped))
		for a := range grouped {
			actions = append(actions, a)
		}
		sort.Strings(actions)
		for _, a := range actions {
			sort.Strings(grouped[a])
			fmt.Fprintf(stderr, "moonwalk: %s, because that takes %s, which %s: %s. To include it, add -allow %s here and to query-file.\n",
				what, a, awsiam.Withheld[a], strings.Join(grouped[a], ", "), a)
		}
	}
	say(plan.LeftOut, "left out")
	say(plan.IdentityBecause, "listed without attributes")
	if len(plan.Unknown) > 0 {
		fmt.Fprintf(stderr, "moonwalk: no permissions are recorded for %s that this provider can list and %s could not: %s. "+
			"The listing will say what it is refused.\n", plural(len(plan.Unknown), "type"), perms.Provider, strings.Join(plan.Unknown, ", "))
	}
}

// awsListTypes returns the AWS resource types in a provider schema that a
// query file from moonwalk lists, narrowed by the patterns, and which of
// them have no region.
func awsListTypes(schema []byte, patterns []string) (types []string, global map[string]bool, err error) {
	var s providerSchemas
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, nil, fmt.Errorf("not the output of 'terraform providers schema -json': %w", err)
	}
	global = map[string]bool{}
	found := false
	for addr, provider := range s.ProviderSchemas {
		if path.Base(addr) != "aws" {
			continue
		}
		found = true
		for t, list := range provider.ListResourceSchemas {
			if len(patterns) > 0 && !matchesAnyPattern(patterns, t) {
				continue
			}
			if needsArgument(list.Block.Attributes, list.Block.BlockTypes) {
				continue
			}
			types = append(types, t)
			if _, regional := list.Block.Attributes["region"]; !regional {
				global[t] = true
			}
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("the schema has no aws provider; policies are written for AWS only so far")
	}
	if len(types) == 0 {
		return nil, nil, fmt.Errorf("the schema has no AWS resource types to list that match; listing needs Terraform 1.14 or later and a provider that supports it")
	}
	sort.Strings(types)
	return types, global, nil
}
