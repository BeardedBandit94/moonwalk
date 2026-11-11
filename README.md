# moonwalk

An explorer for infrastructure as code. moonwalk reads Terraform and OpenTofu
state into one normalized resource graph, with the goal of showing code, state
and the live cloud environment side by side in a single interactive view.

## Status

Early. What works today: loading state and printing its resource graph.

```bash
go run ./cmd/moonwalk graph terraform.tfstate
```

```bash
terraform show -json | go run ./cmd/moonwalk graph -format json -
```

Both raw version 4 state files and `terraform show -json` / `tofu show -json`
output are accepted, and produce the same graph.

### Several states

Any number of states load into one graph, each as its own source. A state is
a file, a directory (every `.tfstate` file below it), or `-` for standard
input, optionally given a shorter name:

```bash
go run ./cmd/moonwalk graph network=network.tfstate app=app.tfstate
```

```bash
go run ./cmd/moonwalk graph live=./environments
```

Dependencies between resources in different states are not detected yet.

### Account and region

Every node records its source and, where it can be worked out, its account
and region. These come from resource attributes for the AWS, Azure and Google
providers (ARN, subscription ID, project and so on). A resource that does not
state its account takes the one its provider's other resources agree on, and
is marked `account_inferred` in JSON output.

### Attributes and secrets

State stores secrets in plain text. moonwalk prints attribute values only
when asked, with `-format json -attributes`, and replaces sensitive values
with `"(sensitive)"`:

- values the state marks as sensitive;
- any other copy of such a value elsewhere in the same state, since
  Terraform does not always carry the mark along;
- in raw state files, strings under attribute names such as `password` or
  `private_key`, in case the version that wrote the file did not record
  every sensitive path.

`show -json` output is the more reliable input, because it always carries
the provider's own list of sensitive attributes. Treat redacted output as
reduced risk, not as safe to publish.

An OpenTofu state encrypted at rest cannot be read directly; pipe
`tofu show -json` into moonwalk instead.

### JSON output

JSON output carries a `schema_version`. It changes only when a field is
removed or changes meaning.

## Layout

| Path | Purpose |
|---|---|
| `cmd/moonwalk` | The command-line entry point |
| `internal/graph` | The normalized graph model every input is translated into |
| `internal/tfstate` | Adapter from Terraform and OpenTofu state to the graph |

New inputs (HCL, live discovery) are added as further adapters that produce a
`graph.Graph`; nothing outside an adapter should know its source format.

## Development

```bash
go test ./...
```

Golden files for the command-line tests are rewritten with:

```bash
go test ./cmd/moonwalk -update
```

The state parsers have a fuzz target and a 100,000-resource benchmark:

```bash
go test ./internal/tfstate -fuzz FuzzLoad -fuzztime 1m
```

```bash
go test ./internal/tfstate -run '^$' -bench Load100k
```

## License

Apache-2.0
