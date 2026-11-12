#!/usr/bin/env python3
"""Finds out which AWS API calls `terraform query` makes for each resource
type the AWS provider can list, by running it against recorder.py.

    probe.py --terraform <binary> --moonwalk <binary> --schema schema.json \\
             --plugins <directory with .terraform and .terraform.lock.hcl> \\
             --out calls.json [--type 'aws_iam_*,aws_vpc'] [--workers 6]

Every type is listed twice: once for identities only, and once with
include_resource, which makes the provider read each resource it finds.
The result says, for each type and each way of listing, what was called
and whether the listing ran to the end. Nothing here talks to AWS.
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
MAIN = """terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}

provider "aws" {
  region            = "us-east-1"
  s3_use_path_style = true
}
"""


def listable(schema):
    """The types a query file from moonwalk lists: those needing no parent."""
    out = []
    lists = schema["provider_schemas"]["registry.terraform.io/hashicorp/aws"].get("list_resource_schemas", {})
    for name, s in lists.items():
        block = s.get("block", {})
        required = any(a.get("required") for a in block.get("attributes", {}).values())
        required = required or any(b.get("min_items", 0) > 0 for b in block.get("block_types", {}).values())
        if not required:
            out.append(name)
    return sorted(out)


def run(args, workdir, port, label, resource_type, identity_only):
    command = [args.moonwalk, "query-file", "-type", resource_type]
    if identity_only:
        command.append("-identity-only")
    query = subprocess.run(command + [args.schema], capture_output=True, text=True, check=True).stdout
    (workdir / "list.tfquery.hcl").write_text(query)
    env = {k: v for k, v in os.environ.items() if k.lower() not in ("https_proxy", "no_proxy", "http_proxy")}
    env.update(
        # Any host name reaches the recorder, which is set as the proxy: some
        # services put the account or a bucket in front of the host.
        AWS_ENDPOINT_URL="http://aws.moonwalk.test",
        HTTP_PROXY=f"http://127.0.0.1:{port}",
        AWS_ACCESS_KEY_ID=label,
        AWS_SECRET_ACCESS_KEY="probe",
        AWS_EC2_METADATA_DISABLED="true",
        AWS_MAX_ATTEMPTS="1",
        TF_IN_AUTOMATION="1",
    )
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
                outcome["errors"].append((d.get("summary", "") + ": " + d.get("detail", "")).strip()[:400])
    except subprocess.TimeoutExpired:
        outcome["errors"].append("timed out")
    return outcome


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--terraform", required=True)
    p.add_argument("--moonwalk", required=True)
    p.add_argument("--schema", required=True)
    p.add_argument("--plugins", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--type", default="*")
    p.add_argument("--workers", type=int, default=6)
    p.add_argument("--port", type=int, default=4711)
    p.add_argument("--timeout", type=int, default=90)
    args = p.parse_args()

    schema = json.load(open(args.schema))
    patterns = args.type.split(",")
    types = [t for t in listable(schema) if any(fnmatch.fnmatch(t, p) for p in patterns)]
    top = pathlib.Path(tempfile.mkdtemp(prefix="moonwalk-probe-"))
    log = top / "calls.jsonl"
    recorder = subprocess.Popen([sys.executable, str(HERE / "recorder.py"), str(args.port), str(log)], stdout=subprocess.PIPE, text=True)
    recorder.stdout.readline()  # It says when it is listening.

    dirs = []
    for i in range(args.workers):
        d = top / f"worker-{i}"
        d.mkdir()
        (d / "main.tf").write_text(MAIN)
        os.symlink(pathlib.Path(args.plugins).resolve() / ".terraform", d / ".terraform")
        shutil.copy(pathlib.Path(args.plugins) / ".terraform.lock.hcl", d / ".terraform.lock.hcl")
        dirs.append(d)
    free = collections.deque(dirs)

    jobs = []
    for i, t in enumerate(types):
        jobs.append((f"MW{i:04d}I", t, True))
        jobs.append((f"MW{i:04d}R", t, False))
    outcomes = {}

    def work(job):
        label, t, identity_only = job
        d = free.popleft()
        try:
            return label, run(args, d, args.port, label, t, identity_only)
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
            key = (r["signing"], r.get("service", ""), r.get("operation") or "?" + str(r.get("unplaced")))
            seen[key] = seen.get(key, 0) + 1
        entry = dict(outcomes[label])
        entry["calls"] = [{"signing": k[0], "service": k[1], "operation": k[2], "times": n} for k, n in sorted(seen.items())]
        result.setdefault(t, {})["identity" if identity_only else "resource"] = entry
    json.dump(result, open(args.out, "w"), indent=1, sort_keys=True)
    shutil.rmtree(top, ignore_errors=True)
    print(f"{len(types)} types in {time.time() - started:.0f}s; written to {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
