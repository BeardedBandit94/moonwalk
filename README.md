# moonwalk

An explorer for infrastructure as code. moonwalk reads Terraform and OpenTofu
state into one normalized resource graph, with the goal of showing code, state
and the live cloud environment side by side in a single interactive view.

## Install

With Homebrew, on macOS or Linux:

```bash
brew install beardedbandit94/tap/moonwalk
```

Or with Go 1.25 or later:

```bash
go install github.com/BeardedBandit94/moonwalk/cmd/moonwalk@latest
```

Or as a container image, built from a checkout:

```bash
docker build -t moonwalk .
```

```bash
docker run --rm --user "$(id -u)" -p 127.0.0.1:7878:7878 -v "$PWD:/work:ro" moonwalk serve -addr 0.0.0.0:7878 /work/terraform.tfstate
```

In a container the explorer has to listen on every address to be reached
at all; publishing the port on `127.0.0.1`, as above, keeps it to this
machine.

The examples below run moonwalk from a checkout with `go run ./cmd/moonwalk`;
with it installed, that is just `moonwalk`.

## Status

Early. What works today: loading state, code, plans and listings of the
cloud into one graph and exploring it in the browser, or printing it, either
collapsed into groups or in full; reading states straight from S3;
collecting the graph into snapshots to look at together; and comparing two
of them. AWS is covered best, then Azure, then Google Cloud, which has no
written permissions yet.

### The explorer

`serve` opens the graph in the browser:

```bash
go run ./cmd/moonwalk serve terraform.tfstate
```

It starts on the same collapsed view that `view` prints, as nested boxes
with the dependencies drawn between them.

- Click a group to open or close it. If opening one would put more than 300
  items on screen, the groups opened longest ago are closed to make room.
- Click a resource to see its details: where it sits, what it depends on,
  what depends on it, and its attributes with secrets redacted.
- Click a link to list the dependencies it stands for.
- The bar at the top holds the grouping, a search, and filters by type,
  account, region and status.
- The address bar always holds a link back to what is on screen.
- When a state file or the code changes on disk, the explorer loads it
  again.

The explorer listens on `127.0.0.1:7878` and answers only requests
addressed to this machine. `-addr` changes that; since the page shows
attribute values, use another address only on a network you trust.

The front end is plain HTML, CSS and JavaScript embedded in the binary, with
no build step and no dependencies.

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

### Code alongside state

Point `-code` at a root module's directory to join the configuration to the
state. It works with all three commands:

```bash
go run ./cmd/moonwalk serve -code ./infra terraform.tfstate
```

- Every resource gains the file and line where its block is written, and
  the explorer shows the block itself.
- A resource that is written in the code but absent from the state appears
  with the status `declared`, drawn with a dashed border, with dependencies
  worked out from its expressions: through locals, variables and module
  outputs. `-status declared` shows only those.
- A resource that is in the state but no longer in the code is marked
  `not in code`.
- The explorer lists which argument of a resource refers to which other
  resource, and through what: `vpc_id → aws_vpc.main through
  local.network_id`.
- State records indirect dependencies next to direct ones. With the code,
  moonwalk drops the ones a resource reaches only through something else
  it refers to, and keeps any that the code does not explain.
- With `-code` and no state, the graph is built from the code alone.
- With several states, name the one the code belongs to:
  `-code network=./infra/network`.

The code is read for its structure only; nothing is evaluated, so no
variables, providers or credentials are needed. `.tf` and `.tofu` files are
read, in the native syntax and the JSON one (`.tf.json`), and override files
are applied. Modules are followed when their source is a relative path, or
when `terraform init` has fetched them into `.terraform/modules`; moonwalk
says which it could not find.

### Plans

Give moonwalk the JSON form of a plan in place of a state:

```bash
terraform plan -out plan.bin
```

```bash
terraform show -json plan.bin > plan.json
```

```bash
go run ./cmd/moonwalk serve plan.json
```

