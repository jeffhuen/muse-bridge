#!/usr/bin/env python3
"""Muse -> OpenCode bridge (localhost only).

Own OAuth device flow (same endpoints pi-meta-oauth uses):
  python3 bridge.py login   # approve in browser once, stores identity (0600)
Daemon forwards /v1/* to api.meta.ai, minting a Model API key daily.
No secrets are logged.
"""
import json
import os
import sys
import time
import urllib.parse
import urllib.request
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

UPSTREAM = "https://api.meta.ai/v1"
MINT_URL = "https://api.meta.ai/muse-code/key"
AUTH_BASE = "https://auth.meta.com"
CLIENT_ID = "1031625952748946"
DEVICE_AUTH_URL = AUTH_BASE + "/oidc/device/authorization/"
DEVICE_TOKEN_URL = AUTH_BASE + "/oidc/device/token/"
DEVICE_GRANT = "urn:ietf:params:oauth:grant-type:device_code"
PORT = 8915
KEY_TTL = 20 * 3600
CHUNK = 65536


def base_dir():
    d = os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config"), "muse-bridge")
    os.makedirs(d, mode=0o700, exist_ok=True)
    return d


def identity_path():
    return os.path.join(base_dir(), "identity.json")


def muse_auth_path():
    return os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config"), "muse", "auth.json")


