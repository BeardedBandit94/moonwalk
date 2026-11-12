package main

import (
	"os"
	"strings"
	"testing"

	"github.com/BeardedBandit94/moonwalk/internal/s3state/s3test"
)

// bucket serves the fixtures the way an S3 backend lays states out: one
// key per configuration, and "env:/<workspace>/" before a workspace's.
func bucket(t *testing.T) *s3test.Server {
	t.Helper()
	fixture := func(name string) string {
		data, err := os.ReadFile(fixtures + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	return s3test.Start(t, "us-east-1", map[string]s3test.Bucket{
		"states": {Region: "eu-west-1", Objects: map[string]string{
			"live/network/terraform.tfstate": fixture("remote-network.tfstate"),
			"live/app/terraform.tfstate":     fixture("remote-app.tfstate"),
			"live/app/README.md":             "not a state",
			"env:/dev/net.tfstate":           fixture("network.tfstate"),
			"empty/readme.txt":               "",
			"locked/a.tfstate":               fixture("raw.tfstate"),
			"locked/b.tfstate":               fixture("raw.tfstate"),
		}, Denied: []string{"locked/b.tfstate"}},
	})
}

func TestStateFromS3(t *testing.T) {
	bucket(t)
	// One object shows what the same file on disk shows.
	want, err := runCLI(t, "", "view", "net="+fixtures+"network.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	got, err := runCLI(t, "", "view", "net=s3://states/env:/dev/net.tfstate")
	if err != nil || got != want {
		t.Errorf("one object (err %v):\n%s", err, got)
	}
	// Unnamed, it is named after its address.
	got, err = runCLI(t, "", "view", "-group-by", "source", "s3://states/env:/dev/net.tfstate")
	if err != nil || !strings.Contains(got, "s3://states/env:/dev/net.tfstate  (source") {
		t.Errorf("unnamed object (err %v):\n%s", err, got)
	}
}

// A prefix loads every state below it, each named by the rest of its key,
// as a directory does, and the states are linked to each other.
func TestStatesUnderAnS3Prefix(t *testing.T) {
	srv := bucket(t)
	want, err := runCLI(t, "", "view", "-group-by", "source",
		"live/network/terraform.tfstate="+fixtures+"remote-network.tfstate", "live/app/terraform.tfstate="+fixtures+"remote-app.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"live=s3://states/live/", "live/=s3://states/live/"} {
		got, err := runCLI(t, "", "view", "-group-by", "source", arg)
		if err != nil || got != want {
			t.Errorf("%s (err %v):\n%s\nwant\n%s", arg, err, got, want)
		}
	}
	if !strings.Contains(want, "-> live/network/terraform.tfstate / ") {
		t.Errorf("the states under the prefix should be linked:\n%s", want)
	}

	// The whole bucket, next to a file on disk, into a snapshot.
	out, err := runCLI(t, "", "collect", "s3://states/live/", "s3://states/env:/dev/net.tfstate", "prod="+fixtures+"raw.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{`"source":"s3://states/live/app/terraform.tfstate"`, `"source":"s3://states/env:/dev/net.tfstate"`, `"source":"prod"`} {
		if !strings.Contains(out, source) {
			t.Errorf("snapshot lacks %s", source)
		}
	}
	for _, r := range srv.Requests() {
		if strings.Contains(r, "README") {
			t.Errorf("an object that is not a state was fetched: %s", r)
		}
	}
}

func TestS3Errors(t *testing.T) {
	bucket(t)
	cases := map[string]struct {
		args []string
		want string
	}{
		"missing object":        {[]string{"view", "s3://states/live/nope.tfstate"}, "s3://states/live/nope.tfstate: not found"},
		"missing bucket":        {[]string{"view", "s3://nowhere/"}, "s3://nowhere/: not found"},
		"nothing under it":      {[]string{"view", "s3://states/empty/"}, "no .tfstate objects found under s3://states/empty/"},
		"no bucket named":       {[]string{"view", "s3://"}, "names no bucket"},
		"one of several denied": {[]string{"view", "s3://states/locked/"}, "s3://states/locked/b.tfstate: access denied"},
		"not a state":           {[]string{"view", "s3://states/live/app/README.md"}, ""},
		"same object twice":     {[]string{"view", "s3://states/env:/dev/net.tfstate", "s3://states/env:/dev/net.tfstate"}, "more than once"},
	}
	for name, c := range cases {
		_, err := runCLI(t, "", c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one that mentions %q", name, err, c.want)
		}
	}
}

// States in S3 are read once, not watched; the files next to them are
// not watched either, so that the explorer never shows half of a reload.
func TestS3IsNotWatched(t *testing.T) {
	for _, args := range [][]string{
		{"s3://states/live/"},
		{"live=s3://states/live/app/terraform.tfstate", fixtures + "raw.tfstate"},
	} {
		if _, ok := fingerprint(args, nil); ok {
			t.Errorf("%v: watched", args)
		}
	}
	if _, ok := fingerprint([]string{fixtures + "raw.tfstate"}, nil); !ok {
		t.Error("a file on disk is not watched")
	}
}
