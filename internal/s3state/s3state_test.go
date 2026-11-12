package s3state

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/s3state/s3test"
)

func TestParse(t *testing.T) {
	cases := map[string]struct {
		loc    Location
		ok     bool
		prefix bool
	}{
		"s3://states/prod/network.tfstate": {Location{"states", "prod/network.tfstate"}, true, false},
		"s3://states/prod/":                {Location{"states", "prod/"}, true, true},
		"s3://states/":                     {Location{"states", ""}, true, true},
		"s3://states":                      {Location{"states", ""}, true, true},
		"s3://states/env:/dev/app.tfstate": {Location{"states", "env:/dev/app.tfstate"}, true, false},
		"states/prod/network.tfstate":      {Location{}, false, false},
		"S3://states/key":                  {Location{}, false, false},
	}
	for address, want := range cases {
		loc, ok, err := Parse(address)
		if err != nil || ok != want.ok || loc != want.loc || (ok && loc.IsPrefix() != want.prefix) {
			t.Errorf("Parse(%q) = %+v, %v, %v", address, loc, ok, err)
		}
	}
	for _, address := range []string{"s3://", "s3:///key"} {
		if _, ok, err := Parse(address); !ok || err == nil {
			t.Errorf("Parse(%q): ok %v, err %v; want an error", address, ok, err)
		}
	}
}

func TestReadAndList(t *testing.T) {
	ctx := context.Background()
	objects := map[string]string{
		"network.tfstate":           "network",
		"prod/app.tfstate":          "app",
		"prod/data/db.tfstate":      "db",
		"prod/notes.txt":            "not a state",
		"prod/app.tfstate.backup":   "not a state either",
		"env:/dev/prod/app.tfstate": "a workspace",
	}
	srv := s3test.Start(t, "eu-west-1", map[string]s3test.Bucket{"states": {Region: "eu-west-1", Objects: objects}})
	var store Store

	data, err := store.Read(ctx, Location{"states", "prod/data/db.tfstate"})
	if err != nil || string(data) != "db" {
		t.Fatalf("Read = %q, %v", data, err)
	}
	data, err = store.Read(ctx, Location{"states", "env:/dev/prod/app.tfstate"})
	if err != nil || string(data) != "a workspace" {
		t.Errorf("Read of a workspace's state = %q, %v", data, err)
	}

	cases := map[string][]string{
		"":         {"env:/dev/prod/app.tfstate", "network.tfstate", "prod/app.tfstate", "prod/data/db.tfstate"},
		"prod/":    {"prod/app.tfstate", "prod/data/db.tfstate"},
		"nothing/": nil,
	}
	for prefix, want := range cases {
		keys, err := store.Keys(ctx, Location{"states", prefix})
		if err != nil || !reflect.DeepEqual(keys, want) {
			t.Errorf("Keys(%q) = %v, %v; want %v", prefix, keys, err, want)
		}
	}

	// The bucket's region is asked for once, not for every request.
	heads := 0
	for _, r := range srv.Requests() {
		if strings.HasPrefix(r, "HEAD ") {
			heads++
		}
	}
	if heads != 1 {
		t.Errorf("the bucket's region was asked for %d times:\n%s", heads, strings.Join(srv.Requests(), "\n"))
	}
}

// A listing longer than one answer is followed to its end.
func TestListIsPaged(t *testing.T) {
	objects := map[string]string{}
	var want []string
	for i := 0; i < 25; i++ {
		key := fmt.Sprintf("team-%02d/terraform.tfstate", i)
		objects[key] = "{}"
		objects[fmt.Sprintf("team-%02d/readme.md", i)] = ""
		want = append(want, key)
	}
	srv := s3test.Start(t, "us-east-1", map[string]s3test.Bucket{"states": {Region: "us-east-1", Objects: objects}})
	srv.PageSize = 7
	var store Store
	keys, err := store.Keys(context.Background(), Location{"states", ""})
	if err != nil || !reflect.DeepEqual(keys, want) {
		t.Errorf("Keys = %v, %v", keys, err)
	}
}

// A bucket in another region than the configured one is read all the
// same: S3 says where it is, and the requests are signed for there.
func TestBucketInAnotherRegion(t *testing.T) {
	ctx := context.Background()
	s3test.Start(t, "us-east-1", map[string]s3test.Bucket{
		"here":  {Region: "us-east-1", Objects: map[string]string{"a.tfstate": "here"}},
		"there": {Region: "ap-southeast-2", Objects: map[string]string{"b.tfstate": "there"}},
	})
	var store Store
	for bucket, key := range map[string]string{"here": "a.tfstate", "there": "b.tfstate"} {
		data, err := store.Read(ctx, Location{bucket, key})
		if err != nil || string(data) != bucket {
			t.Errorf("Read from %s = %q, %v", bucket, data, err)
		}
		keys, err := store.Keys(ctx, Location{bucket, ""})
		if err != nil || !reflect.DeepEqual(keys, []string{key}) {
			t.Errorf("Keys of %s = %v, %v", bucket, keys, err)
		}
	}
}

// With no region configured anywhere, the bucket's is still found.
func TestNoRegionConfigured(t *testing.T) {
	s3test.Start(t, "", map[string]s3test.Bucket{"states": {Region: "eu-central-1", Objects: map[string]string{"a.tfstate": "ok"}}})
	var store Store
	data, err := store.Read(context.Background(), Location{"states", "a.tfstate"})
	if err != nil || string(data) != "ok" {
		t.Errorf("Read = %q, %v", data, err)
	}
}

func TestErrorsSayWhatToDo(t *testing.T) {
	ctx := context.Background()
	s3test.Start(t, "us-east-1", map[string]s3test.Bucket{
		"states": {Region: "us-east-1", Objects: map[string]string{"secret.tfstate": "x"}, Denied: []string{"secret.tfstate"}},
		"closed": {Region: "us-east-1", Denied: []string{""}},
	})
	var store Store
	cases := []struct {
		name string
		call func() error
		want []string
	}{
		{"missing object", func() error { _, err := store.Read(ctx, Location{"states", "nope.tfstate"}); return err },
			[]string{"s3://states/nope.tfstate", "not found"}},
		{"missing bucket", func() error { _, err := store.Read(ctx, Location{"nowhere", "a.tfstate"}); return err },
			[]string{"s3://nowhere/a.tfstate", "not found"}},
		{"object denied", func() error { _, err := store.Read(ctx, Location{"states", "secret.tfstate"}); return err },
			[]string{"s3://states/secret.tfstate", "access denied", "s3:GetObject"}},
		{"listing denied", func() error { _, err := store.Keys(ctx, Location{"closed", "prod/"}); return err },
			[]string{"s3://closed/prod/", "access denied", "s3:ListBucket"}},
		{"missing bucket listed", func() error { _, err := store.Keys(ctx, Location{"nowhere", ""}); return err },
			[]string{"s3://nowhere/", "not found"}},
	}
	for _, c := range cases {
		err := c.call()
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q should mention %q", c.name, err, want)
			}
		}
	}
}

// Without credentials the error says so, and names what was asked for.
func TestNoCredentials(t *testing.T) {
	s3test.Start(t, "us-east-1", map[string]s3test.Bucket{"states": {Region: "us-east-1", Objects: map[string]string{"a.tfstate": "x"}}})
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	var store Store
	_, err := store.Read(context.Background(), Location{"states", "a.tfstate"})
	if err == nil || !strings.Contains(err.Error(), "s3://states/a.tfstate") || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("no credentials: %v", err)
	}
}
