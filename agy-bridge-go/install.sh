#!/usr/bin/env bash
# agy-bridge-go one-shot installer & harness configurator.
# Usage: ./install.sh [--codex] [--opencode] [--pi] [--all]
# Configures Codex CLI, OpenCode, and pi with AGY bridge defaults:
# - Default model: gemini-3.8-flash-high
# - Default reasoning effort: high (highest AGY offers on Flash 3.8)
# - No max effort (AGY offers low, medium, high)
set -euo pipefail

PORT="8917"
BASE_URL="http://127.0.0.1:${PORT}/v1"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_PATH="$(cd "${SCRIPT_DIR}/.." && pwd)/bin/agy-bridge-go"
PLIST="${HOME}/Library/LaunchAgents/com.jeffhuen.agy-bridge-go.plist"

DO_CODEX=0; DO_OPENCODE=0; DO_PI=0
if [ $# -eq 0 ]; then
  DO_CODEX=1; DO_OPENCODE=1; DO_PI=1
else
  for arg in "$@"; do
    case "$arg" in
      --codex) DO_CODEX=1 ;;
      --opencode) DO_OPENCODE=1 ;;
      --pi) DO_PI=1 ;;
      --all) DO_CODEX=1; DO_OPENCODE=1; DO_PI=1 ;;
      *) echo "unknown flag: $arg" >&2; exit 1 ;;
    esac
  done
fi

echo "==> Setting up agy-bridge-go on port ${PORT}..."

# 1. Build and install binary if needed
if [ ! -f "${BIN_PATH}" ]; then
  echo "==> Building binary..."
  mkdir -p "$(dirname "${BIN_PATH}")"
  go build -trimpath -ldflags "-s -w" -o "${BIN_PATH}" ./cmd/agy-bridge-go
fi

# 2. Configure LaunchAgent
mkdir -p "${HOME}/Library/LaunchAgents"
cat << PLIST_EOF > "${PLIST}"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.jeffhuen.agy-bridge-go</string>
    <key>ProgramArguments</key>
    <array>
        <string>${BIN_PATH}</string>
        <string>-port</string>
        <string>${PORT}</string>
    </array>
    <key>WorkingDirectory</key>
    <string>${HOME}/.config/muse-bridge</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>10</integer>
    <key>StandardErrorPath</key>
    <string>${HOME}/.config/muse-bridge/agy-bridge-go.log</string>
    <key>StandardOutPath</key>
    <string>${HOME}/.config/muse-bridge/agy-bridge-go.log</string>
</dict>
</plist>
PLIST_EOF

# Kickstart or load
launchctl bootout "gui/$(id -u)/com.jeffhuen.agy-bridge-go" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "${PLIST}"
launchctl kickstart -k "gui/$(id -u)/com.jeffhuen.agy-bridge-go"

# 3. Configure Codex CLI
if [ "${DO_CODEX}" -eq 1 ]; then
  echo "==> Configuring Codex CLI (default model: gemini-3.8-flash-high, effort: high)..."
  mkdir -p "${HOME}/.codex/model-catalogs"
  cat << 'CATALOG_EOF' > "${HOME}/.codex/model-catalogs/agy.json"
{
  "models": [
    {
      "slug": "gemini-3.8-flash-high",
      "display_name": "Gemini 3.8 Flash (High)",
      "description": "Google Gemini 3.8 Flash via AGY bridge",
      "default_reasoning_level": "high",
      "base_instructions": "You are a coding agent running in Codex CLI.",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "Low reasoning"},
        {"effort": "medium", "description": "Medium reasoning"},
        {"effort": "high", "description": "High reasoning"}
      ],
      "shell_type": "unified_exec",
      "visibility": "list",
      "supported_in_api": true,
      "priority": 0,
      "support_verbosity": false,
      "truncation_policy": {"mode": "bytes", "limit": 10000},
      "context_window": 1048576,
      "max_context_window": 1048576,
      "experimental_supported_tools": []
    },
    {
      "slug": "gemini-3.1-pro-high",
      "display_name": "Gemini 3.1 Pro (High)",
      "description": "Google Gemini 3.1 Pro via AGY bridge",
      "default_reasoning_level": "high",
      "base_instructions": "You are a coding agent running in Codex CLI.",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "Low reasoning"},
        {"effort": "high", "description": "High reasoning"}
      ],
      "shell_type": "unified_exec",
      "visibility": "list",
      "supported_in_api": true,
      "priority": 1,
      "support_verbosity": false,
      "truncation_policy": {"mode": "bytes", "limit": 10000},
      "context_window": 1048576,
      "max_context_window": 1048576,
      "experimental_supported_tools": []
    },
    {
      "slug": "claude-sonnet-4-6",
      "display_name": "Claude Sonnet 4.6 (Thinking)",
      "description": "Anthropic Claude Sonnet 4.6 via AGY bridge",
      "default_reasoning_level": "high",
      "base_instructions": "You are a coding agent running in Codex CLI.",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "Low reasoning"},
        {"effort": "medium", "description": "Medium reasoning"},
        {"effort": "high", "description": "High reasoning"}
      ],
      "shell_type": "unified_exec",
      "visibility": "list",
      "supported_in_api": true,
      "priority": 2,
      "support_verbosity": false,
      "truncation_policy": {"mode": "bytes", "limit": 10000},
      "context_window": 200000,
      "max_context_window": 200000,
      "experimental_supported_tools": []
    },
    {
      "slug": "claude-opus-4-6-thinking",
      "display_name": "Claude Opus 4.6 (Thinking)",
      "description": "Anthropic Claude Opus 4.6 Thinking via AGY bridge",
      "default_reasoning_level": "high",
      "base_instructions": "You are a coding agent running in Codex CLI.",
      "supported_reasoning_levels": [
        {"effort": "high", "description": "High reasoning"}
      ],
      "shell_type": "unified_exec",
      "visibility": "list",
      "supported_in_api": true,
      "priority": 3,
      "support_verbosity": false,
      "truncation_policy": {"mode": "bytes", "limit": 10000},
      "context_window": 200000,
      "max_context_window": 200000,
      "experimental_supported_tools": []
    }
  ]
}
CATALOG_EOF

  cat << CONFIG_EOF > "${HOME}/.codex/agy.config.toml"
