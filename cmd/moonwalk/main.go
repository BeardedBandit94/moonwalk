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
	"github.com/BeardedBandit94/moonwalk/internal/view"
)

const usage = `Usage:
  moonwalk view  [-format text|json] [-group-by <list>] [-depth <n>] <state>...
  moonwalk graph [-format text|json] [-direct] [-attributes] <state>...

Reads Terraform or OpenTofu states ('terraform show -json' output or raw
.tfstate files) into a single resource graph.

  view   prints the graph collapsed into nested groups, sized to stay
         readable however large the states are
  graph  prints every resource and dependency

Each <state> is one of:
  path         a state file, named after its path
  directory    every .tfstate file below it, hidden directories excluded
  -            standard input, named "stdin"
  name=<any of the above>   the same, under a name of your choosing

Options:
  -format      text (default) or json
  -group-by    view: comma-separated levels, outermost first, from source,
               account, region and module (default: all four in that order)
  -depth       view: how many levels to expand (default: as many as fit in
               ` + "300" + ` items)
  -direct      graph: leave out dependencies that others already imply
  -attributes  graph: include attribute values in JSON output; values that
               the state marks as sensitive are replaced with "(sensitive)"
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "moonwalk:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "graph" && args[0] != "view") {
		return fmt.Errorf("expected the 'view' or 'graph' command\n\n%s", usage)
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "")
	var (
		direct, attributes *bool
		groupBy            *string
		depth              *int
	)
	if command == "graph" {
		direct = fs.Bool("direct", false, "")
		attributes = fs.Bool("attributes", false, "")
	} else {
		groupBy = fs.String("group-by", "source,account,region,module", "")
		depth = fs.Int("depth", 0, "")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("expected at least one state\n\n%s", usage)
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown format %q (want text or json)", *format)
	}
	var dims []view.Dimension
	if command == "view" {
		var err error
		if dims, err = view.ParseDimensions(*groupBy); err != nil {
			return err
		}
		if *depth < 0 {
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

	if command == "view" {
		// State lists indirect dependencies too; links should count only
		// the direct ones.
		g.ReduceTransitive(graph.EdgeDependsOn)
		tree := view.Build(g, dims)
		d := min(*depth, tree.Depth())
		if d == 0 {
			d = tree.AutoDepth(view.Budget)
		}
		doc := tree.Document(d)
		if *format == "text" {
			return writeView(stdout, doc)
		}
		return writeJSON(stdout, doc)
	}

	if *direct {
		g.ReduceTransitive(graph.EdgeDependsOn)
	}
	d := g.Document()
	if *format == "text" {
		return writeText(stdout, d)
	}
	if !*attributes {
		for i := range d.Nodes {
			d.Nodes[i].Attributes = nil
		}
	}
	return writeJSON(stdout, d)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeView prints a collapsed view as an indented outline. Under each item
// that stands for resources come the items those resources depend on, named
// relative to the nearest group the two share.
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
	links := map[string][]view.Link{}
	for _, l := range d.Links {
		links[l.From] = append(links[l.From], l)
	}

	var b strings.Builder
	var dims []string
	for _, dim := range d.GroupBy {
		dims = append(dims, string(dim))
	}
	fmt.Fprintf(&b, "%d resources in %d items, grouped by %s\n", d.Resources, len(d.Items), strings.Join(dims, ", "))
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
		for _, l := range links[it.ID] {
			to := path(l.To)
			shared := 0
			for shared < len(from)-1 && shared < len(to)-1 && from[shared].ID == to[shared].ID {
				shared++
			}
			var names []string
			for _, step := range to[shared:] {
				names = append(names, step.Label)
			}
			fmt.Fprintf(&b, "%s    -> %s", indent, strings.Join(names, " / "))
			if l.Count > 1 {
				fmt.Fprintf(&b, "  (%d dependencies)", l.Count)
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

// writeText prints nodes grouped by source, each followed by its dependencies.
func writeText(w io.Writer, d graph.Document) error {
	byID := map[string]graph.Node{}
	var sources []string // In order of first appearance; nodes are sorted by ID.
	for _, n := range d.Nodes {
		byID[n.ID] = n
		if len(sources) == 0 || sources[len(sources)-1] != n.Source {
			sources = append(sources, n.Source)
		}
	}
	deps := map[string][]string{}
	for _, e := range d.Edges {
		deps[e.From] = append(deps[e.From], e.To)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d resources, %d dependencies, %d sources\n", len(d.Nodes), len(d.Edges), len(sources))
	i := 0
	for _, source := range sources {
		fmt.Fprintf(&b, "\n%s\n", source)
		for ; i < len(d.Nodes) && d.Nodes[i].Source == source; i++ {
			n := d.Nodes[i]
			fmt.Fprintf(&b, "  %s%s\n", n.Address, partitionLabel(n))
			for _, id := range deps[n.ID] {
				to := byID[id]
				name := to.Address
				if to.Source != n.Source {
					name = to.ID
				}
				fmt.Fprintf(&b, "    -> %s\n", name)
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
