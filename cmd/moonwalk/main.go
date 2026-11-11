// Command moonwalk explores infrastructure-as-code state as a graph.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

const usage = `Usage:
  moonwalk graph [-format text|json] <state-file>

Reads a Terraform or OpenTofu state ('terraform show -json' output or a raw
.tfstate file) and prints its resource graph. Use "-" to read standard input.
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
	if fs.NArg() != 1 {
		return fmt.Errorf("expected exactly one state file\n\n%s", usage)
	}

	in := stdin
	if path := fs.Arg(0); path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	g, err := tfstate.Load(in)
	if err != nil {
		return err
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(g.Document())
	case "text":
		return writeText(stdout, g.Document())
	default:
		return fmt.Errorf("unknown format %q (want text or json)", *format)
	}
}

func writeText(w io.Writer, d graph.Document) error {
	deps := map[string][]string{}
	for _, e := range d.Edges {
		deps[e.From] = append(deps[e.From], e.To)
	}
	if _, err := fmt.Fprintf(w, "%d resources, %d dependencies\n", len(d.Nodes), len(d.Edges)); err != nil {
		return err
	}
	for _, n := range d.Nodes {
		if _, err := fmt.Fprintf(w, "\n%s\n", n.ID); err != nil {
			return err
		}
		for _, to := range deps[n.ID] {
			if _, err := fmt.Fprintf(w, "  -> %s\n", to); err != nil {
				return err
			}
		}
	}
	return nil
}
