package awsiam

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// small is a made-up set of permissions that shows every case: a regional
// service with an action that can be limited and one that cannot, and a
// global service.
func small() *Permissions {
	return &Permissions{
		Provider: "test",
		Types: map[string]TypePermissions{
			"aws_lambda_function": {List: []string{"lambda:ListFunctions"}, Read: []string{"lambda:GetFunction", "lambda:ListVersionsByFunction"}},
			"aws_vpc":             {List: []string{"ec2:DescribeVpcs"}, Read: []string{"ec2:DescribeVpcAttribute"}},
			"aws_iam_role":        {List: []string{"iam:ListRoles"}, Read: []string{"iam:GetRole"}},
		},
		Actions: map[string]Action{
			"lambda:ListFunctions":          {Level: "List"},
			"lambda:GetFunction":            {Level: "Read", Resources: []string{"arn:${Partition}:lambda:${Region}:${Account}:function:${FunctionName}"}},
			"lambda:ListVersionsByFunction": {Level: "List", Resources: []string{"arn:${Partition}:lambda:${Region}:${Account}:function:${FunctionName}"}},
			"ec2:DescribeVpcs":              {Level: "List"},
			"ec2:DescribeVpcAttribute":      {Level: "Read", Resources: []string{"arn:${Partition}:ec2:${Region}:${Account}:vpc/${VpcId}"}},
			"iam:ListRoles":                 {Level: "List"},
			"iam:GetRole":                   {Level: "Read", Resources: []string{"arn:${Partition}:iam::${Account}:role/${RoleNameWithPath}"}},
		},
	}
}

var all = []string{"aws_iam_role", "aws_lambda_function", "aws_vpc"}