It shows the state the plan was made against, with each resource marked by
what the plan will do to it: create, update, delete or replace. Resources to
be created are added, with their dependencies. `-change` filters on this:
`-change any` shows everything the plan touches, `-change delete,replace`
what it will destroy.

A plan also carries what its refresh found to have drifted: changed or
deleted outside Terraform. Such resources get the status `drifted`. To look
for drift alone, make the plan with `-refresh-only`.

A plan file holds variables and planned values in the clear, secrets
included. moonwalk reads neither.

### What is really in the cloud

Terraform 1.14 and later can list what exists through the provider, with
`terraform query`. moonwalk writes the query file for you, from the
provider's own account of what it can list:

```bash
terraform providers schema -json > schema.json
```

```bash
go run ./cmd/moonwalk query-file -region us-east-1,eu-west-1 schema.json > discover.tfquery.hcl
```

```bash
terraform validate -query
```

Delete any block that `validate` rejects: a few types need an argument that
the schema does not mark as required. Then run the listing, and give
moonwalk the result along with the states:

```bash
terraform query -json > found.jsonl
```

```bash
go run ./cmd/moonwalk serve -group-by account,region,network terraform.tfstate found.jsonl
```

Every listed resource that no loaded state holds appears with the status
`unmanaged`; `-status unmanaged` shows only those, and each group says how
many it contains. Grouping without `source`, as above, puts them next to the
managed resources they sit among.

- **Set a limit.** A list block returns 100 resources and stops, without
  saying so, unless it sets `limit`. The files moonwalk writes do. If you
  write your own and a block returns exactly 100, moonwalk warns.
- **Attributes are used, not shown.** `include_resource` makes Terraform
  return each resource's attributes, which lets moonwalk place it in its
  network. A listing does not say which are secret, so they are never
  printed. `query-file -identity-only` leaves them out altogether.
- **Cloud defaults are left out**: default security groups, service-linked
  roles. The AWS provider itself leaves out default networks and subnets.
- **Azure subscriptions.** A list block of the `azurerm` provider covers one
  subscription, the provider's own unless it names another:
  `query-file -subscription <id>,<id>` writes a block for each.
- **Errors make it incomplete.** If the listing reports one, such as a type
  it may not list, moonwalk warns.
- **Only what is listed is found.** The AWS provider (6.73) can list 280 of
  its 1,766 resource types, 196 of them without naming a parent resource;
  Azure can list 126 and Google 154. `query-file -type` narrows the choice.
- **Missing from the cloud.** A resource in state that the listing covered,
  by type, account and region, and did not find is marked `not found in the
  cloud listing`. It may be gone; it may also be one the provider leaves
  out, such as a default network that the code has adopted.
- **Import blocks** for what is unmanaged come from Terraform itself:
  `terraform query -generate-config-out=generated.tf`.

Listing needs credentials that can read, and nothing more; moonwalk itself
never touches the cloud, unless it is given the address of a state in S3,
and then it reads that and nothing else.

#### Permissions on AWS

`iam-policy` writes the IAM policies that the query file needs, from the
same schema and options, so that the role may do what the listing does and
nothing else:

```bash
go run ./cmd/moonwalk iam-policy -account 111122223333 -region us-east-1,eu-west-1 -out policies schema.json
```

Every action is named in full, with no wildcards. Every one is a read by
AWS's own classification. Resources are limited to their kind and your
account wherever AWS allows an action to be limited, and `"Resource": "*"`
appears only for the actions it does not. The actions were recorded from
the AWS provider itself, run against a stand-in for AWS, and looked up in
AWS's Service Reference; they have not been tried against a real account.
If the role lacks an action, the listing is refused it, and moonwalk says
which.

