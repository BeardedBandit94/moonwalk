package tfstate

import (
	"encoding/json"
	"reflect"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestApplyMask(t *testing.T) {
	cases := []struct{ name, value, mask, want string }{
		{"leaf", `{"a":"x","b":"y"}`, `{"a":true}`, `{"a":"(sensitive)","b":"y"}`},
		{"nested", `{"a":{"b":"x","c":"y"}}`, `{"a":{"b":true}}`, `{"a":{"b":"(sensitive)","c":"y"}}`},
		{"list", `{"a":["x","y"]}`, `{"a":[false,true]}`, `{"a":["x","(sensitive)"]}`},
		{"whole container", `{"a":{"b":"x"}}`, `{"a":true}`, `{"a":"(sensitive)"}`},
		{"null stays null", `{"a":null}`, `{"a":true}`, `{"a":null}`},
		{"numbers and bools", `{"a":5,"b":true}`, `{"a":true,"b":true}`, `{"a":"(sensitive)","b":"(sensitive)"}`},
		{"empty mask", `{"a":"x"}`, `{}`, `{"a":"x"}`},
		{"mask key absent from value", `{"a":"x"}`, `{"b":true}`, `{"a":"x"}`},
		{"shape mismatch hides the value", `{"a":"x"}`, `{"a":{"b":true}}`, `{"a":"(sensitive)"}`},
		{"shape mismatch with nothing marked", `{"a":"x"}`, `{"a":[false]}`, `{"a":"x"}`},
		{"mask longer than list", `{"a":["x"]}`, `{"a":[false,true]}`, `{"a":["x"]}`},
	}
	for _, c := range cases {
		got := secretSet{}.applyMask(decode(t, c.value), decode(t, c.mask))
		if want := decode(t, c.want); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", c.name, got, want)
		}
	}
}

func TestApplyPath(t *testing.T) {
	cases := []struct{ name, value, path, want string }{
		{"attribute", `{"a":"x","b":"y"}`, `[{"type":"get_attr","value":"a"}]`, `{"a":"(sensitive)","b":"y"}`},
		{"list element", `{"a":["x","y"]}`,
			`[{"type":"get_attr","value":"a"},{"type":"index","value":{"value":1,"type":"number"}}]`,
			`{"a":["x","(sensitive)"]}`},
		{"map element", `{"a":{"k":"x","l":"y"}}`,
			`[{"type":"get_attr","value":"a"},{"type":"index","value":{"value":"k","type":"string"}}]`,
			`{"a":{"k":"(sensitive)","l":"y"}}`},
		{"missing attribute", `{"a":"x"}`, `[{"type":"get_attr","value":"b"}]`, `{"a":"x"}`},
		{"index out of range", `{"a":["x"]}`,
			`[{"type":"get_attr","value":"a"},{"type":"index","value":{"value":3,"type":"number"}}]`,
			`{"a":["x"]}`},
		{"path goes deeper than the value", `{"a":"x"}`,
			`[{"type":"get_attr","value":"a"},{"type":"get_attr","value":"b"}]`, `{"a":"(sensitive)"}`},
		{"index into a non-list", `{"a":{"k":"x"}}`,
			`[{"type":"get_attr","value":"a"},{"type":"index","value":{"value":0,"type":"number"}}]`,
			`{"a":"(sensitive)"}`},
		{"unknown step type", `{"a":"x"}`, `[{"type":"splat","value":"a"}]`, `"(sensitive)"`},
		{"malformed step value", `{"a":"x"}`, `[{"type":"get_attr","value":7}]`, `"(sensitive)"`},
		{"empty path", `{"a":"x"}`, `[]`, `"(sensitive)"`},
	}
	for _, c := range cases {
		var path []pathStep
		if err := json.Unmarshal([]byte(c.path), &path); err != nil {
			t.Fatal(err)
		}
		got := secretSet{}.applyPath(decode(t, c.value), path)
		if want := decode(t, c.want); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", c.name, got, want)
		}
	}
}

func TestRedactByName(t *testing.T) {
	v := decode(t, `{
		"master_password": "p1",
		"manage_master_user_password": true,
		"auth": [{"token": "t1", "token_endpoint": "https://example.com"}],
		"client_secret": {"value": "s1", "version": 3},
		"empty_password": "",
		"kms_key_id": "key-1",
		"secret_id": "arn:secret"
	}`)
	want := decode(t, `{
		"master_password": "(sensitive)",
		"manage_master_user_password": true,
		"auth": [{"token": "(sensitive)", "token_endpoint": "https://example.com"}],
		"client_secret": {"value": "(sensitive)", "version": 3},
		"empty_password": "",
		"kms_key_id": "key-1",
		"secret_id": "arn:secret"
	}`)
	s := secretSet{}
	s.redactByName(v)
	if !reflect.DeepEqual(v, want) {
		t.Errorf("got %v\nwant %v", v, want)
	}
	if len(s) != 0 {
		t.Errorf("guessed secrets were remembered: %v", s)
	}
}

func TestScrubRemovesCopiesOfRedactedValues(t *testing.T) {
	s := secretSet{}
	s.redact(decode(t, `{"password":"hunter2","parts":["alpha",""]}`))

	got := s.scrub(decode(t, `{"a":"hunter2","b":["x","alpha"],"c":{"d":"hunter2 "},"e":""}`))
	want := decode(t, `{"a":"(sensitive)","b":["x","(sensitive)"],"c":{"d":"hunter2 "},"e":""}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestUnwrapDynamic(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"primitive", `{"input":{"type":"string","value":"x"}}`, `{"input":"x"}`},
		{"object", `{"input":{"type":["object",{"a":"string","b":["list","number"]}],"value":{"a":"x","b":[1]}}}`,
			`{"input":{"a":"x","b":[1]}}`},
		{"tuple", `{"input":{"type":["tuple",["string","bool"]],"value":["x",true]}}`, `{"input":["x",true]}`},
		{"null", `{"input":{"type":"dynamic","value":null}}`, `{"input":null}`},
		{"nested in a list", `{"l":[{"type":["map","string"],"value":{"k":"v"}}]}`, `{"l":[{"k":"v"}]}`},
		{"ordinary object with other keys", `{"p":{"type":"string","value":"x","name":"n"}}`,
			`{"p":{"type":"string","value":"x","name":"n"}}`},
		{"type is not a type", `{"p":{"type":"String","value":"x"}}`, `{"p":{"type":"String","value":"x"}}`},
		{"malformed descriptor", `{"p":{"type":["list"],"value":[]}}`, `{"p":{"type":["list"],"value":[]}}`},
	}
	for _, c := range cases {
		got := unwrapDynamic(decode(t, c.in))
		if want := decode(t, c.want); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", c.name, got, want)
		}
	}
}
