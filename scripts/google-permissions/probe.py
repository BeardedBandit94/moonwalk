#!/usr/bin/env python3
"""Finds out which requests `terraform query` makes of Google Cloud for
each resource type the google provider can list, by running it against
recorder.py.

    probe.py --terraform <binary> --moonwalk <binary> --schema schema.json \\
             --plugins <directory with .terraform and .terraform.lock.hcl> \\
             --cache <directory> --out calls.json [--type 'google_compute_*']

Every type is listed twice: once for identities only, and once with
include_resource. The result says, for each type and each way of listing,
what was asked for and whether the listing ran to the end.

The provider is given every custom endpoint it has, so that each service
it talks to is the recorder. A few requests it makes at an address of its
own all the same; those are stopped by a proxy setting that leads nowhere,
and show up as errors.
"""
import argparse
import collections
import concurrent.futures
import fnmatch
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time

HERE = pathlib.Path(__file__).resolve().parent


def listable(schema):
    """The types a query file from moonwalk lists: those needing no parent."""
    out = []
    lists = schema["provider_schemas"]["registry.terraform.io/hashicorp/google"].get("list_resource_schemas", {})
    for name, s in lists.items():
        block = s.get("block", {})
        required = any(a.get("required") for a in block.get("attributes", {}).values())
        required = required or any(b.get("min_items", 0) > 0 for b in block.get("block_types", {}).values())
        if not required:
            out.append(name)
    return sorted(out)


def configuration(schema, port, label):
    """A provider block with every service pointed at the recorder."""
    attributes = schema["provider_schemas"]["registry.terraform.io/hashicorp/google"]["provider"]["block"]["attributes"]
    lines = ["terraform {", "  required_providers {", '    google = { source = "hashicorp/google" }', "  }", "}", "",
             'provider "google" {', '  project      = "moonwalk-probe"', '  region       = "europe-west1"',
             '  zone         = "europe-west1-b"', f'  access_token = "{label}"']
    for name in sorted(attributes):
        if name.endswith("_custom_endpoint"):
            lines.append(f'  {name} = "http://127.0.0.1:{port}/{name[:-len("_custom_endpoint")]}/"')
    return "\n".join(lines + ["}"]) + "\n"


def run(args, schema, workdir, label, resource_type, identity_only):
    command = [args.moonwalk, "query-file", "-type", resource_type, "-provider", "google"]
    if identity_only:
        command.append("-identity-only")
    query = subprocess.run(command + [args.schema], capture_output=True, text=True, check=True).stdout
    (workdir / "list.tfquery.hcl").write_text(query)
    (workdir / "main.tf").write_text(configuration(schema, args.port, label))
    env = {k: v for k, v in os.environ.items() if k.lower() not in ("https_proxy", "no_proxy", "http_proxy")}
    # Nothing but the recorder can be reached.
    env.update(HTTPS_PROXY="http://127.0.0.1:9", NO_PROXY="127.0.0.1", TF_IN_AUTOMATION="1")
    outcome = {"found": 0, "complete": False, "errors": []}
    try:
        done = subprocess.run([args.terraform, "query", "-json"], cwd=workdir, env=env, capture_output=True, text=True, timeout=args.timeout)
        for line in done.stdout.splitlines():
            try:
                m = json.loads(line)
            except ValueError:
                continue
            if m.get("type") == "list_resource_found":
                outcome["found"] += 1
            elif m.get("type") == "list_complete":
                outcome["complete"] = True
            elif m.get("type") == "diagnostic" and m["diagnostic"].get("severity") == "error":
                d = m["diagnostic"]
                outcome["errors"].append((d.get("summary", "") + ": " + d.get("detail", "")).strip()[:500])
    except subprocess.TimeoutExpired:
        outcome["errors"].append("timed out")
    return outcome


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--terraform", required=True)
    p.add_argument("--moonwalk", required=True)
    p.add_argument("--schema", required=True)
    p.add_argument("--plugins", required=True)
    p.add_argument("--cache", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--type", default="*")
    p.add_argument("--workers", type=int, default=6)
    p.add_argument("--port", type=int, default=8551)
    p.add_argument("--timeout", type=int, default=120)
    args = p.parse_args()

    schema = json.load(open(args.schema))
    patterns = args.type.split(",")
    types = [t for t in listable(schema) if any(fnmatch.fnmatch(t, p) for p in patterns)]
    top = pathlib.Path(tempfile.mkdtemp(prefix="moonwalk-probe-"))
    log = top / "calls.jsonl"
    recorder = subprocess.Popen([sys.executable, str(HERE / "recorder.py"), str(args.port), args.cache, str(log)], stdout=subprocess.PIPE, text=True)
    recorder.stdout.readline()  # It says when it is listening.

    free = collections.deque()
    for i in range(args.workers):
        d = top / f"worker-{i}"
        d.mkdir()
        os.symlink(pathlib.Path(args.plugins).resolve() / ".terraform", d / ".terraform")
        shutil.copy(pathlib.Path(args.plugins) / ".terraform.lock.hcl", d / ".terraform.lock.hcl")
        free.append(d)

    # The access token of each run says which type it lists and which way.
    jobs = []
    for i, t in enumerate(types):
        jobs.append((f"probe-{i:04d}-identity", t, True))
        jobs.append((f"probe-{i:04d}-resource", t, False))
    outcomes = {}

    def work(job):
        label, t, identity_only = job
        d = free.popleft()
        try:
            return label, run(args, schema, d, label, t, identity_only)
        finally:
            free.append(d)

    started = time.time()
    with concurrent.futures.ThreadPoolExecutor(args.workers) as pool:
        for n, (label, outcome) in enumerate(pool.map(work, jobs), 1):
            outcomes[label] = outcome
            if n % 20 == 0:
                print(f"{n}/{len(jobs)} listings, {time.time() - started:.0f}s", file=sys.stderr, flush=True)
    recorder.terminate()

    calls = collections.defaultdict(list)
    for line in open(log):
        r = json.loads(line)
        calls[r["who"]].append(r)
    result = {}
    for label, t, identity_only in jobs:
        seen = {}
        for r in calls.get(label, []):
            key = (r["method"], r["service"], r["path"], r.get("id", ""), r.get("api", ""))
            seen[key] = seen.get(key, 0) + 1
        entry = dict(outcomes[label])
        entry["calls"] = [{"method": k[0], "service": k[1], "path": k[2], "id": k[3], "api": k[4], "times": n} for k, n in sorted(seen.items())]
        result.setdefault(t, {})["identity" if identity_only else "resource"] = entry
    json.dump(result, open(args.out, "w"), indent=1, sort_keys=True)
    shutil.rmtree(top, ignore_errors=True)
    print(f"{len(types)} types in {time.time() - started:.0f}s; written to {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
