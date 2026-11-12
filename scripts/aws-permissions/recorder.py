#!/usr/bin/env python3
"""A stand-in for every AWS API that records what it is asked.

It answers any AWS request with a made-up but well-formed response: one
item in every list, and no further pages. A client that lists resources
and then reads each one, as the Terraform AWS provider does, therefore
makes every call it would make against a real account with one resource
of each kind, and this writes each call down.

    recorder.py <port> <log file>

Each line of the log is one request: who made it (the access key, which
the caller sets to say what it is testing), the service it was signed for,
and the operation. Nothing here talks to AWS.

The responses are built from the API descriptions that ship with botocore.
"""
import base64
import datetime
import http.server
import json
import re
import sys
import threading
import urllib.parse

import botocore.loaders
import botocore.model

try:
    import cbor2
except ImportError:  # Only the few services that speak CBOR need it.
    cbor2 = None

ACCOUNT = "123456789012"
REGION = "us-east-1"
WHEN = datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc)
DOCUMENT = '{"Version":"2012-10-17","Statement":[]}'

loader = botocore.loaders.Loader()
lock = threading.Lock()


class Catalog:
    """Finds the operation a request is for."""

    def __init__(self):
        self.by_signing = {}  # Signing name to the models signed with it.
        self.by_target = {}  # X-Amz-Target prefix to model.
        self.by_shape = {}  # Smithy service shape name to model, for CBOR.
        self.routes = {}  # Model name to its REST routes, built on first use.
        for name in loader.list_available_services("service-2"):
            model = botocore.model.ServiceModel(loader.load_service_model(name, "service-2"), name)
            meta = model.metadata
            signing = meta.get("signingName") or meta.get("endpointPrefix")
            self.by_signing.setdefault(signing, []).append(model)
            if meta.get("targetPrefix"):
                self.by_target[meta["targetPrefix"]] = model
            self.by_shape.setdefault(meta.get("targetPrefix") or meta.get("serviceId", "").replace(" ", ""), model)

    def rest_routes(self, model):
        if model.service_name not in self.routes:
            routes = []
            for op_name in model.operation_names:
                op = model.operation_model(op_name)
                uri, _, query = op.http.get("requestUri", "/").partition("?")
                uri = uri.rstrip("/") or "/"
                pattern = re.sub(r"\{[^}/]+\+\}", "(.+)", re.sub(r"\{[^}/+]+\}", "([^/]+)", re.escape(uri).replace(r"\{", "{").replace(r"\}", "}").replace(r"\+", "+")))
                required = [p.split("=")[0] for p in query.split("&") if p and "{" not in p]
                literal = len(re.sub(r"\{[^}]+\}", "", uri)) + 10 * len(required)
                routes.append((literal, op.http.get("method", "POST"), re.compile("^" + pattern + "$"), required, op))
            routes.sort(key=lambda r: -r[0])
            self.routes[model.service_name] = routes
        return self.routes[model.service_name]

    def find(self, signing, method, path, query, headers, form):
        candidates = self.by_signing.get(signing, [])
        target = headers.get("X-Amz-Target")
        if target and "." in target:
            prefix, _, op = target.rpartition(".")
            model = self.by_target.get(prefix) or next((m for m in candidates if op in m.operation_names), None)
            if model and op in model.operation_names:
                return model, model.operation_model(op), "json"
        cbor = re.match(r"^/service/([^/]+)/operation/([^/]+)$", path)
        if cbor:
            for model in candidates:
                if cbor.group(2) in model.operation_names:
                    return model, model.operation_model(cbor.group(2)), "cbor"
        if form.get("Action"):
            op = form["Action"][0]
            version = form.get("Version", [""])[0]
            ordered = sorted(candidates, key=lambda m: m.api_version != version)
            for model in ordered:
                if op in model.operation_names:
                    return model, model.operation_model(op), model.metadata["protocol"]
        # Several services can share a signing name, as S3 and S3 Control
        # do; the route with the most fixed text in it is the one meant.
        best = None
        for model in candidates:
            if model.metadata["protocol"] not in ("rest-json", "rest-xml"):
                continue
            for literal, verb, pattern, required, op in self.rest_routes(model):
                if verb == method and pattern.match(path) and all(r in query for r in required):
                    if best is None or literal > best[0]:
                        best = (literal, model, op)
                    break  # A model's routes are sorted: its first match is its best.
        if best:
            return best[1], best[2], best[1].metadata["protocol"]
        return None, None, None