func TestPolicies(t *testing.T) {
	docs, plan, err := small().Policies(Options{
		Types: append([]string{"aws_new_thing"}, all...), Regions: []string{"eu-west-1", "us-east-1"},
		Global: map[string]bool{"aws_iam_role": true}, Account: "123456789012", Allow: []string{"lambda:GetFunction"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Unknown, []string{"aws_new_thing"}) || !reflect.DeepEqual(plan.Full, all) {
		t.Errorf("plan = %+v", plan)
	}
	regions := map[string]map[string][]string{"StringEquals": {"aws:RequestedRegion": {"eu-west-1", "us-east-1"}}}
	want := []Document{{Version: "2012-10-17", Statement: []Statement{
		// What cannot be limited to a resource applies to all of them...
		{Sid: "MoonwalkEc2InRegions", Effect: "Allow", Action: []string{"ec2:DescribeVpcs"}, Resource: []string{"*"}, Condition: regions},
		// ...and what can is limited to its kind, in the one account.
		{Sid: "MoonwalkEc2LimitedInRegions", Effect: "Allow", Action: []string{"ec2:DescribeVpcAttribute"}, Resource: []string{"arn:aws:ec2:*:123456789012:vpc/*"}, Condition: regions},
		// A global service answers from one region whichever are asked for.
		{Sid: "MoonwalkIam", Effect: "Allow", Action: []string{"iam:ListRoles"}, Resource: []string{"*"}},
		{Sid: "MoonwalkIamLimited", Effect: "Allow", Action: []string{"iam:GetRole"}, Resource: []string{"arn:aws:iam::123456789012:role/*"}},
		{Sid: "MoonwalkLambdaInRegions", Effect: "Allow", Action: []string{"lambda:ListFunctions"}, Resource: []string{"*"}, Condition: regions},
		{Sid: "MoonwalkLambdaLimitedInRegions", Effect: "Allow", Action: []string{"lambda:GetFunction", "lambda:ListVersionsByFunction"}, Resource: []string{"arn:aws:lambda:*:123456789012:function:*"}, Condition: regions},
	}}}
	if !reflect.DeepEqual(docs, want) {
		got, _ := json.MarshalIndent(docs, "", "  ")
		t.Errorf("got\n%s", got)
	}
}

func TestIdentityOnlyLeavesOutReads(t *testing.T) {
	docs, _, err := small().Policies(Options{Types: all, IdentityOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, d := range docs {
		for _, s := range d.Statement {
			actions = append(actions, s.Action...)
			if s.Condition != nil || !reflect.DeepEqual(s.Resource, []string{"*"}) {
				t.Errorf("%s: no regions were asked for and no list action here can be limited: %+v", s.Sid, s)
			}
		}
	}
	if !reflect.DeepEqual(actions, []string{"ec2:DescribeVpcs", "iam:ListRoles", "lambda:ListFunctions"}) {
		t.Errorf("actions = %v", actions)
	}
}

// Without an account, what can be limited is still limited to its kind,
// and in another partition the ARNs say so.
func TestResourcesWithoutAccountAndInAnotherPartition(t *testing.T) {
	docs, _, err := small().Policies(Options{Types: []string{"aws_lambda_function"}, Partition: "aws-us-gov", Allow: []string{"lambda:GetFunction"}})
	if err != nil {
		t.Fatal(err)
	}
	got := docs[0].Statement[1].Resource
	if !reflect.DeepEqual(got, []string{"arn:aws-us-gov:lambda:*:*:function:*"}) {
		t.Errorf("resources = %v", got)
	}
	if _, _, err := small().Policies(Options{Types: all, Account: "1234"}); err == nil {
		t.Error("an account ID that is not 12 digits was accepted")
	}
}

// An action that hands over what a service stores is in no policy unless
// it is asked for by name. A type that cannot be listed without one is
// left out; one that only reads its attributes with it is listed without
// them.
func TestWithheldActions(t *testing.T) {
	p := small()
	p.Types["aws_ssm_parameter"] = TypePermissions{List: []string{"ssm:DescribeParameters"}, Read: []string{"ssm:GetParameter", "ssm:ListTagsForResource"}}
	p.Types["aws_s3_bucket"] = TypePermissions{List: []string{"s3:ListAllMyBuckets", "s3:ListBucket"}}
	for a, level := range map[string]string{"ssm:DescribeParameters": "List", "ssm:GetParameter": "Read", "ssm:ListTagsForResource": "Read", "s3:ListAllMyBuckets": "List", "s3:ListBucket": "List"} {
		p.Actions[a] = Action{Level: level}
	}
	types := []string{"aws_lambda_function", "aws_s3_bucket", "aws_ssm_parameter", "aws_vpc"}
	actions := func(o Options) []string {
		docs, _, err := p.Policies(o)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range docs {
			for _, s := range d.Statement {
				out = append(out, s.Action...)
			}
		}
		return out
	}

	plan, err := p.Plan(Options{Types: types})
	if err != nil {
		t.Fatal(err)
	}
	want := Plan{
		Full:            []string{"aws_vpc"},
		Identity:        []string{"aws_lambda_function", "aws_ssm_parameter"},
		IdentityBecause: map[string]string{"aws_lambda_function": "lambda:GetFunction", "aws_ssm_parameter": "ssm:GetParameter"},
		LeftOut:         map[string]string{"aws_s3_bucket": "s3:ListBucket"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan = %+v", plan)
	}
	got := actions(Options{Types: types})
	if !reflect.DeepEqual(got, []string{"ec2:DescribeVpcs", "ec2:DescribeVpcAttribute", "lambda:ListFunctions", "ssm:DescribeParameters"}) {
		t.Errorf("actions without any allowed = %v", got)
	}
	for _, a := range got {
		if _, withheld := Withheld[a]; withheld {
			t.Errorf("%s is in the policy without having been asked for", a)
		}
	}

	// Each is added by name, and only the one named.
	got = actions(Options{Types: types, Allow: []string{"ssm:GetParameter"}})
	if !slices.Contains(got, "ssm:GetParameter") || !slices.Contains(got, "ssm:ListTagsForResource") || slices.Contains(got, "lambda:GetFunction") || slices.Contains(got, "s3:ListBucket") {
		t.Errorf("actions with ssm:GetParameter allowed = %v", got)
	}
	// With identities only, there is nothing to read attributes with.
	plan, _ = p.Plan(Options{Types: types, IdentityOnly: true})
	if !reflect.DeepEqual(plan.Identity, []string{"aws_lambda_function", "aws_ssm_parameter", "aws_vpc"}) || len(plan.IdentityBecause) != 0 || len(plan.LeftOut) != 1 {
		t.Errorf("identity-only plan = %+v", plan)
	}
	if _, err := p.Plan(Options{Types: types, Allow: []string{"s3:GetObject"}}); err == nil {
		t.Error("an action that is not on the list was accepted as allowed")
	}
}

func TestFill(t *testing.T) {
	cases := map[string]string{
		"arn:${Partition}:s3:::${BucketName}":                                               "arn:aws:s3:::*",
		"arn:${Partition}:s3:::${BucketName}/${ObjectName}":                                 "arn:aws:s3:::*",
		"arn:${Partition}:iam::${Account}:role/${RoleNameWithPath}":                         "arn:aws:iam::111122223333:role/*",
		"arn:${Partition}:ecs:${Region}:${Account}:service/${Cluster}/${Service}":           "arn:aws:ecs:*:111122223333:service/*",
		"arn:${Partition}:logs:${Region}:${Account}:log-group:${Name}:log-stream:${Stream}": "arn:aws:logs:*:111122223333:log-group:*:log-stream:*",
	}
	for format, want := range cases {
		if got := fill(format, "aws", "111122223333"); got != want {
			t.Errorf("fill(%q) = %q, want %q", format, got, want)
		}
	}
}

// IAM accepts 6,144 characters in a managed policy. More than that is
// spread over several documents, and a statement too large for one is
// divided, with nothing lost.
func TestPoliciesAreSplitToFit(t *testing.T) {
	p := &Permissions{Types: map[string]TypePermissions{}, Actions: map[string]Action{}}
	var types []string
	want := map[string]bool{}
	for s := 0; s < 4; s++ {
		for i := 0; i < 150; i++ {
			name := fmt.Sprintf("aws_service%d_thing%03d", s, i)
			action := fmt.Sprintf("service%d:DescribeAVeryLongNamedThingNumber%03d", s, i)
			p.Types[name] = TypePermissions{List: []string{action}}
			p.Actions[action] = Action{Level: "List"}
			types = append(types, name)
			want[action] = true
		}
	}
	docs, _, err := p.Policies(Options{Types: types})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) < 4 {
		t.Fatalf("%d documents for 600 long actions", len(docs))
	}
	for i, d := range docs {
		if Size(d) > MaxSize {
			t.Errorf("document %d is %d characters", i+1, Size(d))
		}
		sids := map[string]bool{} // Unique within a document, as IAM requires.
		for _, s := range d.Statement {
			if sids[s.Sid] {
				t.Errorf("document %d uses the statement ID %s twice", i+1, s.Sid)
			}
			sids[s.Sid] = true
			for _, a := range s.Action {
				if !want[a] {
					t.Errorf("%s appears twice or was never asked for", a)
				}
				delete(want, a)
			}
		}
	}
	if len(want) != 0 {
		t.Errorf("%d actions were lost in the split", len(want))
	}
}

func TestStatePolicy(t *testing.T) {
	key := "arn:aws:kms:eu-west-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	d, err := StatePolicy([]StateLocation{
		{Bucket: "acme-states", Key: "live/"},
		{Bucket: "acme-states", Key: "env:/"},
		{Bucket: "acme-legacy", Key: "network/terraform.tfstate"},
		{Bucket: "acme-sandbox", Key: ""},
	}, []string{key}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := Document{Version: "2012-10-17", Statement: []Statement{
		// A whole bucket is listed without conditions.
		{Sid: "MoonwalkListStates2", Effect: "Allow", Action: []string{"s3:ListBucket"}, Resource: []string{"arn:aws:s3:::acme-sandbox"}},
		// Under prefixes, only those prefixes.
		{Sid: "MoonwalkListStates3", Effect: "Allow", Action: []string{"s3:ListBucket"}, Resource: []string{"arn:aws:s3:::acme-states"},
			Condition: map[string]map[string][]string{"StringLike": {"s3:prefix": {"env:/*", "live/*"}}}},
		// One object is read by name, and under a prefix only states are.
		{Sid: "MoonwalkReadStates", Effect: "Allow", Action: []string{"s3:GetObject"}, Resource: []string{
			"arn:aws:s3:::acme-legacy/network/terraform.tfstate",
			"arn:aws:s3:::acme-sandbox/*.tfstate",
			"arn:aws:s3:::acme-states/env:/*.tfstate",
			"arn:aws:s3:::acme-states/live/*.tfstate",
		}},
		// The key decrypts for S3 and nothing else.
		{Sid: "MoonwalkDecryptStates1", Effect: "Allow", Action: []string{"kms:Decrypt"}, Resource: []string{key},
			Condition: map[string]map[string][]string{"StringEquals": {"kms:ViaService": {"s3.eu-west-1.amazonaws.com"}}}},
	}}
	if !reflect.DeepEqual(d, want) {
		got, _ := json.MarshalIndent(d, "", "  ")
		t.Errorf("got\n%s", got)
	}

	for name, call := range map[string]func() error{
		"no locations": func() error { _, err := StatePolicy(nil, nil, ""); return err },
		"a wildcard":   func() error { _, err := StatePolicy([]StateLocation{{Bucket: "acme-*", Key: ""}}, nil, ""); return err },
		"a key alias": func() error {
			_, err := StatePolicy([]StateLocation{{Bucket: "b", Key: "k"}}, []string{"arn:aws:kms:eu-west-1:123456789012:alias/states"}, "")
			return err
		},
	} {
		if call() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// What ships with moonwalk, whatever it was recorded from, keeps to the
// rules: every action named in full, every one of them reading and none
// writing, and none of them an action that reads what is stored in a
// service, as opposed to how it is set up.
func TestShippedPermissionsKeepToTheRules(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Types) < 100 || p.Provider == "" || p.Catalog == "" {
		t.Fatalf("%d types from %q, checked against %q", len(p.Types), p.Provider, p.Catalog)
	}
	explicit := regexp.MustCompile(`^[a-z0-9-]+:[A-Za-z0-9]+$`)
	used := map[string]bool{}
	for name, tp := range p.Types {
		if len(tp.List) == 0 {
			t.Errorf("%s has no list action", name)
		}
		for _, a := range append(append([]string{}, tp.List...), tp.Read...) {
			used[a] = true
			if _, ok := p.Actions[a]; !ok {
				t.Errorf("%s uses %s, which is not described", name, a)
			}
		}
	}
	for a, info := range p.Actions {
		if !explicit.MatchString(a) {
			t.Errorf("%q is not one action named in full", a)
		}
		if info.Level != "List" && info.Level != "Read" {
			t.Errorf("%s is of access level %q", a, info.Level)
		}
		if !used[a] {
			t.Errorf("%s is described and never used", a)
		}
		for _, r := range info.Resources {
			if !strings.HasPrefix(r, "arn:${Partition}:") {
				t.Errorf("%s: %q is not an ARN format", a, r)
			}
		}
		// What reads stored data is recorded, since the provider calls
		// it, and must be on the list of what is withheld by default.
		if _, withheld := Withheld[a]; (dataReading[a] || info.DataPlane) && !withheld {
			t.Errorf("%s reads what a service stores and is not withheld", a)
		}
	}
	for a := range Withheld {
		if _, ok := p.Actions[a]; !ok {
			t.Errorf("%s is withheld and nothing uses it", a)
		}
	}
}

// With nothing allowed, no policy made from what ships holds a withheld
// action, an action with a wildcard, or a statement that does not allow.
func TestShippedPoliciesHoldNothingWithheld(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for name := range p.Types {
		types = append(types, name)
	}
	docs, _, err := p.Policies(Options{Types: types, Account: "123456789012", Regions: []string{"us-east-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 || len(docs) > 10 {
		t.Errorf("%d documents; a role takes 10 managed policies", len(docs))
	}
	for i, d := range docs {
		if Size(d) > MaxSize {
			t.Errorf("document %d is %d characters", i+1, Size(d))
		}
		for _, s := range d.Statement {
			if s.Effect != "Allow" || len(s.Action) == 0 || len(s.Resource) == 0 {
				t.Errorf("%s: %+v", s.Sid, s)
			}
			for _, a := range s.Action {
				if _, withheld := Withheld[a]; withheld || strings.ContainsAny(a, "*?") {
					t.Errorf("%s holds %s", s.Sid, a)
				}
			}
			// "*" alone is for the actions AWS lets nobody limit, and for
			// no statement that says it limits.
			if strings.Contains(s.Sid, "Limited") == slices.Contains(s.Resource, "*") {
				t.Errorf("%s has the resources %v", s.Sid, s.Resource)
			}
		}
	}
}

// dataReading lists actions that AWS classes as Read and that hand over
// what a service stores or protects. None may ever be in a discovery policy.
var dataReading = map[string]bool{
	"s3:GetObject": true, "s3:GetObjectVersion": true,
	"secretsmanager:GetSecretValue": true, "secretsmanager:BatchGetSecretValue": true,
	"ssm:GetParameter": true, "ssm:GetParameters": true, "ssm:GetParametersByPath": true, "ssm:GetParameterHistory": true,
	"kms:Decrypt": true, "kms:GenerateDataKey": true,
	"dynamodb:GetItem": true, "dynamodb:BatchGetItem": true, "dynamodb:Query": true, "dynamodb:Scan": true,
	"logs:GetLogEvents": true, "logs:FilterLogEvents": true, "logs:StartQuery": true, "logs:GetQueryResults": true,
	"sqs:ReceiveMessage": true, "kinesis:GetRecords": true,
	"ecr:BatchGetImage": true, "ecr:GetDownloadUrlForLayer": true, "ecr:GetAuthorizationToken": true,
	"codecommit:GitPull": true, "codecommit:GetFile": true, "codecommit:GetBlob": true,
	"athena:GetQueryResults": true, "rds-data:ExecuteStatement": true,
	"sts:AssumeRole": true, "iam:PassRole": true,
	"lambda:InvokeFunction": true, "ec2:GetPasswordData": true, "ec2:GetConsoleOutput": true, "ec2:GetConsoleScreenshot": true,
	"cognito-idp:AdminGetUser": true, "appsync:GetApiCache": true,
}
