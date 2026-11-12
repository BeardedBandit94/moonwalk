#!/usr/bin/env python3
"""Turns what probe.py recorded into the Google Cloud permissions each
resource type needs, checked against the permissions Google publishes.

    derive.py --calls calls.json --provider "hashicorp/google 8.11.0" \\
              --cache <directory> --out permissions.json

A request is an API method, which Google names: compute.subnetworks.list,
pubsub.projects.topics.list. The permission that authorizes it is named
after it: the service, the kind of resource and the verb, without the
parts of the name that only say where the resource is. Each is looked up
on Google's page of permissions for the service
(docs.cloud.google.com/iam/docs/roles-permissions/<service>, downloaded
once into the cache). A type is written out only if every permission it
needs is on that page, and is one that Google's list of what a custom role
may hold marks as supported.

Anything that is not a plain read is not a permission to give a role for
looking: a request that is not a GET, or a read of what a service stores.
A type that makes one is marked, and listed without its attributes or not
at all.
"""
import argparse
import datetime
import json
import os
import re
import sys
import urllib.request

PAGES = "https://docs.cloud.google.com/iam/docs/roles-permissions/"
SUPPORT = "https://docs.cloud.google.com/iam/docs/custom-roles-permissions-support"

# API names that are not the service's name in a permission.
SERVICE = {"cloudresourcemanager": "resourcemanager", "sql": "cloudsql", "sqladmin": "cloudsql"}

# Parts of a method's name that say where a resource is, not what it is.
PLACES = {"projects", "locations", "regions", "zones", "global", "apps", "organizations", "folders"}

# Reads, by Google's naming, that hand over what a service stores.
STORED = {
    "secretmanager.versions.access": "a secret's value",
    "storage.objects.get": "an object's contents",
    "storage.objects.list": "the objects in a bucket",
    "compute.instances.getSerialPortOutput": "a machine's console output",
    "compute.instances.getScreenshot": "a machine's screen",
    "compute.instances.getGuestAttributes": "what a machine's guest has written",
}

# What an error says when the provider asked at an address of its own.
ESCAPED = re.compile(r"googleapis\.com|proxyconnect|127\.0\.0\.1:9")


def published(cache, service):
    """The permissions on Google's page for a service."""
    os.makedirs(cache, exist_ok=True)
    path = os.path.join(cache, f"permissions-{service}.html")
    if not os.path.exists(path):
        try:
            urllib.request.urlretrieve(PAGES + service, path)
        except Exception:
            open(path, "w").close()
    text = open(path, encoding="utf-8", errors="replace").read()
    return set(re.findall(r"\b" + re.escape(service) + r"\.[A-Za-z]+\.[A-Za-z]+\b", text))


# Methods whose permission is not named after them.
RENAMED = {
    # Listing datasets returns the ones the caller may get.
    "bigquery.datasets.list": "bigquery.datasets.get",
}


def support_levels(cache):
    """Each permission's support level in custom roles, by Google's list."""
    path = os.path.join(cache, "custom-roles-support.html")
    if not os.path.exists(path):
        urllib.request.urlretrieve(SUPPORT, path)
    text = open(path, encoding="utf-8", errors="replace").read()
    rows = re.findall(r"<code[^>]*>([a-z][A-Za-z0-9]*\.[A-Za-z0-9]+\.[A-Za-z0-9]+)</code>\s*</td>\s*<td[^>]*>\s*(?:<[^>]+>\s*)*([A-Z_]+)", text)
    return dict(rows)