def post_form(url, fields, timeout=30):
    data = urllib.parse.urlencode(fields).encode()
    req = urllib.request.Request(url, data=data,
        headers={"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, json.load(resp)
    except urllib.error.HTTPError as exc:
        try:
            body = json.loads(exc.read().decode() or "{}")
        except Exception:
            body = {}
        return exc.code, body


def mint_api_key(identity, timeout=30):
    req = urllib.request.Request(
        MINT_URL, data=b"{}",
        headers={"Accept": "application/json", "Authorization": "Bearer " + identity,
                 "Content-Type": "application/json", "x-api-version": "1.0.0"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = json.load(resp)
            return body.get("api_key"), body
    except urllib.error.HTTPError as exc:
        try:
            body = json.loads(exc.read().decode() or "{}")
        except Exception:
            body = {}
        raise RuntimeError("mint HTTP %s: %s" % (exc.code, json.dumps(body)[:200]))


def do_login():
    status, auth = post_form(DEVICE_AUTH_URL, {"client_id": CLIENT_ID})
    if status != 200 or not auth.get("device_code") or not auth.get("user_code") or not auth.get("verification_uri"):
        raise RuntimeError("device auth start failed HTTP %s: %s" % (status, json.dumps(auth)[:200]))
    uri = auth.get("verification_uri_complete") or auth["verification_uri"]
    interval = auth.get("interval") if isinstance(auth.get("interval"), (int, float)) and auth["interval"] > 0 else 5
    expires = auth.get("expires_in") if isinstance(auth.get("expires_in"), (int, float)) and auth["expires_in"] > 0 else 900
    print("Approve in browser: %s" % uri, flush=True)
    print("User code: %s (expires in %ds)" % (auth["user_code"], expires), flush=True)
    deadline = time.time() + expires
    identity = None
    while time.time() < deadline:
        time.sleep(interval)
        status, tok = post_form(DEVICE_TOKEN_URL, {"grant_type": DEVICE_GRANT,
            "device_code": auth["device_code"], "client_id": CLIENT_ID})
        if status == 200 and tok.get("access_token"):
            identity = tok["access_token"]
            break
        err = tok.get("error")
        if err in ("authorization_pending", None) and status in (200, 400):
            if err is None and status == 200:
                raise RuntimeError("unexpected token response: %s" % json.dumps(tok)[:200])
            continue
        if err == "slow_down":
            interval += 5
            continue
        if err == "access_denied":
            raise RuntimeError("login denied in browser")
        if err == "expired_token":
            raise RuntimeError("device code expired; run login again")
        raise RuntimeError("login failed HTTP %s: %s" % (status, json.dumps(tok)[:200]))
    if not identity:
        raise RuntimeError("login timed out; run login again")
    key, _ = mint_api_key(identity)
    if not key:
        raise RuntimeError("login approved but no API key minted; check billing at https://dev.meta.ai/billing")
    path = identity_path()
    with open(path, "w") as fh:
        json.dump({"identity": identity, "stored_at": int(time.time())}, fh)
    os.chmod(path, 0o600)
    print("identity stored (0600). Mint works.", flush=True)


def load_identity():
    val = os.environ.get("MUSE_BRIDGE_IDENTITY")
    if val and len(val.strip()) > 20:
        return val.strip(), "env:MUSE_BRIDGE_IDENTITY"
    try:
        with open(identity_path()) as fh:
            ident = json.load(fh).get("identity", "")
        if len(ident) > 20:
            return ident, "identity.json"
    except Exception:
        pass
    return None, None


def load_direct_keys():
    keys = []
    for var in ("MUSE_BRIDGE_KEY", "META_API_KEY", "MODEL_API_KEY"):
        val = os.environ.get(var)
        if val and len(val.strip()) > 20:
            keys.append((var, val.strip()))
    try:
        with open(muse_auth_path()) as fh:
            data = json.load(fh)
        found = []

        def walk(node):
            if isinstance(node, str):
                if node.startswith("LLM_") or node.startswith("LLM|"):
                    found.append(node)
            elif isinstance(node, dict):
                for v in node.values():
                    walk(v)
            elif isinstance(node, list):
                for v in node:
                    walk(v)

        walk(data.get("providers", data) if isinstance(data, dict) else data)
        for item in dict.fromkeys(found):
            keys.append(("muse-auth.json", item))
    except Exception as exc:
        print("muse auth not readable: %s" % exc, flush=True)
    return keys


_state = {"key": None, "at": 0.0}


def current_key():
    if _state["key"] and time.time() - _state["at"] < KEY_TTL:
        return _state["key"]
    ident, src = load_identity()
    if ident:
        try:
            key = mint_api_key(ident)[0] if isinstance(mint_api_key(ident), tuple) else None
        except Exception as exc:
            print("mint via %s failed: %s" % (src, exc), flush=True)
        else:
            if key:
                _state.update(key=key, at=time.time())
                print("minted key via %s" % src, flush=True)
                return key
            print("mint via %s returned no key" % src, flush=True)
    for var, val in load_direct_keys():
        _state.update(key=val, at=time.time())
        print("using direct key from %s" % var, flush=True)
        return val
    raise RuntimeError("no usable credential; run `python3 bridge.py login` once")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "muse-bridge/1.0"

    def log_message(self, *args):
        pass

    def _proxy(self):
        try:
            length = int(self.headers.get("Content-Length", 0))
        except ValueError:
            length = 0
        body = self.rfile.read(length) if length > 0 else None
        if self.path.startswith("/v1/responses") and body:
            try:
                payload = json.loads(body)
                payload.setdefault("prompt_cache_retention", "24h")
                reasoning = payload.get("reasoning")
                if isinstance(reasoning, dict) and reasoning.get("effort") in (None, "none"):
                    payload.pop("reasoning", None)
                body = json.dumps(payload).encode()
            except Exception:
                pass
        try:
            key = current_key()
        except Exception as exc:
            out = json.dumps({"error": str(exc)}).encode()
            self.send_response(503)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(out)))
            self.end_headers()
            self.wfile.write(out)
            return
        if self.path.startswith("/v1"):
            url = UPSTREAM + self.path[3:]
        else:
            url = UPSTREAM + self.path
        headers = {"Accept": "application/json", "Authorization": "Bearer " + key}
        if body:
            headers["Content-Type"] = self.headers.get("Content-Type", "application/json")
        req = urllib.request.Request(url, data=body, headers=headers, method=self.command)
        try:
            upstream = urllib.request.urlopen(req, timeout=300)
        except urllib.error.HTTPError as exc:
            data = exc.read()
            if exc.code == 401:
                _state["key"] = None
            self.send_response(exc.code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        self.send_response(upstream.status)
        self.send_header("Content-Type", upstream.headers.get("Content-Type", "application/json"))
        self.close_connection = True
        self.end_headers()
        try:
            while True:
                chunk = upstream.read(CHUNK)
                if not chunk:
                    break
                self.wfile.write(chunk)
                self.wfile.flush()
        finally:
            upstream.close()

    do_GET = _proxy
    do_POST = _proxy
    do_PUT = _proxy
    do_PATCH = _proxy
    do_DELETE = _proxy


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "login":
        do_login()
    else:
        server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
        print("muse-bridge listening on 127.0.0.1:%d" % PORT, flush=True)
        server.serve_forever()