catalog = Catalog()

# Members that say there is more to fetch. Leaving them out ends the listing.
MORE = re.compile(r"^(next|continuation|last)?_?(page_?)?(token|marker|key)$|^next|marker$|^lastevaluated|^position$|^nextpagetoken$", re.I)


def tokens(op):
    """The names of an operation's members that point at a further page."""
    try:
        page = loader.load_service_model(op.service_model.service_name, "paginators-1")["pagination"].get(op.name, {})
    except Exception:
        page = {}
    names = set()
    for key in ("output_token", "more_results"):
        value = page.get(key, [])
        for v in value if isinstance(value, list) else [value]:
            names.add(v.split(".")[0].split("[")[0])
    return names


NUMERIC = re.compile(r"max|count|limit|size|number|port|length|capacity|quantity|seconds|minutes|hours|days|percent|priority|weight|ttl|timeout|interval|period|threshold|bytes|total|index|order|rank|iops|throughput|duration")


def text(name, shape, signing):
    """A value for a string member, shaped like what its name promises."""
    if shape.enum:
        return shape.enum[0]
    lower = name.lower()
    # What a name promises, where a client checks that it got it.
    if "kms" in lower or lower.endswith("keyid") or lower.endswith("keyarn") or lower == "encryptionkey":
        return f"arn:aws:kms:{REGION}:{ACCOUNT}:key/1234abcd-12ab-34cd-56ef-1234567890ab"
    if "instanceprofile" in lower:
        return f"arn:aws:iam::{ACCOUNT}:instance-profile/moonwalk-probe"
    if "role" in lower and "name" not in lower:
        return f"arn:aws:iam::{ACCOUNT}:role/moonwalk-probe"
    if lower.endswith("arn") or lower.endswith("arns"):
        kind = re.sub("arns?$", "", lower) or "resource"
        if "topic" in lower:
            return f"arn:aws:sns:{REGION}:{ACCOUNT}:moonwalk-probe"
        if "bucket" in lower:
            return "arn:aws:s3:::moonwalk-probe"
        if "function" in lower or "lambda" in lower:
            return f"arn:aws:lambda:{REGION}:{ACCOUNT}:function:moonwalk-probe"
        if "certificate" in lower:
            return f"arn:aws:acm:{REGION}:{ACCOUNT}:certificate/1234abcd-12ab-34cd-56ef-1234567890ab"
        return f"arn:aws:{signing}:{REGION}:{ACCOUNT}:{kind}/moonwalk-probe"
    if "account" in lower or lower in ("ownerid", "owner"):
        return ACCOUNT
    if "region" in lower:
        return REGION
    if "availabilityzone" in lower or lower == "zone":
        return REGION + "a"
    if re.search(r"document|policy$|policytext|definition|template|schema|json|statement$", lower):
        return DOCUMENT
    if "cidr" in lower:
        return "10.0.0.0/16"
    if lower.endswith("url") or lower.endswith("uri") or "endpoint" in lower:
        return "https://moonwalk-probe.example.com/"
    if lower.endswith("id") or lower.endswith("ids"):
        return "probe-0123456789abcdef0"
    if "version" in lower or NUMERIC.search(lower):
        return "1"  # Some APIs send numbers as strings.
    return "moonwalk-probe"


def fake(shape, name, signing, skip, depth=0, above=()):
    """A value of the given shape with one of everything in it."""
    kind = shape.type_name
    if kind == "structure":
        if depth > 7 or shape.name in above:
            return None
        if getattr(shape, "is_document_type", False):
            return {}
        out = {}
        for member_name, member in shape.members.items():
            if depth == 0 and (member_name in skip or MORE.match(member_name)):
                continue
            if out and getattr(shape, "is_tagged_union", False):
                break  # A union holds one of its members.
            if member_name.lower() in ("istruncated", "truncated", "hasmore", "hasmoreresults"):
                out[member_name] = False
                continue
            value = fake(member, member_name, signing, skip, depth + 1, above + (shape.name,))
            if value is not None:
                out[member_name] = value
        return out
    if kind == "list":
        value = fake(shape.member, name, signing, skip, depth + 1, above)
        return [] if value is None else [value]
    if kind == "map":
        value = fake(shape.value, name, signing, skip, depth + 1, above)
        return {} if value is None else {"Name": value}
    if kind == "string":
        return text(name, shape, signing)
    if kind in ("integer", "long"):
        return 1
    if kind in ("float", "double"):
        return 1.0
    if kind == "boolean":
        return False
    if kind == "timestamp":
        return WHEN
    if kind == "blob":
        return b"moonwalk-probe"
    return None