def permissions(method_id):
    """The permissions a method may be authorized by, likeliest first, and
    the service whose page says which exists."""
    parts = method_id.split(".")
    service = SERVICE.get(parts[0], parts[0])
    verb = "list" if parts[-1] == "aggregatedList" else parts[-1]
    kinds = [p for p in parts[1:-1] if p not in PLACES] or parts[-2:-1]
    kind = kinds[-1]
    lower = lambda s: s[:1].lower() + s[1:]
    # Compute names a method for where a resource is ("regionAutoscalers")
    # or what it belongs to ("networkFirewallPolicies") and often has one
    # permission for the kind wherever it is.
    without_network = lower(kind.replace("Network", "").replace("network", "", 1)) if "etwork" in kind and kind not in ("networks", "subnetworks") else kind
    without_region = lambda k: lower(k[len("region"):]) if k.startswith("region") and len(k) > len("region") else k
    names = [kind, without_network, without_region(kind), without_region(without_network)]
    out = []
    for k in names:
        name = f"{service}.{k}.{verb}"
        name = RENAMED.get(name, name)
        if name not in out:
            out.append(name)
    return out, service


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--calls", required=True, action="append", help="later files replace the types they hold")
    p.add_argument("--provider", required=True)
    p.add_argument("--cache", required=True)
    p.add_argument("--out", required=True)
    args = p.parse_args()

    recorded = {}
    for path in args.calls:
        recorded.update(json.load(open(path)))

    pages = {}
    levels = support_levels(args.cache)
    if len(levels) < 1000:
        sys.exit("could not read Google's list of permissions supported in custom roles")
    types, left_out = {}, {}
    for resource_type, modes in sorted(recorded.items()):
        reasons = []
        reads = {"identity": set(), "resource": set()}
        beyond = {"identity": "", "resource": ""}
        for mode in ("identity", "resource"):
            for call in modes[mode]["calls"]:
                if not call.get("id"):
                    reasons.append(f"a request the recorder could not place, {call['method']} {call['service']} {call['path']}")
                    continue
                names, service = permissions(call["id"])
                if call["method"] != "GET":
                    beyond[mode] = beyond[mode] or f"{call['id']}, which is more than a read"
                    continue
                if names[0] in STORED:
                    beyond[mode] = beyond[mode] or f"{names[0]}, which returns {STORED[names[0]]}"
                    continue
                if service not in pages:
                    pages[service] = published(args.cache, service)
                name = next((n for n in names if n in pages[service]), None)
                if name is None:
                    reasons.append(f"a permission that Google's published list does not name, for {call['id']}")
                    continue
                if levels.get(name) != "SUPPORTED":
                    reasons.append(f"{name}, which is {levels.get(name, 'not listed')} for custom roles")
                    continue
                reads[mode].add(name)
        if not modes["identity"]["calls"] and any(ESCAPED.search(e) for e in modes["identity"]["errors"]):
            reasons.append("a request to an address of the provider's own choosing, which could not be recorded")
        elif not reads["identity"] and not reasons:
            reasons.append("requests of which none was recorded")
        if reasons:
            # What listing it takes is not known, so no role can be said
            # to cover it.
            left_out[resource_type] = sorted(set(reasons))
            types[resource_type] = {"list": [], "list_unknown": "; ".join(left_out[resource_type])}
            continue
        entry = {"list": sorted(reads["identity"])}
        read = sorted(reads["resource"] - reads["identity"])
        if read:
            entry["read"] = read
        if beyond["identity"]:
            entry["list_beyond"] = beyond["identity"]
        if beyond["resource"] and not beyond["identity"]:
            entry["beyond"] = beyond["resource"]
        if not modes["identity"]["complete"] or modes["identity"]["found"] == 0:
            entry["list_unconfirmed"] = True
        if not beyond["resource"] and (not modes["resource"]["complete"] or modes["resource"]["found"] == 0):
            entry["read_unconfirmed"] = True
        types[resource_type] = entry

    out = {
        "provider": args.provider,
        "catalog": f"Google's published IAM permissions and their support in custom roles, read {datetime.date.today().isoformat()}",
        "types": types,
    }
    with open(args.out, "w") as f:
        json.dump(out, f, indent=1)
        f.write("\n")
    for resource_type, reasons in left_out.items():
        print(f"left out {resource_type}: {'; '.join(reasons)}", file=sys.stderr)
    used = {a for t in types.values() for a in t["list"] + t.get("read", [])}
    confirmed = sum(1 for t in types.values() if "read_unconfirmed" not in t and "list_unconfirmed" not in t)
    print(f"{len(types) - len(left_out)} types written, {confirmed - len(left_out)} of them recorded to the end; {len(left_out)} left out; {len(used)} permissions", file=sys.stderr)


if __name__ == "__main__":
    main()
