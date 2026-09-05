# muse-bridge

Use your Meta login in any coding tool. The bridge runs on your own
machine. It gives your tools a fresh API key when they need one. You
log in one time. Your tools then just work.

## Terms used in this file

- **Bridge**: the small server in this folder. It listens only on your
  own machine at `http://127.0.0.1:8915`.
- **Harness**: any coding tool you point at the bridge. OpenCode and pi
  are harnesses. A plain script is also a harness.
- **Identity**: your Meta login, stored as a file. The bridge uses it
  to mint keys. It is not a key itself.
- **Key**: a short-lived API key. The bridge mints a new one about
  one time per day. Tools send it with each request.

## What you need before you start

- A Mac with Python 3. No extra packages are required.
- A Meta developer account with access to the Model API.
- One of these tools: OpenCode, pi, or any tool that accepts a custom
  server address (`baseURL`).

## Where the files live

| File            | Purpose                                              |
|-----------------|------------------------------------------------------|
| `bridge.py`     | The bridge server and the login command.             |
| `identity.json` | Your login. Created by the login step. Keep secret. |
| `bridge.log`    | Log of key mints. Contains no secrets.              |
| `login.log`     | Record of the last login.                           |
| `README.md`     | This file.                                          |

The folder is a git repo. Only `bridge.py`, `README.md`, and
`.gitignore` are tracked. `identity.json` and all logs stay out of
git. Do not force-add them.

## Install the bridge

Run each step in order.

1. Check Python:
   ```bash
   python3 --version
   ```
   You need Python 3.8 or later.

2. Keep this folder where it is:
   `~/.config/muse-bridge`. The login step and the auto-start entry
   both expect that path. If you move the folder, read
   “Move the folder” below first.

3. Start the bridge by hand to test it:
   ```bash
   nohup python3 ~/.config/muse-bridge/bridge.py > ~/.config/muse-bridge/bridge.log 2>&1 &
   curl -s http://127.0.0.1:8915/v1/models | head -c 300; echo
   ```
   Expected result: a 503 error that tells you to run login. That
   means the server runs but has no login yet. This is correct.

4. Keep it running after reboot. The Mac entry for this is a
   LaunchAgent. The file is:
   `~/Library/LaunchAgents/com.jeffhuen.muse-bridge.plist`.
   It starts the bridge at login and restarts it if it stops.
   Load it one time:
   ```bash
   launchctl load ~/Library/LaunchAgents/com.jeffhuen.muse-bridge.plist
   ```

## Get the OAuth login

You do this one time. The login stays valid. The bridge mints fresh
keys from it on its own.

1. Start the login:
   ```bash
   python3 ~/.config/muse-bridge/bridge.py login
   ```
2. The command prints a web address and a user code. Open the address
   in your browser. The page shows a box for the code. Type the code
   from your terminal. Approve the request on the Meta page. Return
   to your terminal and wait. A full run looks like this
   (your code will differ):
   ```text
   Approve in browser: https://auth.meta.com/oauth/device/?code=XXXX-XXXX
   User code: XXXX-XXXX (expires in 600s)
   identity stored (0600). Mint works.
   ```
   The middle line means the terminal is waiting for you. Nothing
   proceeds until you approve in the browser.
3. Wait for this line: `identity stored (0600). Mint works.`
   Expected result: the file `identity.json` now exists and only you
   can read it. Check:
   ```bash
   ls -l ~/.config/muse-bridge/identity.json
   ```
4. Confirm the bridge serves models (this call is free):
   ```bash
   curl -s http://127.0.0.1:8915/v1/models
   ```
   Expected result: a list that contains `muse-spark-1.3`.

If approval takes too long, the code expires after about 10 minutes.
Run the login command again and approve faster.

## Use the bridge with OpenCode

1. Open `~/.config/opencode/opencode.json`. Add this provider block:
   ```json
   "provider": {
     "meta-bridge": {
       "npm": "@ai-sdk/openai",
       "name": "Meta via Muse bridge",
       "options": { "baseURL": "http://127.0.0.1:8915/v1" },
       "models": {
         "muse-spark-1.3": {
           "name": "Muse Spark 1.3",
           "limit": { "context": 1048576, "output": 256000 },
           "variants": {
             "minimal": { "reasoningEffort": "minimal" },
             "low": { "reasoningEffort": "low" },
             "medium": { "reasoningEffort": "medium" },
             "high": { "reasoningEffort": "high" },
             "xhigh": { "reasoningEffort": "xhigh" },
             "max": { "reasoningEffort": "max" }
           }
         }
       }
     }
   }
   ```