def as_json(value, shape):
    """value in the form the JSON protocols put on the wire."""
    kind = shape.type_name
    if kind == "structure":
        if getattr(shape, "is_document_type", False):
            return value
        return {
            shape.members[k].serialization.get("name", k): as_json(v, shape.members[k])
            for k, v in value.items()
            if shape.members[k].serialization.get("location") in (None, "body")
        }
    if kind == "list":
        return [as_json(v, shape.member) for v in value]
    if kind == "map":
        return {k: as_json(v, shape.value) for k, v in value.items()}
    if kind == "timestamp":
        if shape.serialization.get("timestampFormat") == "iso8601":
            return value.strftime("%Y-%m-%dT%H:%M:%SZ")
        return value.timestamp()
    if kind == "blob":
        return base64.b64encode(value).decode()
    return value


def as_cbor(value, shape):
    kind = shape.type_name
    if kind == "structure":
        return {k: as_cbor(v, shape.members[k]) for k, v in value.items()}
    if kind == "list":
        return [as_cbor(v, shape.member) for v in value]
    if kind == "map":
        return {k: as_cbor(v, shape.value) for k, v in value.items()}
    return value


def escape(s):
    return str(s).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def as_xml(tag, value, shape):
    """value as an element, by the rules the XML protocols share."""
    kind = shape.type_name
    if kind == "structure":
        inner = "".join(
            as_xml(member_tag(k, shape.members[k]), v, shape.members[k])
            for k, v in value.items()
            if shape.members[k].serialization.get("location") in (None, "body")
        )
        return f"<{tag}>{inner}</{tag}>"
    if kind == "list":
        if shape.serialization.get("flattened"):
            return "".join(as_xml(tag, v, shape.member) for v in value)
        item = shape.member.serialization.get("name", "member")
        return f"<{tag}>" + "".join(as_xml(item, v, shape.member) for v in value) + f"</{tag}>"
    if kind == "map":
        key = shape.key.serialization.get("name", "key")
        val = shape.value.serialization.get("name", "value")
        entries = "".join(f"<{key}>{escape(k)}</{key}>" + as_xml(val, v, shape.value) for k, v in value.items())
        if shape.serialization.get("flattened"):
            return "".join(f"<{tag}>{e}</{tag}>" for e in [entries])
        return f"<{tag}><entry>{entries}</entry></{tag}>"
    if kind == "timestamp":
        return f"<{tag}>{value.strftime('%Y-%m-%dT%H:%M:%SZ')}</{tag}>"
    if kind == "blob":
        return f"<{tag}>{base64.b64encode(value).decode()}</{tag}>"
    if kind == "boolean":
        return f"<{tag}>{'true' if value else 'false'}</{tag}>"
    return f"<{tag}>{escape(value)}</{tag}>"


def member_tag(name, shape):
    if shape.type_name == "list" and shape.serialization.get("flattened"):
        return shape.member.serialization.get("name") or shape.serialization.get("name", name)
    return shape.serialization.get("name", name)


