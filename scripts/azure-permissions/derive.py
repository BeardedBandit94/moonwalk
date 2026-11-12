#!/usr/bin/env python3
"""Turns what probe.py recorded into the Azure permissions each resource
type needs, checked against the permissions Microsoft publishes.

    derive.py --calls calls.json --provider "hashicorp/azurerm 5.13.0" \\
              --cache <directory> --out permissions.json

A request to Azure Resource Manager is authorized by a permission that
follows from it: the resource provider and the types in its path, and
"read" for a GET. Each is looked up in Microsoft's list of permissions
(the pages under learn.microsoft.com/azure/role-based-access-control/
permissions, whose source is downloaded once into the cache), which gives
its spelling. That list trails the services, so a read that is not in it
is kept as the request spells it and named as unlisted.

Anything that is not a plain read is not a permission to give a role for
looking: a POST, which is how keys are asked for; a read that returns
keys all the same; a request to a service's own endpoint for what it
stores. A type that makes one is marked, and listed without its attributes
or not at all.
"""
import argparse
import datetime
import json
import os
import re
import sys
import urllib.request

SOURCE = "https://raw.githubusercontent.com/MicrosoftDocs/azure-docs/main/articles/role-based-access-control/permissions/"
CATEGORIES = ["ai-machine-learning", "analytics", "compute", "containers", "databases", "devops", "general",
              "hybrid-multicloud", "identity", "integration", "internet-of-things", "management-and-governance",
              "migration", "monitor", "networking", "security", "storage", "web-and-mobile"]

# Reads, by Azure's naming, that hand over a key or a secret.
KEY_READS = {
    "microsoft.automation/automationaccounts/agentregistrationinformation/read": "the account's registration keys",
}

# What an error says when the provider went to a service's own endpoint.
DATA_PLANE = re.compile(r"Data Plane|keyvault\.BaseClient|No scheme detected in URL|\.test[/:\"]", re.I)


def load_catalog(cache):
    """Microsoft's permissions, by lower-cased name: (name as written, is a data action)."""
    os.makedirs(cache, exist_ok=True)
    catalog, dated = {}, ""
    for category in CATEGORIES:
        path = os.path.join(cache, category + ".md")
        if not os.path.exists(path):
            urllib.request.urlretrieve(SOURCE + category + ".md", path)
        data = False
        for line in open(path, encoding="utf-8"):
            if line.startswith("ms.date:"):
                dated = max(dated, line.split(":", 1)[1].strip())
            if re.match(r"> \| Action \| Description \|", line):
                data = False
            elif re.match(r"> \| \*\*(Action|DataAction)\*\* \|", line):
                data = "DataAction" in line
            else:
                m = re.match(r"> \| `([^`]+)` \|", line)
                if m:
                    catalog.setdefault(m.group(1).lower(), (m.group(1), data))
    return catalog, dated


def permission(method, path):
    """The permission that authorizes a request, by Azure's naming."""
    verb = {"GET": "read", "HEAD": "read", "PUT": "write", "PATCH": "write", "DELETE": "delete", "POST": "action"}[method]
    parts = [p for p in path.split("/") if p]
    lower = [p.lower() for p in parts]
    if "providers" in lower:
        i = len(lower) - 1 - lower[::-1].index("providers")
        if i + 1 < len(parts):
            namespace, rest = parts[i + 1], parts[i + 2:]
            if verb == "action" and len(rest) % 2 == 1:
                # The last part names what is asked for: .../listKeys.
                return "/".join([namespace] + rest[0:-1:2] + [rest[-1], "action"])
            return "/".join([namespace] + rest[0::2] + [verb])
    if lower[:1] == ["subscriptions"]:
        if len(parts) <= 2:
            return "Microsoft.Resources/subscriptions/" + verb
        return "Microsoft.Resources/subscriptions/" + parts[2] + "/" + verb
    return None


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--calls", required=True, action="append", help="later files replace the types they hold")
    p.add_argument("--provider", required=True)
    p.add_argument("--cache", required=True)
    p.add_argument("--out", required=True)
    args = p.parse_args()

    catalog, dated = load_catalog(args.cache)
    recorded = {}
    for path in args.calls:
        recorded.update(json.load(open(path)))

    types, left_out, unlisted = {}, {}, set()
    for resource_type, modes in sorted(recorded.items()):
        reasons = []
        reads = {"identity": set(), "resource": set()}
        beyond = {"identity": "", "resource": ""}
        for mode in ("identity", "resource"):
            for call in modes[mode]["calls"]:
                name = permission(call["method"], call["path"])
                if name is None:
                    reasons.append(f"a request that names no permission: {call['method']} {call['path']}")
                    continue
                known = catalog.get(name.lower())
                if not name.lower().endswith("/read"):
                    what = "the resource's keys" if re.search(r"keys?/action$", name, re.I) else "more than a read"
                    beyond[mode] = beyond[mode] or f"{known[0] if known else name}, which asks for {what}"
                elif name.lower() in KEY_READS:
                    beyond[mode] = beyond[mode] or f"{known[0] if known else name}, which returns {KEY_READS[name.lower()]}"
                elif known is not None and known[1]:
                    reasons.append(f"{name} is a data action")
                elif known is None:
                    # The published list trails the services: a read that
                    # the provider makes and the list lacks is kept, as
                    # the request spells it, and named as unlisted.
                    reads[mode].add(name)
                    unlisted.add(name)
                else:
                    reads[mode].add(known[0])
            if not beyond[mode] and any(DATA_PLANE.search(e) for e in modes[mode]["errors"]):
                beyond[mode] = "a request to the service's own endpoint, for what it stores"
        if not reads["identity"] and not reasons:
            reasons.append("no listing was recorded")
        if reasons:
            left_out[resource_type] = sorted(set(reasons))
            continue
        entry = {"list": sorted(reads["identity"], key=str.lower)}
        read = sorted(reads["resource"] - reads["identity"], key=str.lower)
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
        "catalog": f"Microsoft's published Azure permissions, dated {dated}, read {datetime.date.today().isoformat()}",
        "types": types,
        "unlisted": sorted(a for a in unlisted if any(a in t["list"] + t.get("read", []) for t in types.values())),
    }
    with open(args.out, "w") as f:
        json.dump(out, f, indent=1)
        f.write("\n")
    for resource_type, reasons in left_out.items():
        print(f"left out {resource_type}: {'; '.join(reasons)}", file=sys.stderr)
    used = {a for t in types.values() for a in t["list"] + t.get("read", [])}
    confirmed = sum(1 for t in types.values() if "read_unconfirmed" not in t and "list_unconfirmed" not in t)
    print(f"{len(types)} types written, {confirmed} of them recorded to the end; {len(left_out)} left out; {len(used)} permissions", file=sys.stderr)


if __name__ == "__main__":
    main()
