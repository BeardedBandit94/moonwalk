package main

import (
	"os"
	"strings"
	"testing"
)

// Everything moonwalk is built with is credited where a reader would look:
// the NOTICE file, which ships with every release, and the README. A new
// dependency in go.mod fails this until it is named in both.
func TestDependenciesAreCredited(t *testing.T) {
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	gomod := read("../../go.mod")
	credits := map[string]string{"NOTICE": read("../../NOTICE"), "README.md": read("../../README.md")}

	direct := 0
	for _, line := range strings.Split(gomod, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(fields[0], "/") || strings.Contains(line, "// indirect") || fields[0] == "module" {
			continue
		}
		direct++
		// A repository is credited once, whichever of its modules are
		// used: github.com/aws/aws-sdk-go-v2/service/s3 by its repository.
		parts := strings.Split(fields[0], "/")
		for file, text := range credits {
			named := false
			for n := len(parts); n >= 3 && !named; n-- {
				named = strings.Contains(text, strings.Join(parts[:n], "/"))
			}
			if !named {
				t.Errorf("%s does not credit %s", file, fields[0])
			}
		}
	}
	if direct == 0 {
		t.Fatal("found no direct dependencies in go.mod")
	}
}
