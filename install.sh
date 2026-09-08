#!/usr/bin/env bash
# muse-bridge one-shot installer.
# Usage: ./install.sh [--opencode] [--pi] [--go | --go-only]   (no harness flag = ask)
#        ./install.sh --uninstall [--purge]
# Does: daemon + login (opens browser) + harness config. Safe to re-run.
# Default daemon is Python. --go adds the Go sidecar on port 8916;
# --go-only installs the Go daemon instead of the Python one.
set -euo pipefail

BRIDGE_DIR="${HOME}/.config/muse-bridge"
SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
PLIST="${HOME}/Library/LaunchAgents/com.jeffhuen.muse-bridge.plist"
PLIST_GO="${HOME}/Library/LaunchAgents/com.jeffhuen.muse-bridge-go.plist"
BASE_URL="http://127.0.0.1:8915/v1"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
need python3
need curl
# The installer itself uses PATH python3 (login in Python mode, config merge
# in every mode), so verify it actually executes (a broken Xcode shim
# passes `need` but fails to run).
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)' 2>/dev/null \
  || { echo "python3 on PATH is broken or older than 3.8; fix Xcode/CLT or install python3" >&2; exit 1; }

OS_NAME="$(uname -s)"
IS_WSL=0
if [ -r /proc/version ] && grep -qi 'microsoft\|wsl' /proc/version 2>/dev/null; then IS_WSL=1; fi
open_url() {
  if [ "${OS_NAME}" = "Darwin" ]; then
    open "$1"
  elif [ "${IS_WSL}" -eq 1 ] && command -v wslview >/dev/null 2>&1; then
    wslview "$1"
  elif [ "${IS_WSL}" -eq 1 ]; then
    powershell.exe -NoProfile -Command "Start-Process '$1'" 2>/dev/null || echo "Open: $1"
  elif command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$1"
  else
    echo "Open: $1"
  fi
}

DO_OPENCODE=0; DO_PI=0; DO_GO=0; DO_GO_ONLY=0; DO_UNINSTALL=0; DO_PURGE=0
for arg in "$@"; do
  case "$arg" in
    --opencode) DO_OPENCODE=1 ;;
    --pi) DO_PI=1 ;;
    --go) DO_GO=1 ;;
    --go-only) DO_GO=1; DO_GO_ONLY=1 ;;
    --uninstall) DO_UNINSTALL=1 ;;
    --purge) DO_PURGE=1 ;;
    *) echo "unknown flag: $arg (use --opencode/--pi/--go/--go-only/--uninstall/--purge)" >&2; exit 1 ;;
  esac
done
if [ "$DO_GO" -eq 1 ]; then
  command -v go >/dev/null 2>&1 || { echo "missing: go (install with: brew install go)" >&2; exit 1; }
fi

if [ "$DO_UNINSTALL" -eq 1 ]; then
  echo "==> Uninstalling muse-bridge"
  if [ "$(uname -s)" = "Darwin" ]; then
    launchctl unload "${PLIST}" 2>/dev/null || true
    launchctl unload "${PLIST_GO}" 2>/dev/null || true
  else
    systemctl --user disable --now muse-bridge.service 2>/dev/null || true
    systemctl --user disable --now muse-bridge-go.service 2>/dev/null || true
    rm -f "${HOME}/.config/systemd/user/muse-bridge.service" \
      "${HOME}/.config/systemd/user/muse-bridge-go.service"
    systemctl --user daemon-reload 2>/dev/null || true
  fi
  pkill -f muse-bridge/bridge.py 2>/dev/null || true
  pkill -f muse-bridge/bin/muse-bridge-go 2>/dev/null || true
  rm -f "${PLIST}" "${PLIST_GO}"
  if [ "$DO_PURGE" -eq 1 ]; then
    rm -rf "${BRIDGE_DIR}"
    echo "Purged ${BRIDGE_DIR} (login included)."
  else
    echo "Daemon stopped and auto-start removed."
    echo "Kept: ${BRIDGE_DIR} (login + logs). Re-run install.sh to restore,"
    echo "  or re-run with --purge to delete everything."
  fi
  echo "Optional harness cleanup:"
  echo "  OpenCode: delete the meta-bridge and meta-bridge-go blocks in ~/.config/opencode/opencode.json"
  echo "  pi: delete the meta-bridge and meta-bridge-go blocks in ~/.pi/agent/models.json"
  exit 0
