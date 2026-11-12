#!/usr/bin/env python3
"""A stand-in for Google Cloud's APIs that records what it is asked.

The Terraform google provider is pointed at it with its custom endpoints,
one for each service, so that a request for a service arrives here under
that service's name. Every listing is answered with one made-up resource,
built from the description of the API that Google publishes (its discovery
document), and every read of a resource with the same. A client that lists
resources and then reads each one therefore makes the requests it would
make of a real project with one resource of each kind, and this writes
each request down.

    recorder.py <port> <cache directory> <log file>

Each line of the log is one request: who made it (the access token, which
the caller sets to say what it is testing), the service, the method and
the path, and the API method it is, by Google's name for it. Nothing here
talks to Google Cloud; it downloads the discovery documents once, into the
cache.
"""
import http.server
import json
import os
import re
import sys
import threading
import urllib.parse
import urllib.request

PROJECT = "moonwalk-probe"
REGION = "europe-west1"
ZONE = "europe-west1-b"
COMPUTE = "https://www.googleapis.com/compute/v1/"
DIRECTORY = "https://www.googleapis.com/discovery/v1/apis"

# The provider's name for a service, in its custom endpoints, to the API
# and version behind it.
APIS = {
    "apigee": ("apigee", "v1"),
    "app_engine": ("appengine", "v1"),
    "big_query": ("bigquery", "v2"),
    "cloud_scheduler": ("cloudscheduler", "v1"),
    "compute": ("compute", "v1"),
    "dns": ("dns", "v1"),
    "iam": ("iam", "v1"),
    "iam_beta": ("iam", "v1"),
    "identity_platform": ("identitytoolkit", "v2"),
    "monitoring": ("monitoring", "v3"),
    "pubsub": ("pubsub", "v1"),
    "resource_manager": ("cloudresourcemanager", "v1"),
    "resource_manager_v3": ("cloudresourcemanager", "v3"),
    "secret_manager": ("secretmanager", "v1"),
    "service_usage": ("serviceusage", "v1"),
    "storage": ("storage", "v1"),
    "vertex_ai": ("aiplatform", "v1"),
}

# The start of a path that says which version of an API it is, which some
# descriptions carry and the provider's requests do not: "v1/", "dns/v1/".
VERSION = re.compile(r"^(?:[a-z]+/)?v\d+\w*/")

lock = threading.Lock()


class Catalog:
    """Google's descriptions of its APIs, and the method a request is for."""

    def __init__(self, cache):
        self.cache = cache
        os.makedirs(cache, exist_ok=True)
        self.docs = {}
        self.routes = {}

    def fetch(self, name, url):
        path = os.path.join(self.cache, name)
        if not os.path.exists(path):
            urllib.request.urlretrieve(url, path)
        return json.load(open(path))

    def doc(self, api, version):
        if (api, version) not in self.docs:
            directory = self.fetch("directory.json", DIRECTORY)
            url = next((i["discoveryRestUrl"] for i in directory["items"] if i["name"] == api and i["version"] == version), None)
            if url is None:
                url = f"https://{api}.googleapis.com/$discovery/rest?version={version}"
            self.docs[(api, version)] = self.fetch(f"{api}-{version}.json", url)
        return self.docs[(api, version)]

    def methods(self, doc):
        def walk(node):
            for m in node.get("methods", {}).values():
                yield m
            for r in node.get("resources", {}).values():
                yield from walk(r)
        return walk(doc)

    def find(self, key, method, rest):
        """The API method for a request to a service, with its description."""
        if key not in APIS:
            return None, None
        api, version = APIS[key]
        doc = self.doc(api, version)
        if (api, version) not in self.routes:
            routes = []
            for m in self.methods(doc):
                template = m.get("flatPath") or m["path"]
                template = VERSION.sub("", template)
                pattern = re.sub(r"\\\{\\\+[^}]+\\\}", "(.+)", re.sub(r"\\\{[^}+]+\\\}", "([^/]+)", re.escape(template)))
                literal = len(re.sub(r"\{[^}]+\}", "", template))
                routes.append((literal, m["httpMethod"], re.compile("^" + pattern + "$"), m))
            routes.sort(key=lambda r: -r[0])
            self.routes[(api, version)] = routes
        rest = VERSION.sub("", rest.strip("/"))
        for _, verb, pattern, m in self.routes[(api, version)]:
            if verb == method and pattern.match(rest):
                return m, doc
        return None, doc