model = "gemini-3.8-flash-high"
model_provider = "agy"
model_reasoning_effort = "high"
model_context_window = 1048576
model_catalog_json = "${HOME}/.codex/model-catalogs/agy.json"

features.multi_agent = true
tools.web_search = false
apps._default.enabled = false

[model_providers.agy]
name = "Antigravity"
base_url = "${BASE_URL}"
experimental_bearer_token = "bridge"
wire_api = "responses"
CONFIG_EOF
fi

# 4. Configure OpenCode
if [ "${DO_OPENCODE}" -eq 1 ]; then
  echo "==> Configuring OpenCode (default reasoning: high, variants: low, medium, high)..."
  python3 - << 'PYEOF'
import json, os

path = os.path.expanduser("~/.config/opencode/opencode.json")
os.makedirs(os.path.dirname(path), exist_ok=True)
data = {}
if os.path.exists(path):
    with open(path) as f:
        data = json.load(f)

data.setdefault("$schema", "https://opencode.ai/config.json")
providers = data.setdefault("provider", {})
providers["agy-bridge-go"] = {
    "npm": "@ai-sdk/openai",
    "name": "Google Antigravity via AGY bridge (Go)",
    "options": {
        "baseURL": "http://127.0.0.1:8917/v1",
        "forceReasoning": True
    },
    "models": {
        "gemini-3.8-flash-high": {
            "name": "Gemini 3.8 Flash (High)",
            "limit": {"context": 1048576, "output": 256000},
            "options": {"reasoningEffort": "high"},
            "variants": {
                "low": {"reasoningEffort": "low"},
                "medium": {"reasoningEffort": "medium"},
                "high": {"reasoningEffort": "high"}
            }
        },
        "gemini-3.1-pro-high": {
            "name": "Gemini 3.1 Pro (High)",
            "limit": {"context": 1048576, "output": 256000},
            "options": {"reasoningEffort": "high"},
            "variants": {
                "low": {"reasoningEffort": "low"},
                "high": {"reasoningEffort": "high"}
            }
        },
        "claude-sonnet-4-6": {
            "name": "Claude Sonnet 4.6 (Thinking)",
            "limit": {"context": 200000, "output": 64000},
            "options": {"reasoningEffort": "high"},
            "variants": {
                "low": {"reasoningEffort": "low"},
                "medium": {"reasoningEffort": "medium"},
                "high": {"reasoningEffort": "high"}
            }
        }
    }
}

with open(path, "w") as f:
    json.dump(data, f, indent=4)
    f.write("\n")
PYEOF
fi

# 5. Configure pi
if [ "${DO_PI}" -eq 1 ]; then
  echo "==> Configuring pi (default reasoning: high, variants: low, medium, high)..."
  python3 - << 'PYEOF'
import json, os

mpath = os.path.expanduser("~/.pi/agent/models.json")
os.makedirs(os.path.dirname(mpath), exist_ok=True)
mdata = {}
if os.path.exists(mpath):
    with open(mpath) as f:
        mdata = json.load(f)

providers = mdata.setdefault("providers", {})
providers["agy-bridge-go"] = {
    "baseUrl": "http://127.0.0.1:8917/v1",
    "apiKey": "bridge",
    "api": "openai-responses",
    "models": [
        {
            "id": "gemini-3.8-flash-high",
            "name": "Gemini 3.8 Flash (High)",
            "reasoning": True,
            "input": ["text", "image"],
            "contextWindow": 1048576,
            "maxTokens": 256000,
            "thinkingLevelMap": {
                "off": None,
                "low": "low",
                "medium": "medium",
                "high": "high"
            }
        },
        {
            "id": "gemini-3.1-pro-high",
            "name": "Gemini 3.1 Pro (High)",
            "reasoning": True,
            "input": ["text", "image"],
            "contextWindow": 1048576,
            "maxTokens": 256000,
            "thinkingLevelMap": {
                "off": None,
                "low": "low",
                "high": "high"
            }
        },
        {
            "id": "claude-sonnet-4-6",
            "name": "Claude Sonnet 4.6 (Thinking)",
            "reasoning": True,
            "input": ["text", "image"],
            "contextWindow": 200000,
            "maxTokens": 64000,
            "thinkingLevelMap": {
                "off": None,
                "low": "low",
                "medium": "medium",
                "high": "high"
            }
        }
    ]
}

with open(mpath, "w") as f:
    json.dump(mdata, f, indent=4)
    f.write("\n")

spath = os.path.expanduser("~/.pi/agent/settings.json")
if os.path.exists(spath):
    with open(spath) as f:
        sdata = json.load(f)
    levels = sdata.setdefault("modelThinkingLevels", {})
    levels["agy-bridge-go/gemini-3.8-flash-high"] = "high"
    levels["agy-bridge-go/gemini-3.1-pro-high"] = "high"
    levels["agy-bridge-go/claude-sonnet-4-6"] = "high"
    with open(spath, "w") as f:
        json.dump(sdata, f, indent=2)
        f.write("\n")
PYEOF
fi

chmod +x "${BIN_PATH}"
echo "==> Done! agy-bridge-go is active and all harnesses default to high effort."
