# AWS permissions for moonwalk

What an AWS role needs so that moonwalk can show an account: to list what
exists, and, separately, to read Terraform states from S3. Both are written
by `moonwalk aws-iam-policy`; the policies in this directory are what it
writes for everything it can list, kept here to be read without running
anything.

| Directory | For | Documents | Actions |
|---|---|---|---|
| [`identities-only`](identities-only) | Finding what exists: names, IDs, ARNs | 3 | 216 |
| [`with-attributes`](with-attributes) | The same, and each resource's settings, which lets moonwalk place it in its network | 5 | 337 |
| [`state`](state) | Reading states from one bucket | 1 | 3 |

They are written for account `111122223333` and no particular region. Write
your own with your account, your regions and only the types you list:

```bash
moonwalk aws-iam-policy -account 111122223333 -region eu-west-1,us-east-1 -out policies schema.json
```

Give `aws-iam-policy` and `query-file` the same schema and the same `-type`,
`-identity-only` and `-allow`, and the policy allows what the query file
asks for and nothing else.

Always give `-account`. Without it every resource has `*` where the
account goes: `arn:aws:ec2:*:*:vpc/*` where `-account 111122223333` gives
`arn:aws:ec2:*:111122223333:vpc/*`. The command does not write that
unasked. At a terminal it warns and asks `[y/N]`. In a script, where there
is nobody to ask, it stops with an error, unless `-any-account` says that a
policy for no account in particular is what is wanted, such as one kept as
a template.

## The rules the policies keep

1. **Every action is named in full.** No `*` or `?` in any action; no
   `ec2:Describe*`.
2. **Reads only.** Every action is one that AWS's Service Reference marks
   as neither a write, nor permission management, nor tagging.
3. **Nothing that hands over what is stored.** Three actions the provider
   calls return stored content or the means to fetch it. They are left out
   unless you name them (see below).
4. **Resources as narrow as AWS allows, named as AWS names them.** An
   action that can be limited to kinds of resource is limited to those
   kinds, each written in the ARN format AWS defines for it, in your
   account: `arn:aws:lambda:*:111122223333:function:*`,
   `arn:aws:iam::111122223333:role/*`. These are the statements whose ID
   ends in `LimitedToKinds`. An action that applies to more than six
   kinds, which in practice is a service's listing of tags, has a
   statement of its own, `LimitedToService`, for everything of that
   service in your account: `arn:aws:rds:*:111122223333:*`.
5. **`"Resource": "*"` only where AWS leaves no choice.** Many actions that
   list or describe cannot be limited to a resource at all: AWS's Service
   Reference gives them no resource type, and AWS evaluates such a call
   against the resource `*`. An ARN in their statement, however well
   formed, matches nothing, and the call is refused. Those actions, and
   only those, are in statements with `*`: 156 of the 337. A statement
   whose ID contains `Limited` never has it.
6. **Regions.** With `-region`, every statement of a regional service
   carries `aws:RequestedRegion`. The global services (IAM, Route 53,
   CloudFront, Organizations) answer from one region for all and are not
   held to it.
7. **Within IAM's limits.** A managed policy holds 6,144 characters, so the
   actions are spread over several, by service. A role takes ten.

The tests check rules 1 to 5 and 7 on these documents, and that the
documents are what the command writes.

## Where the actions come from

They are recorded, not written by hand. The Terraform AWS provider (6.73.0)
was run against a stand-in for AWS that notes every API call, once for each
type it can list, with and without attributes. Each call was then looked up
in AWS's [Service Reference](https://servicereference.us-east-1.amazonaws.com/),
which says which actions authorize an operation. `scripts/aws-permissions`
does it again for a newer provider.

**None of this has been run against a real AWS account.** Two things follow:

- For 30 of the 194 types the recording stopped before the end, because
  the provider did not accept the stand-in's made-up answers. What was
  called up to that point is in the policy; a real account may show more.
  `moonwalk aws-iam-policy` names those types.
- A call the provider makes only for some resources, depending on how they
  are set up, may not have been seen.

