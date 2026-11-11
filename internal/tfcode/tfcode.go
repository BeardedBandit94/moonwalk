// Package tfcode reads Terraform and OpenTofu configuration: the resource
// blocks of a root module and of the modules it calls, where each one is
// written, and which other blocks its expressions refer to.
//
// It reads structure only. Nothing is evaluated, so no variables, providers
// or credentials are needed. Both the native syntax and the JSON syntax are
// read, and override files are applied on top of the blocks they change.
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
	// References says where each of those comes from, ordered by attribute.
	References []Reference
}

// Reference is one place in a block where another block is referred to.
type Reference struct {
	// Attribute is the argument the reference is written in. Inside a
	// nested block it is prefixed with the block's name, as in
	// "ingress.security_groups".
	Attribute string `json:"attribute"`
	// Written is the reference as it stands in the code, such as
	// "local.network_id" or "aws_vpc.main.id".
	Written string `json:"written"`
	// To is the address of the block it leads to.
	To string `json:"to"`
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

	blocks  map[string]*Block      // By local address, such as "aws_vpc.main".
	exprs   map[string][]namedExpr // Each block's expressions, for its references.
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

// namedExpr is an expression and the argument it is written in. Inside a
// nested block the name is prefixed with the block's, as in
// "ingress.security_groups".
type namedExpr struct {
	attribute string
	expr      hcl.Expression
}

// item is one top-level block of a configuration file, in a form that does
// not depend on which syntax the file is written in.
type item struct {
	kind   string // resource, data, locals, output or module.
	labels []string
	exprs  []namedExpr
	// Where it is written. EndLine and text are unknown for the JSON syntax.
	file          string
	line, endLine int
	text          string
}

func (l *loader) load(dir, path string, parent *module, args map[string]hcl.Expression, depth int) (*module, error) {
	m := &module{
		path: path, parent: parent, args: args,
		blocks:  map[string]*Block{},
		exprs:   map[string][]namedExpr{},
		locals:  map[string]hcl.Expression{},
		outputs: map[string]hcl.Expression{},
		calls:   map[string]*module{},
	}
	primary, overrides, err := configFiles(dir)
	if err != nil {
		return nil, err
	}
	// Module calls are loaded once every file is read, since an override
	// file may change what a call passes or where its source is.
	callArgs := map[string]map[string]hcl.Expression{}
	var callOrder []string

	for i, file := range append(primary, overrides...) {
		override := i >= len(primary)
		items, err := l.parseFile(file)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			switch it.kind {
			case "resource", "data":
				local := it.labels[0] + "." + it.labels[1]
				mode := graph.ModeManaged
				if it.kind == "data" {
					mode, local = graph.ModeData, "data."+local
				}
				if override {
					// An override changes arguments of a block written
					// elsewhere; on its own it declares nothing.
					if _, ok := m.blocks[local]; ok {
						m.exprs[local] = mergeExprs(m.exprs[local], it.exprs)
					}
					continue
				}
				blk := &Block{
					Address: join(path, local), Module: path, Mode: mode,
					Type: it.labels[0], Name: it.labels[1],
					File: it.file, Line: it.line, EndLine: it.endLine, Text: it.text,
				}
				m.blocks[local] = blk
				m.exprs[local] = it.exprs
				l.cfg.Blocks[blk.Address] = blk
			case "locals":
				for _, e := range it.exprs {
					m.locals[e.attribute] = e.expr
				}
			case "output":
				for _, e := range it.exprs {
					if e.attribute == "value" {
						m.outputs[it.labels[0]] = e.expr
					}
				}
			case "module":
				name := it.labels[0]
				if _, known := callArgs[name]; !known {
					if override {
						continue
					}
					callArgs[name] = map[string]hcl.Expression{}
					callOrder = append(callOrder, name)
				}
				for _, e := range it.exprs {
					callArgs[name][e.attribute] = e.expr
				}
			}
		}
	}

	for _, name := range callOrder {
		childPath := join(path, "module."+name)
		childDir, found := l.moduleDir(dir, childPath, callArgs[name]["source"])
		if !found || depth >= maxModuleDepth {
			m.calls[name] = nil
			l.cfg.Unresolved = append(l.cfg.Unresolved, childPath)
			continue
		}
		child, err := l.load(childDir, childPath, m, callArgs[name], depth+1)
		if err != nil {
			return nil, err
		}
		m.calls[name] = child
	}
	return m, nil
}

