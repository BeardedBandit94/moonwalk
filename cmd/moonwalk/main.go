// Command moonwalk explores infrastructure-as-code state as a graph.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

const usage = `Usage:
  moonwalk graph [-format text|json] [-attributes] <state>...

Reads Terraform or OpenTofu states ('terraform show -json' output or raw
.tfstate files) and prints them as a single resource graph.

Each <state> is one of:
  path         a state file, named after its path
  directory    every .tfstate file below it, hidden directories excluded
  -            standard input, named "stdin"
  name=<any of the above>   the same, under a name of your choosing

Options:
  -format      text (default) or json
  -attributes  include attribute values in JSON output; values that the
               state marks as sensitive are replaced with "(sensitive)"
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "moonwalk:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "graph" {
		return fmt.Errorf("expected the 'graph' command\n\n%s", usage)
	}
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "")
	attributes := fs.Bool("attributes", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("expected at least one state\n\n%s", usage)
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown format %q (want text or json)", *format)
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

	d := g.Document()
	if *format == "text" {
		return writeText(stdout, d)
	}
	if !*attributes {
		for i := range d.Nodes {
			d.Nodes[i].Attributes = nil
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
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