Either shows up the first time the role is used: AWS says which action it
refused, and moonwalk repeats it when it reads the listing:

```
moonwalk: the credentials that made the listing in found.jsonl were refused 1 action: ec2:DescribeVpcAttribute. Add them to the policy, or leave those types out of the query file.
```

Two of the 196 listable types are not covered: `aws_efs_mount_target`,
which cannot be listed without naming a file system, and
`aws_s3_directory_bucket`.

## What is withheld

| Action | What it returns | Without it |
|---|---|---|
| `lambda:GetFunction` | A link to download the function's code | Lambda functions are not listed |
| `s3:ListBucket` | The objects in a bucket; the provider needs it to find a bucket's region | S3 buckets are not listed |
| `ssm:GetParameter` | A parameter's value | SSM parameters are listed without attributes |

To include one, give both commands `-allow` with the action:

```bash
moonwalk aws-iam-policy -allow lambda:GetFunction -account 111122223333 -out policies schema.json
```

```bash
moonwalk query-file -allow lambda:GetFunction schema.json > discover.tfquery.hcl
```

The AWS CLI route below finds buckets and functions by ARN without any of
the three.

Settings can hold secrets that people put there: a task definition's
environment, a launch template's user data, a build project's variables.
Reading attributes reads those. moonwalk uses a listing's attributes to
place resources and never shows them, but the role can read them; where
that is too much, use `identities-only`.

## Setting up the role

One role in each account, assumed for the length of a listing. No user, no
access keys.

1. Create the policies, from your own or from these documents:

   ```bash
   aws iam create-policy --policy-name moonwalk-discovery-01 --policy-document file://policies/moonwalk-discovery-01.json
   ```

2. Create the role with a trust policy that names exactly who may assume
   it. From a pipeline's role in another account of the same organization:

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Sid": "AssumedByTheListingPipeline",
         "Effect": "Allow",
         "Principal": { "AWS": "arn:aws:iam::444455556666:role/listing-pipeline" },
         "Action": "sts:AssumeRole",
         "Condition": { "StringEquals": { "aws:PrincipalOrgID": "o-exampleorgid" } }
       }
     ]
   }
   ```

   ```bash
   aws iam create-role --role-name moonwalk-discovery --max-session-duration 3600 --assume-role-policy-document file://trust.json
   ```

3. Attach each policy:

   ```bash
   aws iam attach-role-policy --role-name moonwalk-discovery --policy-arn arn:aws:iam::111122223333:policy/moonwalk-discovery-01
   ```

4. Check them with IAM Access Analyzer before use, and again after any
   change:

   ```bash
   aws accessanalyzer validate-policy --policy-type IDENTITY_POLICY --policy-document file://policies/moonwalk-discovery-01.json
   ```

Further, as your organization requires: a permissions boundary on the role;
a service control policy that denies it everything outside these actions;
and, after a few weeks, IAM's last-accessed report to drop the actions for
types you turn out not to have.

## Reading states

A state holds secrets in plain text, so reading states is a policy of its
own, to attach only where states are to be read, and possibly to a
different role than the one that lists.

```bash
moonwalk aws-iam-policy -state s3://acme-terraform-states/live/ \
  -kms-key arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab
```

It allows listing the bucket under the prefix, reading objects whose key
ends in `.tfstate` there and no others, and decrypting with the key through
S3 only. Nothing is written and no lock is taken, so it needs no access to
a DynamoDB lock table.

## Listing with the AWS CLI instead

The two commands moonwalk reads in place of `terraform query` take one
action each. These are from the documentation of those APIs, not recorded.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ListTaggedResources",
      "Effect": "Allow",
      "Action": ["tag:GetResources"],
      "Resource": ["*"]
    }
  ]
}
```

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "SearchOneView",
      "Effect": "Allow",
      "Action": ["resource-explorer-2:Search"],
      "Resource": ["arn:aws:resource-explorer-2:eu-west-1:111122223333:view/all-resources/1234abcd-12ab-34cd-56ef-1234567890ab"]
    }
  ]
}
```
