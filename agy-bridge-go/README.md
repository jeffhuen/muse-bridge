# agy-bridge-go (AGY Protocol Translator)

`agy-bridge-go` is a bidirectional protocol translator, schema compiler, and cryptographic state engine that connects OpenAI-compatible coding tools (Codex CLI, OpenCode, pi, Zed, Cursor, Aider) directly to Google's internal Antigravity (AGY) PredictionService (`aicode-consumers`). Runs locally on `http://127.0.0.1:8917`.

Uses your authenticated **Antigravity subscription session** via macOS Keychain (zero pay-as-you-go API keys required, and zero CLI subprocesses spawned).

---

## Architecture: Bridge vs. Protocol Translator

While named `agy-bridge-go` for convention alongside `muse-bridge`, the internal architecture is fundamentally different from a standard reverse proxy:

| Feature | `muse-bridge-go` (Reverse Proxy) | `agy-bridge-go` (Protocol Translator) |
| :--- | :--- | :--- |
| **Upstream Target** | Meta API (`/v1/responses`) | Google Internal PredictionService (`aicode-consumers`) |
| **Protocol Translation** | None (OpenAI in $\to$ OpenAI out) | **Full bidirectional translation** (OpenAI Responses/Chat $\leftrightarrow$ Google `GenerateContentRequest`) |
| **Stream Synthesis** | Direct HTTP chunk forwarding | **Real-time SSE synthesis** (translating Google deltas to OpenAI stream events) |
| **Cryptographic Signatures** | None (Meta has no HMAC thought signatures) | **Stateful Cryptographic Engine** (authoritative turn accumulator, encrypted carriers, sibling tracking, provenance policy) |
| **Model Switching** | Stateless pass-through | **Provenance-aware migration** (`skip_thought_signature_validator` for foreign/unknown turns) |

---

## Core Engine Systems