fi
if [ "$DO_OPENCODE" -eq 0 ] && [ "$DO_PI" -eq 0 ]; then
  echo "Configure which harness?"
  echo "  1) OpenCode  2) pi  3) both"
  read -r -p "Choice [3]: " choice
  case "${choice:-3}" in
    1) DO_OPENCODE=1 ;;
    2) DO_PI=1 ;;
    *) DO_OPENCODE=1; DO_PI=1 ;;
  esac
fi

echo "==> Installing bridge files to ${BRIDGE_DIR}"
mkdir -p "${BRIDGE_DIR}"
chmod 700 "${BRIDGE_DIR}"
if [ "${SRC_DIR}" != "${BRIDGE_DIR}" ]; then
  cp "${SRC_DIR}/bridge.py" "${BRIDGE_DIR}/bridge.py"
  cp "${SRC_DIR}/run-bridge.sh" "${BRIDGE_DIR}/run-bridge.sh"
  rm -rf "${BRIDGE_DIR}/muse-bridge-go"
  cp -r "${SRC_DIR}/muse-bridge-go" "${BRIDGE_DIR}/muse-bridge-go"
  rm -rf "${BRIDGE_DIR}/muse-bridge-go/dist"
fi
chmod +x "${BRIDGE_DIR}/run-bridge.sh"

echo "==> Starting daemon"
start_darwin() {
  mkdir -p "$(dirname "${PLIST}")"
  cat > "${PLIST}" <<PLEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.jeffhuen.muse-bridge</string>
    <key>ProgramArguments</key>
    <array>
        <string>${BRIDGE_DIR}/run-bridge.sh</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>10</integer>
    <key>StandardOutPath</key>
    <string>${BRIDGE_DIR}/bridge.log</string>
    <key>StandardErrorPath</key>
    <string>${BRIDGE_DIR}/bridge.log</string>
</dict>
</plist>
PLEOF
  # Unload + load (not kickstart): kickstart reuses the already-loaded job
  # definition, so it would ignore a changed plist.
  launchctl unload "${PLIST}" 2>/dev/null || true
  sleep 1
  launchctl load "${PLIST}" 2>&1 || true
}
start_linux() {
  UNIT_DIR="${HOME}/.config/systemd/user"
  if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
    mkdir -p "${UNIT_DIR}"
    cat > "${UNIT_DIR}/muse-bridge.service" <<'SVCEOF'
[Unit]
Description=muse-bridge Meta OAuth proxy
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%h/.config/muse-bridge/run-bridge.sh
Restart=always
RestartSec=10
StandardOutput=append:%h/.config/muse-bridge/bridge.log
StandardError=append:%h/.config/muse-bridge/bridge.log

[Install]
WantedBy=default.target
SVCEOF
    systemctl --user daemon-reload
    systemctl --user enable --now muse-bridge.service
  else
    echo "No systemd user session here (plain WSL?). Starting without auto-restart."
    pkill -f muse-bridge/bridge.py 2>/dev/null || true
    nohup "${BRIDGE_DIR}/run-bridge.sh" > "${BRIDGE_DIR}/bridge.log" 2>&1 &
    echo "It will NOT survive reboot here. Re-run install.sh after restart,"
    echo "or enable systemd in WSL and re-run."
  fi
}
build_go() {
  echo "==> Building muse-bridge-go"
  mkdir -p "${BRIDGE_DIR}/bin"
  VERSION="$(git -C "${SRC_DIR}" rev-parse --short HEAD 2>/dev/null || echo dev)"
  (cd "${BRIDGE_DIR}/muse-bridge-go" && go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o ../bin/muse-bridge-go ./cmd/muse-bridge-go)
}
start_go_darwin() {
  mkdir -p "$(dirname "${PLIST_GO}")"
  cat > "${PLIST_GO}" <<PLEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.jeffhuen.muse-bridge-go</string>
    <key>ProgramArguments</key>
    <array>
        <string>${BRIDGE_DIR}/bin/muse-bridge-go</string>
        <string>-port</string>
        <string>8916</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>10</integer>
    <key>StandardOutPath</key>
    <string>${BRIDGE_DIR}/bridge-go.log</string>
    <key>StandardErrorPath</key>
    <string>${BRIDGE_DIR}/bridge-go.log</string>
</dict>
</plist>
PLEOF
  launchctl unload "${PLIST_GO}" 2>/dev/null || true
  sleep 1
  launchctl load "${PLIST_GO}" 2>&1 || true
}
start_go_linux() {
  UNIT_DIR="${HOME}/.config/systemd/user"
  if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
    mkdir -p "${UNIT_DIR}"
    cat > "${UNIT_DIR}/muse-bridge-go.service" <<'SVCEOF'
[Unit]
Description=muse-bridge-go Meta OAuth proxy
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%h/.config/muse-bridge/bin/muse-bridge-go -port 8916
Restart=always
RestartSec=10
StandardOutput=append:%h/.config/muse-bridge/bridge-go.log
StandardError=append:%h/.config/muse-bridge/bridge-go.log

[Install]
WantedBy=default.target
SVCEOF
    systemctl --user daemon-reload
    systemctl --user enable --now muse-bridge-go.service
  else
    echo "No systemd user session here (plain WSL?). Starting Go daemon without auto-restart."
    pkill -f muse-bridge/bin/muse-bridge-go 2>/dev/null || true
    nohup "${BRIDGE_DIR}/bin/muse-bridge-go" -port 8916 > "${BRIDGE_DIR}/bridge-go.log" 2>&1 &
  fi
}
stop_python() {
  if [ "${OS_NAME}" = "Darwin" ]; then
    launchctl unload "${PLIST}" 2>/dev/null || true
  else
    systemctl --user disable --now muse-bridge.service 2>/dev/null || true
  fi
  pkill -f muse-bridge/bridge.py 2>/dev/null || true
}
if [ "$DO_GO_ONLY" -eq 1 ]; then
  echo "==> Stopping Python daemon (--go-only)"
  stop_python
