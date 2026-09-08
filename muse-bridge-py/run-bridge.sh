#!/usr/bin/env bash
# Launcher for the muse-bridge daemon. Resolves a working python3 at every
# (re)start, so the daemon self-heals when an interpreter breaks after install
# (e.g. a macOS Xcode/CLT mismatch killing /usr/bin/python3).
# Invoked by the LaunchAgent (macOS) or user service (Linux); rarely by hand.
# Override: MUSE_BRIDGE_PYTHON=/path/to/python3 (checked first).
set -u

PY_DIR="$(cd "$(dirname "$0")" && pwd)" # this script lives next to bridge.py
BRIDGE_HOME="$(dirname "$PY_DIR")"      # ...inside the bridge home

is_good() { # $1 = candidate path: must execute and be 3.8+
  [ -n "${1:-}" ] && [ -x "$1" ] || return 1
  "$1" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)' >/dev/null 2>&1
}

candidates=(
  "${MUSE_BRIDGE_PYTHON:-}"
  /opt/homebrew/bin/python3
  /usr/local/bin/python3
)
if command -v python3 >/dev/null 2>&1; then
  candidates+=("$(command -v python3)")
fi
candidates+=(
  /usr/bin/python3
  /Library/Developer/CommandLineTools/usr/bin/python3
)

tried=""
for cand in "${candidates[@]}"; do
  [ -n "$cand" ] || continue
  case ":${tried}:" in *":${cand}:"*) continue ;; esac
  tried="${tried}:${cand}"
  if is_good "$cand"; then
    line="run-bridge: using ${cand} ($("$cand" --version 2>&1))"
    echo "$line"
    # The daemon owns bridge.log from here on; this one pre-exec line is
    # ours to append (single writer at this point, so no clash).
    echo "$line" >> "${BRIDGE_HOME}/bridge.log" 2>/dev/null || true
    exec "$cand" "${PY_DIR}/bridge.py"
  fi
done

echo "run-bridge: no working python3 (>=3.8) found; fix Xcode/CLT or install python3" >&2
exit 3
