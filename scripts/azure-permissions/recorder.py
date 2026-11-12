#!/usr/bin/env python3
"""A stand-in for Azure that records what it is asked.

It plays the three parts the Terraform azurerm provider talks to: the
metadata service that says where everything is, the sign-in service that
hands out a token, and Azure Resource Manager. Every listing is answered
with one made-up resource in the shape all Azure resources share, and
every read of a resource with the same. A client that lists resources and
then reads each one therefore makes the requests it would make against a
real subscription with one resource of each kind, and this writes each
request down.

    recorder.py <port> <certificate> <key> <log file>

Each line of the log is one request to Resource Manager: who made it (the
client ID it signed in with, which the caller sets to say what it is
testing), the method and the path. Nothing here talks to Azure.
"""
import base64
import http.server
import json
import ssl
import sys
import threading
import urllib.parse

SUBSCRIPTION = "11111111-2222-3333-4444-555555555555"
TENANT = "99999999-0000-0000-0000-000000000000"
GROUP = "moonwalk-probe"
lock = threading.Lock()


def b64(value):
    return base64.urlsafe_b64encode(json.dumps(value).encode()).rstrip(b"=").decode()


def token(base, client):
    """An access token that says who asked for it. Nothing checks its signature."""
    claims = {
        "aud": base + "/", "iss": f"{base}/{TENANT}/v2.0", "tid": TENANT, "appid": client,
        "oid": "aaaaaaaa-0000-0000-0000-000000000001", "sub": "aaaaaaaa-0000-0000-0000-000000000001",
        "exp": 4102444800, "iat": 1700000000, "nbf": 1700000000, "idtyp": "app",
    }
    return b64({"alg": "none", "typ": "JWT"}) + "." + b64(claims) + ".c2ln"


def who(header):
    try:
        part = header.split(" ", 1)[1].split(".")[1]
        return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))["appid"]
    except Exception:
        return ""


def metadata(base):
    """Where a cloud's services are, as its metadata service says it."""
    names = ["galleryEndpoint", "graph", "portal", "graphAudience", "activeDirectoryDataLake", "batch", "media",
             "sqlManagement", "microsoftGraphResourceId", "appInsightsResourceId", "logAnalyticsResourceId",
             "ossrDbmsResourceId", "synapseAnalyticsResourceId", "attestationResourceId"]
    out = {name: f"{base}/{name}/" for name in names}
    out.update({
        "name": "AzureCloud",
        "resourceManager": base + "/",
        "authentication": {"loginEndpoint": base, "audiences": [base + "/", base], "tenant": "common", "identityProvider": "AAD"},
        # Services of their own that a client reaches by name. These names
        # lead nowhere, so a request for what a service stores fails, and
        # says so.
        "suffixes": {
            "acrLoginServer": "azurecr.test", "sqlServerHostname": "database.test", "keyVaultDns": "vault.test",
            "storage": "core.test", "azureDataLakeStoreFileSystem": "azuredatalakestore.test",
            "azureDataLakeAnalyticsCatalogAndJob": "azuredatalakeanalytics.test", "mhsmDns": "managedhsm.test",
            "mysqlServerEndpoint": "mysql.database.test", "postgresqlServerEndpoint": "postgres.database.test",
            "mariadbServerEndpoint": "mariadb.database.test", "synapseAnalytics": "dev.azuresynapse.test",
            "attestationEndpoint": "attest.test", "storageSyncEndpointSuffix": "afs.test",
        },
    })
    return out


def resource(resource_id, resource_type):
    """A resource in the shape every Azure resource has."""
    return {
        "id": resource_id, "name": resource_id.rsplit("/", 1)[-1], "type": resource_type,
        "location": "westeurope", "tags": {}, "kind": "probe",
        "sku": {"name": "Standard_LRS", "tier": "Standard"},
        "properties": {"provisioningState": "Succeeded"},
    }


def answer(method, path):
    """What Resource Manager says to a request."""
    if method != "GET":
        return {}
    parts = [p for p in path.split("/") if p]
    lower = [p.lower() for p in parts]
    if "providers" in lower and len(parts) > len(lower) - lower[::-1].index("providers"):
        i = len(lower) - 1 - lower[::-1].index("providers")
        namespace, rest = parts[i + 1], parts[i + 2:]
        kind = namespace + "/" + "/".join(rest[0::2])
        if len(rest) % 2 == 0:
            return resource(path, kind)  # Ends in a name: one resource.
        # Ends in a type: a listing. Across a subscription, what is found
        # sits in a resource group.
        scope = "/" + "/".join(parts[:i]) if i else ""
        if len(rest) == 1 and lower[:1] == ["subscriptions"] and "resourcegroups" not in lower[:i]:
            scope = f"/subscriptions/{parts[1]}/resourceGroups/{GROUP}"
        return {"value": [resource(f"{scope}/providers/{namespace}/{'/'.join(rest)}/probe", kind)]}
    if lower[:1] == ["subscriptions"]:
        subscription = {"id": f"/subscriptions/{SUBSCRIPTION}", "subscriptionId": SUBSCRIPTION, "tenantId": TENANT, "displayName": "probe", "state": "Enabled"}
        if len(parts) == 1:
            return {"value": [subscription]}
        if len(parts) == 2:
            return subscription
        if lower[2] == "resourcegroups":
            group = {"id": f"/subscriptions/{parts[1]}/resourceGroups/{GROUP}", "name": GROUP, "type": "Microsoft.Resources/resourceGroups",
                     "location": "westeurope", "tags": {}, "properties": {"provisioningState": "Succeeded"}}
            if len(parts) == 3:
                return {"value": [group]}
            group.update(id=path, name=parts[3])
            return group
    return {"value": []}


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    log = None
    base = ""

    def log_message(self, *args):
        pass

    def handle_any(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        parsed = urllib.parse.urlsplit(self.path)
        path = parsed.path
        if path.startswith("/metadata/endpoints"):
            out = metadata(self.base)
        elif "/oauth2/" in path and self.command == "POST":
            client = urllib.parse.parse_qs(body.decode()).get("client_id", [""])[0]
            out = {"token_type": "Bearer", "expires_in": 3600, "ext_expires_in": 3600, "access_token": token(self.base, client)}
        else:
            out = answer(self.command, path)
            with lock:
                Handler.log.write(json.dumps({"who": who(self.headers.get("Authorization") or ""), "method": self.command, "path": path}) + "\n")
                Handler.log.flush()
        data = json.dumps(out).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(data)

    do_GET = do_POST = do_PUT = do_DELETE = do_HEAD = do_PATCH = handle_any


if __name__ == "__main__":
    port, certificate, key, path = int(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
    Handler.log = open(path, "a")
    Handler.base = f"https://localhost:{port}"
    server = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(certificate, key)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    print(f"recording on {Handler.base}", flush=True)
    server.serve_forever()
