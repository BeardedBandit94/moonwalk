package tfstate

import (
	"encoding/json"
	"strings"
)

// Redacted replaces every attribute value that state marks as sensitive.
const Redacted = "(sensitive)"

// secretSet collects the string values redacted while reading one state.
//
// Terraform does not always carry a sensitivity mark along with a value: a
// secret passed into a resource can come back unmarked in another attribute
// of the same state. Remembering the redacted strings lets scrub remove
// those copies too.
type secretSet map[string]struct{}

// redact returns the replacement for a sensitive value and remembers the
// strings inside it. A null stays null, since it holds nothing to hide.
func (s secretSet) redact(v any) any {
	if v == nil {
		return nil
	}
	s.remember(v)
	return Redacted
}

func (s secretSet) remember(v any) {
	switch t := v.(type) {
	case string:
		if t != "" {
			s[t] = struct{}{}
		}
	case map[string]any:
		for _, e := range t {
			s.remember(e)
		}
	case []any:
		for _, e := range t {
			s.remember(e)
		}
	}
}

// scrub replaces, in place, every string in v that equals a remembered
// secret, and returns v.
func (s secretSet) scrub(v any) any {
	if len(s) == 0 {
		return v
	}
	switch t := v.(type) {
	case string:
		if _, secret := s[t]; secret {
			return Redacted
		}
	case map[string]any:
		for k, e := range t {
			t[k] = s.scrub(e)
		}
	case []any:
		for i, e := range t {
			t[i] = s.scrub(e)
		}
	}
	return v
}

// applyMask redacts v according to a `sensitive_values` mask from
// `terraform show -json`: true marks a whole value, and objects and arrays
// mirror the shape of v. It works in place and returns v.
func (s secretSet) applyMask(v, mask any) any {
	switch m := mask.(type) {
	case bool:
		if m {
			return s.redact(v)
		}
	case map[string]any:
		obj, ok := v.(map[string]any)
		if !ok {
			return s.redactOnMismatch(v, mask)
		}
		for k, sub := range m {
			if cur, ok := obj[k]; ok {
				obj[k] = s.applyMask(cur, sub)
			}
		}
	case []any:
		list, ok := v.([]any)
		if !ok {
			return s.redactOnMismatch(v, mask)
		}
		for i, sub := range m {
			if i < len(list) {
				list[i] = s.applyMask(list[i], sub)
			}
		}
	}
	return v
}

// redactOnMismatch handles a mask whose shape does not match the value. If
// the mask marks anything at all, the whole value is hidden to be safe.
func (s secretSet) redactOnMismatch(v, mask any) any {
	if maskMarksAnything(mask) {
		return s.redact(v)
	}
	return v
}

func maskMarksAnything(mask any) bool {
	switch m := mask.(type) {
	case bool:
		return m
	case map[string]any:
		for _, sub := range m {
			if maskMarksAnything(sub) {
				return true
			}
		}
	case []any:
		for _, sub := range m {
			if maskMarksAnything(sub) {
				return true
			}
		}
	}
	return false
}

// pathStep is one step of a `sensitive_attributes` path in a raw state file:
// {"type":"get_attr","value":"password"} selects an attribute, and
// {"type":"index","value":{"value":0,"type":"number"}} an element.
type pathStep struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// applyPath redacts the value that path leads to inside v. It works in place
// and returns v. A path that does not fit the value hides the value at the
// point where it stops fitting.
func (s secretSet) applyPath(v any, path []pathStep) any {
	if len(path) == 0 {
		return s.redact(v)
	}
	step, rest := path[0], path[1:]

	var key string
	if step.Type == "get_attr" {
		if json.Unmarshal(step.Value, &key) != nil {
			return s.redact(v)
		}
	} else {
		var idx struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		}
		if step.Type != "index" || json.Unmarshal(step.Value, &idx) != nil {
			return s.redact(v)
		}
		if idx.Type == "number" {
			var n int
			list, ok := v.([]any)
			if !ok || json.Unmarshal(idx.Value, &n) != nil {
				return s.redact(v)
			}
			if n >= 0 && n < len(list) {
				list[n] = s.applyPath(list[n], rest)
			}
			return v
		}
		if json.Unmarshal(idx.Value, &key) != nil {
			return s.redact(v)
		}
	}

	obj, ok := v.(map[string]any)
	if !ok {
		return s.redact(v)
	}
	if cur, ok := obj[key]; ok {
		obj[key] = s.applyPath(cur, rest)
	}
	return v
}

// redactByName hides, in place, every string held under an attribute whose
// name says it is a secret. Raw state files get this extra pass in case the
// version that wrote them did not record every sensitive path.
//
// These are guesses, so the values are not remembered: scrubbing every copy
// of a value that was never secret would hide ordinary data across the state.
func (s secretSet) redactByName(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if secretName(k) {
				t[k] = s.redactStrings(e)
			} else {
				s.redactByName(e)
			}
		}
	case []any:
		for _, e := range t {
			s.redactByName(e)
		}
	}
}

// redactStrings hides every non-empty string in v, keeping its structure.
func (s secretSet) redactStrings(v any) any {
	switch t := v.(type) {
	case string:
		if t != "" {
			return Redacted
		}
	case map[string]any:
		for k, e := range t {
			t[k] = s.redactStrings(e)
		}
	case []any:
		for i, e := range t {
			t[i] = s.redactStrings(e)
		}
	}
	return v
}

// secretName reports whether an attribute name indicates a secret. The list
// is kept narrow: broad words such as "key" or "id" would hide the
// identifiers the graph is built from.
func secretName(name string) bool {
	n := strings.ToLower(name)
	for _, part := range []string{
		"password", "passwd", "passphrase", "private_key", "secret_key",
		"api_key", "connection_string", "secret_string", "secret_binary",
	} {
		if strings.Contains(n, part) {
			return true
		}
	}
	for _, word := range []string{"secret", "token"} {
		if n == word || strings.HasSuffix(n, "_"+word) {
			return true
		}
	}
	return false
}

// unwrapDynamic undoes, in place, the envelope raw state puts around an
// attribute whose type the provider leaves open: {"type": <type>, "value":
// <value>} becomes <value>, which is how `terraform show -json` prints it
// and what sensitive paths refer to. It returns the unwrapped value.
func unwrapDynamic(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 2 {
			if typ, ok := t["type"]; ok && isTypeDescriptor(typ) {
				if value, ok := t["value"]; ok {
					return unwrapDynamic(value)
				}
			}
		}
		for k, e := range t {
			t[k] = unwrapDynamic(e)
		}
	case []any:
		for i, e := range t {
			t[i] = unwrapDynamic(e)
		}
	}
	return v
}

// isTypeDescriptor reports whether v is a type as state encodes it: a
// primitive name, or a pair such as ["list", "string"] or ["object", {...}].
func isTypeDescriptor(v any) bool {
	switch t := v.(type) {
	case string:
		return t == "string" || t == "number" || t == "bool" || t == "dynamic"
	case []any:
		if len(t) < 2 {
			return false
		}
		switch t[0] {
		case "list", "set", "map":
			return len(t) == 2 && isTypeDescriptor(t[1])
		case "object":
			attrs, ok := t[1].(map[string]any)
			if !ok {
				return false
			}
			for _, a := range attrs {
				if !isTypeDescriptor(a) {
					return false
				}
			}
			return true // A third element may list optional attributes.
		case "tuple":
			elems, ok := t[1].([]any)
			if !ok || len(t) != 2 {
				return false
			}
			for _, e := range elems {
				if !isTypeDescriptor(e) {
					return false
				}
			}
			return true
		}
	}
	return false
}
