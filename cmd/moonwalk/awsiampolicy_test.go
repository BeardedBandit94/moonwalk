package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/awsiam"
)

// examples is where the policies for everything moonwalk can list are
// kept, to be read without running anything.
const examples = "../../docs/aws-iam/"

// The example policies are what the command writes today. Run the tests
// with -update after the permissions change, and read the difference: it
// is the change in what moonwalk asks an account for.
func TestExamplePolicies(t *testing.T) {
	const key = "arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	sets := map[string][]string{
		"with-attributes": {"-account", "111122223333"},
		"identities-only": {"-account", "111122223333", "-identity-only"},
		"state":           {"-state", "s3://example-terraform-states/", "-kms-key", key},
	}
	for name, args := range sets {
		dir := t.TempDir()
		if _, err := runCLI(t, "", append(append([]string{"aws-iam-policy"}, args...), "-out", dir)...); err != nil {
			t.Fatal(err)
		}
		written, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil || len(written) == 0 {
			t.Fatalf("%s: nothing written (%v)", name, err)
		}
		kept := filepath.Join(examples, name)
		if *update {
			old, _ := filepath.Glob(filepath.Join(kept, "*.json"))
			for _, f := range old {
				os.Remove(f)
			}
			if err := os.MkdirAll(kept, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		var names []string
		for _, f := range written {
			names = append(names, filepath.Base(f))
			got, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(kept, filepath.Base(f))
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil || string(got) != string(want) {
				t.Errorf("%s differs from what the command writes (run `go test ./cmd/moonwalk -update`, and read the difference)", path)
			}
		}
		there, _ := filepath.Glob(filepath.Join(kept, "*.json"))
		if len(there) != len(names) {
			t.Errorf("%s holds %d documents, and the command writes %d", kept, len(there), len(names))
		}
	}
}

// statements reads the policy documents in a directory.
func statements(t *testing.T, dir string) []awsiam.Statement {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no policy documents in %s (%v)", dir, err)
	}
	sort.Strings(files)
	var out []awsiam.Statement
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d awsiam.Document
		if err := json.Unmarshal(data, &d); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if d.Version != "2012-10-17" || len(d.Statement) == 0 {
			t.Errorf("%s: version %q, %d statements", f, d.Version, len(d.Statement))
		}
		if size := awsiam.Size(d); size > awsiam.MaxSize {
			t.Errorf("%s is %d characters; IAM accepts %d", f, size, awsiam.MaxSize)
		}
		out = append(out, d.Statement...)
	}
	return out
}

// The rules every policy moonwalk writes keeps to, checked on the ones
// that are kept as examples.
func TestExamplePoliciesKeepToTheRules(t *testing.T) {
	explicit := regexp.MustCompile(`^[a-z0-9-]+:[A-Za-z0-9]+$`)
	for _, set := range []string{"with-attributes", "identities-only", "state"} {
		for _, s := range statements(t, filepath.Join(examples, set)) {
			if s.Effect != "Allow" {
				t.Errorf("%s/%s: effect %q", set, s.Sid, s.Effect)
			}
			for _, a := range s.Action {
				if !explicit.MatchString(a) {
					t.Errorf("%s/%s: %q is not one action named in full", set, s.Sid, a)
				}
			}
			all := len(s.Resource) == 1 && s.Resource[0] == "*"
			limited := strings.Contains(s.Sid, "Limited") || set == "state"
			if all == limited {
				t.Errorf("%s/%s: resources %v", set, s.Sid, s.Resource)
			}
			for _, r := range s.Resource {
				if !all && !strings.HasPrefix(r, "arn:aws:") {
					t.Errorf("%s/%s: %q is not an ARN", set, s.Sid, r)
				}
			}
		}
	}
	// The policy for identities holds no action that reads attributes,
	// and is smaller for it.
	count := func(set string) int {
		n := 0
		for _, s := range statements(t, filepath.Join(examples, set)) {
			n += len(s.Action)
		}
		return n
	}
	if with, without := count("with-attributes"), count("identities-only"); without >= with || without < 100 {
		t.Errorf("%d actions with attributes, %d for identities only", with, without)
	}
	// Reading states is the one place a policy reads what is stored, and
	// it reads states and nothing else.
	for _, s := range statements(t, filepath.Join(examples, "state")) {
		for _, r := range s.Resource {
			if s.Action[0] == "s3:GetObject" && !strings.HasSuffix(r, ".tfstate") {
				t.Errorf("the state policy reads %s", r)
			}
		}
	}
}

