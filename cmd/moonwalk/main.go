// Command moonwalk explores infrastructure-as-code state as a graph.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/relate"
	"github.com/BeardedBandit94/moonwalk/internal/view"
)

const usage = `Usage:
  moonwalk view  [options] <state>...
  moonwalk graph [options] <state>...

Reads Terraform or OpenTofu states ('terraform show -json' output or raw
.tfstate files) into a single resource graph.

  view   prints the graph collapsed into nested groups, sized to stay
         readable however large the states are
  graph  prints every resource and relationship

Each <state> is one of:
  path         a state file, named after its path
  directory    every .tfstate file below it, hidden directories excluded
  -            standard input, named "stdin"
  name=<any of the above>   the same, under a name of your choosing

Options for both:
  -format      text (default) or json

Options for view:
  -group-by    comma-separated levels, outermost first, from source,
               account, region, module and network
               (default: source,account,region,module)
  -depth       how many levels to expand (default: as many as fit in 300
               items)
  -type, -account, -region, -source, -status
               show only resources with one of the comma-separated values;
               -type also takes patterns such as 'aws_iam_*'
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
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "moonwalk:", err)
		os.Exit(1)
	}
}

// options holds every flag; each command registers only its own.
type options struct {
	format             string
	direct, attributes bool // graph
	groupBy            string
	depth              int
	types, accounts    string
	regions, sources   string
	statuses           string
	search, around     string
	glue               bool
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "graph" && args[0] != "view") {
		return fmt.Errorf("expected the 'view' or 'graph' command\n\n%s", usage)
	}
	command := args[0]
	var o options
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.format, "format", "text", "")
	if command == "graph" {
		fs.BoolVar(&o.direct, "direct", false, "")
		fs.BoolVar(&o.attributes, "attributes", false, "")
	} else {
		fs.StringVar(&o.groupBy, "group-by", "source,account,region,module", "")
		fs.IntVar(&o.depth, "depth", 0, "")
		fs.StringVar(&o.types, "type", "", "")
		fs.StringVar(&o.accounts, "account", "", "")
		fs.StringVar(&o.regions, "region", "", "")
		fs.StringVar(&o.sources, "source", "", "")
		fs.StringVar(&o.statuses, "status", "", "")
		fs.StringVar(&o.search, "search", "", "")
		fs.StringVar(&o.around, "around", "", "")
		fs.BoolVar(&o.glue, "glue", false, "")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("expected at least one state\n\n%s", usage)
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

	inputs, err := resolveInputs(fs.Args())
	if err != nil {
		return err
	}
	g := graph.New()
	for _, in := range inputs {
		if err := in.load(g, stdin); err != nil {
			return err
		}
	}
	// Work out containment, drop dependencies on instances that are
	// evidently not meant, and find the glue.
	relate.Enrich(g)

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
			}
		}
		return writeJSON(stdout, d)
	}

	filter := view.Filter{
		Types:    list(o.types),
		Accounts: list(o.accounts),
		Regions:  list(o.regions),
		Sources:  list(o.sources),
		Statuses: list(o.statuses),
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
			fmt.Fprintf(&b, "%s%s\n", indent, it.Label)
		case it.Expanded:
			fmt.Fprintf(&b, "%s%s  (%s, %s)\n", indent, it.Label, kindName(it.Kind), plural(it.Resources, "resource"))
		default:
			fmt.Fprintf(&b, "%s+ %s  (%s, %s)\n", indent, it.Label, kindName(it.Kind), plural(it.Resources, "resource"))
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
			fmt.Fprintf(&b, "  %s%s\n", n.Address, partitionLabel(n))
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
