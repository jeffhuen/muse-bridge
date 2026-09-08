#!/usr/bin/env bash
# muse-bridge one-shot installer.
# Usage: ./install.sh [--opencode] [--pi]   (no flag = ask)
#        ./install.sh --uninstall [--purge]
# Does: daemon + login (opens browser) + harness config. Safe to re-run.
set -euo pipefail

BRIDGE_DIR="${HOME}/.config/muse-bridge"
SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
PLIST="${HOME}/Library/LaunchAgents/com.jeffhuen.muse-bridge.plist"
BASE_URL="http://127.0.0.1:8915/v1"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
need python3
need curl
# The installer itself runs login + config merge with PATH python3, so verify
# it actually executes (a broken Xcode shim passes `need` but fails to run).
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

DO_OPENCODE=0; DO_PI=0; DO_UNINSTALL=0; DO_PURGE=0
for arg in "$@"; do
  case "$arg" in
    --opencode) DO_OPENCODE=1 ;;
    --pi) DO_PI=1 ;;
    --uninstall) DO_UNINSTALL=1 ;;
    --purge) DO_PURGE=1 ;;
    *) echo "unknown flag: $arg (use --opencode/--pi/--uninstall/--purge)" >&2; exit 1 ;;
  esac
done

if [ "$DO_UNINSTALL" -eq 1 ]; then
  echo "==> Uninstalling muse-bridge"
  if [ "$(uname -s)" = "Darwin" ]; then
    launchctl unload "${PLIST}" 2>/dev/null || true
  else
    systemctl --user disable --now muse-bridge.service 2>/dev/null || true
    rm -f "${HOME}/.config/systemd/user/muse-bridge.service"
    systemctl --user daemon-reload 2>/dev/null || true
  fi
  pkill -f muse-bridge/bridge.py 2>/dev/null || true
  rm -f "${PLIST}"
  if [ "$DO_PURGE" -eq 1 ]; then
    rm -rf "${BRIDGE_DIR}"
    echo "Purged ${BRIDGE_DIR} (login included)."
  else
    echo "Daemon stopped and auto-start removed."
    echo "Kept: ${BRIDGE_DIR} (login + logs). Re-run install.sh to restore,"
    echo "  or re-run with --purge to delete everything."
  fi
  echo "Optional harness cleanup:"
  echo "  OpenCode: delete the meta-bridge block in ~/.config/opencode/opencode.json"
  echo "  pi: delete the meta-bridge block in ~/.pi/agent/models.json"
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
if [ "${OS_NAME}" = "Darwin" ]; then start_darwin; else start_linux; fi
sleep 3

probe() { curl -s -o /dev/null -w '%{http_code}' --max-time 15 "${BASE_URL}/models"; }

echo "==> Checking login"
if [ "$(probe)" = "200" ]; then
  echo "Login OK (bridge serves models)."
else
  echo "No working login. Starting device login (browser opens)..."
  : > "${BRIDGE_DIR}/login.log"
  python3 "${BRIDGE_DIR}/bridge.py" login > "${BRIDGE_DIR}/login.log" 2>&1 &
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
  [ "$(probe)" = "200" ] || { echo "login failed; see login.log" >&2; exit 1; }
  echo "Login OK (bridge serves models)."
fi

merge_json() { # $1=file $2=kind(opencode|pi)
  python3 - "$1" "$2" <<'PYEOF'
import json, sys
path, kind = sys.argv[1], sys.argv[2]
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
    prov = doc.setdefault("provider", {}).setdefault("meta-bridge", {})
    prov.update({"npm": "@ai-sdk/openai", "name": "Meta via Muse bridge",
                 "options": {"baseURL": "http://127.0.0.1:8915/v1",
                             "forceReasoning": True}})
    models = prov.setdefault("models", {})
    for mid, name in (("muse-spark-1.3", "Muse Spark 1.3"),
                      ("muse-spark-1.3-contributor", "Muse Spark 1.3 Contributor")):
        m = models.setdefault(mid, {})
        m.update({"name": name, "limit": {"context": 1048576, "output": 256000},
                  "variants": variants})
else:
    prov = doc.setdefault("providers", {}).setdefault("meta-bridge", {})
    prov.update({"baseUrl": "http://127.0.0.1:8915/v1", "apiKey": "bridge",
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

[ "$DO_OPENCODE" -eq 1 ] && merge_json "${HOME}/.config/opencode/opencode.json" opencode
[ "$DO_PI" -eq 1 ] && merge_json "${HOME}/.pi/agent/models.json" pi

echo "==> Done. Next steps:"
[ "$DO_OPENCODE" -eq 1 ] && echo "  OpenCode: /connect -> meta-bridge -> type anything, then /models"
[ "$DO_PI" -eq 1 ] && echo "  pi: /model -> meta-bridge/muse-spark-1.3"
