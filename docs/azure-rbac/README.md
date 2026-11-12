# Azure permissions for moonwalk

What an Azure identity needs so that moonwalk can show a subscription: one
custom role, written by `moonwalk azure-role`. The roles in this directory
are what it writes for everything it can list, kept here to be read without
running anything.

| File | For | Permissions |
|---|---|---|
| [`identities-only.json`](identities-only.json) | Finding what exists: names and IDs | 61 |
| [`with-attributes.json`](with-attributes.json) | The same, and each resource's settings | 63 |

They are written for subscription `00000000-0000-0000-0000-000000000000`.
Write your own with your subscriptions and only the types you list:

```bash
moonwalk azure-role -subscription 11111111-2222-3333-4444-555555555555 schema.json > moonwalk-discovery.json
```

Give `azure-role` and `query-file` the same schema and the same `-type` and
`-identity-only`, and the role allows what the query file asks for and
nothing else.

## The rules the role keeps

1. **Every permission is named in full.** No `*` anywhere: not
   `*/read`, which is what the built-in Reader role is, and not
   `Microsoft.Network/*/read`.
2. **Reads only.** Every permission ends in `/read`, which authorizes a
   GET. There is no `/action`, which is how keys are asked for, and no
   `/write` or `/delete`.
3. **No data actions.** `DataActions` is empty, so nothing a service stores
   can be read: no blobs, no secrets, no queue messages.
4. **No keys by another name.** A read that returns keys all the same is
   treated as what it is, and left out (see below).
5. **Assignable only where you say.** `AssignableScopes` holds the
   subscriptions given and nothing wider.

The tests check all five on these files, and that the files are what the
command writes. One role holds everything: Azure accepts a definition of up
to 1 MB, and these are a few kilobytes.

Unlike AWS, an Azure role has no resource patterns to narrow. What limits
it is where it is assigned: assign it at a subscription to list the
subscription, or at a resource group to list only that group.

## Where the permissions come from

They are recorded, not written by hand. The Terraform azurerm provider
(5.13.0) was run against a stand-in for Azure that notes every request,
once for each type it can list, with and without attributes. A request to
Azure Resource Manager is authorized by a permission that follows from its
path, and each was looked up in the
[permissions Microsoft publishes](https://learn.microsoft.com/azure/role-based-access-control/resource-provider-operations).
`scripts/azure-permissions` does it again for a newer provider.

For most types, listing takes a single request and a single permission:
the listing already holds the attributes.

**None of this has been run against a real subscription.** What follows
from that:

- For 14 of the 66 types the recording stopped before the end, because the
  provider did not accept the stand-in's made-up answers. What was asked
  for up to that point is in the role; a real subscription may show more.
  `moonwalk azure-role` names those types.
- Two permissions are not in Microsoft's published list, which trails the
  services: `Microsoft.Network/natGateways/read` and
  `Microsoft.StorageMover/storageMovers/read`. They are spelled as the
  provider's requests spell them.

If the role lacks a permission, Azure says which in its refusal, and
moonwalk repeats it when it reads the listing.

One of the 67 listable types is not covered: `azurerm_mssql_database`,
which cannot be listed without naming a server.

## What takes more than reading

For eight types the provider asks for something a read-only role must not
give. The query file and the role both leave it out:

| Type | What the provider asks for | So it is |
|---|---|---|
| `azurerm_storage_account` | The account's keys (`listkeys/action`) | Listed without attributes |
| `azurerm_cognitive_account` | The account's keys (`listKeys/action`) | Listed without attributes |
| `azurerm_log_analytics_workspace` | The workspace's shared keys (`sharedkeys/action`) | Listed without attributes |
| `azurerm_signalr_service` | The service's keys (`listkeys/action`) | Listed without attributes |
| `azurerm_automation_account` | Its registration keys, through a permission that is named as a read | Listed without attributes |
| `azurerm_key_vault` | The vault's own endpoint, for its certificate contacts | Listed without attributes |
| `azurerm_attestation_provider` | The provider's own endpoint | Listed without attributes |
| `azurerm_servicebus_namespace` | The namespace's keys, even to list it | Left out |

There is no option to add these back. A role that can list a storage
account's keys can read everything in it.

## What Terraform cannot list

Virtual networks and subnets are not in the query file: the provider lists
a network only within a resource group that is named, and a subnet only
within a network. So a listing made with `terraform query` finds network
interfaces, security groups and route tables, and not the networks they
are in. `az resource list`, below, finds the networks.

## Setting up the role

One identity for the listing, with no secret to keep: a managed identity
where the listing runs in Azure, or workload identity federation from a
pipeline.

1. Create the role:

   ```bash
   az role definition create --role-definition moonwalk-discovery.json
   ```

2. Assign it to the identity, at the subscription:

   ```bash
   az role assignment create --assignee-object-id <object id> --assignee-principal-type ServicePrincipal \
     --role "moonwalk discovery" --scope /subscriptions/11111111-2222-3333-4444-555555555555
   ```

3. Tell the provider not to register resource providers. Registering is a
   write, which the role does not allow:

   ```hcl
   provider "azurerm" {
     features {}
     subscription_id                 = "11111111-2222-3333-4444-555555555555"
     resource_provider_registrations = "none"
   }
   ```

For several subscriptions, name them all with `-subscription`, assign the
role at each, and give `query-file` the same list.

## Listing with the Azure CLI instead

The commands moonwalk reads in place of `terraform query` take less. These
permissions are from Microsoft's descriptions of them, not recorded and not
tried.

| Command | Permission |
|---|---|
| `az resource list` | `Microsoft.Resources/subscriptions/resources/read` |
| `az group list` | `Microsoft.Resources/subscriptions/resourceGroups/read` |
| `az graph query` | `Microsoft.ResourceGraph/resources/read`, and read access to each resource it is to return |

A role for the first two:

```json
{
  "Name": "moonwalk resource listing",
  "IsCustom": true,
  "Description": "Lets the Azure CLI list the resources and resource groups of a subscription, by name, type and ID.",
  "Actions": [
    "Microsoft.Resources/subscriptions/resourceGroups/read",
    "Microsoft.Resources/subscriptions/resources/read"
  ],
  "NotActions": [],
  "DataActions": [],
  "NotDataActions": [],
  "AssignableScopes": ["/subscriptions/11111111-2222-3333-4444-555555555555"]
}
```

Resource Graph returns only what its caller can already read, so it needs
the discovery role above as well.

## Reading states

moonwalk does not read Azure Storage itself. State kept there by the
`azurerm` backend is read through Terraform, with whatever access the
backend already has:

```bash
terraform state pull | moonwalk view prod=-
```
