// Command moonwalk explores infrastructure-as-code state as a graph.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BeardedBandit94/moonwalk/internal/discover"
	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/relate"
	"github.com/BeardedBandit94/moonwalk/internal/tfcode"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
	"github.com/BeardedBandit94/moonwalk/internal/view"
	"github.com/BeardedBandit94/moonwalk/internal/web"
)

const usage = `Usage:
  moonwalk serve [options] <state>...
  moonwalk view  [options] <state>...
  moonwalk graph [options] <state>...

Reads Terraform or OpenTofu states ('terraform show -json' output or raw
.tfstate files) into a single resource graph.

  serve  opens the graph as an interactive explorer in the browser
  view   prints the graph collapsed into nested groups, sized to stay
         readable however large the states are
  graph  prints every resource and relationship

Add the configuration with -code to see where each resource is declared and
the resources that are written but not applied yet. With -code and no state,
the graph is built from the configuration alone.

Each <state> is one of:
  path         a state file; or the JSON form of a plan ('terraform show
               -json <planfile>'), which shows the state the plan was made
               against with its changes and any drift marked; or the output
               of 'terraform query -json', whose resources are shown as
               unmanaged unless a state holds them; named after its path
  directory    every .tfstate file below it, hidden directories excluded
  -            standard input, named "stdin"
  name=<any of the above>   the same, under a name of your choosing

Options for all three:
  -code        [name=]directory of a root module's .tf or .tofu files. The
               name says which state the code belongs to; it can be left
               out when there is one state or none. May be given repeatedly.

Options for serve:
  -addr        address to listen on (default 127.0.0.1:7878). The explorer
               shows attribute values, so listen on another address only
               on a network you trust.

Options for view and graph:
  -format      text (default) or json

Options for view:
  -group-by    comma-separated levels, outermost first, from source,
               account, region, module and network
               (default: source,account,region,module)
  -depth       how many levels to expand (default: as many as fit in 300
               items)
  -type, -account, -region, -source, -status, -change
               show only resources with one of the comma-separated values;
               -type also takes patterns such as 'aws_iam_*', and -change
               takes create, update, delete, replace or any
  -search      show only resources whose address or type contains the text
  -around      show only the resource with this address and the resources
               directly related to it
  -glue        show glue resources, such as route table associations,
               instead of folding each into a link between what it joins

Options for graph:
  -direct      leave out dependencies that others already imply
  -attributes  include attribute values in JSON output; values that the
               state marks as sensitive are replaced with "(sensitive)"
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "moonwalk:", err)
		os.Exit(1)
	}
}

// options holds every flag; each command registers only its own.
type options struct {
	code               multiFlag
	addr               string // serve
	format             string
	direct, attributes bool // graph
	groupBy            string
	depth              int
	types, accounts    string
	regions, sources   string
	statuses, changes  string
	search, around     string
	glue               bool
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || !slices.Contains([]string{"serve", "view", "graph"}, args[0]) {
		return fmt.Errorf("expected the 'serve', 'view' or 'graph' command\n\n%s", usage)
	}
	command := args[0]
	var o options
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var(&o.code, "code", "")
	switch command {
	case "serve":
		fs.StringVar(&o.addr, "addr", "127.0.0.1:7878", "")
		o.format = "text"
	case "graph":
		fs.StringVar(&o.format, "format", "text", "")
		fs.BoolVar(&o.direct, "direct", false, "")
		fs.BoolVar(&o.attributes, "attributes", false, "")
	default:
		fs.StringVar(&o.format, "format", "text", "")
		fs.StringVar(&o.groupBy, "group-by", "source,account,region,module", "")
		fs.IntVar(&o.depth, "depth", 0, "")
		fs.StringVar(&o.types, "type", "", "")
		fs.StringVar(&o.accounts, "account", "", "")
		fs.StringVar(&o.regions, "region", "", "")
		fs.StringVar(&o.sources, "source", "", "")
		fs.StringVar(&o.statuses, "status", "", "")
		fs.StringVar(&o.changes, "change", "", "")
		fs.StringVar(&o.search, "search", "", "")
		fs.StringVar(&o.around, "around", "", "")
		fs.BoolVar(&o.glue, "glue", false, "")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if fs.NArg() == 0 && len(o.code) == 0 {
		return fmt.Errorf("expected at least one state or -code directory\n\n%s", usage)
	}
	if o.format != "text" && o.format != "json" {
		return fmt.Errorf("unknown format %q (want text or json)", o.format)
	}
	var dims []view.Dimension
	if command == "view" {
		var err error
		if dims, err = view.ParseDimensions(o.groupBy); err != nil {
			return err
		}
		if o.depth < 0 {
			return fmt.Errorf("-depth must be 1 or more")
		}
	}

	g, code, err := loadGraph(fs.Args(), o.code, stdin, stderr)
	if err != nil {
		return err
	}
	warnUnresolved(stderr, code)

	if command == "serve" {
		warnWithheld(stderr, g.Document())
		return serve(g, code, fs.Args(), o.code, o.addr, stdout, stderr)
	}
	if command == "graph" {
		if o.direct {
			g.ReduceTransitive(graph.EdgeDependsOn)
		}
		d := g.Document()
		if o.format == "text" {
			return writeGraph(stdout, d)
		}
		if !o.attributes {
			for i := range d.Nodes {
				d.Nodes[i].Attributes = nil
				d.Nodes[i].AttributesWithheld = false // Nothing was asked for.
			}
		} else {
			warnWithheld(stderr, d)
		}
		return d.WriteJSON(stdout)
	}

	filter := view.Filter{
		Types:    list(o.types),
		Accounts: list(o.accounts),
		Regions:  list(o.regions),
		Sources:  list(o.sources),
		Statuses: list(o.statuses),
		Changes:  list(o.changes),
		Search:   o.search,
	}
	if o.around != "" {
		if filter.Around, err = findNode(g, o.around); err != nil {
			return err
		}
	}
	// State lists indirect dependencies too; links should count only the
	// direct ones.
	g.ReduceTransitive(graph.EdgeDependsOn)
	tree := view.Build(g, view.Options{Dimensions: dims, Filter: filter, ShowGlue: o.glue})
	depth := min(o.depth, tree.Depth())
	if depth == 0 {
		depth = tree.AutoDepth(view.Budget)
	}
	doc := tree.Document(depth)
	if o.format == "text" {
		return writeView(stdout, doc)
	}
	return writeJSON(stdout, doc)
}

// warnWithheld says which sources' attributes are left out of the output,
// and how to get them.
func warnWithheld(stderr io.Writer, d graph.Document) {
	var sources []string
	for _, n := range d.Nodes {
		if n.AttributesWithheld && !slices.Contains(sources, n.Source) {
			sources = append(sources, n.Source)
		}
	}
	for _, source := range sources {
		fmt.Fprintf(stderr, "moonwalk: attribute values of %s are withheld: it was written by a version of Terraform or OpenTofu "+
			"that did not record which attributes are sensitive. To see them, load the output of 'terraform show -json' from version 1.5 or later.\n", source)
	}
}

// multiFlag collects the values of a flag that may be given repeatedly.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// loadGraph reads the states the arguments name into one graph, adds what
// listings of the cloud found beyond them, joins the configurations, and
// works out containment and glue. It returns
// the configurations by the source each belongs to.
func loadGraph(args, codeDirs []string, stdin io.Reader, stderr io.Writer) (*graph.Graph, map[string]*tfcode.Config, error) {
	inputs, err := resolveInputs(args)
	if err != nil {
		return nil, nil, err
	}
	g := graph.New()
	// States first: what a listing of the cloud found is unmanaged only if
	// no state holds it.
	type listing struct {
		source string
		data   []byte
	}
	var listings []listing
	for _, in := range inputs {
		data, err := in.read(stdin)
		if err != nil {
			return nil, nil, err
		}
		if discover.IsQueryOutput(data) {
			listings = append(listings, listing{in.source, data})
			continue
		}
		if err := tfstate.Load(g, in.source, bytes.NewReader(data)); err != nil {
			return nil, nil, err
		}
	}
	states := len(inputs) - len(listings)
	for _, l := range listings {
		res, err := discover.Load(g, l.source, bytes.NewReader(l.data))
		if err != nil {
			return nil, nil, err
		}
		for _, e := range res.Errors {
			fmt.Fprintf(stderr, "moonwalk: the listing in %s is incomplete, so resources may be missing from it: %s\n", l.source, e)
		}
	}

	code := map[string]*tfcode.Config{}
	for _, spec := range codeDirs {
		source, dir := splitName(spec)
		switch {
		case source != "":
		case states == 1:
			for _, in := range inputs {
				if !slices.ContainsFunc(listings, func(l listing) bool { return l.source == in.source }) {
					source = in.source
				}
			}
		case states == 0:
			source = dir
		default:
			return nil, nil, fmt.Errorf("say which state the code in %s belongs to: -code <name>=%s", dir, dir)
		}
		if _, dup := code[source]; dup {
			return nil, nil, fmt.Errorf("%s has more than one -code directory", source)
		}
		cfg, err := tfcode.Load(dir)
		if err != nil {
			return nil, nil, err
		}
		if _, err := cfg.Apply(g, source); err != nil {
			return nil, nil, err
		}
		code[source] = cfg
	}
	relate.Enrich(g)
	return g, code, nil
}

// warnUnresolved names the modules whose code could not be found.
func warnUnresolved(stderr io.Writer, code map[string]*tfcode.Config) {
	for _, cfg := range code {
		for _, module := range cfg.Unresolved {
			fmt.Fprintf(stderr, "moonwalk: the code of %s in %s was not found on disk, so its resources have no code location. "+
				"Running 'terraform init' there fetches it.\n", module, cfg.Root)
		}
	}
}

// snippets returns the lookup the explorer uses to show a resource's block
// and what it refers to.
func snippets(code map[string]*tfcode.Config) web.CodeLookup {
	return func(source, configAddress string) (web.CodeInfo, bool) {
		cfg, ok := code[source]
		if !ok {
			return web.CodeInfo{}, false
		}
		blk, ok := cfg.Blocks[configAddress]
		if !ok {
			return web.CodeInfo{}, false
		}
		info := web.CodeInfo{Text: blk.Text}
		for _, r := range blk.References {
			info.References = append(info.References, web.CodeReference{Attribute: r.Attribute, Written: r.Written, To: r.To})
		}
		return info, true
	}
}

// serve runs the explorer until the process is stopped, loading the states
// again whenever one of their files changes.
func serve(g *graph.Graph, code map[string]*tfcode.Config, args, codeDirs []string, addr string, stdout, stderr io.Writer) error {
	// Views count what is there; indirect dependencies would inflate them.
	g.ReduceTransitive(graph.EdgeDependsOn)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := web.New(g, snippets(code))
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		srv.LocalOnly = true
	} else {
		fmt.Fprintln(stdout, "Warning: listening beyond this machine. Anyone who can reach this address can read the loaded state's attribute values.")
		if ip != nil && ip.IsUnspecified() {
			host = "localhost"
		}
	}
	nodes, _ := g.Len()
	fmt.Fprintf(stdout, "Serving %s at http://%s/ (Ctrl-C to stop)\n", plural(nodes, "resource"), net.JoinHostPort(host, port))

	go watch(args, codeDirs, 2*time.Second, func() {
		// Standard input was read once and cannot be read again; watch
		// never calls back when it is among the states.
		fresh, freshCode, err := loadGraph(args, codeDirs, nil, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "moonwalk: a file changed but could not be loaded, so the explorer still shows the last good version: %v\n", err)
			return
		}
		fresh.ReduceTransitive(graph.EdgeDependsOn)
		srv.Replace(fresh, snippets(freshCode))
		n, _ := fresh.Len()
		fmt.Fprintf(stdout, "Reloaded: %s\n", plural(n, "resource"))
	})
	return (&http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}).Serve(ln)
}

// watch calls changed each time the files behind the arguments change, and
// never returns. It does nothing if they include standard input.
func watch(args, codeDirs []string, every time.Duration, changed func()) {
	last, ok := fingerprint(args, codeDirs)
	if !ok {
		return
	}
	for range time.Tick(every) {
		now, _ := fingerprint(args, codeDirs)
		if now != last {
			last = now
			changed()
		}
	}
}

// fingerprint summarizes the files the arguments name: which there are,
// and each one's size and modification time. For a code directory those
// are the configuration files below it, hidden directories excluded. ok is
// false if the states include standard input, which cannot be watched.
func fingerprint(args, codeDirs []string) (print string, ok bool) {
	inputs, err := resolveInputs(args)
	if err != nil {
		return "unreadable: " + err.Error(), true
	}
	var b strings.Builder
	stamp := func(path string) {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", path, err)
			return
		}
		fmt.Fprintf(&b, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
	}
	for _, in := range inputs {
		if in.path == "-" {
			return "", false
		}
		stamp(in.path)
	}
	for _, spec := range codeDirs {
		_, dir := splitName(spec)
		err := filepath.WalkDir(dir, func(p string, d iofs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != dir && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if tfcode.IsConfigFile(d.Name()) {
				stamp(p)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", dir, err)
		}
	}
	return b.String(), true
}

// list splits a comma-separated flag value; an empty value gives no list.
func list(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// findNode returns the ID of the resource with the given ID or address.
func findNode(g *graph.Graph, name string) (string, error) {
	if _, ok := g.Node(name); ok {
		return name, nil
	}
	var found []string
	for n := range g.AllNodes() {
		if n.Address == name {
			found = append(found, n.ID)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no resource has the address %q", name)
	case 1:
		return found[0], nil
	}
	slices.Sort(found)
	return "", fmt.Errorf("the address %q is in several states; use one of: %s", name, strings.Join(found, ", "))
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeView prints a collapsed view as an indented outline. Under each item
// that stands for resources come the items it is related to, named relative
// to the nearest group the two share: "->" for what it depends on and "--"
// for what glue resources attach it to.
func writeView(w io.Writer, d view.Document) error {
	byID := make(map[string]view.Item, len(d.Items))
	for _, it := range d.Items {
		byID[it.ID] = it
	}
	// path returns the items from the top level down to the given one.
	path := func(id string) []view.Item {
		var p []view.Item
		for id != "" {
			p = append(p, byID[id])
			id = byID[id].Parent
		}
		slices.Reverse(p)
		return p
	}
	// A link without direction is listed under both of its ends.
	type relation struct {
		arrow, other string
		count        int
	}
	relations := map[string][]relation{}
	for _, l := range d.Links {
		if l.Kind == graph.EdgeLinks {
			relations[l.From] = append(relations[l.From], relation{"--", l.To, l.Count})
			relations[l.To] = append(relations[l.To], relation{"--", l.From, l.Count})
		} else {
			relations[l.From] = append(relations[l.From], relation{"->", l.To, l.Count})
		}
	}

	var b strings.Builder
	var dims []string
	for _, dim := range d.GroupBy {
		dims = append(dims, string(dim))
	}
	fmt.Fprintf(&b, "%s in %s, grouped by %s\n", plural(d.Resources, "resource"), plural(len(d.Items), "item"), strings.Join(dims, ", "))
	if left := d.Total - d.GlueFolded - d.Resources; left > 0 {
		fmt.Fprintf(&b, "%s left out by the filter\n", plural(left, "resource"))
	}
	if d.GlueFolded > 0 {
		fmt.Fprintf(&b, "%s folded into links marked -- (-glue shows them)\n", plural(d.GlueFolded, "glue resource"))
	}
	if d.Depth < d.MaxDepth {
		fmt.Fprintf(&b, "Expanded to depth %d of %d; groups marked + hold more (-depth %d)\n", d.Depth, d.MaxDepth, d.Depth+1)
	}
	b.WriteByte('\n')
	for _, it := range d.Items {
		indent := strings.Repeat("  ", it.Depth-1)
		switch {
		case it.Kind == view.KindResource:
			fmt.Fprintf(&b, "%s%s%s\n", indent, it.Label, marks(statusText(it.Status, it.Drift), it.Change, it.NotInCode, "  (", ")"))
		case it.Expanded:
			fmt.Fprintf(&b, "%s%s  (%s, %s%s)\n", indent, it.Label, kindName(it.Kind), plural(it.Resources, "resource"), tally(it))
		default:
			fmt.Fprintf(&b, "%s+ %s  (%s, %s%s)\n", indent, it.Label, kindName(it.Kind), plural(it.Resources, "resource"), tally(it))
		}
		from := path(it.ID)
		for _, r := range relations[it.ID] {
			to := path(r.other)
			shared := 0
			for shared < len(from)-1 && shared < len(to)-1 && from[shared].ID == to[shared].ID {
				shared++
			}
			var names []string
			for _, step := range to[shared:] {
				names = append(names, step.Label)
			}
			fmt.Fprintf(&b, "%s    %s %s", indent, r.arrow, strings.Join(names, " / "))
			if r.count > 1 {
				fmt.Fprintf(&b, "  (%d)", r.count)
			}
			b.WriteByte('\n')
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// statusText names a status, saying how a drifted resource drifted.
func statusText(status graph.Status, drift string) string {
	if status == graph.StatusDrifted && drift != "" {
		return "drifted: " + drift
	}
	return string(status)
}

// tally says how many of a group's resources are not simply managed.
func tally(it view.Item) string {
	out := ""
	for _, c := range []struct {
		n    int
		what string
	}{{it.Unmanaged, "unmanaged"}, {it.Drifted, "drifted"}, {it.Declared, "declared"}} {
		if c.n > 0 {
			out += fmt.Sprintf(", %d %s", c.n, c.what)
		}
	}
	return out
}

// marks renders what is unusual about a resource: a status other than
// managed, a change a plan intends, and being absent from the code.
func marks(status, change string, notInCode bool, open, shut string) string {
	var parts []string
	switch {
	case change == "create":
		parts = append(parts, "to create") // Says more than "declared" does.
	case status != "":
		parts = append(parts, status)
	}
	if change != "" && change != "create" {
		parts = append(parts, "to "+change)
	}
	if notInCode {
		parts = append(parts, "not in code")
	}
	if len(parts) == 0 {
		return ""
	}
	return open + strings.Join(parts, ", ") + shut
}

func kindName(k view.Kind) string {
	return strings.ReplaceAll(string(k), "_", " ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func pluralY(n int, stem string) string {
	if n == 1 {
		return "1 " + stem + "y"
	}
	return fmt.Sprintf("%d %sies", n, stem)
}

// writeGraph prints every resource, grouped by source, followed by its
// relationships: "->" for what it depends on, "contains" for what sits
// inside it, and "--" for what a glue resource attaches it to.
func writeGraph(w io.Writer, d graph.Document) error {
	byID := map[string]graph.Node{}
	var sources []string // In order of first appearance; nodes are sorted by ID.
	for _, n := range d.Nodes {
		byID[n.ID] = n
		if len(sources) == 0 || sources[len(sources)-1] != n.Source {
			sources = append(sources, n.Source)
		}
	}
	edges := map[string][]graph.Edge{}
	dependencies := 0
	for _, e := range d.Edges {
		edges[e.From] = append(edges[e.From], e)
		if e.Kind == graph.EdgeDependsOn {
			dependencies++
		}
	}
	// name refers to another node from the point of view of n.
	name := func(n graph.Node, id string) string {
		if other := byID[id]; other.Source == n.Source {
			return other.Address
		}
		return id
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s, %s, %s\n", plural(len(d.Nodes), "resource"), pluralY(dependencies, "dependenc"), plural(len(sources), "source"))
	i := 0
	for _, source := range sources {
		fmt.Fprintf(&b, "\n%s\n", source)
		for ; i < len(d.Nodes) && d.Nodes[i].Source == source; i++ {
			n := d.Nodes[i]
			status := ""
			if n.Status != graph.StatusManaged {
				status = statusText(n.Status, n.Drift)
			}
			fmt.Fprintf(&b, "  %s%s%s\n", n.Address, partitionLabel(n), marks(status, n.Change, n.NotInCode, "  [", "]"))
			for _, e := range edges[n.ID] {
				switch e.Kind {
				case graph.EdgeDependsOn:
					fmt.Fprintf(&b, "    -> %s\n", name(n, e.To))
				case graph.EdgeContains:
					fmt.Fprintf(&b, "    contains %s\n", name(n, e.To))
				case graph.EdgeLinks:
					fmt.Fprintf(&b, "    -- %s  (by %s)\n", name(n, e.To), name(n, e.Via))
				}
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// partitionLabel renders a node's account and region, when known.
func partitionLabel(n graph.Node) string {
	switch {
	case n.Account != "" && n.Region != "":
		return "  (" + n.Account + ", " + n.Region + ")"
	case n.Account != "" || n.Region != "":
		return "  (" + n.Account + n.Region + ")"
	}
	return ""
}
