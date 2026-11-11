package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

// input is one state to load: where it is and what to call it.
type input struct {
	source string
	path   string // "-" means standard input.
}

func (in input) load(g *graph.Graph, stdin io.Reader) error {
	if in.path == "-" {
		return tfstate.Load(g, in.source, stdin)
	}
	f, err := os.Open(in.path)
	if err != nil {
		return err
	}
	defer f.Close()
	return tfstate.Load(g, in.source, f)
}

// resolveInputs expands the command-line arguments into the states to load,
// and rejects a source name that would be used twice.
func resolveInputs(args []string) ([]input, error) {
	var inputs []input
	seen := map[string]bool{}
	for _, arg := range args {
		name, path := splitName(arg)
		found, err := expand(name, path)
		if err != nil {
			return nil, err
		}
		for _, in := range found {
			if seen[in.source] {
				return nil, fmt.Errorf("source name %q is used more than once", in.source)
			}
			seen[in.source] = true
			inputs = append(inputs, in)
		}
	}
	return inputs, nil
}

// splitName separates an optional "name=" prefix from a path. An argument
// that exists on disk as written is always a path, so a directory such as
// "env=prod" still works.
func splitName(arg string) (name, path string) {
	if arg == "-" {
		return "", arg
	}
	if _, err := os.Stat(arg); err == nil {
		return "", arg
	}
	if i := strings.Index(arg, "="); i > 0 {
		return arg[:i], arg[i+1:]
	}
	return "", arg
}

// expand turns one path into inputs: itself, or for a directory every
// .tfstate file below it.
func expand(name, path string) ([]input, error) {
	if path == "-" {
		if name == "" {
			name = "stdin"
		}
		return []input{{name, path}}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = path
	}
	if !info.IsDir() {
		return []input{{name, path}}, nil
	}

	var inputs []input
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Hidden directories hold tool metadata, not state: .terraform
			// keeps a backend stub that is named like a state file.
			if p != path && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".tfstate") {
			rel, err := filepath.Rel(path, p)
			if err != nil {
				return err
			}
			source := strings.TrimSuffix(name, "/") + "/" + filepath.ToSlash(rel)
			inputs = append(inputs, input{source, p})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no .tfstate files found in %s", path)
	}
	return inputs, nil
}
