package tfstate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// FuzzLoad checks that no input makes Load panic, and that whatever it
// accepts yields a graph that can be serialized. Run it for longer with
// `go test ./internal/tfstate -fuzz FuzzLoad`.
func FuzzLoad(f *testing.F) {
	seeds, err := filepath.Glob("testdata/*")
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range seeds {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue // testdata/fuzz holds inputs the fuzzer found; Go adds those itself.
		}
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(`{"version":4,"resources":[{"mode":"managed","type":"t","name":"n","provider":"p",` +
		`"instances":[{"index_key":"k","attributes":{"a":{"type":"string","value":"v"}},` +
		`"sensitive_attributes":[[{"type":"get_attr","value":"a"}]],"dependencies":["module.m.t.n"]}]}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		g := graph.New()
		if err := Load(g, "fuzz", bytes.NewReader(data)); err != nil {
			return
		}
		if _, err := json.Marshal(g.Document()); err != nil {
			t.Fatalf("accepted state does not serialize: %v", err)
		}
	})
}