def text(name, prop, own):
    """A value for a string, shaped like what its name promises. own is the
    name of the resource the string belongs to."""
    if prop.get("enum"):
        return prop["enum"][0]
    fmt = prop.get("format", "")
    lower = name.lower()
    if fmt in ("int64", "uint64", "int32", "uint32"):
        return "1234567890123456789" if lower == "id" else "1"
    if fmt in ("date-time", "google-datetime") or lower.endswith("timestamp") or lower.endswith("time"):
        return "2026-01-01T00:00:00.000Z"
    if fmt == "byte":
        return "cHJvYmU="
    if fmt == "google-duration":
        return "60s"
    if fmt == "google-fieldmask":
        return ""
    compute = own.startswith("projects/") and ("/global/" in own or "/regions/" in own or "/zones/" in own) and "/locations/" not in own
    if lower == "selflink":
        return COMPUTE + own
    if lower == "name":
        return "probe" if compute or not own.startswith("projects/") else own
    if lower == "id":
        return "probe"
    if lower == "region":
        return COMPUTE + f"projects/{PROJECT}/regions/{REGION}"
    if lower == "zone":
        return COMPUTE + f"projects/{PROJECT}/zones/{ZONE}"
    if lower == "network":
        return COMPUTE + f"projects/{PROJECT}/global/networks/probe"
    if lower == "subnetwork":
        return COMPUTE + f"projects/{PROJECT}/regions/{REGION}/subnetworks/probe"
    if lower in ("project", "projectid"):
        return PROJECT
    if lower == "projectnumber":
        return "123456789012"
    if lower in ("location", "locationid"):
        return REGION
    if "email" in lower:
        return f"probe@{PROJECT}.iam.gserviceaccount.com"
    if lower == "uniqueid":
        return "112233445566778899000"
    if "iprange" in lower or "cidr" in lower:
        return "10.0.0.0/24"
    if lower.endswith("address"):
        return "10.0.0.1"
    # Compute refers to other resources by their URLs, and the provider
    # checks that it got one.
    if compute and re.search(r"\bURLs?\b|fully-qualified|self-?link", prop.get("description", ""), re.I):
        return COMPUTE + f"projects/{PROJECT}/zones/{ZONE}/{name}s/probe"
    return "probe"


def fake(doc, prop, name, own, depth=0, above=()):
    """A value of the given kind with one of everything in it."""
    if "$ref" in prop:
        ref = prop["$ref"]
        if ref in above or depth > 6:
            return None
        return fake(doc, doc["schemas"][ref], name, own, depth, above + (ref,))
    kind = prop.get("type")
    if kind == "object":
        out = {}
        for key, sub in prop.get("properties", {}).items():
            # Whatever says there is another page is left out, which ends the listing.
            if depth == 0 and re.search(r"pagetoken|nextpage", key, re.I):
                continue
            value = fake(doc, sub, key, own, depth + 1, above)
            if value is not None:
                out[key] = value
        if not prop.get("properties") and isinstance(prop.get("additionalProperties"), dict):
            value = fake(doc, prop["additionalProperties"], name, own, depth + 1, above)
            if value is not None:
                out["probe"] = value
        return out
    if kind == "array":
        value = fake(doc, prop.get("items", {}), name, own, depth + 1, above)
        return [] if value is None else [value]
    if kind == "string":
        return text(name, prop, own)
    if kind == "integer":
        return 1
    if kind == "number":
        return 1.0
    if kind == "boolean":
        return False
    return None


def answer(catalog, key, method, rest):
    """The API method a request is, by name, and a made-up answer to it."""
    found, doc = catalog.find(key, method, rest)
    if found is None:
        return None, {}
    response = found.get("response")
    if method != "GET" or not response:
        return found["id"], {}
    rest = VERSION.sub("", rest.strip("/"))
    # A listing is a method whose answer holds a list of resources.
    schema = doc["schemas"].get(response["$ref"], {})
    listing = any(p.get("type") == "array" for p in schema.get("properties", {}).values()) and re.search(r"list|aggregated|search", found["id"].rsplit(".", 1)[-1], re.I) is not None
    # What a listing finds is called "probe"; one resource, read by name, is
    # the one that was asked for.
    value = fake(doc, response, "", rest + "/probe" if listing else rest)
    return found["id"], value or {}


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    log = None
    catalog = None

    def log_message(self, *args):
        pass

    def handle_any(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length:
            self.rfile.read(length)
        parsed = urllib.parse.urlsplit(self.path)
        key, _, rest = parsed.path.strip("/").partition("/")
        if re.match(r"^v\d", key):  # One service comes without its name.
            key, rest = "service_usage", parsed.path.strip("/")
        record = {"who": (self.headers.get("Authorization") or "").removeprefix("Bearer "), "service": key, "method": self.command, "path": rest}
        try:
            method_id, out = answer(self.catalog, key, self.command, rest)
        except Exception as e:  # A request this cannot answer is still a finding.
            method_id, out = None, {}
            record["failed"] = repr(e)
        if method_id:
            record["id"] = method_id
            record["api"] = "/".join(APIS[key])
        with lock:
            Handler.log.write(json.dumps(record) + "\n")
            Handler.log.flush()
        data = json.dumps(out).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json; charset=UTF-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(data)

    do_GET = do_POST = do_PUT = do_DELETE = do_HEAD = do_PATCH = handle_any


if __name__ == "__main__":
    port, cache, path = int(sys.argv[1]), sys.argv[2], sys.argv[3]
    Handler.log = open(path, "a")
    Handler.catalog = Catalog(cache)
    for api, version in sorted(set(APIS.values())):
        Handler.catalog.doc(api, version)  # Downloaded before anything is asked.
    server = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"recording on http://127.0.0.1:{port}", flush=True)
    server.serve_forever()
