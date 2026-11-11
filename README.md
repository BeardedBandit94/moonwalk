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

Attribute values are not loaded yet. State stores secrets in plain text, so
they stay out of the graph until redaction exists.

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

## License

Apache-2.0
