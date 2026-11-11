// Package tfcode reads Terraform and OpenTofu configuration: the resource
// blocks of a root module and of the modules it calls, where each one is
// written, and which other blocks its expressions refer to.
//
// It reads structure only. Nothing is evaluated, so no variables, providers
// or credentials are needed.
package tfcode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Block is one resource or data block.
type Block struct {
	// Address is the config address: no instance keys, and prefixed with
	// the module path, as in `module.logs.aws_flow_log.vpc`.
	Address string
	// Module is the module path, or "" for the root module.
	Module string
	Mode   graph.Mode
	Type   string
	Name   string
	// File is relative to the configuration's root directory, with forward
	// slashes. Line and EndLine are the first and last line of the block.
	File    string
	Line    int
	EndLine int
	// Text is the block as written.
	Text string
	// DependsOn lists the addresses of the blocks this one's expressions
	// refer to, directly or through locals, variables and module outputs.
	DependsOn []string
}

// Config is the configuration found under one root directory.
type Config struct {
	Root   string
	Blocks map[string]*Block
	// Unresolved lists the module calls whose source could not be found
	// on disk, such as a registry module before `terraform init`. Their
	// resources are missing from Blocks.
	Unresolved []string
}

// module is one module as called from one place.
type module struct {
	path   string // Config module path, "" for the root.
	parent *module
	args   map[string]hcl.Expression // What the caller passes, by variable name.

	blocks  map[string]*Block          // By local address, such as "aws_vpc.main".
	bodies  map[string]*hclsyntax.Body // Each block's body, for its references.
	locals  map[string]hcl.Expression
	outputs map[string]hcl.Expression
	calls   map[string]*module // By call name; nil if unresolved.
}

// Load reads the configuration whose root module is in dir.
func Load(dir string) (*Config, error) {
	l := &loader{
		root:   dir,
		parser: hclparse.NewParser(),
		cfg:    &Config{Root: dir, Blocks: map[string]*Block{}},
	}
	l.readModulesManifest()
	root, err := l.load(dir, "", nil, nil, 0)
	if err != nil {
		return nil, err
	}
	l.resolve(root)
	sort.Strings(l.cfg.Unresolved)
	return l.cfg, nil
}

type loader struct {
	root     string
	parser   *hclparse.Parser
	cfg      *Config
	manifest map[string]string // Module key ("a.b") to directory, from `terraform init`.
}

// readModulesManifest reads the record `terraform init` keeps of where it
// put each module, which is how registry and git modules are found.
func (l *loader) readModulesManifest() {
	data, err := os.ReadFile(filepath.Join(l.root, ".terraform", "modules", "modules.json"))
	if err != nil {
		return
	}
	var manifest struct {
		Modules []struct{ Key, Dir string }
	}
	if json.Unmarshal(data, &manifest) != nil {
		return
	}
	l.manifest = map[string]string{}
	for _, m := range manifest.Modules {
		if m.Key != "" {
			l.manifest[m.Key] = filepath.Join(l.root, filepath.FromSlash(m.Dir))
		}
	}
}

// maxModuleDepth stops a module that calls itself from recursing forever.
const maxModuleDepth = 20

func (l *loader) load(dir, path string, parent *module, args map[string]hcl.Expression, depth int) (*module, error) {
	m := &module{
		path: path, parent: parent, args: args,
		blocks:  map[string]*Block{},
		bodies:  map[string]*hclsyntax.Body{},
		locals:  map[string]hcl.Expression{},
		outputs: map[string]hcl.Expression{},
		calls:   map[string]*module{},
	}
	files, err := configFiles(dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		f, diags := l.parser.ParseHCL(src, file)
		if diags.HasErrors() {
			return nil, fmt.Errorf("%s", diags.Error())
		}
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		rel, err := filepath.Rel(l.root, file)
		if err != nil {
			rel = file
		}
		for _, b := range body.Blocks {
			switch {
			case (b.Type == "resource" || b.Type == "data") && len(b.Labels) == 2:
				blk := &Block{
					Module:  path,
					Mode:    graph.ModeManaged,
					Type:    b.Labels[0],
					Name:    b.Labels[1],
					File:    filepath.ToSlash(rel),
					Line:    b.Range().Start.Line,
					EndLine: b.Range().End.Line,
					Text:    string(b.Range().SliceBytes(src)),
				}
				local := blk.Type + "." + blk.Name
				if b.Type == "data" {
					blk.Mode = graph.ModeData
					local = "data." + local
				}
				blk.Address = join(path, local)
				m.blocks[local] = blk
				m.bodies[local] = b.Body
				l.cfg.Blocks[blk.Address] = blk
			case b.Type == "locals":
				for name, attr := range b.Body.Attributes {
					m.locals[name] = attr.Expr
				}
			case b.Type == "output" && len(b.Labels) == 1:
				if attr, ok := b.Body.Attributes["value"]; ok {
					m.outputs[b.Labels[0]] = attr.Expr
				}
			case b.Type == "module" && len(b.Labels) == 1:
				name := b.Labels[0]
				childPath := join(path, "module."+name)
				childArgs := map[string]hcl.Expression{}
				for attr, a := range b.Body.Attributes {
					childArgs[attr] = a.Expr
				}
				childDir, found := l.moduleDir(dir, childPath, b.Body)
				if !found || depth >= maxModuleDepth {
					m.calls[name] = nil
					l.cfg.Unresolved = append(l.cfg.Unresolved, childPath)
					continue
				}
				child, err := l.load(childDir, childPath, m, childArgs, depth+1)
				if err != nil {
					return nil, err
				}
				m.calls[name] = child
			}
		}
	}
	return m, nil
}

