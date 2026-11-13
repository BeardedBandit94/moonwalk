# Changelog

What changed in each release of moonwalk, newest first. The release workflow publishes a version's section as its release notes, so each paragraph here is one line: GitHub keeps line breaks in release notes.

## v0.2.0

moonwalk now writes the permissions that listing a cloud takes, for AWS, Azure and Google Cloud, and Google Cloud is covered like the other two.

The permissions were recorded by running each Terraform provider against a stand-in for its cloud. **None of them has been tried against a real account, subscription or project yet.** If a permission is missing, the listing is refused it and moonwalk names it.

### Added

- **`moonwalk aws-iam-policy`** writes the IAM policies that the query file needs. Every action is named in full and is a read; resources are limited to their kind and your account wherever AWS allows it. With `-state` it also writes the policy for reading states from S3.
- **`moonwalk azure-role`** writes one Azure custom role: every permission named in full and ending in `/read`, no data actions, assignable only at the subscriptions named.
- **`moonwalk gcp-role`** writes one Google Cloud custom role, as the YAML `gcloud iam roles create --file` takes: every permission named in full, each a list or a get.
- **Google Cloud in the graph.** Resources are placed in their networks and subnetworks, peerings are folded into links, and `gcloud asset search-all-resources` listings are read.
- **`query-file -project` and `-zone`** write a Google list block for every project and region, zone or location a type takes.
- **Refused permissions are named.** When a listing is refused an action, moonwalk's warning says which, for all three clouds.
- **A command reference** in the README: every command, its inputs and every option with its default.
- **Permission guides in the archives.** `docs/aws-iam`, `docs/azure-rbac` and `docs/gcp-iam` hold ready-made policies and roles for everything moonwalk can list, the rules they keep and how to set them up. They now ship next to the binary, where the README's links lead.

### Changed

`query-file` now asks only for what the policy or role from the commands above allows. A query file written by v0.1.0 lists more:

- **AWS:** Lambda functions and S3 buckets are no longer listed, and SSM parameters are listed without attributes, because listing them takes an action that returns stored content. Give `query-file` and `aws-iam-policy` `-allow` with `lambda:GetFunction`, `s3:ListBucket` or `ssm:GetParameter` to include them.
- **Azure:** seven types, storage accounts and key vaults among them, are listed without attributes, because their attributes take keys or a service's own endpoint. Service Bus namespaces are left out.
- **Google Cloud:** four types are left out, because the permission that listing them takes could not be worked out: `google_project`, `google_app_engine_domain_mapping` and the two `google_identity_platform_*_config` types.

Each query file says in its opening comment what it leaves out, and why.

### Good to know

- `aws-iam-policy` does not write a policy with `*` in the account position unasked. Without `-account` it warns and asks at the terminal; in a script it stops with an error unless `-any-account` is given.
- Give `query-file` and the permissions command for the same cloud the same schema and options, and the policy or role allows what the query file asks for and nothing else.

## v0.1.0

The first release, on 12 November 2026.

- Reads Terraform and OpenTofu states, configuration, plans and listings of the cloud into one graph, with every resource marked managed, unmanaged, drifted or declared.
- `serve` opens the graph as an explorer in the browser; `view` prints it collapsed into groups; `graph` prints all of it.
- `query-file` writes the query file for `terraform query`.
- Reads states from S3, links states to each other, and collects, loads and compares snapshots.
- Covers AWS and Azure.
