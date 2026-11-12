# Security

## Reporting a vulnerability

Please report it privately, not in a public issue: on the repository's
**Security** tab, choose **Report a vulnerability**. That opens a report
only the maintainers can see.

Say what you found, how to reproduce it, and which version or commit you
used (`moonwalk version`). If the report needs a state file to reproduce,
make one up: do not send real state, which holds secrets.

You should hear back within a week. A fix is released before the details
are made public, and you are credited in the release notes unless you would
rather not be.

Only the latest release gets fixes.

## What counts

moonwalk reads Terraform and OpenTofu state, which stores secrets in plain
text, so these are treated as vulnerabilities:

- A secret that state marks as sensitive, or any attribute value from a
  state that does not mark its secrets, appearing anywhere moonwalk writes:
  its output, a snapshot, the explorer, a warning or an error message.
- A web page from another site being able to read what the explorer
  serves, or the explorer answering under another host name while it
  listens on this machine only.
- moonwalk writing to, or changing, anything it was given to read: a state,
  a bucket, a configuration directory.
- A crafted input (state, plan, snapshot, listing, configuration) that
  makes moonwalk run code, read files it was not pointed at, or use memory
  or time out of all proportion to its size.

These are known limits, described in the README, and not vulnerabilities:

- Redaction relies on what the state marks. A secret that a provider does
  not declare sensitive, in a state new enough to carry marks, is shown.
  Treat redacted output as reduced risk, not as safe to publish.
- Resource names, addresses, IDs and ARNs are shown, in output and in
  snapshots. They describe your infrastructure in detail.
- `serve -addr` with an address that is not this machine's own serves
  attribute values to anyone who can reach it. moonwalk warns, and does it.

## What moonwalk touches

It makes no network requests, except to read the states it is given as
`s3://` addresses. For those it gets credentials the way the AWS command
line does, which can mean asking AWS for them, and then it only reads. It
sends nothing anywhere else: no telemetry, no update check. Listing what exists in a cloud is done by Terraform or the cloud's
own command line; moonwalk reads their output.
