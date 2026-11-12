# Recording what the azurerm provider needs

`internal/azurerbac/permissions.json` says which Azure permissions listing
each resource type takes. It is recorded, not written: these scripts run
the Terraform azurerm provider against a stand-in for Azure that notes
every request, and turn the requests into permissions. Run them again when
a newer provider can list more types. Nothing here talks to Azure, and
none of it is part of the build.

## What it needs

- Terraform 1.14 or later, and the `openssl` command.
- A directory where `terraform init` has installed the azurerm provider
  (its `.terraform` and `.terraform.lock.hcl`), and that provider's schema:

  ```bash
  terraform providers schema -json > schema.json
  ```

- A moonwalk binary, for the query files.
- Python 3, with nothing added.
- Access to `raw.githubusercontent.com`, for the source of Microsoft's
  published list of permissions. `derive.py` downloads it once into the
  directory given as `--cache`.

## Running it

```bash
python3 probe.py --terraform terraform --moonwalk moonwalk \
  --schema schema.json --plugins <directory> --out calls.json
```

```bash
python3 derive.py --calls calls.json --provider "hashicorp/azurerm <version>" \
  --cache catalog --out ../../internal/azurerbac/permissions.json
```

Then `go test ./cmd/moonwalk -update` to rewrite the example roles in
`docs/azure-rbac`, and read the difference before committing it: it is the
change in what moonwalk asks a subscription for.

## How it works

`recorder.py` plays the three parts the provider talks to: the metadata
service that says where everything is, the sign-in service, and Azure
Resource Manager. The provider is pointed at it with `metadata_host`, and
trusts its certificate for the length of the run. Every listing is
answered with one made-up resource in the shape all Azure resources share,
so the provider finds one resource of each type and reads it as it would a
real one. The client ID of each run says which type and which way, so
every recorded request is known to belong to it.

`derive.py` names the permission for each request, which follows from its
path and method, and takes its spelling from Microsoft's list. Anything
that is not a plain read marks the type: a POST, which is how keys are
asked for; a read that returns keys all the same, of which it keeps a
short list; and a request to a service's own endpoint, which shows as an
error because the stand-in gives those endpoints names that lead nowhere.

## What it cannot see

- A request the provider makes only for some resources, depending on how
  they are set up.
- Requests after the point where the provider rejects a made-up answer.
  Such types are marked `list_unconfirmed` or `read_unconfirmed`, and
  `moonwalk azure-role` names them.
- What a request to a service's own endpoint would have asked for.

The first two show up the first time the role is used: Azure names the
permission it refused, and moonwalk repeats it when it reads the listing.