Three actions that the provider calls return what a service stores:
`lambda:GetFunction` (a link to the code), `s3:ListBucket` (a bucket's
objects) and `ssm:GetParameter` (a parameter's value). They are left out,
and so are the types that cannot be listed without them, unless both
commands are given `-allow` with the action. `-identity-only` asks for
least: about two thirds of the actions, and no resource's settings.

[docs/aws-iam](docs/aws-iam) has the policies for everything moonwalk can
list, the rules they keep, how to set up the role, and the policy for
reading states from S3.

#### Permissions on Azure

`azure-role` writes the custom role that the query file needs, in the form
`az role definition create` takes:

```bash
go run ./cmd/moonwalk azure-role -subscription 11111111-2222-3333-4444-555555555555 schema.json > moonwalk-discovery.json
```

Every permission is named in full and ends in `/read`; there are no
wildcards, no data actions, and nothing that asks for keys. The role can be
assigned only at the subscriptions named. As for AWS, the permissions were
recorded from the provider itself, run against a stand-in, and have not
been tried against a real subscription; a permission the role lacks is
named when the listing is refused it.

Eight types take more than reading: a storage account's attributes, for
one, take its keys. Those are listed without attributes, or in one case
left out, by the query file and the role alike. Set
`resource_provider_registrations = "none"` in the provider: registering
resource providers is a write. [docs/azure-rbac](docs/azure-rbac) has the
roles, the rules they keep and how to assign them.

#### Without `terraform query`

OpenTofu has no `query` command (as of 1.10), and Terraform before 1.14 has
none either. Two ways round it:

- Use a Terraform 1.14 binary for the listing alone. It needs only a
  directory with the provider configuration and the query file; it does not
  touch your state.
- Give moonwalk a listing from the AWS CLI instead:

```bash
aws resourcegroupstaggingapi get-resources > found.json
```

```bash
aws resource-explorer-2 search --query-string "*" > found.json
```

These name resources by ARN only. A resource is matched to state by its
ARN, and an unmanaged one is placed in its account and region but not in a
network. The first command knows only resources that have, or once had,
tags; the second needs Resource Explorer switched on. Both page their
results: make sure the file holds all of them.

For Azure, any of these works the same way:

```bash
az resource list > found.json
```

```bash
az group list > groups.json
```

```bash
az graph query -q "Resources" --first 1000 > found.json
```

A resource is matched to state by its ID, compared without regard to case,
as Azure compares IDs. An unmanaged one is placed in its subscription,
resource group and region, but not in a network. Left out as Azure's own:
network watchers, the resource group a Kubernetes cluster keeps its nodes
in (`MC_...`) and what is in it. A listing does not say whether a virtual
machine runs Linux or Windows, so an unmanaged one shows as
`azurerm_virtual_machine`. Resource Graph pages its results too.

For Google Cloud, the asset inventory:

```bash
gcloud asset search-all-resources --scope=projects/acme-prod --format=json > found.json
```

A resource is matched to state by its name, whichever way each side writes
it; where a service names a resource by its project's number, the rest of
the listing says which project that is. An unmanaged one is placed in its
project and region, but not in a network. Left out as Google's own: the
`default` network with its subnetworks, firewall rules and routes, and the
service accounts of Google's services.

### What moonwalk works out

State does not record everything worth showing, so some of the graph is
inferred from resource attributes. Where the attributes do not settle a
question, nothing is changed.

- **Containment.** A resource that names a subnet or network by ID sits
  inside it; one that spans several subnets sits in their network. This
  works across states, for AWS, Azure and Google Cloud networks and
  subnets. On AWS, a database or cache that names a subnet group sits
  where the group's subnets put it. On Azure, a virtual machine sits where
  its network interfaces do, and a subnet or peering is part of the
  network its ID starts with. On Google Cloud, a network or subnetwork is
  recognised however it is named: by its self link, by the same without
  the address in front, or by its name alone within its project.
- **Which instance is meant.** State records a dependency on a block with
  `count` or `for_each` as a dependency on every instance. Where a
  resource's attributes hold a value that only some instances have, such as
  one subnet's ID, the other dependencies are dropped. Where they hold
  none, as a virtual machine holds only its network interface's ID and
  state still lists the subnets, the instances kept are the ones that the
  resource's other dependencies depend on.
- **Region by parent.** An Azure subnet states no location, nor does a
  database on a server. Each takes the region of the resource its ID says
  it is part of, marked `region_inferred`.
- **Links between states.** A resource that holds the ID or ARN of a
  resource in another state depends on it. The value has to appear where a
  reference would: under an attribute named like one (`vpc_id`,
  `role_arn`, `subnet_ids`), as a path such as an ARN, among the outputs
  of a `terraform_remote_state` data source, or as the ID of a data
  source that looked the resource up. An ID that two resources claim
  links nothing. Azure IDs are compared without regard to case, and a
  Google Cloud name is the same with or without the address in front.
- **Glue.** A resource named as an association, attachment or membership
  that only joins two others is folded into a link between them, shown as
  `--`. So is a security group rule between two groups, and an Azure or
  Google Cloud peering between two networks. `-glue` shows such resources
  instead.

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
output are accepted, and produce the same graph. So is the JSON form of a
plan; see below.

### Several states

Any number of states load into one graph, each as its own source. A state is
a file, a directory (every `.tfstate` file below it), an address in S3
(see below), or `-` for standard input, optionally given a shorter name:

```bash
go run ./cmd/moonwalk graph network=network.tfstate app=app.tfstate
```

```bash
go run ./cmd/moonwalk graph live=./environments
```

A resource that names one in another state is linked to it; see
[what moonwalk works out](#what-moonwalk-works-out).

### States in S3

A state that the `s3` backend keeps in a bucket is read where it is:

```bash
go run ./cmd/moonwalk serve prod=s3://acme-terraform-state/prod/network/terraform.tfstate
```

An address that ends in `/`, or names only the bucket, loads every object
below it whose key ends in `.tfstate`, each named by the rest of its key.
That is a whole organization's states in one command, workspaces included,
since the backend keeps those under `env:/<workspace>/`:

```bash
go run ./cmd/moonwalk serve live=s3://acme-terraform-state/
```

Credentials and the region come from where the AWS command line and
Terraform take them: the environment, `~/.aws` and its profiles
(`AWS_PROFILE`), single sign-on, an assumed role, or the machine moonwalk
runs on. A bucket in another region than the configured one is found.
Reading needs `s3:GetObject` on the states and, for an address ending in
`/`, `s3:ListBucket` on the bucket; if the bucket is encrypted with a KMS
key, `kms:Decrypt` on that key as well. `moonwalk iam-policy -state
s3://acme-terraform-state/ -kms-key <key ARN>` writes exactly that policy.
Nothing is written and no lock is taken. Another store that speaks the S3 protocol is reached by setting
`AWS_ENDPOINT_URL_S3`.

States in S3 are read once: the explorer does not reload them when they
change, as it does files on disk. To keep a copy for later, or to compare
one day with the next, `collect` them into a snapshot (below).

State kept in any other backend is read through Terraform, which already
knows where it is and how to get at it:

```bash
terraform state pull | go run ./cmd/moonwalk view prod=-
```

### Snapshots

`collect` writes everything it loaded, with every relationship it worked
out, to one file. The other commands read that file in place of a state:

```bash
go run ./cmd/moonwalk collect -code . prod=terraform.tfstate found.jsonl > prod.snapshot.json
```

```bash
go run ./cmd/moonwalk serve prod.snapshot.json
```

This is how to look at an estate that no single set of credentials can
read. Collect in each account, pipeline or team, where the state and the
cloud can be reached; pass the snapshots on; and load them together, where
the resources of one are linked to those of another:

```bash
go run ./cmd/moonwalk serve network.snapshot.json payments.snapshot.json data.snapshot.json
```

A snapshot keeps the source names it was collected under, so give each
state a name that is unique across all of them. It holds one resource on
each line, and compresses to a few hundredths of its size; moonwalk reads
any input compressed with gzip:

```bash
go run ./cmd/moonwalk collect prod=terraform.tfstate | gzip > prod.snapshot.json.gz
```

A snapshot of 100,000 resources is about 100 MB, under 4 MB compressed, and
loads faster than the state it was made from.

A snapshot holds attribute values as moonwalk would show them: sensitive
ones redacted, and none at all from a state that does not mark them (see
[attributes and secrets](#attributes-and-secrets)). It still describes your
infrastructure in detail, so share it as you would share that. The code
shown in the explorer comes from the directory given with `-code`, and is
not in a snapshot.

### What changed

`diff` compares two snapshots, states or plans, the older one first:

```bash
go run ./cmd/moonwalk diff monday.snapshot.json friday.snapshot.json
```

```
1 added, 1 removed, 1 changed

Added
  + terraform_data.queue

Removed
  - terraform_data.gateway

Changed
  ~ terraform_data.server
      attributes: input, output

Dependencies removed
  - terraform_data.server → terraform_data.subnet[0]
  - terraform_data.server → terraform_data.subnet[1]
```

A resource counts as changed when its status, drift, planned change,
account or region differs, when it enters or leaves the code or the cloud
listing, or when an attribute differs. Attributes are named and their values
are not printed. Collected on a schedule, snapshots that include a listing
of the cloud show what appeared outside Terraform between two dates.

Resources are matched by source name and address. Two single states are
matched by address alone, whatever they are called. `-format json` prints
the same as JSON.

### Account and region

Every node records its source and, where it can be worked out, its account
and region. These come from resource attributes for the AWS, Azure and Google
providers (ARN, subscription ID, project and so on). A resource that does not
state its account takes the one its provider's other resources agree on, and
is marked `account_inferred` in JSON output. The same goes for the region of
an AWS resource (`region_inferred`). AWS resources of services without
regions, such as IAM, get the region `global`, and so do Google Cloud
resources that belong to no region, such as networks and firewall rules.
A Google Cloud resource in a zone is in the zone's region.

An Azure resource also records its resource group, taken from its
`resource_group_name` or its ID. `-group-by` takes `resource_group` as a
level, and the explorer offers it when there are any:

```bash
go run ./cmd/moonwalk view -group-by account,resource_group,network terraform.tfstate
```

### Attributes and secrets

State stores secrets in plain text. moonwalk prints attribute values only
when asked, with `graph -format json -attributes`, in the explorer and in
snapshots, and replaces sensitive values with `"(sensitive)"`:

- values the state marks as sensitive;
- any other copy of such a value elsewhere in the same state, since
  Terraform does not always carry the mark along;
- in raw state files, strings under attribute names such as `password` or
  `private_key`, in case the version that wrote the file did not record
  every sensitive path.

Older versions of Terraform and OpenTofu did not record the attributes that
a provider declares sensitive, such as a generated password: state files
written before version 1.8, and `show -json` output from before Terraform
1.5. moonwalk cannot redact what is not marked, so it withholds every
attribute value of such a state and says so. Relationships are still worked
out from them. To see the attributes, load `show -json` output from version
1.5 or later.

Treat redacted output as reduced risk, not as safe to publish.

An OpenTofu state encrypted at rest cannot be read directly; pipe
`tofu show -json` into moonwalk instead.

### JSON output

`view`, `graph` and `diff` take `-format json`. The output of the first two,
and a snapshot, carry a `schema_version`. It changes only when a field is removed or changes meaning.

## Layout

| Path | Purpose |
|---|---|
| `cmd/moonwalk` | The command-line entry point |
| `internal/graph` | The normalized graph model every input is translated into |
| `internal/tfstate` | Adapter from Terraform and OpenTofu state to the graph |
| `internal/tfcode` | Adapter from Terraform and OpenTofu configuration to the graph |
| `internal/discover` | Adapter from `terraform query` output, and from AWS and Azure CLI listings, to the graph |
| `internal/s3state` | Reads states from S3; `s3test` is a stand-in for S3 to test against |
| `internal/azureid` | Parsing and comparison of Azure resource IDs |
| `internal/awsiam` | The IAM policies that listing an AWS account takes, from recorded permissions |
| `scripts/aws-permissions` | Records which AWS API calls the provider makes; not part of the build |
| `docs/aws-iam` | The policies for everything moonwalk can list, and how to set up the role |
| `internal/azurerbac` | The Azure custom role that listing a subscription takes, from recorded permissions |
| `scripts/azure-permissions` | Records which requests the azurerm provider makes of Azure; not part of the build |
| `docs/azure-rbac` | The roles for everything moonwalk can list in Azure, and how to assign them |
| `internal/relate` | Relationships inferred from attributes: containment, glue, links between states |
| `internal/snapshot` | A graph written to a file and read back, and the comparison of two |
| `internal/view` | Grouping of a graph, filters, and the collapsed views of it |
| `internal/web` | The explorer: a JSON API over the graph and the browser front end |
| `e2e` | A browser test of the explorer; not part of the build |
| `internal/tfaddr` | Parsing of Terraform resource addresses |

New inputs (HCL, live discovery) are added as further adapters that produce a
`graph.Graph`; nothing outside an adapter should know its source format.

## Development

```bash
go test ./...
```

The explorer has a browser test, which needs Node and Playwright:

```bash
go build -o moonwalk ./cmd/moonwalk
```

```bash
(cd e2e && npm install && npx playwright install chromium)
```

```bash
MOONWALK_BIN=./moonwalk node e2e/explorer.cjs
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

### Releasing

Pushing a tag publishes that version:

```bash
git tag v0.1.0
```

```bash
git push origin v0.1.0
```

The release workflow tests the tag, builds archives for Linux, macOS and
Windows on amd64 and arm64 with their checksums, attaches them and a
Homebrew formula to a release on GitHub, and pushes a container image to
`ghcr.io`. A tag with a suffix, such as `v0.2.0-rc1`, becomes a pre-release.
`scripts/build-release.sh 0.1.0 dist` builds the same archives locally, and
every pull request builds them, and the image, without publishing.

## Credits

moonwalk builds on other people's work.

**Built with.** These are compiled into the binary, unmodified; their
licences ship with every release, in `THIRD_PARTY_LICENSES`.

- [HCL](https://github.com/hashicorp/hcl),
  [terraform-json](https://github.com/hashicorp/terraform-json) and
  [go-version](https://github.com/hashicorp/go-version), by HashiCorp, for
  reading configuration and Terraform's JSON formats. MPL-2.0.
- [go-cty](https://github.com/zclconf/go-cty), by Martin Atkins, the type
  system under HCL. MIT.
- The [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2), for
  reading states from S3. Apache-2.0.
- [Go](https://go.dev) and its standard library. BSD-3-Clause.

The browser test drives the explorer with
[Playwright](https://playwright.dev) (Apache-2.0), and the test fixtures
were made with [OpenTofu](https://opentofu.org) and Terraform.

**Ideas from.** No code is taken from these projects, but moonwalk would
look different without them:

- [Inframap](https://github.com/cycloidio/inframap) (MIT) showed that a
  graph of infrastructure becomes readable once the resources that only
  join two others are folded into the line between them. moonwalk's glue
  links are that idea.
- [Rover](https://github.com/im2nguyen/rover) (MIT) showed state and plans
  explored interactively in the browser, served by the tool itself.
- [driftctl](https://github.com/snyk/driftctl) (Apache-2.0) established
  the three-way reading of an estate that moonwalk shows: managed,
  unmanaged and drifted.
- [Terraformer](https://github.com/GoogleCloudPlatform/terraformer)
  (Apache-2.0) showed what exists in a cloud described in Terraform's own
  terms.
- [Steampipe](https://github.com/turbot/steampipe) (AGPL-3.0) and
  [Fix Inventory](https://github.com/someengineering/fixinventory)
  (Apache-2.0) showed many accounts and clouds brought into one model that
  can be asked questions.

And to Terraform and OpenTofu themselves: moonwalk reads the formats they
define, and leaves listing the cloud to `terraform query`. Terraform and
OpenTofu are trademarks of their owners; moonwalk is an independent
project, not affiliated with or endorsed by them.

## Security

State holds secrets, and moonwalk is careful with them; see
[attributes and secrets](#attributes-and-secrets). To report a
vulnerability, see [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