def respond(model, op, protocol, signing):
    """The status, headers and body of a made-up answer to op."""
    shape = op.output_shape
    value = fake(shape, op.name, signing, tokens(op)) if shape is not None else {}
    request_id = "00000000-0000-4000-8000-000000000000"
    if protocol == "json":
        version = model.metadata.get("jsonVersion", "1.1")
        return 200, {"Content-Type": f"application/x-amz-json-{version}"}, json.dumps(as_json(value, shape) if shape else {}).encode()
    if protocol == "cbor":
        if cbor2 is None:
            return 500, {}, b"cbor2 is not installed"
        body = cbor2.dumps(as_cbor(value, shape) if shape else {}, datetime_as_timestamp=True)
        return 200, {"Content-Type": "application/cbor", "Smithy-Protocol": "rpc-v2-cbor"}, body
    namespace = model.metadata.get("xmlNamespace", "")
    xmlns = f' xmlns="{namespace}"' if namespace else ""
    if protocol == "query":
        inner = as_xml("x", value, shape)[3:-4] if shape else ""
        wrapper = shape.serialization.get("resultWrapper") if shape else None
        if wrapper:
            inner = f"<{wrapper}>{inner}</{wrapper}>"
        body = f"<{op.name}Response{xmlns}>{inner}<ResponseMetadata><RequestId>{request_id}</RequestId></ResponseMetadata></{op.name}Response>"
        return 200, {"Content-Type": "text/xml"}, body.encode()
    if protocol == "ec2":
        inner = as_xml("x", value, shape)[3:-4] if shape else ""
        body = f'<?xml version="1.0" encoding="UTF-8"?><{op.name}Response{xmlns}><requestId>{request_id}</requestId>{inner}</{op.name}Response>'
        return 200, {"Content-Type": "text/xml;charset=UTF-8"}, body.encode()
    # The REST protocols: some members travel as headers, one may be the whole body.
    headers = {}
    status = op.http.get("responseCode", 200)
    payload = shape.serialization.get("payload") if shape else None
    if shape:
        for k, v in value.items():
            member = shape.members[k]
            if member.serialization.get("location") == "header" and isinstance(v, (str, int, bool)):
                headers[member.serialization.get("name", k)] = str(v)
    if protocol == "rest-json":
        headers["Content-Type"] = "application/json"
        if payload and payload in value:
            member = shape.members[payload]
            if member.type_name in ("blob", "string"):
                body = value[payload] if isinstance(value[payload], bytes) else str(value[payload]).encode()
            else:
                body = json.dumps(as_json(value[payload], member)).encode()
        else:
            body = json.dumps(as_json(value, shape) if shape else {}).encode()
        return status, headers, body
    headers["Content-Type"] = "application/xml"
    if not shape:
        return status, headers, b""
    if payload and payload in value:
        member = shape.members[payload]
        if member.type_name in ("blob", "string"):
            return status, headers, value[payload] if isinstance(value[payload], bytes) else str(value[payload]).encode()
        tag = member.serialization.get("name", payload)
        body = as_xml(tag, value[payload], member)
    else:
        body = as_xml(shape.serialization.get("name", shape.name), value, shape)
    body = body.replace(">", f"{xmlns}>", 1) if xmlns else body
    return status, headers, ('<?xml version="1.0" encoding="UTF-8"?>' + body).encode()


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    log = None

    def log_message(self, *args):
        pass

    def handle_any(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        parsed = urllib.parse.urlsplit(self.path)
        query = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
        form = {}
        if "x-www-form-urlencoded" in (self.headers.get("Content-Type") or ""):
            form = urllib.parse.parse_qs(raw.decode("utf-8", "replace"), keep_blank_values=True)
        scope = re.search(r"Credential=([^/]+)/\d{8}/([^/]+)/([^/]+)/aws4_request", self.headers.get("Authorization") or "")
        who, signing = (scope.group(1), scope.group(3)) if scope else ("", "")
        try:
            # The path as sent: a name or an ARN in it arrives encoded, and
            # decoding it first would add slashes that are not separators.
            model, op, protocol = catalog.find(signing, self.command, parsed.path.rstrip("/") or "/", query, self.headers, form)
        except Exception as e:  # A request this cannot place is still a finding.
            model, op, protocol = None, None, None
            sys.stderr.write(f"cannot place {self.command} {self.path}: {e}\n")
        record = {"who": who, "signing": signing, "method": self.command, "path": parsed.path}
        if op is None:
            record["unplaced"] = self.headers.get("X-Amz-Target") or (form.get("Action") or [""])[0] or parsed.path
            status, headers, body = 400, {"Content-Type": "application/json"}, b'{"__type":"UnknownOperationException","message":"moonwalk recorder: unknown operation"}'
        else:
            record.update(service=model.service_name, operation=op.name, protocol=protocol)
            try:
                status, headers, body = respond(model, op, protocol, signing)
            except Exception as e:
                record["failed"] = repr(e)
                status, headers, body = 500, {}, b""
        with lock:
            Handler.log.write(json.dumps(record) + "\n")
            Handler.log.flush()
        self.send_response(status)
        for k, v in headers.items():
            self.send_header(k, v)
        self.send_header("x-amzn-RequestId", "00000000-0000-4000-8000-000000000000")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    do_GET = do_POST = do_PUT = do_DELETE = do_HEAD = do_PATCH = handle_any


if __name__ == "__main__":
    port, path = int(sys.argv[1]), sys.argv[2]
    Handler.log = open(path, "a")
    server = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"recording on 127.0.0.1:{port}", flush=True)
    server.serve_forever()
