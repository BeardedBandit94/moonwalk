# Recording what the AWS provider needs

`internal/awsiam/permissions.json` says which IAM actions listing each AWS
resource type takes. It is recorded, not written: these scripts run the
Terraform AWS provider against a stand-in for AWS that notes every call,
and turn the calls into IAM actions. Run them again when a newer provider
can list more types. Nothing here talks to AWS, and none of it is part of
the build.

## What it needs

- Terraform 1.14 or later.
- A directory where `terraform init` has installed the AWS provider (its
  `.terraform` and `.terraform.lock.hcl`), and that provider's schema:

  ```bash
  terraform providers schema -json > schema.json
  ```

- A moonwalk binary, for the query files.
- Python 3 with `botocore` and `cbor2`:

  ```bash
  python3 -m venv venv && venv/bin/pip install botocore cbor2
  ```

- Access to `servicereference.us-east-1.amazonaws.com`, where AWS
  publishes what authorizes each API operation. `derive.py` downloads it
  once into the directory given as `--cache`.

## Running it

```bash
venv/bin/python probe.py --terraform terraform --moonwalk moonwalk \
  --schema schema.json --plugins <directory> --out calls.json
```

```bash
venv/bin/python derive.py --calls calls.json --schema schema.json \
  --provider "hashicorp/aws <version>" --cache reference \
  --out ../../internal/awsiam/permissions.json
```

Then `go test ./cmd/moonwalk -update` to rewrite the example policies in
`docs/aws-iam`, and read the difference before committing it: it is the
change in what moonwalk asks an account for.

## How it works

`recorder.py` answers any AWS request with a made-up, well-formed response
built from the API descriptions in botocore: one item in every list, and no
further pages. `probe.py` points the provider at it and lists each type
twice, once for identities and once with `include_resource`, so the
provider finds one resource of each type and reads it as it would a real
one. The access key of each run says which type and which way, so every
recorded call is known to belong to it.

`derive.py` looks each call up in AWS's Service Reference, which says which
actions authorize an operation, whether an action writes, manages
permissions or only tags, and which kinds of resource it can be limited
to. A type is written out only if every call it made is answered there and
every action is a plain read; the rest are reported and left out.

## What it cannot see

- A call the provider makes only for some resources: one that depends on a
  setting the made-up resource does not have.
- Calls after the point where the provider rejects a made-up answer. Such
  types are marked `list_unconfirmed` or `read_unconfirmed`, and
  `moonwalk aws-iam-policy` names them.

Both show up the first time the policy is used: AWS says which action it
refused, and moonwalk repeats it when it reads the listing.
