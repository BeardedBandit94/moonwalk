package tfaddr

import (
	"reflect"
	"testing"
)

func TestConfigAddress(t *testing.T) {
	cases := map[string]string{
		`aws_vpc.main`:                        `aws_vpc.main`,
		`aws_subnet.s[3]`:                     `aws_subnet.s`,
		`module.a["x"].module.b[0].aws_s3.b`:  `module.a.module.b.aws_s3.b`,
		`aws_s3.b["we]ird.\"key"]`:            `aws_s3.b`,
		`module.m["a.b"].data.aws_ami.u["k"]`: `module.m.data.aws_ami.u`,
	}
	for in, want := range cases {
		if got := ConfigAddress(in); got != want {
			t.Errorf("ConfigAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlock(t *testing.T) {
	cases := []struct {
		in, block string
		keyed     bool
	}{
		{`aws_vpc.main`, `aws_vpc.main`, false},
		{`aws_subnet.s[3]`, `aws_subnet.s`, true},
		{`module.a["x"].aws_subnet.s`, `module.a["x"].aws_subnet.s`, false},
		{`module.a["x"].aws_subnet.s["k]"]`, `module.a["x"].aws_subnet.s`, true},
		{`aws_subnet.s[`, `aws_subnet.s`, true},
	}
	for _, c := range cases {
		block, keyed := Block(c.in)
		if block != c.block || keyed != c.keyed {
			t.Errorf("Block(%q) = %q, %v; want %q, %v", c.in, block, keyed, c.block, c.keyed)
		}
	}
}

func TestModuleSteps(t *testing.T) {
	cases := map[string][]ModuleStep{
		``:                            nil,
		`module.a`:                    {{"a", ""}},
		`module.a["x.y"].module.b[0]`: {{"a", `["x.y"]`}, {"b", "[0]"}},
		`module.a["]\""].module.b`:    {{"a", `["]\""]`}, {"b", ""}},
	}
	for in, want := range cases {
		if got := ModuleSteps(in); !reflect.DeepEqual(got, want) {
			t.Errorf("ModuleSteps(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModuleNames(t *testing.T) {
	if got, want := ModuleNames("module.a.module.b.data.aws_ami.u"), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ModuleNames = %q, want %q", got, want)
	}
	if got := ModuleNames("aws_vpc.main"); got != nil {
		t.Errorf("ModuleNames for a root resource = %q", got)
	}
}
