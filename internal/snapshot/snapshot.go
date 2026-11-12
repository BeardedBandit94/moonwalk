// Package snapshot writes a graph to a file and reads it back.
//
// A snapshot holds what a graph held when it was written: every resource
// with its redacted attributes, and every relationship already worked out.
// It lets the collecting be done where the credentials are, one account or
// trust boundary at a time, and the looking be done somewhere else, over
// several snapshots at once. Two snapshots of the same thing, taken at
// different times, can be compared.
//
// Attributes that a resource withholds are not in a snapshot, as they are
// not in any output. A relationship that could only be worked out from them
// is kept if it was worked out before the snapshot was written, and cannot
// be found afterwards.
package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// Kind marks a document as a snapshot.
const Kind = "moonwalk-snapshot"

// Write writes g as a snapshot taken at the given time.
func Write(w io.Writer, g *graph.Graph, at time.Time) error {
	d := g.Document()
	d.Kind = Kind
	d.Created = at.UTC().Format(time.RFC3339)
	return d.WriteCompact(w)
}

// Is reports whether data is a snapshot. It reads only the start: a
// snapshot says what it is before it lists anything, and what else is
// loaded can be far too large to parse just to find out it is not one.
func Is(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	for {
		key, err := dec.Token()
		if err != nil {
			return false
		}
		value, err := dec.Token()
		if err != nil {
			return false
		}
		if _, nested := value.(json.Delim); nested {
			return false
		}
		if key == "kind" {
			return value == Kind
		}
	}
}

// Info describes a snapshot that was loaded.
type Info struct {
	Created   time.Time // Zero if the snapshot does not say.
	Resources int
}

// Load adds the resources and relationships of the snapshot in r to g. A
// resource that g already has, which happens when two inputs use one
// source name, is an error.
//
// The snapshot is read one resource at a time, so its text is never held
// in memory whole.
func Load(g *graph.Graph, r io.Reader) (Info, error) {
	info, err := read(g, r)
	if err != nil {
		return Info{}, fmt.Errorf("snapshot: %w", err)
	}
	return info, nil
}

func read(g *graph.Graph, r io.Reader) (Info, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber() // As the states are read, so that numbers compare equal and stay exact.
	notOne := errors.New("not a moonwalk snapshot")
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return Info{}, notOne
	}
	var info Info
	kind := ""
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return Info{}, err
		}
		switch key {
		case "kind":
			err = dec.Decode(&kind)
		case "schema_version":
			var version int
			if err = dec.Decode(&version); err == nil && version > graph.SchemaVersion {
				err = fmt.Errorf("written by a newer moonwalk (schema %d; this one reads up to %d)", version, graph.SchemaVersion)
			}
		case "created":
			var created string
			if err = dec.Decode(&created); err == nil {
				info.Created, _ = time.Parse(time.RFC3339, created)
			}
		case "nodes":
			if kind != Kind {
				return Info{}, notOne
			}
			err = each(dec, func(n graph.Node) error {
				info.Resources++
				if err := g.AddNode(n); err != nil {
					return fmt.Errorf("%w; two inputs may share the source name %q", err, n.Source)
				}
				return nil
			})
		case "edges":
			err = each(dec, g.AddEdge)
		default:
			var unknown json.RawMessage
			err = dec.Decode(&unknown)
		}
		if err != nil {
			return Info{}, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return Info{}, err
	}
	if kind != Kind {
		return Info{}, notOne
	}
	return info, nil
}

// each decodes the items of the list dec is at, one at a time.
func each[T any](dec *json.Decoder, add func(T) error) error {
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return errors.New("expected a list")
	}
	for dec.More() {
		var item T
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if err := add(item); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	return err
}
