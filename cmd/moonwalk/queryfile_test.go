package main

import (
	"strings"
	"testing"
)

// schema is the shape of `terraform providers schema -json`, cut down to
// two providers: one that can list four types, one that can list none.
const schema = `{"format_version":"1.0","provider_schemas":{
  "registry.terraform.io/hashicorp/aws":{
    "resource_schemas":{"aws_vpc":{}},
    "list_resource_schemas":{
      "aws_vpc":{"block":{"attributes":{"region":{"type":"string","optional":true}},"block_types":{"filter":{"nesting_mode":"list"}}}},
      "aws_iam_role":{"block":{"attributes":{}}},
      "aws_cloudwatch_log_stream":{"block":{"attributes":{"log_group_name":{"type":"string","required":true},"region":{"type":"string","optional":true}}}},
      "aws_thing_with_block":{"block":{"attributes":{},"block_types":{"parent":{"nesting_mode":"list","min_items":1}}}}}},
  "registry.terraform.io/hashicorp/random":{"resource_schemas":{"random_id":{}}}}}`

func TestQueryFile(t *testing.T) {
	out, err := runCLI(t, schema, "query-file", "-region", "us-east-1,eu-west-1", "-")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "query-file.golden", out)

	for _, want := range []string{
		`list "aws_vpc" "us_east_1"`, `list "aws_vpc" "eu_west_1"`, `region = "eu-west-1"`,
		// A type with no region of its own is listed once, wherever it is asked for.
		`list "aws_iam_role" "all"`,
		"limit            = 1000000",
		"aws_cloudwatch_log_stream, aws_thing_with_block", // Named as left out.
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `list "aws_cloudwatch_log_stream"`) || strings.Contains(out, `list "aws_thing_with_block"`) {
		t.Error("a type that needs a parent argument was given a list block")
	}
	if n := strings.Count(out, "\nlist "); n != 3 {
		t.Errorf("%d list blocks, want 3", n)
	}
}

func TestQueryFileOptions(t *testing.T) {
	out, err := runCLI(t, schema, "query-file", "-type", "aws_iam_*", "-identity-only", "-")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\nlist ") != 1 || !strings.Contains(out, `list "aws_iam_role" "all"`) || strings.Contains(out, "include_resource") {
		t.Errorf("type pattern with identity only:\n%s", out)
	}
	// Without regions each type is listed once, in the provider's own.
	out, err = runCLI(t, schema, "query-file", "-")
	if err != nil || strings.Count(out, "\nlist ") != 2 || strings.Contains(out, "config {") {
		t.Errorf("no regions: %v\n%s", err, out)
	}

	for name, args := range map[string][]string{
		"a provider that lists nothing": {"query-file", "-provider", "random", "-"},
		"no type matches":               {"query-file", "-type", "azurerm_*", "-"},
		"no schema argument":            {"query-file"},
		"a missing file":                {"query-file", "no-such-schema.json"},
	} {
		if _, err := runCLI(t, schema, args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := runCLI(t, "not json", "query-file", "-"); err == nil {
		t.Error("garbage was accepted as a schema")
	}
}
