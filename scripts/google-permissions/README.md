# Recording what the google provider needs

`internal/gcprole/permissions.json` says which Google Cloud permissions
listing each resource type takes. It is recorded, not written: these
scripts run the Terraform google provider against a stand-in for Google's
APIs that notes every request, and turn the requests into permissions. Run
them again when a newer provider can list more types. Nothing here asks
Google about a project, and none of it is part of the build.

## What it needs

- Terraform 1.14 or later.
- A directory where `terraform init` has installed the google provider
  (its `.terraform` and `.terraform.lock.hcl`), and that provider's schema:

  ```bash
  terraform providers schema -json > schema.json
  ```

- A moonwalk binary, for the query files.
- Python 3, with nothing added.
- Access to `www.googleapis.com` and the `<service>.googleapis.com` hosts,
  for Google's descriptions of its APIs (the discovery documents), and to
  `docs.cloud.google.com`, for its lists of permissions. Both are public
  and need no credentials. They are downloaded once into the directory
  given as `--cache`.

## Running it

```bash
python3 probe.py --terraform terraform --moonwalk moonwalk \
  --schema schema.json --plugins <directory> --cache cache --out calls.json
```

```bash
python3 derive.py --calls calls.json --provider "hashicorp/google <version>" \
  --cache cache --out ../../internal/gcprole/permissions.json
```

Then `go test ./cmd/moonwalk -update` to rewrite the example role in
`docs/gcp-iam`, and read the difference before committing it: it is the
change in what moonwalk asks a project for.

## How it works

The provider can be told, service by service, where each of Google's APIs
is. `probe.py` gives it every such setting it has, all pointing at
`recorder.py`, with the service's name at the front of the path. The
access token of each run says which type and which way of listing, so
every recorded request is known to belong to it.

`recorder.py` finds the method a request calls in the service's discovery
document, and answers as that document says the method answers: a list
with one made-up resource in it, shaped by the schema, so that the
provider finds one resource of each type and reads it as it would a real
one.

`derive.py` names the permission for each request after its method: a
request that calls `secretmanager.projects.secrets.list` takes
`secretmanager.secrets.list`. It
keeps a name only if Google's page of permissions for the service has it,
and only if Google's list of what custom roles support marks it
`SUPPORTED`. Anything that is not a GET marks the type, and so does a read
of what a service stores, of which it keeps a short list.

## What it cannot see

- A request the provider makes only for some resources, depending on how
  they are set up.
- Requests after the point where the provider rejects a made-up answer.
  Such types are marked `list_unconfirmed` or `read_unconfirmed`, and
  `moonwalk gcp-role` names them.
- Requests the provider sends to an address of its own choosing, whatever
  it was told. The probe sets a proxy that leads nowhere, so that these
  fail instead of reaching Google with a made-up token. Such a type is
  marked `list_unknown`, and so is one whose method has no permission of
  the same name on Google's page. Both are left out of the role and of the
  query file.
- Whether the permission named after a method is the one Google checks.
  That is the rule for nearly all of them, and where it is known not to
  hold, `derive.py` has the exception written down.

The first two, and the last, show up the first time the role is used:
Google names the permission it refused, and moonwalk repeats it when it
reads the listing.