// moduleDir finds the directory of a module call: the manifest's entry if
// `terraform init` wrote one, else a source given as a relative path.
func (l *loader) moduleDir(callerDir, childPath string, body *hclsyntax.Body) (string, bool) {
	key := strings.ReplaceAll(strings.TrimPrefix(childPath, "module."), ".module.", ".")
	if dir, ok := l.manifest[key]; ok {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, true
		}
	}
	attr, ok := body.Attributes["source"]
	if !ok {
		return "", false
	}
	v, diags := attr.Expr.Value(nil) // A literal string needs no context.
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() || !v.Type().Equals(ctyString) {
		return "", false
	}
	source := v.AsString()
	if !strings.HasPrefix(source, "./") && !strings.HasPrefix(source, "../") {
		return "", false
	}
	dir := filepath.Join(callerDir, filepath.FromSlash(source))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// configFiles lists the configuration files of one module directory, in
// order. Where both x.tofu and x.tf exist, OpenTofu reads only the former,
// and so does this. Override files and the JSON syntax are not read.
func configFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			names[e.Name()] = true
		}
	}
	var files []string
	for name := range names {
		base, ok := strings.CutSuffix(name, ".tf")
		isTofu := false
		if !ok {
			base, isTofu = strings.CutSuffix(name, ".tofu")
			ok = isTofu
		}
		if !ok || strings.HasPrefix(name, ".") || base == "override" || strings.HasSuffix(base, "_override") {
			continue
		}
		if !isTofu && names[base+".tofu"] {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no .tf or .tofu files in %s", dir)
	}
	return files, nil
}

func join(path, local string) string {
	if path == "" {
		return local
	}
	return path + "." + local
}

// resolve fills in DependsOn for every block of m and of the modules below.
func (l *loader) resolve(m *module) {
	for local, blk := range m.blocks {
		found := map[string]bool{}
		for _, t := range traversals(m.bodies[local]) {
			m.follow(t, found, map[hcl.Expression]bool{})
		}
		delete(found, blk.Address) // A block may refer to itself through self-like idioms.
		blk.DependsOn = make([]string, 0, len(found))
		for addr := range found {
			blk.DependsOn = append(blk.DependsOn, addr)
		}
		slices.Sort(blk.DependsOn)
	}
	for _, child := range m.calls {
		if child != nil {
			l.resolve(child)
		}
	}
}

// traversals returns every reference in a block body, nested and dynamic
// blocks included.
func traversals(body *hclsyntax.Body) []hcl.Traversal {
	var out []hcl.Traversal
	for _, attr := range body.Attributes {
		out = append(out, attr.Expr.Variables()...)
	}
	for _, b := range body.Blocks {
		out = append(out, traversals(b.Body)...)
	}
	return out
}

// follow adds to found the blocks that a reference written in module m
// leads to. seen guards against expressions that refer to each other.
func (m *module) follow(t hcl.Traversal, found map[string]bool, seen map[hcl.Expression]bool) {
	names := attributeNames(t)
	if len(names) == 0 {
		return
	}
	expr := func(scope *module, e hcl.Expression) {
		if e == nil || seen[e] {
			return
		}
		seen[e] = true
		for _, inner := range e.Variables() {
			scope.follow(inner, found, seen)
		}
	}
	switch names[0] {
	case "var":
		// A variable's value comes from the call, written in the parent.
		if m.parent != nil && len(names) > 1 {
			expr(m.parent, m.args[names[1]])
		}
	case "local":
		if len(names) > 1 {
			expr(m, m.locals[names[1]])
		}
	case "module":
		if len(names) < 2 {
			return
		}
		child := m.calls[names[1]]
		if child == nil {
			return
		}
		if len(names) > 2 {
			expr(child, child.outputs[names[2]])
			return
		}
		// The module as a whole, as in depends_on: everything in it.
		child.all(found)
	case "data":
		if len(names) > 2 {
			if blk, ok := m.blocks["data."+names[1]+"."+names[2]]; ok {
				found[blk.Address] = true
			}
		}
	default:
		// count, each, self, path, terraform and the like match no block.
		if len(names) > 1 {
			if blk, ok := m.blocks[names[0]+"."+names[1]]; ok {
				found[blk.Address] = true
			}
		}
	}
}

// all adds every block of m and of the modules below it.
func (m *module) all(found map[string]bool) {
	for _, blk := range m.blocks {
		found[blk.Address] = true
	}
	for _, child := range m.calls {
		if child != nil {
			child.all(found)
		}
	}
}

// attributeNames returns the names along a reference, skipping indexes:
// `module.net["a"].ids[0]` gives [module net ids].
func attributeNames(t hcl.Traversal) []string {
	var names []string
	for _, step := range t {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			names = append(names, s.Name)
		case hcl.TraverseAttr:
			names = append(names, s.Name)
		}
	}
	return names
}