2. In OpenCode, run `/connect`. Select `meta-bridge`. Type any dummy
   value, for example `bridge`. The bridge sets the real key itself.
   Your dummy value is ignored.
3. Run `/models`. Select `meta-bridge/muse-spark-1.3`.
4. Send a test message. Expected result: a normal answer.

## Use the bridge with pi

1. Open `~/.pi/agent/models.json`. Add this provider:
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
   The `apiKey` value can be any text. Pi requires the field. The
   bridge replaces it with the real key.
2. Select the model with `/model` and send a test message.

## Use the bridge with anything else

Any tool that accepts a custom OpenAI-style server works. Set its
server address to `http://127.0.0.1:8915/v1` and its key to any dummy
text. Example with curl (replace `MODEL` as needed):
```bash
curl -s http://127.0.0.1:8915/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"muse-spark-1.3","input":"Reply exactly: META_OK"}'
```
Note: this call spends a small number of tokens. The `/models` call
above is free. Use `/models` for connection checks.

## Maintain the bridge

- **Read the log.** Run `tail -f ~/.config/muse-bridge/bridge.log`.
  Normal lines say `minted key via identity.json`. No line ever
  contains a key.
- **Restart it.** Run:
  ```bash
  launchctl kickstart -k gui/$(id -u)/com.jeffhuen.muse-bridge
  ```
- **Stop it.** Run:
  ```bash
  launchctl unload ~/Library/LaunchAgents/com.jeffhuen.muse-bridge.plist
  ```
- **Log in again.** If requests fail with a mint error, the login has
  died. Run `python3 ~/.config/muse-bridge/bridge.py login` again.
  You do not need to touch your tools.
- **Update it.** Pull or copy the new `bridge.py`, then restart with
  the kickstart command above.
- **Move the folder.** If you move it, fix two paths: the
  `ProgramArguments` entry in the LaunchAgent file and any tool
  config that names the folder. Then unload and reload the agent.

## Fix common problems

| Sign | Cause | Action |
|------|-------|--------|
| `503 no usable credential; run login` | No login stored yet, or it died | Run the login step again |
| `402 billing_not_configured` | The Meta account has no valid billing | Fix billing at `dev.meta.ai`, then retry |
| `401` from upstream, then recovery | The minted key expired early | No action. The bridge drops it and mints a new one on the next request |
| Tool cannot reach `127.0.0.1:8915` | The bridge is not running | Check the log, then kickstart |
| Blank `401 Authentication Error` at login mint | The approval did not grant API access | Check account access, then log in again |

## Security rules

- The bridge listens on `127.0.0.1` only. Do not change this to a
  public address.
- Treat `identity.json` like a password. Never paste it, mail it, or
  commit it.
- `bridge.log` never holds keys. You can share it when you ask for help.
- Your `muse` tokens live in the macOS Keychain. This bridge keeps its
  own separate login, so `muse` and the bridge do not disturb each
  other.

## How it works, in short

Each request passes through three steps. The bridge picks a key: it
reuses a minted key younger than 20 hours, else mints one from the
stored login, else falls back to a static `LLM_...` key from the
environment. It then forwards your request to
`https://api.meta.ai/v1`, swaps in the real key, and adds
`prompt_cache_retention: 24h` to Responses calls so prompt caching
works. It streams the answer back as it arrives.

## Credits and inspiration

- [pi-meta-oauth](https://github.com/BlockedPath/pi-meta-oauth) (MIT)
  by blockedredemption. It proved the flow this bridge copies: device
  auth at `auth.meta.com`, key mint at `/muse-code/key`, Responses-only
  traffic with `prompt_cache_retention: 24h`. Like this bridge, it uses
  your own login and does not pose as the Muse Code app. No code was
  copied. The Python here is a fresh take of the same three calls.
- [pi-muse-spark](https://github.com/EclipseAditya/pi-muse-spark) (MIT)
  for the static-key model table the fallback path still accepts.

## Limits you must know

- The bridge relies on Meta login pages that Meta does not document.
  If Meta changes them, login breaks until `bridge.py` is updated.
  Static pay-as-you-go keys still work in that case.
- The server decides billing from the key you send. Keys you create by
  hand on the dashboard bill pay-as-you-go. Keys the bridge mints bill
  to what your login is entitled to. The bridge cannot pick
  “subscription first”. The key picks the pool.
- Cheap `*-contributor` models let Meta train on your prompts. Use
  standard `muse-spark-1.3` for private code.