// mergeExprs applies an override: its arguments replace those of the same
// name, nested blocks included, and the rest stay.
func mergeExprs(base, override []namedExpr) []namedExpr {
	top := func(attribute string) string {
		name, _, _ := strings.Cut(attribute, ".")
		return name
	}
	replaced := map[string]bool{}
	for _, e := range override {
		replaced[top(e.attribute)] = true
	}
	var out []namedExpr
	for _, e := range base {
		if !replaced[top(e.attribute)] {
			out = append(out, e)
		}
	}
	return append(out, override...)
}

// parseFile reads one configuration file in either syntax.
func (l *loader) parseFile(file string) ([]item, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(l.root, file)
	if err != nil {
		rel = file
	}
	rel = filepath.ToSlash(rel)

	if strings.HasSuffix(file, ".json") {
		f, diags := l.parser.ParseJSON(src, file)
		if diags.HasErrors() {
			return nil, fmt.Errorf("%s", diags.Error())
		}
		// Anything else at the top level (terraform, provider, variable)
		// is of no interest here.
		content, _, diags := f.Body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
			{Type: "resource", LabelNames: []string{"type", "name"}},
			{Type: "data", LabelNames: []string{"type", "name"}},
			{Type: "module", LabelNames: []string{"name"}},
			{Type: "output", LabelNames: []string{"name"}},
			{Type: "locals"},
		}})
		if diags.HasErrors() {
			return nil, fmt.Errorf("%s", diags.Error())
		}
		var items []item
		for _, b := range content.Blocks {
			// In JSON a nested block is an argument holding an object, and
			// an expression reports the references anywhere inside it.
			attrs, _ := b.Body.JustAttributes()
			it := item{kind: b.Type, labels: b.Labels, file: rel, line: b.DefRange.Start.Line, endLine: b.DefRange.Start.Line}
			for _, name := range sortedNames(attrs) {
				it.exprs = append(it.exprs, namedExpr{name, attrs[name].Expr})
			}
			items = append(items, it)
		}
		return items, nil
	}

	f, diags := l.parser.ParseHCL(src, file)
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s", diags.Error())
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return nil, nil
	}
	var items []item
	for _, b := range body.Blocks {
		it := item{
			kind: b.Type, labels: b.Labels, file: rel,
			line: b.Range().Start.Line, endLine: b.Range().End.Line,
			text: string(b.Range().SliceBytes(src)),
		}
		switch {
		case (b.Type == "resource" || b.Type == "data") && len(b.Labels) == 2:
			collect(b.Body, "", &it.exprs)
		case b.Type == "locals", b.Type == "module" && len(b.Labels) == 1, b.Type == "output" && len(b.Labels) == 1:
			for _, name := range sortedNames(b.Body.Attributes) {
				it.exprs = append(it.exprs, namedExpr{name, b.Body.Attributes[name].Expr})
			}
		default:
			continue
		}
		items = append(items, it)
	}
	return items, nil
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// collect gathers every expression in a block body with the name of the
// argument it is written in. Nested blocks prefix their name; a dynamic
// block counts as the block it generates.
func collect(body *hclsyntax.Body, prefix string, into *[]namedExpr) {
	for _, name := range sortedNames(body.Attributes) {
		*into = append(*into, namedExpr{prefix + name, body.Attributes[name].Expr})
	}
	for _, b := range body.Blocks {
		switch {
		case b.Type == "dynamic" && len(b.Labels) == 1:
			collect(b.Body, prefix+b.Labels[0]+".", into)
		case b.Type == "content":
			collect(b.Body, prefix, into) // The body of a dynamic block.
		default:
			collect(b.Body, prefix+b.Type+".", into)
		}
	}
}

