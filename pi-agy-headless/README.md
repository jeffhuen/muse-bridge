# pi-agy-headless

A standalone, zero-npm Google Antigravity extension for [Pi](https://github.com/earendil-works/pi).

## Why This Fork?

1. **Zero npm Dependencies**: Pure TypeScript utilizing Node's built-in `fetch`, `crypto`, and `http`. No `npm install`, no `node_modules`, no `package.json` required. Pi natively loads it straight from `~/.pi/agent/extensions/pi-agy-headless.ts`.
2. **Terminal-Only / Headless Environments**:
   - Automatically retrieves existing tokens from **macOS Keychain** (`security find-generic-password -s gemini -a antigravity`) without prompting or opening a browser.
   - Automatically checks `ANTIGRAVITY_TOKEN` or `AGY_TOKEN` environment variables.
   - In remote/SSH sessions without a desktop, `/login antigravity` provides an interactive terminal paste prompt for authorization codes so localhost redirects never hang.
   - When a desktop is present, standard loopback callback redirects are seamlessly supported.
3. **Full Gemini 3.8 Flash Support**:
   - First-class support for `gemini-3.8-flash` with default high reasoning, plus `low` and `medium` variants.
   - Connects directly to Google's official Antigravity PredictionService (`https://daily-cloudcode-pa.googleapis.com`).
   - Automatically attaches and preserves `thoughtSignature` across multi-turn tool calling.
4. **No Background Daemons or Open Ports**:
   - Executes purely inside Pi during your session. No background server, no `launchd` plist, and no port conflicts.

---

## Installation

Install directly via Pi's package manager:

```bash
pi install git:github.com/jeffhuen/pi-agy-headless
```

Or for local development:

```bash
pi install ./pi-agy-headless
```

---

## Supported Models

| Public Model ID | Upstream Runtime Model | Supported Thinking Levels | Context Window | Max Output |
| :--- | :--- | :--- | :--- | :--- |
| `gemini-3.8-flash` | `gemini-3.8-flash-high` *(default)* | `low`, `medium`, `high` | 1,048,576 | 65,536 |
| `gemini-3.7-flash` | `gemini-3.7-flash-high` | `low`, `medium`, `high` | 1,048,576 | 65,536 |
| `gemini-3.6-flash` | `gemini-3.6-flash-high` | `low`, `medium`, `high` | 1,048,576 | 65,536 |
| `gemini-3.1-pro` | `gemini-pro-agent` | `low`, `high` | 1,048,576 | 65,535 |
| `claude-sonnet-4-6` | `claude-sonnet-4-6` | `high` | 200,000 | 64,000 |
| `claude-opus-4-6` | `claude-opus-4-6-thinking` | `high` | 250,000 | 64,000 |
| `gpt-oss-120b` | `gpt-oss-120b-medium` | `medium` | 131,072 | 32,768 |

---

## Usage in Pi

1. Start `pi`:
   ```bash
   pi
   ```
2. Switch to Gemini 3.8 Flash:
   ```text
   /model antigravity/gemini-3.8-flash
   ```
3. Check connection & diagnostics:
   ```text
   /antigravity.doctor
   ```
