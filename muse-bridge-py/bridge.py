#!/usr/bin/env python3
"""Muse -> OpenCode bridge (localhost only).

Own OAuth device flow (same endpoints pi-meta-oauth uses):
  python3 bridge.py login   # approve in browser once, stores identity (0600)
Daemon forwards /v1/* to api.meta.ai, minting a Model API key daily.
Upstream uses a pooled keep-alive connection set shared across handler
threads, so steady-state requests skip the TLS handshake.
No secrets are logged.
"""
import http.client
import json
import logging
import os
import sys
import threading
import time
import urllib.parse
import urllib.request
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from logging.handlers import RotatingFileHandler

UPSTREAM = "https://api.meta.ai/v1"
UPSTREAM_HOST = "api.meta.ai"
MINT_URL = "https://api.meta.ai/muse-code/key"
AUTH_BASE = "https://auth.meta.com"
CLIENT_ID = "1031625952748946"
DEVICE_AUTH_URL = AUTH_BASE + "/oidc/device/authorization/"
DEVICE_TOKEN_URL = AUTH_BASE + "/oidc/device/token/"
DEVICE_GRANT = "urn:ietf:params:oauth:grant-type:device_code"
PORT = 8915
KEY_TTL = 20 * 3600
CHUNK = 65536
MAX_BODY = 25 * 1024 * 1024
MAX_IDLE_CONNS = 16
IDLE_CONN_TTL = 60
MAX_UPSTREAM_FLIGHTS = 64
SOCKET_TIMEOUT = 300
LOG_MAX_BYTES = 64 * 1024
LOG_BACKUPS = 2

# --- shared upstream connection pool (keep-alive across threads) ---

_idle_conns = []
_idle_lock = threading.Lock()


def _pool_take():
    now = time.monotonic()
    with _idle_lock:
        while _idle_conns:
            conn, stamped = _idle_conns.pop()
            if now - stamped < IDLE_CONN_TTL:
                return conn
            try:
                conn.close()
            except Exception:
                pass
    return http.client.HTTPSConnection(UPSTREAM_HOST, timeout=SOCKET_TIMEOUT)


def _pool_give(conn):
    with _idle_lock:
        if len(_idle_conns) < MAX_IDLE_CONNS:
            _idle_conns.append((conn, time.monotonic()))
            return
    try:
        conn.close()
    except Exception:
        pass


def _pool_drop(conn):
    try:
        conn.close()
    except Exception:
        pass


# --- key state (mint lock stops stampedes on expiry) ---

_state = {"key": None, "at": 0.0}
_key_lock = threading.Lock()
_upstream_sem = threading.BoundedSemaphore(MAX_UPSTREAM_FLIGHTS)

log = logging.getLogger("bridge")


def _setup_logging():
    """File logging with rotation. Daemon mode only: the login flow keeps
    printing to the terminal. Launchers must not redirect stdout to this
    file or the two writers will corrupt it."""
    handler = RotatingFileHandler(
        os.path.join(base_dir(), "bridge.log"),
        maxBytes=LOG_MAX_BYTES, backupCount=LOG_BACKUPS)
    handler.setFormatter(logging.Formatter(
        "%(asctime)s.%(msecs)03d %(message)s", "%Y/%m/%d %H:%M:%S"))
    log.addHandler(handler)
    log.setLevel(logging.INFO)


def base_dir():
    d = os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config"), "muse-bridge")
    os.makedirs(d, mode=0o700, exist_ok=True)
    return d


def _debug_on():
    try:
        return os.path.exists(os.path.join(base_dir(), "debug"))
    except Exception:
        return False


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
    except FileNotFoundError:
        pass  # normal: no Muse app installed
    except Exception as exc:
        log.warning("muse auth not readable: %s", exc)
    return keys


def _resolve_key():
    ident, src = load_identity()
    if ident:
        try:
            key, _ = mint_api_key(ident)
        except Exception as exc:
            log.warning("mint via %s failed: %s", src, exc)
        else:
            if key:
                log.info("minted key via %s", src)
                return key
            log.warning("mint via %s returned no key", src)
    for var, val in load_direct_keys():
        log.info("using direct key from %s", var)
        return val
    raise RuntimeError("no usable credential; run `python3 bridge.py login` once")


def current_key():
    fresh = _state["key"]
    if fresh and time.time() - _state["at"] < KEY_TTL:
        return fresh
    with _key_lock:
        fresh = _state["key"]
        if fresh and time.time() - _state["at"] < KEY_TTL:
            return fresh
        log.info("resolving key")
        key = _resolve_key()
        _state.update(key=key, at=time.time())
        return key


def invalidate_key(expected):
    """Drop the cached key, but only if it is still the one that failed.

    Concurrent 401s for an already-rotated key must not trigger
    cascading re-mints."""
    with _key_lock:
        if _state["key"] == expected:
            _state["key"] = None


FWD_HEADERS = ("Retry-After", "X-Request-Id", "X-Ratelimit-Remaining")


