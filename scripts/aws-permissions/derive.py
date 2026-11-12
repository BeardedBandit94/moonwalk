#!/usr/bin/env python3
"""Turns what probe.py recorded into the IAM actions each resource type
needs, by AWS's own account of them.

    derive.py --calls calls.json [--calls more.json] --provider "hashicorp/aws 6.73.0" \\
              --cache <directory> --out permissions.json

AWS publishes, for every service, which actions each API operation is
authorized by, what each action is (a write, a listing, permission
management), and which kinds of resource it can be limited to: the Service
Reference, at servicereference.us-east-1.amazonaws.com. Each recorded call
is looked up there. A type is written out only if every call it made has
an answer there and every action is a plain read: not a write, not
permission management, not tagging. The rest are reported and left out;
nothing is guessed.
"""
import argparse
import datetime
import json
import os
import sys
import urllib.request

INDEX = "https://servicereference.us-east-1.amazonaws.com/"

# Calls every run makes that need no permission of their own.
FREE = {("sts", "GetCallerIdentity")}

# Signing names that differ from the IAM prefix, for calls the reference
# does not list under their SDK name.
PREFIX = {"monitoring": "cloudwatch", "tagging": "tag", "email": "ses"}


def load_reference(cache):
    """Every service's reference, downloaded once into cache."""
    os.makedirs(cache, exist_ok=True)
    index_path = os.path.join(cache, "index.json")
    if not os.path.exists(index_path):
        urllib.request.urlretrieve(INDEX, index_path)
    services = {}
    for entry in json.load(open(index_path)):
        path = os.path.join(cache, entry["service"] + ".json")
        if not os.path.exists(path):
            urllib.request.urlretrieve(entry["url"], path)
        services[entry["service"]] = json.load(open(path))
    return services


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--calls", required=True, action="append", help="later files replace the types they hold")
    p.add_argument("--provider", required=True)
    p.add_argument("--cache", required=True)
    p.add_argument("--out", required=True)
    args = p.parse_args()

    reference = load_reference(args.cache)
    # (SDK service, operation) to the actions that authorize it.
    by_operation = {}
    described = {}
    for prefix, service in reference.items():
        formats = {r["Name"]: r.get("ARNFormats", []) for r in service.get("Resources", [])}
        for a in service.get("Actions", []):
            props = a.get("Annotations", {}).get("Properties", {})
            described[f"{prefix}:{a['Name']}"] = {
                "props": props,
                "resources": sorted({f for r in a.get("Resources", []) for f in formats.get(r["Name"], [])}),
            }
        for op in service.get("Operations", []):
            for sdk in op.get("SDK", []):
                by_operation.setdefault((sdk["Name"], op["Name"]), []).extend(
                    f"{a['Service']}:{a['Name']}" for a in op.get("AuthorizedActions", [])
                )

    recorded = {}
    for path in args.calls:
        recorded.update(json.load(open(path)))

    actions = {}
    types = {}
    left_out = {}

    def authorize(call):
        """The actions for one call, or a reason there are none."""
        if (call["signing"], call["operation"]) in FREE:
            return [], None
        if call["operation"].startswith("?"):
            return None, f"a request the recorder could not place ({call['operation'][1:60]})"
        prefix = PREFIX.get(call["signing"], call["signing"])
        found = sorted(set(by_operation.get((call["service"], call["operation"]), [])))
        if not found and f"{prefix}:{call['operation']}" in described:
            found = [f"{prefix}:{call['operation']}"]
        if not found:
            return None, f"the Service Reference does not say what authorizes {call['service']} {call['operation']}"
        # An operation that several services authorize, each for its own
        # kind of resource, is taken for the service it was signed for.
        own = [a for a in found if a.split(":")[0] == prefix]
        found = own or found
        # Where the action named after the operation is among them, it is
        # the one a plain call needs; the others cover special cases, as
        # dynamodb:ReplicateSettings does for DescribeTable.
        named = [a for a in found if a.split(":")[1] == call["operation"]]
        found = named or found
        for a in found:
            info = described.get(a)
            if info is None:
                return None, f"the Service Reference does not describe {a}"
            props = info["props"]
            if props.get("IsWrite") or props.get("IsPermissionManagement") or props.get("IsTaggingOnly"):
                return None, f"{a} is not a plain read: {props}"
            actions[a] = {"level": "List" if props.get("IsList") else "Read", "resources": info["resources"]}
            if props.get("IsDataPlane"):
                actions[a]["data_plane"] = True
        return found, None

    for resource_type, modes in sorted(recorded.items()):
        reasons = []
        sets = {}
        for mode in ("identity", "resource"):
            found = set()
            for call in modes[mode]["calls"]:
                got, reason = authorize(call)
                if got is None:
                    reasons.append(reason)
                else:
                    found.update(got)
            sets[mode] = found
        if not sets["identity"] and not reasons:
            reasons.append("no list call was recorded")
        if reasons:
            left_out[resource_type] = sorted(set(reasons))
            continue
        entry = {"list": sorted(sets["identity"])}
        read = sorted(sets["resource"] - sets["identity"])
        if read:
            entry["read"] = read
        # Whether the stand-in's made-up answers took the provider all the
        # way through. Where they did not, a real account may show calls
        # that are not recorded here.
        if not modes["identity"]["complete"] or modes["identity"]["found"] == 0:
            entry["list_unconfirmed"] = True
        if not modes["resource"]["complete"] or modes["resource"]["found"] == 0:
            entry["read_unconfirmed"] = True
        types[resource_type] = entry

    used = {a for t in types.values() for a in t["list"] + t.get("read", [])}
    out = {
        "provider": args.provider,
        "catalog": "AWS Service Reference, read " + datetime.date.today().isoformat(),
        "types": types,
        "actions": {a: actions[a] for a in sorted(used)},
    }
    with open(args.out, "w") as f:
        json.dump(out, f, indent=1)
        f.write("\n")
    for resource_type, reasons in left_out.items():
        print(f"left out {resource_type}: {'; '.join(reasons)}", file=sys.stderr)
    confirmed = sum(1 for t in types.values() if "read_unconfirmed" not in t and "list_unconfirmed" not in t)
    print(f"{len(types)} types written, {confirmed} of them recorded to the end; {len(left_out)} left out; {len(used)} actions", file=sys.stderr)


if __name__ == "__main__":
    main()
