#!/usr/bin/env python3
"""Finds out which requests `terraform query` makes of Azure for each
resource type the azurerm provider can list, by running it against
recorder.py.

    probe.py --terraform <binary> --moonwalk <binary> --schema schema.json \\
             --plugins <directory with .terraform and .terraform.lock.hcl> \\
             --out calls.json [--type 'azurerm_network_*'] [--workers 6]

Every type is listed twice: once for identities only, and once with
include_resource. The result says, for each type and each way of listing,
what was asked for and whether the listing ran to the end. It needs the
openssl command, for a certificate the provider will trust for the length
of the run. Nothing here talks to Azure.
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
    azurerm = { source = "hashicorp/azurerm" }
  }
}

provider "azurerm" {
  features {}
  metadata_host                   = "localhost:%d"
  subscription_id                 = "11111111-2222-3333-4444-555555555555"
  tenant_id                       = "99999999-0000-0000-0000-000000000000"
  client_id                       = "%s"
  client_secret                   = "probe"
  resource_provider_registrations = "none"
}
"""


def listable(schema):
    """The types a query file from moonwalk lists: those needing no parent."""
    out = []
    lists = schema["provider_schemas"]["registry.terraform.io/hashicorp/azurerm"].get("list_resource_schemas", {})
    for name, s in lists.items():
        block = s.get("block", {})
        required = any(a.get("required") for a in block.get("attributes", {}).values())
        required = required or any(b.get("min_items", 0) > 0 for b in block.get("block_types", {}).values())
        if not required:
            out.append(name)
    return sorted(out)


def run(args, workdir, certificate, label, resource_type, identity_only):
    command = [args.moonwalk, "query-file", "-type", resource_type, "-provider", "azurerm"]
    if identity_only:
        command.append("-identity-only")
    query = subprocess.run(command + [args.schema], capture_output=True, text=True, check=True).stdout
    (workdir / "list.tfquery.hcl").write_text(query)
    (workdir / "main.tf").write_text(MAIN % (args.port, label))
    env = {k: v for k, v in os.environ.items() if k.lower() not in ("https_proxy", "no_proxy", "http_proxy")}
    env.update(SSL_CERT_FILE=str(certificate), NO_PROXY="localhost,127.0.0.1", TF_IN_AUTOMATION="1")
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
    p.add_argument("--out", required=True)
    p.add_argument("--type", default="*")
    p.add_argument("--workers", type=int, default=6)
    p.add_argument("--port", type=int, default=8443)
    p.add_argument("--timeout", type=int, default=120)
    args = p.parse_args()

    schema = json.load(open(args.schema))
    patterns = args.type.split(",")
    types = [t for t in listable(schema) if any(fnmatch.fnmatch(t, p) for p in patterns)]
    top = pathlib.Path(tempfile.mkdtemp(prefix="moonwalk-probe-"))
    certificate, key, log = top / "cert.pem", top / "key.pem", top / "calls.jsonl"
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-subj", "/CN=localhost",
                    "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1", "-keyout", str(key), "-out", str(certificate)],
                   check=True, capture_output=True)
    recorder = subprocess.Popen([sys.executable, str(HERE / "recorder.py"), str(args.port), str(certificate), str(key), str(log)],
                                stdout=subprocess.PIPE, text=True)
    recorder.stdout.readline()  # It says when it is listening.

    free = collections.deque()
    for i in range(args.workers):
        d = top / f"worker-{i}"
        d.mkdir()
        os.symlink(pathlib.Path(args.plugins).resolve() / ".terraform", d / ".terraform")
        shutil.copy(pathlib.Path(args.plugins) / ".terraform.lock.hcl", d / ".terraform.lock.hcl")
        free.append(d)

    # The client ID of each run says which type it lists and which way.
    jobs = []
    for i, t in enumerate(types):
        jobs.append((f"0000{i:04d}-0000-0000-0000-00000000000a", t, True))
        jobs.append((f"0000{i:04d}-0000-0000-0000-00000000000b", t, False))
    outcomes = {}

    def work(job):
        label, t, identity_only = job
        d = free.popleft()
        try:
            return label, run(args, d, certificate, label, t, identity_only)
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
            seen[(r["method"], r["path"])] = seen.get((r["method"], r["path"]), 0) + 1
        entry = dict(outcomes[label])
        entry["calls"] = [{"method": k[0], "path": k[1], "times": n} for k, n in sorted(seen.items())]
        result.setdefault(t, {})["identity" if identity_only else "resource"] = entry
    json.dump(result, open(args.out, "w"), indent=1, sort_keys=True)
    shutil.rmtree(top, ignore_errors=True)
    print(f"{len(types)} types in {time.time() - started:.0f}s; written to {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