def _exchange(method, path, headers, body):
    """One upstream round trip. Returns (kind, status, payload, fwd, conn).

    kind is 'error' with payload=bytes and fwd=dict of forwarded
    upstream metadata headers, or 'stream' with payload=response
    object, fwd=None, and conn checked out until closed.
    Retries once on a dead pooled connection (safe: buffered body)."""
    for attempt in range(2):
        conn = _pool_take()
        try:
            conn.request(method, path, body=body, headers=headers)
            resp = conn.getresponse()
        except Exception:
            _pool_drop(conn)
            if attempt == 1:
                raise
            continue
        if resp.status >= 400:
            data = resp.read(1024 * 1024 + 1)
            fwd = {}
            for name in FWD_HEADERS:
                val = resp.getheader(name)
                if val:
                    fwd[name] = val
            _pool_give(conn)
            return ("error", resp.status, data, fwd, None)
        return ("stream", resp.status, resp, None, conn)
    raise RuntimeError("unreachable")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "muse-bridge/1.1"

    def log_message(self, *args):
        pass

    def _read_body(self):
        try:
            length = int(self.headers.get("Content-Length", 0))
        except ValueError:
            length = 0
        if length > MAX_BODY:
            return None, True
        if length > 0:
            return self.rfile.read(length), False
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            chunks = []
            total = 0
            while True:
                line = self.rfile.readline(128).split(b";")[0].strip()
                try:
                    size = int(line, 16)
                except ValueError:
                    break
                if size <= 0:
                    self.rfile.readline(128)
                    break
                total += size
                if total > MAX_BODY:
                    return None, True
                chunks.append(self.rfile.read(size))
                self.rfile.readline(128)
            return b"".join(chunks) or None, False
        return None, False

    def _send_json(self, code, obj):
        out = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def _proxy(self):
        body, too_big = self._read_body()
        if too_big:
            self.close_connection = True
            self._send_json(413, {"error": "request body exceeds bridge limit"})
            return
        # Normalize before the rewrite check so bare paths (e.g. /responses
        # without the /v1 prefix) get the same munging as prefixed ones.
        if self.path.startswith("/v1"):
            upstream_path = self.path
        else:
            upstream_path = "/v1" + self.path
        if upstream_path.startswith("/v1/responses") and body:
            try:
                payload = json.loads(body)
                payload.setdefault("prompt_cache_retention", "24h")
                if _debug_on():
                    log.info("req model=%s reasoning=%s", payload.get("model"), payload.get("reasoning"))
                reasoning = payload.get("reasoning")
                if isinstance(reasoning, dict) and reasoning.get("effort") in (None, "none"):
                    payload.pop("reasoning", None)
                tools = payload.get("tools")
                if isinstance(tools, list):
                    for t in tools:
                        if isinstance(t, dict) and t.get("type") == "function" and not t.get("parameters"):
                            t["parameters"] = {"type": "object"}
                body = json.dumps(payload).encode()
            except Exception:
                pass
        try:
            key = current_key()
        except Exception as exc:
            self._send_json(503, {"error": str(exc)})
            return
        headers = {"Accept": "application/json", "Authorization": "Bearer " + key}
        if body:
            headers["Content-Type"] = self.headers.get("Content-Type", "application/json")
        if not _upstream_sem.acquire(timeout=60):
            self._send_json(503, {"error": "bridge busy; retry"})
            return
        try:
            result = self._forward(upstream_path, headers, body, key)
        finally:
            _upstream_sem.release()
        if result is not None:
            self._send_json(result[0], result[1])

    def _forward(self, upstream_path, headers, body, key):
        """Returns (code, obj) for JSON errors, else streams and returns None."""
        for attempt in range(2):
            try:
                kind, status, payload, fwd, conn = _exchange(self.command, upstream_path, headers, body)
            except Exception as exc:
                return 502, {"error": "upstream unreachable: %s" % exc}
            if kind == "error":
                log.warning("upstream %s %s -> %s", self.command, upstream_path, status)
                if status == 401:
                    invalidate_key(key)
                    if attempt == 0:
                        try:
                            key = current_key()
                        except Exception as exc:
                            return 503, {"error": str(exc)}
                        headers["Authorization"] = "Bearer " + key
                        continue
                try:
                    obj = json.loads(payload.decode() or "{}")
                except Exception:
                    obj = {"error": "upstream HTTP %s" % status}
                    snippet = payload.decode("utf-8", "replace").strip()[:500]
                    if snippet:
                        obj["body"] = snippet
                self.send_response(status)
                for name, val in fwd.items():
                    self.send_header(name, val)
                raw = json.dumps(obj).encode()
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
                return None
            resp, conn = payload, conn
            self.send_response(status)
            self.send_header("Content-Type", resp.getheader("Content-Type", "application/json"))
            for name in FWD_HEADERS:
                val = resp.getheader(name)
                if val:
                    self.send_header(name, val)
            self.close_connection = True
            self.end_headers()
            clean = False
            try:
                while True:
                    chunk = resp.read(CHUNK)
                    if not chunk:
                        break
                    self.wfile.write(chunk)
                    self.wfile.flush()
                clean = True
            except (BrokenPipeError, ConnectionResetError):
                pass
            finally:
                try:
                    if clean:
                        resp.read()
                except Exception:
                    clean = False
                if clean:
                    _pool_give(conn)
                else:
                    _pool_drop(conn)
            return None
        return 502, {"error": "upstream retry exhausted"}

    do_GET = _proxy
    do_POST = _proxy
    do_PUT = _proxy
    do_PATCH = _proxy
    do_DELETE = _proxy


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "login":
        do_login()
    else:
        _setup_logging()
        server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
        server.daemon_threads = True
        log.info("muse-bridge listening on 127.0.0.1:%d", PORT)
        server.serve_forever()