func TestAWSIAMPolicyCommand(t *testing.T) {
	// For the provider in a schema: its types, with global services not
	// held to the regions asked for.
	out, warnings, err := runCLIWithWarnings(t, schema, "aws-iam-policy", "-region", "eu-west-1,us-east-1", "-account", "111122223333", "-")
	if err != nil {
		t.Fatal(err)
	}
	var d awsiam.Document
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("not a policy document: %v\n%s", err, out)
	}
	conditions := map[string]bool{}
	var actions []string
	for _, s := range d.Statement {
		service, _, _ := strings.Cut(s.Action[0], ":")
		conditions[service] = conditions[service] || s.Condition != nil
		actions = append(actions, s.Action...)
		if s.Condition != nil && strings.Join(s.Condition["StringEquals"]["aws:RequestedRegion"], ",") != "eu-west-1,us-east-1" {
			t.Errorf("%s: condition %v", s.Sid, s.Condition)
		}
	}
	if !conditions["ec2"] || conditions["iam"] {
		t.Errorf("ec2 should be held to the regions and iam not: %v", conditions)
	}
	if !strings.Contains(strings.Join(actions, " "), "ec2:DescribeVpcs") || !strings.Contains(strings.Join(actions, " "), "iam:ListRoles") {
		t.Errorf("actions = %v", actions)
	}
	if !strings.Contains(out, `"arn:aws:ec2:*:111122223333:vpc/*"`) {
		t.Errorf("resources are not limited to the account:\n%s", out)
	}
	// The schema has two types that need a parent and one that nothing
	// is recorded about; none of that is an error.
	if strings.Contains(warnings, "aws_cloudwatch_log_stream") {
		t.Errorf("a type that the query file leaves out is mentioned: %s", warnings)
	}

	// Narrowed to one type, for identities: the one action that takes.
	out, err = runCLI(t, schema, "aws-iam-policy", "-any-account", "-type", "aws_vpc", "-identity-only", "-")
	if err != nil || strings.Count(out, `"ec2:`) != 1 || !strings.Contains(out, `"ec2:DescribeVpcs"`) {
		t.Errorf("one type, identities only (err %v):\n%s", err, out)
	}

	// Without a schema: what the recorded provider can list.
	out, warnings, err = runCLIWithWarnings(t, "", "aws-iam-policy", "-any-account", "-type", "aws_iam_*", "-identity-only")
	if err != nil || !strings.Contains(out, `"iam:ListUsers"`) || !strings.Contains(warnings, "no provider schema was given") {
		t.Errorf("without a schema (err %v, warnings %q):\n%s", err, warnings, out)
	}

	// More than one document needs somewhere to go.
	if _, err := runCLI(t, "", "aws-iam-policy", "-any-account"); err == nil || !strings.Contains(err.Error(), "-out") {
		t.Errorf("several documents and no directory: %v", err)
	}

	for name, args := range map[string][]string{
		"an account that is not one": {"aws-iam-policy", "-account", "prod", "-type", "aws_vpc"},
		"an account and any account": {"aws-iam-policy", "-account", "111122223333", "-any-account", "-type", "aws_vpc"},
		"a key without states":       {"aws-iam-policy", "-kms-key", "arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab", "-type", "aws_vpc"},
		"states that are not in S3":  {"aws-iam-policy", "-state", "terraform.tfstate"},
		"an action not withheld":     {"aws-iam-policy", "-allow", "s3:GetObject", "-type", "aws_vpc"},
		"no type that matches":       {"aws-iam-policy", "-type", "azurerm_*"},
		"two schemas":                {"aws-iam-policy", "a.json", "b.json"},
		"unknown flag":               {"aws-iam-policy", "-depth", "2"},
	} {
		if _, err := runCLI(t, "", args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// What hands over stored data is in neither the policy nor the query
// file until it is asked for by name, and then in both.
func TestWithheldActionsInPolicyAndQueryFile(t *testing.T) {
	const lambdaAndParameters = `{"format_version":"1.0","provider_schemas":{"registry.terraform.io/hashicorp/aws":{"list_resource_schemas":{
	  "aws_lambda_function":{"block":{"attributes":{"region":{"type":"string","optional":true}}}},
	  "aws_ssm_parameter":{"block":{"attributes":{"region":{"type":"string","optional":true}}}},
	  "aws_sqs_queue":{"block":{"attributes":{"region":{"type":"string","optional":true}}}}}}}}`

	policy, warnings, err := runCLIWithWarnings(t, lambdaAndParameters, "aws-iam-policy", "-any-account", "-")
	if err != nil {
		t.Fatal(err)
	}
	query, err := runCLI(t, lambdaAndParameters, "query-file", "-")
	if err != nil {
		t.Fatal(err)
	}
	for _, withheld := range []string{"lambda:GetFunction", "ssm:GetParameter"} {
		if strings.Contains(policy, `"`+withheld+`"`) {
			t.Errorf("%s is in the policy without having been asked for", withheld)
		}
		if !strings.Contains(warnings, withheld) || !strings.Contains(query, withheld) {
			t.Errorf("%s is left out without a word: warnings %q", withheld, warnings)
		}
	}
	// The function is not listed; the parameter is, without attributes;
	// the queue is untouched.
	if strings.Contains(query, `list "aws_lambda_function"`) || strings.Contains(policy, "lambda:") {
		t.Errorf("the function is listed or allowed:\n%s", query)
	}
	parameter := query[strings.Index(query, `list "aws_ssm_parameter"`):]
	parameter = parameter[:strings.Index(parameter, "}")]
	if strings.Contains(parameter, "include_resource") || !strings.Contains(policy, `"ssm:DescribeParameters"`) {
		t.Errorf("the parameter should be listed by identity alone:\n%s", parameter)
	}
	queue := query[strings.Index(query, `list "aws_sqs_queue"`):]
	if !strings.Contains(queue, "include_resource = true") {
		t.Errorf("the queue should be listed with its attributes:\n%s", queue)
	}

	// Asked for by name, in both.
	policy, _, err = runCLIWithWarnings(t, lambdaAndParameters, "aws-iam-policy", "-any-account", "-allow", "lambda:GetFunction,ssm:GetParameter", "-")
	if err != nil || !strings.Contains(policy, `"lambda:GetFunction"`) || !strings.Contains(policy, `"ssm:GetParameter"`) {
		t.Errorf("allowed by name (err %v):\n%s", err, policy)
	}
	query, err = runCLI(t, lambdaAndParameters, "query-file", "-allow", "lambda:GetFunction,ssm:GetParameter", "-")
	if err != nil || !strings.Contains(query, `list "aws_lambda_function"`) || strings.Count(query, "include_resource = true") != 3 {
		t.Errorf("allowed by name (err %v):\n%s", err, query)
	}
}

// A listing that was refused says which actions the policy lacks.
func TestRefusedActionsAreNamed(t *testing.T) {
	listing := `{"@level":"info","type":"version","terraform":"1.14.0"}` + "\n" +
		`{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"Error Listing Remote Resources","detail":"listing EC2 VPCs: operation error EC2: DescribeVpcs, https response error StatusCode: 403, api error UnauthorizedOperation: You are not authorized to perform this operation. User: arn:aws:sts::111122223333:assumed-role/moonwalk/s is not authorized to perform: ec2:DescribeVpcs because no identity-based policy allows the ec2:DescribeVpcs action"}}` + "\n" +
		`{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"Error Listing Remote Resources","detail":"listing IAM Roles: operation error IAM: ListRoles, api error AccessDenied: User: arn:aws:sts::111122223333:assumed-role/moonwalk/s is not authorized to perform: iam:ListRoles on resource: arn:aws:iam::111122223333:role/"}}` + "\n" +
		`{"@level":"error","type":"diagnostic","diagnostic":{"severity":"error","summary":"Error Listing Remote Resources","detail":"is not authorized to perform: ec2:DescribeVpcs with an explicit deny"}}` + "\n"
	_, warnings, err := runCLIWithWarnings(t, listing, "graph", "cloud=-")
	if err != nil || !strings.Contains(warnings, "were refused 2 actions: ec2:DescribeVpcs, iam:ListRoles.") {
		t.Errorf("err %v, warnings %q", err, warnings)
	}
}

// Without an account, the policy's resources are open where the account
// goes. That is never written unasked: it takes a yes at the terminal, or
// -any-account where there is no terminal.
func TestPolicyForNoAccountIsAgreedTo(t *testing.T) {
	args := []string{"aws-iam-policy", "-type", "aws_vpc"}
	const open, limited = `"arn:aws:ec2:*:*:vpc/*"`, `"arn:aws:ec2:*:111122223333:vpc/*"`

	// With an account there is nothing to ask.
	out, warnings, err := runCLIWithWarnings(t, "", append(args, "-account", "111122223333")...)
	if err != nil || !strings.Contains(out, limited) || strings.Contains(warnings, "-account") {
		t.Errorf("with an account (err %v, warnings %q):\n%s", err, warnings, out)
	}

	// In a script: an error that names both ways out, and no policy.
	out, _, err = runCLIWithWarnings(t, "", args...)
	if err == nil || out != "" || !strings.Contains(err.Error(), "-account") || !strings.Contains(err.Error(), "-any-account") {
		t.Errorf("no account and no terminal (err %v):\n%s", err, out)
	}
	// The same where standard input is the schema: it cannot also be asked.
	asked := isTerminal
	defer func() { isTerminal = asked }()
	isTerminal = func(io.Reader) bool { return true }
	const schema = `{"format_version":"1.0","provider_schemas":{"registry.terraform.io/hashicorp/aws":{"list_resource_schemas":{
	  "aws_vpc":{"block":{"attributes":{"region":{"type":"string","optional":true}}}}}}}}`
	if out, err := runCLI(t, schema, "aws-iam-policy", "-"); err == nil || out != "" {
		t.Errorf("the schema on standard input (err %v):\n%s", err, out)
	}

	// At a terminal: asked, with what it means, and written on a yes.
	for answer, written := range map[string]bool{"y\n": true, "YES\n": true, "n\n": false, "\n": false, "": false, "maybe\n": false} {
		out, warnings, err := runCLIWithWarnings(t, answer, args...)
		if !strings.Contains(warnings, "warning: no -account was given") || !strings.Contains(warnings, "[y/N]") {
			t.Errorf("answer %q: not asked: %q", answer, warnings)
		}
		if written != (err == nil && strings.Contains(out, open)) || (!written && (err == nil || out != "")) {
			t.Errorf("answer %q (err %v):\n%s", answer, err, out)
		}
	}
	isTerminal = asked

	// Agreed to beforehand: warned, not asked, and written.
	out, warnings, err = runCLIWithWarnings(t, "", append(args, "-any-account")...)
	if err != nil || !strings.Contains(out, open) || !strings.Contains(warnings, "warning: no -account was given") || strings.Contains(warnings, "[y/N]") {
		t.Errorf("-any-account (err %v, warnings %q):\n%s", err, warnings, out)
	}

	// Only states: their policy names buckets, which have no account in
	// them, so there is nothing to agree to.
	out, warnings, err = runCLIWithWarnings(t, "", "aws-iam-policy", "-state", "s3://example-terraform-states/")
	if err != nil || !strings.Contains(out, "arn:aws:s3:::example-terraform-states") || strings.Contains(warnings, "-account") {
		t.Errorf("states only (err %v, warnings %q):\n%s", err, warnings, out)
	}
}
