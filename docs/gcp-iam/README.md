# Google Cloud permissions for moonwalk

What an identity needs in Google Cloud so that moonwalk can show a project:
one custom role, written by `moonwalk gcp-role`. The role in this directory
is what it writes for everything it can list, kept here to be read without
running anything.

| File | For | Permissions |
|---|---|---|
| [`moonwalk-discovery.yaml`](moonwalk-discovery.yaml) | Finding what exists, and each resource's settings | 58 |

Write your own for only the types you list:

```bash
moonwalk gcp-role schema.json > moonwalk-discovery.yaml
```

Give `gcp-role` and `query-file` the same schema and the same `-type`, and
the role allows what the query file asks for and nothing else.

There is one file where AWS and Azure have two, because for Google Cloud a
listing already holds each resource's settings: with `-identity-only` the
role has the same permissions.

## The rules the role keeps

1. **Every permission is named in full.** A custom role cannot hold a
   wildcard, and this one holds no stand-in for one either: it is not built
   from `roles/viewer`, which holds thousands of permissions.
2. **Lists and gets only.** Every permission ends in `.list` or `.get`,
   which authorize a GET. All but one end in `.list`; the exception is
   `bigquery.datasets.get`, which is the permission BigQuery asks of whoever
   lists datasets.
3. **Nothing a service stores.** No secret's value
   (`secretmanager.versions.access`), no object, no service account key, no
   serial port output. A type whose listing took one of those would be left
   out, or listed without attributes; with this provider, none does.
4. **Only permissions Google supports in custom roles.** Each is marked
   `SUPPORTED` on
   [Google's list](https://docs.cloud.google.com/iam/docs/custom-roles-permissions-support).

The tests check the first three on the file here, and that the file is what
the command writes. One role holds everything: Google accepts up to 3,000
permissions and 64 KB in a custom role.

A role has no resource patterns to narrow. What limits it is where it is
granted: grant it on a project to list that project and nothing else.

## Where the permissions come from

They are recorded, not written by hand. The Terraform google provider
(8.11.0) was run against a stand-in for Google's APIs that notes every
request, once for each type it can list, with and without attributes. Each
request was matched to the method it calls in Google's own description of
the API, the permission was named after the method, and the name was kept
only if
[Google's list of permissions](https://docs.cloud.google.com/iam/docs/roles-permissions)
has it. `scripts/google-permissions` does it again for a newer provider.

**None of this has been run against a real project.** What follows from
that:

- For one of the 61 types, `google_vertex_ai_tensorboard`, the recording
  stopped before the end, because the provider did not accept the
  stand-in's made-up answer. What was asked for up to that point is in the
  role; a real project may show more. `moonwalk gcp-role` names it.
- A permission is named after its method, which is how nearly all of
  Google's are named, but not a promise Google makes. A name that is not
  on Google's list is not used. One that is on the list and still not the
  one Google checks would show up as a refusal.

If the role lacks a permission, Google says which in its refusal, and
moonwalk repeats it when it reads the listing.

Four of the 65 types that can be listed without naming a parent are not
covered, and the query file leaves them out as well:

| Type | Why |
|---|---|
| `google_app_engine_domain_mapping` | Google's list has no permission named after the method that lists them |
| `google_identity_platform_inbound_saml_config` | The same |
| `google_identity_platform_oauth_idp_config` | The same |
| `google_project` | The provider asks at an address of its own choosing, which the stand-in could not take the place of |

## What the role cannot do

**Turn on an API.** Google answers for a service only in a project where
that service's API is enabled, whatever the role holds. Enabling one is a
write, and not in the role. Where the Apigee or Vertex AI API is off, the
listing reports an error for those blocks and moonwalk warns that it is
incomplete. Narrow the file to the services in use:

```bash
moonwalk query-file -type 'google_compute_*,google_storage_*,google_pubsub_*' -project acme-prod schema.json > discover.tfquery.hcl
```

```bash
moonwalk gcp-role -type 'google_compute_*,google_storage_*,google_pubsub_*' schema.json > moonwalk-discovery.yaml
```

## What Terraform cannot list

The provider (8.11.0) has list resources for 154 types, 65 of them without
naming a parent. Networks are not among them, nor are GKE clusters or Cloud
SQL instances. So a listing made with `terraform query` finds subnetworks,
firewall rules and instances, and not the networks they are in. The asset
inventory, below, finds the networks and the rest.

## Setting up the role

One identity for the listing, with no key to keep: a service account that
is attached to where the listing runs, or workload identity federation from
a pipeline. Do not create a service account key for it.

1. Create the role in the project. Its ID is yours to choose, of letters,
   digits, underscores and periods:

   ```bash
   gcloud iam roles create moonwalkDiscovery --project=acme-prod --file=moonwalk-discovery.yaml
   ```

2. Grant it to the identity, on the project:

   ```bash
   gcloud projects add-iam-policy-binding acme-prod \
     --member='serviceAccount:moonwalk@acme-prod.iam.gserviceaccount.com' \
     --role='projects/acme-prod/roles/moonwalkDiscovery'
   ```

3. Say where to list. A list block covers one project, and one region, zone
   or location where its type has one:

   ```bash
   moonwalk query-file -project acme-prod -region europe-west1 -zone europe-west1-b,europe-west1-c schema.json > discover.tfquery.hcl
   ```

For several projects, name them all with `-project` and grant the role on
each. A role created once for an organization, with `--organization=<id>`
in place of `--project`, can be granted on any of its projects as
`organizations/<id>/roles/moonwalkDiscovery`. When the permissions change,
`gcloud iam roles update` takes the new file.

## Listing with the asset inventory instead

The command moonwalk reads in place of `terraform query` takes one
permission. It is from Google's description of the command, not recorded
and not tried.

| Command | Permission |
|---|---|
| `gcloud asset search-all-resources --scope=projects/<project>` | `cloudasset.assets.searchAllResources`, on the project |

As a role:

```yaml
title: "moonwalk asset listing"
description: "Lets gcloud search the asset inventory of a project for its resources: name, type, location and labels."
stage: GA
includedPermissions:
- cloudasset.assets.searchAllResources
```

The Cloud Asset API has to be enabled in the project the search is made
from. The inventory covers far more types than Terraform can list, in one
request per page, and it returns less about each: enough to say what exists
and where, and not to place it in a network.

## Reading states

moonwalk does not read Cloud Storage itself. State kept there by the `gcs`
backend is read through Terraform, with whatever access the backend already
has:

```bash
terraform state pull | moonwalk view prod=-
```
