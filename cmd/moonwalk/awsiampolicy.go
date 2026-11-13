package main

import (
	"bufio"
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

// runAWSIAMPolicy handles the aws-iam-policy command: the IAM policies that the
// query file for the same schema and options needs, and the policy for
// reading states from S3.
func runAWSIAMPolicy(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("aws-iam-policy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	types := fs.String("type", "", "")
	regions := fs.String("region", "", "")
	account := fs.String("account", "", "")
	anyAccount := fs.Bool("any-account", false, "")
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
	if fs.NArg() > 1 {
		return fmt.Errorf("expected one schema file at most, or - for standard input\n\n%s", usage)
	}
	if len(keys) > 0 && len(states) == 0 {
		return fmt.Errorf("-kms-key goes with -state: it is the key the states are encrypted with")
	}
	if *anyAccount && *account != "" {
		return fmt.Errorf("-any-account says to write the policy for no account in particular, and -account names one; give one of them")
	}

	type file struct {
		name string
		doc  awsiam.Document
	}
	var files []file

	// With a schema, the policy is for what that provider can list. With
	// none, it is for what the recorded provider can, unless only states
	// were asked about.
	if fs.NArg() == 1 || len(states) == 0 || *types != "" {
		perms, err := awsiam.Load()
		if err != nil {
			return err
		}
		var listed []string
		var global map[string]bool
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
			if listed, global, err = awsListTypes(schema, list(*types)); err != nil {
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
		// Everything else about the request holds. Before anything is
		// written, a policy for no account in particular is agreed to.
		if *account == "" {
			if err := confirmAnyAccount(stdin, stderr, *anyAccount, fs.Arg(0) == "-"); err != nil {
				return err
			}
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
	return providerListTypes(schema, "aws", patterns)
}

// providerListTypes returns the resource types of one provider in a
// schema that a query file from moonwalk lists, narrowed by the patterns,
// and which of them take no region.
func providerListTypes(schema []byte, name string, patterns []string) (types []string, global map[string]bool, err error) {
	var s providerSchemas
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, nil, fmt.Errorf("not the output of 'terraform providers schema -json': %w", err)
	}
	global = map[string]bool{}
	found := false
	for addr, provider := range s.ProviderSchemas {
		if path.Base(addr) != name {
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
		return nil, nil, fmt.Errorf("the schema has no %s provider", name)
	}
	if len(types) == 0 {
		return nil, nil, fmt.Errorf("the schema has no %s resource types to list that match; listing needs Terraform 1.14 or later and a provider that supports it", name)
	}
	sort.Strings(types)
	return types, global, nil
}

// isTerminal reports whether r is a terminal, where someone can be asked
// a question. Tests replace it.
var isTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// confirmAnyAccount settles what happens when no account was given. The
// policy's resources then have * where the account goes, which is wider
// than a policy for one account has to be, so it is not written without
// a yes: from -any-account, or typed at the terminal. Where there is
// neither, such as in a script, it is an error and nothing is written.
func confirmAnyAccount(stdin io.Reader, stderr io.Writer, accepted, stdinTaken bool) error {
	const warning = "moonwalk: warning: no -account was given, so the resources in the policy are not limited to an account. " +
		"Each has * where the account goes: arn:aws:ec2:*:*:vpc/* where -account 111122223333 gives arn:aws:ec2:*:111122223333:vpc/*."
	const remedy = "give -account with the 12-digit account the policy is for, or -any-account to write it for no account in particular"
	if accepted {
		fmt.Fprintln(stderr, warning)
		return nil
	}
	if stdinTaken || !isTerminal(stdin) {
		return fmt.Errorf("no -account was given, and there is no terminal to ask at whether that is meant: %s", remedy)
	}
	fmt.Fprintf(stderr, "%s\nWrite the policy for any account? [y/N] ", warning)
	answer, _ := bufio.NewReader(stdin).ReadString('\n')
	if !strings.HasSuffix(answer, "\n") {
		// The input ended without an answer; what follows starts a line.
		fmt.Fprintln(stderr)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return fmt.Errorf("nothing was written; %s", remedy)
}