// moduleDir finds the directory of a module call: the manifest's entry if
// `terraform init` wrote one, else a source given as a relative path.
func (l *loader) moduleDir(callerDir, childPath string, source hcl.Expression) (string, bool) {
	key := strings.ReplaceAll(strings.TrimPrefix(childPath, "module."), ".module.", ".")
	if dir, ok := l.manifest[key]; ok {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, true
		}
	}
	if source == nil {
		return "", false
	}
	v, diags := source.Value(nil) // A literal string needs no context.
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() || !v.Type().Equals(ctyString) {
		return "", false
	}
	path := v.AsString()
	if !strings.HasPrefix(path, "./") && !strings.HasPrefix(path, "../") {
		return "", false
	}
	dir := filepath.Join(callerDir, filepath.FromSlash(path))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// configSuffixes are the endings of configuration files: the native and
// JSON syntaxes, each as Terraform and as OpenTofu name them.
var configSuffixes = []string{".tofu.json", ".tf.json", ".tofu", ".tf"}

// IsConfigFile reports whether a file name is that of a configuration file.
func IsConfigFile(name string) bool {
	for _, suffix := range configSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// configFiles lists the configuration files of one module directory, each
// group in order: the ordinary ones, then the override files, which are
// applied on top. Where both x.tofu and x.tf exist, OpenTofu reads only
// the former, and so does this.
func configFiles(dir string) (primary, overrides []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	names := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			names[e.Name()] = true
		}
	}
	for name := range names {
		if strings.HasPrefix(name, ".") {
			continue
		}
		for _, suffix := range configSuffixes {
			base, ok := strings.CutSuffix(name, suffix)
			if !ok {
				continue
			}
			tofuTwin := base + strings.Replace(suffix, ".tf", ".tofu", 1)
			if !strings.HasPrefix(suffix, ".tofu") && names[tofuTwin] {
				break
			}
			if base == "override" || strings.HasSuffix(base, "_override") {
				overrides = append(overrides, filepath.Join(dir, name))
			} else {
				primary = append(primary, filepath.Join(dir, name))
			}
			break
		}
	}
	sort.Strings(primary)
	sort.Strings(overrides)
	if len(primary) == 0 {
		return nil, nil, fmt.Errorf("no configuration files (.tf, .tofu or their .json forms) in %s", dir)
	}
	return primary, overrides, nil
}

func join(path, local string) string {
	if path == "" {
		return local
	}
	return path + "." + local
}

// resolve fills in DependsOn and References for every block of m and of
// the modules below.
func (l *loader) resolve(m *module) {
	for local, blk := range m.blocks {
		all := map[string]bool{}
		seenRef := map[Reference]bool{}
		for _, e := range m.exprs[local] {
			for _, t := range e.expr.Variables() {
				found := map[string]bool{}
				m.follow(t, found, map[hcl.Expression]bool{})
				written := l.written(t)
				for addr := range found {
					if addr == blk.Address {
						continue // A block may mention itself.
					}
					all[addr] = true
					if ref := (Reference{Attribute: e.attribute, Written: written, To: addr}); !seenRef[ref] {
						seenRef[ref] = true
						blk.References = append(blk.References, ref)
					}
				}
			}
		}
		blk.DependsOn = make([]string, 0, len(all))
		for addr := range all {
			blk.DependsOn = append(blk.DependsOn, addr)
		}
		slices.Sort(blk.DependsOn)
		slices.SortFunc(blk.References, func(a, b Reference) int {
			if c := strings.Compare(a.Attribute, b.Attribute); c != 0 {
				return c
			}
			if c := strings.Compare(a.To, b.To); c != 0 {
				return c
			}
			return strings.Compare(a.Written, b.Written)
		})
	}
	for _, child := range m.calls {
		if child != nil {
			l.resolve(child)
		}
	}
}

// written returns a reference as it stands in its file. The JSON syntax
// gives no exact position, so there the reference is spelled out again.
func (l *loader) written(t hcl.Traversal) string {
	rng := t.SourceRange()
	if f := l.parser.Files()[rng.Filename]; f != nil && !strings.HasSuffix(rng.Filename, ".json") && rng.CanSliceBytes(f.Bytes) {
		return string(rng.SliceBytes(f.Bytes))
	}
	return strings.Join(attributeNames(t), ".")
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