### 1. Authoritative Turn Architecture (Record-First Pipeline)
- **Direct Upstream Ingestion:** Upstream streaming chunks are accumulated directly into an authoritative turn record ([`turn_record.go`](file:///Users/jeffhuen/.config/muse-bridge/agy-bridge-go/internal/protocols/turn_record.go)) before output projection.
- **Deterministic Stream & Final Parity:** Output items strictly follow the chronological `OutputOrder` established by upstream generation, eliminating race conditions or index mismatches between SSE events and final JSON response outputs.
- **Token Limit Integrity:** Generation reaching token limits during reasoning omits phantom empty messages that streaming never announced.

### 2. Cryptographic State Preservation
Google PredictionService requires cryptographic HMAC thought signatures on all model thoughts and tool calls in multi-turn history. Stateless OpenAI harnesses (which drop custom metadata) normally trigger `HTTP 400: Function call is missing a thought_signature` or `HTTP 400: Corrupted thought signature`.
- **Encrypted Carrier Embedding:** When serving `/v1/responses`, the bridge embeds turn state (parts, tool signatures, sibling sets, text signatures) into standard `reasoning.encrypted_content`. Adapters that preserve reasoning items (like Pi or OpenCode) automatically retain full state across turns even with an empty local cache.
- **Content-Validated Signature Cache:** [`signature_cache.go`](file:///Users/jeffhuen/.config/muse-bridge/agy-bridge-go/internal/upstream/signature_cache.go) records and persists tool signatures along with verified function names and arguments. Restorations require matching content, preventing ID-reuse or argument-tampering bypasses.
- **Parallel Sibling Reconciliation:** Multiple tool calls emitted in the same model turn are verified as legitimate siblings; unverified or injected tool calls cannot borrow signatures from parallel leads.

### 3. Provenance Contract & Mid-Session Model Switching
When using agent harnesses like OpenCode that switch models mid-session (e.g. `muse-spark-1.3` $\to$ `gemini-3.8-flash-high`), history contains foreign tool calls without Gemini signatures. The bridge enforces an explicit 5-row provenance policy ([`provenance.go`](file:///Users/jeffhuen/.config/muse-bridge/agy-bridge-go/internal/protocols/provenance.go)):

| Evidence | Handling |
| :--- | :--- |
| **Matching native record with sufficient state** | Replay original parts and authentic thought signatures. |
| **Known native record with mismatched content or missing state** | Reject clearly (`content mismatch against native turn record` or `unverified call in native turn`). Never bypass integrity failures. |
| **Affirmatively imported call with corresponding result** | Apply migration marker (`skip_thought_signature_validator`), including within current-turn tool chains. |
| **Unknown-origin call with corresponding result** | Allow migration recovery (`skip_thought_signature_validator`); retain classification as unknown; record recovery safely without logging signatures or arguments. |
| **Incomplete or invalid call/result pairing** | Reject clearly with actionable validation error (`unknown-origin call has no corresponding tool result`). |

---

## Features

- **Direct PredictionService Connection**: Connects directly to Google's internal `PredictionService` via HTTP/SSE with automatic OAuth token refresh—no nested CLI harnesses, zero subprocess overhead, and zero macOS TCC permission prompts.
- **Cryptographic Thought Signatures**: Full stateful multi-turn tool calling preserving Gemini 3 thought signatures across turns.
- **Native Gemini 3 Reasoning**: High-effort reasoning enabled by default (`thinkingLevel: "high"`, `includeThoughts: true`), streamed via standard `reasoning_content` deltas.
- **Codex Responses API**: Fully implements `wire_api = "responses"` on `/v1/responses` with Server-Sent Events (SSE).
- **OpenAI Chat Completions API**: Implements `/v1/chat/completions` (streaming chunked and non-streaming) for OpenCode, pi, Zed, etc.
- **Model Discovery**: Exposes `GET /v1/models` listing Gemini 3.8 Flash, 3.1 Pro, Claude Sonnet 4.6, Claude Opus 4.6, etc.
- **Instant Turn Latency**: Direct HTTP streaming begins within ~300ms.
- **Memory & Resource Efficient**: Pure Go standard library, single static ~6.7MB binary, ~12MB resident memory, 0% idle CPU.
- **Robust & Resilient**: Self-rotating 64KB log file (`agy-bridge-go.log`, `.1`, `.2`), concurrency bounding with semaphores, context-propagated cancellation.

## Quick Start

### Automated Install (Recommended)

Run the one-shot configurator to build the binary, register the LaunchAgent, and configure Codex CLI, OpenCode, and pi with high reasoning defaults:

```bash
cd agy-bridge-go
./install.sh --all
```

Or target specific tools:
```bash
./install.sh --codex
./install.sh --opencode
./install.sh --pi
```

### Manual Build & Run

```bash
cd agy-bridge-go
go test -v ./...
go build -o ../bin/agy-bridge-go ./cmd/agy-bridge-go
../bin/agy-bridge-go -port 8917
```

### Persistent Install (macOS LaunchAgent)

LaunchAgent template: `~/Library/LaunchAgents/com.jeffhuen.agy-bridge-go.plist`

```bash
launchctl load ~/Library/LaunchAgents/com.jeffhuen.agy-bridge-go.plist
```

Check status:
```bash
curl -s http://127.0.0.1:8917/healthz
tail -f ~/.config/muse-bridge/agy-bridge-go.log
```

Restart daemon:
```bash
launchctl kickstart -k gui/$(id -u)/com.jeffhuen.agy-bridge-go
```

## Reasoning Effort Handling

- **Default Effort**: `high` across Codex CLI, OpenCode, and pi. This is the highest reasoning effort available on AGY Flash 3.8 and Pro 3.1.
- **No "max" on AGY**: AGY models only provide `low`, `medium`, and `high` (Pro provides `low` and `high`). If a client requests `max` or `xhigh`, the bridge gracefully caps it to `high`.
- **Dynamic Remapping**: If a user switches to `low` or `medium` reasoning in any harness, the bridge dynamically remaps the request to `gemini-3.8-flash-low` or `gemini-3.8-flash-medium` behind the scenes.

## Tool Configurations

### 1. Codex CLI

Add or update `~/.codex/agy.config.toml`:

```toml
model = "gemini-3.8-flash-high"
model_provider = "agy"
model_reasoning_effort = "high"
model_context_window = 1048576
model_catalog_json = "/Users/jeffhuen/.codex/model-catalogs/agy.json"

features.multi_agent = true
tools.web_search = false
apps._default.enabled = false

[model_providers.agy]
name = "Antigravity"
base_url = "http://127.0.0.1:8917/v1"
experimental_bearer_token = "bridge"
wire_api = "responses"
```

Run Codex:
```bash
codex -p agy
```

### 2. OpenCode

In `~/.config/opencode/opencode.json`:

```json
"provider": {
  "agy-bridge-go": {
    "npm": "@ai-sdk/openai",
    "name": "Google Antigravity via AGY bridge (Go)",
    "options": {
      "baseURL": "http://127.0.0.1:8917/v1",
      "forceReasoning": true
    },
    "models": {
      "gemini-3.8-flash-high": {
        "name": "Gemini 3.8 Flash (High)",
        "limit": { "context": 1048576, "output": 256000 },
        "options": { "reasoningEffort": "high" },
        "variants": {
          "low": { "reasoningEffort": "low" },
          "medium": { "reasoningEffort": "medium" },
          "high": { "reasoningEffort": "high" }
        }
      },
      "gemini-3.1-pro-high": {
        "name": "Gemini 3.1 Pro (High)",
        "limit": { "context": 1048576, "output": 256000 },
        "options": { "reasoningEffort": "high" },
        "variants": {
          "low": { "reasoningEffort": "low" },
          "high": { "reasoningEffort": "high" }
        }
      },
      "claude-sonnet-4-6": {
        "name": "Claude Sonnet 4.6 (Thinking)",
        "limit": { "context": 200000, "output": 64000 },
        "options": { "reasoningEffort": "high" },
        "variants": {
          "low": { "reasoningEffort": "low" },
          "medium": { "reasoningEffort": "medium" },
          "high": { "reasoningEffort": "high" }
        }
      }
    }
  }
}
```

Connect in OpenCode: `/connect` -> `agy-bridge-go`.

### 3. pi

In `~/.pi/agent/models.json`:

```json
"agy-bridge-go": {
  "baseUrl": "http://127.0.0.1:8917/v1",
  "apiKey": "bridge",
  "api": "openai-responses",
  "models": [
    {
      "id": "gemini-3.8-flash-high",
      "name": "Gemini 3.8 Flash (High)",
      "reasoning": true,
      "input": ["text", "image"],
      "contextWindow": 1048576,
      "maxTokens": 256000,
      "thinkingLevelMap": {
        "off": null,
        "low": "low",
        "medium": "medium",
        "high": "high"
      }
    }
  ]
}
```

In `~/.pi/agent/settings.json`:
```json
{
  "modelThinkingLevels": {
    "agy-bridge-go/gemini-3.8-flash-high": "high"
  }
}
```

Run in pi: `/model` -> `agy-bridge-go/gemini-3.8-flash-high`.

---

## Verification & Testing

The test suite verifies offline unit stubs, multi-turn state preservation, SSE streaming / final output alignment, parallel sibling reconciliation, and the complete 5-row provenance policy contract:

```bash
# Run all tests with concurrency race detector
go test -race -count=1 ./...

# Static analysis (must be silent)
go vet ./...
```
