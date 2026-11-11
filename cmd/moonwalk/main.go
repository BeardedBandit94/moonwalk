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
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

const usage = `Usage:
  moonwalk graph [-format text|json] <state-file>...

Reads one or more Terraform or OpenTofu states ('terraform show -json' output
or raw .tfstate files) and prints them as a single resource graph. Each state
is a separate source, named after its path. Use "-" to read standard input.
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
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("expected at least one state file\n\n%s", usage)
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown format %q (want text or json)", *format)
	}

	g := graph.New()
	seen := map[string]bool{}
	for _, path := range fs.Args() {
		if seen[path] {
			return fmt.Errorf("state %q given more than once", path)
		}
		seen[path] = true
		if err := loadState(g, path, stdin); err != nil {
			return err
		}
	}

	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(g.Document())
	}
	return writeText(stdout, g.Document())
}

func loadState(g *graph.Graph, path string, stdin io.Reader) error {
	if path == "-" {
		return tfstate.Load(g, "stdin", stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return tfstate.Load(g, path, f)
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
