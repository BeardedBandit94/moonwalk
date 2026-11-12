package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BeardedBandit94/moonwalk/internal/s3state"
)

// input is one file to load: where it is and what to call it.
type input struct {
	source string
	path   string // "-" means standard input, and s3://bucket/key an object in S3.
}

// sources is where inputs are read from besides the disk.
type sources struct {
	stdin io.Reader
	s3    s3state.Store
	// fetched holds what fetch has read ahead, by path.
	fetched map[string][]byte
}

// read returns the bytes of the input.
func (from *sources) read(in input) ([]byte, error) {
	if data, ok := from.fetched[in.path]; ok {
		delete(from.fetched, in.path) // Held once, by whoever reads it.
		return data, nil
	}
	if in.path == "-" {
		return io.ReadAll(from.stdin)
	}
	if loc, ok, err := s3state.Parse(in.path); ok {
		if err != nil {
			return nil, err
		}
		return from.s3.Read(context.Background(), loc)
	}
	return os.ReadFile(in.path)
}

// fetch reads ahead the inputs that are in S3, several at a time: a bucket
// of states is hundreds of small objects, and one after another they would
// take as many round trips.
func (from *sources) fetch(inputs []input) error {
	type result struct {
		path string
		data []byte
		err  error
	}
	var remote []input
	for _, in := range inputs {
		if isRemote(in.path) {
			remote = append(remote, in)
		}
	}
	if len(remote) < 2 {
		return nil
	}
	work, results := make(chan input), make(chan result)
	var wg sync.WaitGroup
	for range min(8, len(remote)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for in := range work {
				data, err := from.read(in)
				results <- result{in.path, data, err}
			}
		}()
	}
	go func() {
		for _, in := range remote {
			work <- in
		}
		close(work)
		wg.Wait()
		close(results)
	}()
	fetched := map[string][]byte{}
	var first error
	for r := range results {
		if r.err != nil && first == nil {
			first = r.err
		}
		fetched[r.path] = r.data
	}
	if first != nil {
		return first
	}
	from.fetched = fetched
	return nil
}

// isRemote reports whether a path names something that is not on disk
// and not standard input.
func isRemote(path string) bool {
	return strings.HasPrefix(path, s3state.Scheme)
}

// resolveInputs expands the command-line arguments into the states to load,
// and rejects a source name that would be used twice.
func resolveInputs(args []string, from *sources) ([]input, error) {
	var inputs []input
	seen := map[string]bool{}
	for _, arg := range args {
		name, path := splitName(arg)
		found, err := expand(name, path, from)
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
	if arg == "-" || isRemote(arg) {
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

// expand turns one path into inputs: itself, or for a directory or a
// prefix in S3 every .tfstate file below it.
func expand(name, path string, from *sources) ([]input, error) {
	if path == "-" {
		if name == "" {
			name = "stdin"
		}
		return []input{{name, path}}, nil
	}
	if loc, ok, err := s3state.Parse(path); ok {
		if err != nil {
			return nil, err
		}
		if name == "" {
			name = path
		}
		if !loc.IsPrefix() {
			return []input{{name, path}}, nil
		}
		keys, err := from.s3.Keys(context.Background(), loc)
		if err != nil {
			return nil, err
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("no .tfstate objects found under %s", loc)
		}
		var inputs []input
		for _, key := range keys {
			source := strings.TrimSuffix(name, "/") + "/" + strings.TrimPrefix(key, loc.Key)
			inputs = append(inputs, input{source, s3state.Location{Bucket: loc.Bucket, Key: key}.String()})
		}
		return inputs, nil
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