else
  if [ "${OS_NAME}" = "Darwin" ]; then start_darwin; else start_linux; fi
fi
if [ "$DO_GO" -eq 1 ]; then
  build_go
  if [ "${OS_NAME}" = "Darwin" ]; then start_go_darwin; else start_go_linux; fi
fi
sleep 3

probe() { port="${1:-8915}"; curl -s -o /dev/null -w '%{http_code}' --max-time 15 "http://127.0.0.1:${port}/v1/models"; }

if [ "$DO_GO_ONLY" -eq 1 ]; then
  DAEMON_PORT=8916
  LOGIN_PROG=("${BRIDGE_DIR}/bin/muse-bridge-go" login)
else
  DAEMON_PORT=8915
  LOGIN_PROG=(python3 "${BRIDGE_DIR}/bridge.py" login)
fi
echo "==> Checking login"
if [ "$(probe "$DAEMON_PORT")" = "200" ]; then
  echo "Login OK (bridge serves models)."
else
  echo "No working login. Starting device login (browser opens)..."
  : > "${BRIDGE_DIR}/login.log"
  "${LOGIN_PROG[@]}" > "${BRIDGE_DIR}/login.log" 2>&1 &
  LOGIN_PID=$!
  trap 'kill ${LOGIN_PID} 2>/dev/null || true' INT TERM
  URL=""
  for _ in $(seq 1 60); do
    URL=$(sed -n 's/^Approve in browser: //p' "${BRIDGE_DIR}/login.log" | head -1)
    [ -n "${URL}" ] && break
    sleep 2
  done
  if [ -z "${URL}" ]; then echo "login did not start; see login.log" >&2; exit 1; fi
  grep 'User code:' "${BRIDGE_DIR}/login.log" || true
  open_url "${URL}"
  echo "Approve in the browser, then wait here..."
  wait "${LOGIN_PID}"
  trap - INT TERM
  [ "$(probe "$DAEMON_PORT")" = "200" ] || { echo "login failed; see login.log" >&2; exit 1; }
  echo "Login OK (bridge serves models)."
fi

if [ "$DO_GO" -eq 1 ] && [ "$DO_GO_ONLY" -eq 0 ]; then
  echo "==> Checking Go daemon"
  # Login is shared via identity.json, so this only verifies the Go
  # daemon came up; no second login flow exists.
  [ "$(probe 8916)" = "200" ] || { echo "go daemon not responding; see bridge-go.log" >&2; exit 1; }
  echo "Go OK (bridge serves models on 8916)."
fi

