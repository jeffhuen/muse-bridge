# muse-bridge

Local OAuth bridge: use your Meta (Muse) login from any coding harness —
OpenCode, pi, curl, SDKs — without pasting static `LLM|...` keys around.

It speaks the OpenAI **Responses** API on localhost and forwards everything
to the Meta Model API with a daily-minted key.

## Why this exists

Meta has two credential worlds that look identical but bill differently:

- **Manually created API keys** (`dev.meta.ai` → Create API key) are
  **pay-as-you-go**. The dashboard table does not label this per row —
  the `Name` column is just your own label.
- The **Muse Code subscription** (flat monthly) is bound to the credential
  auto-connected during `muse` onboarding. That credential is OAuth, and
  the tokens live in the macOS **Keychain** — `~/.config/muse/auth.json`
  only holds metadata (`mechanism: oauth`, `storage: keychain`,
  `api_base_url`, your name/email). There is nothing to copy out of it.

`pi-muse-spark`-style extensions only accept static PAYG keys, and
OpenCode custom providers cannot run an OAuth device flow. So this bridge
runs the device flow **once**, stores its own identity, and mints Model
API keys on demand for any local client.

## Architecture / routing

```
┌──────────────┐   POST /v1/responses    ┌──────────────┐  POST /v1/responses  ┌───────────────┐
│   harness    │ ────────────────────── │  muse-bridge │ ─────────────────── │ api.meta.ai   │
│ opencode/pi/ │  http://127.0.0.1:8915 │  (this repo) │  https://api.meta.ai │  Meta Model   │
│ curl / SDK   │  /v1/*  (any client    │              │  /v1/*  + Bearer     │  API          │
└──────────────┘   auth, ignored)       └──────┬───────┘  minted key          └───────────────┘
                                               │  daily: POST /muse-code/key
                                               │  login: auth.meta.com OIDC device flow
```

Per request the bridge:

1. Resolves a key: cached minted key (< 20h old) → else mint via stored
   identity → else static `LLM_...` from env (`MUSE_BRIDGE_KEY`,
   `META_API_KEY`, `MODEL_API_KEY`) or `muse/auth.json` → else `503`
   telling you to run `bridge.py login`.
2. Rewrites the request: strips any client `Authorization`, sets
   `Bearer <key>`, preserves path/query/method/body.
3. For `/v1/responses` JSON payloads: sets `prompt_cache_retention: "24h"`
   when absent (Muse prompt cache is ~0% without it) and drops
   `reasoning.effort: none` (Meta 400s on it).
4. Streams the upstream response back chunk-by-chunk (SSE-safe).
5. On upstream `401` it drops the cached key so the next request re-mints.
   Other errors (e.g. `402 billing_not_configured`) pass through untouched —
   they are account-side, not bridge bugs.

Login (one time, `python3 bridge.py login`):

1. `POST https://auth.meta.com/oidc/device/authorization/`
   (`client_id 1031625952748946`, same client pi-meta-oauth uses).
2. Prints verification URL + user code; polls
   `.../oidc/device/token/` handling `authorization_pending` / `slow_down` /
   `access_denied` / `expired_token`.
3. Mints via `POST https://api.meta.ai/muse-code/key`
   (`x-api-version: 1.0.0`) to prove the identity works.
4. Stores `identity.json` (`0600`). The identity is long-lived; API keys
   derived from it rotate roughly daily.

## Files

| File           | What                         | Committed? |
|----------------|------------------------------|------------|
| `bridge.py`    | Proxy + login, stdlib only   | yes        |
| `README.md`    | This doc                     | yes        |
| `.gitignore`   | Keeps secrets out of git     | yes        |
| `identity.json`| OAuth identity (`0600`)      | **never**  |
| `bridge.log`   | Daemon log (no secrets)      | never      |
| `login.log`    | Last login transcript        | never      |

## Use with any harness

Anything that lets you set a custom `baseURL` works, because the bridge
is just an OpenAI Responses endpoint.

**OpenCode** (`~/.config/opencode/opencode.json`):

```json
"provider": {
  "meta-bridge": {
    "npm": "@ai-sdk/openai",
    "name": "Meta via Muse bridge",
    "options": { "baseURL": "http://127.0.0.1:8915/v1" },
    "models": {
      "muse-spark-1.3": { "name": "Muse Spark 1.3",
        "limit": { "context": 1048576, "output": 256000 } }
    }
  }
}
```

Then `/connect` → `meta-bridge` → enter anything (the bridge sets real
auth itself), `/models` → `meta-bridge/muse-spark-1.3`.

**pi** (`~/.pi/agent/models.json`):

```json
{ "providers": { "meta-bridge": {
  "baseUrl": "http://127.0.0.1:8915/v1",
  "apiKey": "bridge",
  "api": "openai-responses",
  "models": [{ "id": "muse-spark-1.3", "name": "Muse Spark 1.3",
    "reasoning": true, "input": ["text", "image"],
    "contextWindow": 1048576, "maxTokens": 256000 }]
}}}
```

(`apiKey` value is ignored by the bridge; pi just requires the field.)

**curl / SDKs**: point `base_url` at `http://127.0.0.1:8915/v1`,
any dummy key. `GET /v1/models` lists `muse-spark-1.3`,
`1.3-contributor`, `1.2*`, `1.1`.

## Run / maintain

- macOS persistence: LaunchAgent `com.jeffhuen.muse-bridge`
  (`~/Library/LaunchAgents/`), `RunAtLoad` + `KeepAlive`.
- Logs: `tail -f ~/.config/muse-bridge/bridge.log`
  (resolutions/mints only, keys never printed).
- Re-login when minting fails: `python3 ~/.config/muse-bridge/bridge.py login`.
- Running `muse` occasionally is **not** needed — the bridge refreshes
  independently. Separate OAuth sessions, same Meta account.

## Security notes

- Binds `127.0.0.1` only. Never expose the port.
- `identity.json` is as sensitive as a password: `0600`, gitignored.
  Never paste it anywhere.
- Contributor models (`*-contributor`, ~10x cheaper) let Meta train on
  prompts/completions. Use standard `muse-spark-1.3` for private code.

## Limitations

- Depends on undocumented Meta endpoints + a pinned `client_id`. If Meta
  rotates them, `login` breaks (static PAYG keys keep working) — the fix
  is to mirror whatever `pi-meta-oauth` (MIT) updates to.
- Key selects billing pool server-side: requests minted here bill to
  whatever the identity is entitled to, PAYG keys bill PAYG. There is no
  client-side "subscription first" ordering.
