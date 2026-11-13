package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// commands is every command that takes options.
var commands = []string{"serve", "view", "graph", "collect", "diff", "query-file", "aws-iam-policy", "azure-role", "gcp-role"}

// acceptedOptions finds out which options each command takes by asking
// it: every option registered anywhere in this package is tried on every
// command, and a command that does not have it says so.
func acceptedOptions(t *testing.T) map[string][]string {
	t.Helper()
	registered := regexp.MustCompile(`\.(?:String|Bool|Int|Var|StringVar|BoolVar|IntVar)\((?:&[\w.]+, )?"([a-z-]+)"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range registered.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(names, m[1]) {
				names = append(names, m[1])
			}
		}
	}
	if len(names) < 25 {
		t.Fatalf("found only %d options in the source: %v", len(names), names)
	}
	accepted := map[string][]string{}
	for _, command := range commands {
		for _, name := range names {
			// Given alone, an option either is unknown to the command or
			// gets it no further than a complaint about what is missing.
			err := run([]string{command, "-" + name}, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				accepted[command] = append(accepted[command], name)
			}
		}
		sort.Strings(accepted[command])
	}
	return accepted
}

// The help that moonwalk prints names every option of every command,
// under a heading that names the command, and no option a command lacks.
func TestUsageNamesEveryOption(t *testing.T) {
	heading := regexp.MustCompile(`^Options for (.+):$`)
	options := regexp.MustCompile(`^  (-[a-z-]+(?:, -[a-z-]+)*)(?:\s|$)`)
	inUsage := map[string][]string{}
	var current []string
	for _, line := range strings.Split(usage, "\n") {
		if m := heading.FindStringSubmatch(line); m != nil {
			current = strings.FieldsFunc(strings.ReplaceAll(m[1], " and ", ","), func(r rune) bool { return r == ',' || r == ' ' })
			for _, c := range current {
				if !slices.Contains(commands, c) {
					t.Errorf("the help has options for %q, which is not a command", c)
				}
			}
			continue
		}
		if m := options.FindStringSubmatch(line); m != nil {
			for _, name := range strings.Split(m[1], ", ") {
				for _, c := range current {
					inUsage[c] = append(inUsage[c], strings.TrimPrefix(name, "-"))
				}
			}
		}
	}
	for command, want := range acceptedOptions(t) {
		got := inUsage[command]
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the help names %v, and the command takes %v", command, got, want)
		}
	}
}

// The README's command reference has a section for every command, with
// the same synopsis as the help and a row for every option it takes.
func TestREADMEDocumentsEveryOption(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	start := strings.Index(readme, "\n## Command reference\n")
	if start < 0 {
		t.Fatal("the README has no command reference")
	}
	reference := readme[start+1:]
	if end := strings.Index(reference[1:], "\n## "); end >= 0 {
		reference = reference[:end+1]
	}

	heading := regexp.MustCompile("^### `moonwalk ([a-z-]+)`$")
	row := regexp.MustCompile("^\\| `-([a-z-]+)[ `]")
	documented := map[string][]string{}
	var sections []string
	current := ""
	for _, line := range strings.Split(reference, "\n") {
		if m := heading.FindStringSubmatch(line); m != nil {
			current = m[1]
			sections = append(sections, current)
		} else if strings.HasPrefix(line, "#") {
			current = ""
		} else if m := row.FindStringSubmatch(line); m != nil && current != "" {
			documented[current] = append(documented[current], m[1])
		}
	}
	if want := append(slices.Clone(commands), "version"); !reflect.DeepEqual(sections, want) {
		t.Errorf("the reference has sections for %v, want %v", sections, want)
	}
	for command, want := range acceptedOptions(t) {
		got := documented[command]
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the README documents %v, and the command takes %v", command, got, want)
		}
	}

	// The synopsis of each command, as the help has it.
	synopsis := regexp.MustCompile(`(?m)^  (moonwalk \S+.*)$`)
	var lines []string
	for _, m := range synopsis.FindAllStringSubmatch(usage[:strings.Index(usage, "\n\n")], -1) {
		lines = append(lines, strings.Join(strings.Fields(m[1]), " "))
	}
	if len(lines) != len(commands)+1 {
		t.Fatalf("the help has %d commands: %v", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.Contains(reference, "\n"+line+"\n") {
			t.Errorf("the reference does not have the synopsis %q", line)
		}
	}
}