merge_json() { # $1=file $2=kind(opencode|pi) $3=provider $4=port $5=name suffix
  python3 - "$1" "$2" "$3" "$4" "$5" <<'PYEOF'
import json, sys
path, kind, provider, port, suffix = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
try:
    with open(path) as fh:
        doc = json.load(fh)
except FileNotFoundError:
    doc = {}
except json.JSONDecodeError as exc:
    print("refusing to touch invalid JSON: %s (%s)" % (path, exc))
    sys.exit(1)
import os, shutil, time
os.makedirs(os.path.dirname(path), exist_ok=True)
if os.path.exists(path):
    shutil.copy(path, "%s.bak-%d" % (path, int(time.time())))
variants = {
    "minimal": {"reasoningEffort": "minimal"},
    "low": {"reasoningEffort": "low"},
    "medium": {"reasoningEffort": "medium"},
    "high": {"reasoningEffort": "high"},
    "xhigh": {"reasoningEffort": "xhigh"},
    "max": {"reasoningEffort": "max"},
}
if kind == "opencode":
    doc.setdefault("$schema", "https://opencode.ai/config.json")
    prov = doc.setdefault("provider", {}).setdefault(provider, {})
    prov.update({"npm": "@ai-sdk/openai", "name": "Meta via Muse bridge" + suffix,
                 "options": {"baseURL": "http://127.0.0.1:%s/v1" % port,
                             "forceReasoning": True}})
    models = prov.setdefault("models", {})
    for mid, name in (("muse-spark-1.3", "Muse Spark 1.3"),
                      ("muse-spark-1.3-contributor", "Muse Spark 1.3 Contributor")):
        m = models.setdefault(mid, {})
        m.update({"name": name, "limit": {"context": 1048576, "output": 256000},
                  "variants": variants})
else:
    prov = doc.setdefault("providers", {}).setdefault(provider, {})
    prov.update({"baseUrl": "http://127.0.0.1:%s/v1" % port, "apiKey": "bridge",
                 "api": "openai-responses"})
    models = prov.setdefault("models", [])
    want = {"muse-spark-1.3", "muse-spark-1.3-contributor"}
    have = {m.get("id") for m in models if isinstance(m, dict)}
    for mid in sorted(want - have):
        models.append({"id": mid, "name": mid.replace("-", " ").title(),
                       "reasoning": True, "input": ["text", "image"],
                       "contextWindow": 1048576, "maxTokens": 256000,
                       "thinkingLevelMap": {"off": None, "minimal": "minimal",
                                            "low": "low", "medium": "medium",
                                            "high": "high", "xhigh": "xhigh",
                                            "max": "max"}})
with open(path, "w") as fh:
    json.dump(doc, fh, indent=4)
    fh.write("\n")
print("updated %s (backup kept)" % path)
PYEOF
}

if [ "$DO_GO_ONLY" -eq 1 ]; then
  [ "$DO_OPENCODE" -eq 1 ] && merge_json "${HOME}/.config/opencode/opencode.json" opencode meta-bridge-go 8916 " (Go)"
  [ "$DO_PI" -eq 1 ] && merge_json "${HOME}/.pi/agent/models.json" pi meta-bridge-go 8916 " (Go)"
else
  [ "$DO_OPENCODE" -eq 1 ] && merge_json "${HOME}/.config/opencode/opencode.json" opencode meta-bridge 8915 ""
  [ "$DO_PI" -eq 1 ] && merge_json "${HOME}/.pi/agent/models.json" pi meta-bridge 8915 ""
  if [ "$DO_GO" -eq 1 ]; then
    [ "$DO_OPENCODE" -eq 1 ] && merge_json "${HOME}/.config/opencode/opencode.json" opencode meta-bridge-go 8916 " (Go)"
    [ "$DO_PI" -eq 1 ] && merge_json "${HOME}/.pi/agent/models.json" pi meta-bridge-go 8916 " (Go)"
  fi
fi

echo "==> Done. Next steps:"
if [ "$DO_GO_ONLY" -eq 0 ]; then
  [ "$DO_OPENCODE" -eq 1 ] && echo "  OpenCode: /connect -> meta-bridge -> type anything, then /models"
  [ "$DO_PI" -eq 1 ] && echo "  pi: /model -> meta-bridge/muse-spark-1.3"
fi
if [ "$DO_GO" -eq 1 ]; then
  [ "$DO_OPENCODE" -eq 1 ] && echo "  OpenCode (Go): /connect -> meta-bridge-go -> type anything, then /models"
  [ "$DO_PI" -eq 1 ] && echo "  pi (Go): /model -> meta-bridge-go/muse-spark-1.3"
fi
