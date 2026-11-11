# moonwalk

An explorer for infrastructure as code. moonwalk reads Terraform and OpenTofu
state into one normalized resource graph, with the goal of showing code, state
and the live cloud environment side by side in a single interactive view.

## Status

Early. What works today: loading state and printing its resource graph,
either collapsed into groups or in full.

### A readable view

`view` arranges the resources into nested groups (state, account, region,
module, then the instances of each resource block) and expands as many
levels as fit in 300 items, whatever the size of the estate:

```bash
go run ./cmd/moonwalk view terraform.tfstate
```

A group marked `+` is collapsed. Dependencies are shown between the visible
items, so resources hidden inside a collapsed group count towards one link
from that group. Go deeper with `-depth`, or change the levels and their
order with `-group-by`:

```bash
go run ./cmd/moonwalk view -depth 5 -group-by account,module terraform.tfstate
```

No group ever has more than 100 children: a crowded group is split by
resource type, and then into ranges.

`-group-by` also takes `network`, which puts each resource under the network
and subnet that contain it:

```bash
go run ./cmd/moonwalk view -group-by source,region,network terraform.tfstate
```

Narrow the view down with filters. Each takes comma-separated values, and
`-type` takes patterns:

```bash
go run ./cmd/moonwalk view -type 'aws_iam_*' -region global terraform.tfstate
```

```bash
go run ./cmd/moonwalk view -search bastion terraform.tfstate
```

`-around` shows one resource and everything directly related to it:

```bash
go run ./cmd/moonwalk view -around aws_instance.app terraform.tfstate
```

### What moonwalk works out

State does not record everything worth showing, so some of the graph is
inferred from resource attributes. Where the attributes do not settle a
question, nothing is changed.

- **Containment.** A resource that names a subnet or network by ID sits
  inside it; one that spans several subnets sits in their network. This
  works across states. Only AWS networks and subnets are covered so far.
- **Which instance is meant.** State records a dependency on a block with
  `count` or `for_each` as a dependency on every instance. Where a
  resource's attributes hold a value that only some instances have, such as
  one subnet's ID, the other dependencies are dropped.
- **Glue.** A resource named as an association, attachment or membership
  that only joins two others is folded into a link between them, shown as
  `--`. `-glue` shows such resources instead.

### The full graph

`graph` prints every resource and relationship:

```bash
go run ./cmd/moonwalk graph terraform.tfstate
```

```bash
terraform show -json | go run ./cmd/moonwalk graph -format json -
```

State lists indirect dependencies next to direct ones; `-direct` leaves out
those that others already imply. `view` always does.

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
is marked `account_inferred` in JSON output. The same goes for the region of
an AWS resource (`region_inferred`). AWS resources of services without
regions, such as IAM, get the region `global`.

### Attributes and secrets

State stores secrets in plain text. moonwalk prints attribute values only
when asked, with `graph -format json -attributes`, and replaces sensitive values
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

Both commands take `-format json`, and the output carries a
`schema_version`. It changes only when a field is removed or changes meaning.

## Layout

| Path | Purpose |
|---|---|
| `cmd/moonwalk` | The command-line entry point |
| `internal/graph` | The normalized graph model every input is translated into |
| `internal/tfstate` | Adapter from Terraform and OpenTofu state to the graph |
| `internal/relate` | Relationships inferred from attributes: containment, glue |
| `internal/view` | Grouping of a graph, filters, and the collapsed views of it |
| `internal/tfaddr` | Parsing of Terraform resource addresses |

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
