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

- A Mac or Linux machine with Python 3 (3.8+). No extra packages
  are required. On WSL2, run everything inside the Linux distro.
- A Meta developer account with access to the Model API.
- One of these tools: OpenCode, pi, or any tool that accepts a custom
  server address (`baseURL`).

## Linux and WSL notes

- The bridge itself (`bridge.py`) is portable: Python standard parts
  only, no Mac-only calls.
- Persistence differs by system, and the installer picks for you. Mac
  uses a LaunchAgent. Linux with systemd uses a user service (check
  it with `systemctl --user status muse-bridge`). Plain WSL without
  systemd falls back to a manual background start that does not
  survive reboot.
- The installer opens the login page with `open` (Mac), `xdg-open`
  (Linux), or your Windows browser (WSL). If none works, it prints
  the address for you to open by hand.
- On WSL2, `localhost` is shared with Windows, so a Windows-side tool
  can also reach `http://127.0.0.1:8915`. Keep the bridge inside the
  distro that holds your login.
- Maintain commands differ: Mac uses the `launchctl` lines under
  Maintain; on systemd Linux use
  `systemctl --user restart|stop|status muse-bridge`.

## Where the files live

| File            | Purpose                                              |
|-----------------|------------------------------------------------------|
| `bridge.py`     | The bridge server and the login command.             |
| `install.sh`    | One-shot setup. Daemon, login, harness wiring.      |
| `identity.json` | Your login. Created by the login step. Keep secret. |
| `bridge.log`    | Log of key mints. Contains no secrets.              |
| `login.log`     | Record of the last login.                           |
| `README.md`     | This file.                                          |

The folder is a git repo. Only `bridge.py`, `install.sh`,
`README.md`, and `.gitignore` are tracked. `identity.json` and all
logs stay out of git. Do not force-add them.

---

## A. Simple installation (recommended)

Run two commands. Total time is about five minutes. Most of that is
the browser approval, which needs you. Everything else is automatic.

```bash
git clone https://github.com/jeffhuen/muse-bridge.git ~/.config/muse-bridge
~/.config/muse-bridge/install.sh
```

With no flags the script asks which harness to wire: OpenCode, pi, or
both. Use `--opencode` and/or `--pi` to skip the question.

What the script does, in order, and what you will see:

1. **Files (~10 seconds).** It copies the bridge into place. No
   output means success.
2. **Daemon (~10 seconds).** It registers the Mac auto-start entry
   and starts the bridge. You see `Starting daemon`.
3. **Login check (~5 seconds).** It asks the bridge for the model
   list. This call is free. If your login already works, it prints
   `Login OK` and skips to step 5.
4. **Login, only if step 3 fails (~2 minutes, needs you).** Your
   browser opens at the Meta approval page. Your terminal shows a
   user code. Type the code in the browser and approve. Return to
   the terminal and wait for `identity stored (0600). Mint works.`
   The code expires after about 10 minutes. If it expires, run the
   script again and approve faster.
5. **Harness wiring (~5 seconds).** It adds the bridge entry to your
   tool config. It backs up each file it touches first. You see
   `updated <path> (backup kept)`.
6. **Two in-tool steps (needs you, one minute).** The script cannot
   click inside your tools, so finish there:
   - OpenCode: `/connect` → `meta-bridge` → type any dummy text,
     then `/models` → `meta-bridge/muse-spark-1.3`.
   - pi: `/model` → `meta-bridge/muse-spark-1.3`.

The script is safe to run again at any time. It skips steps that are
already done and never duplicates config entries.

---

## B. Manual installation

Do this only if the script does not fit your setup. Each part says
what it is for. Do the parts in order.

### B.1. Place the files and start the daemon

1. Check Python:
   ```bash
   python3 --version
   ```
   You need Python 3.8 or later.
2. Keep this folder where it is:
   `~/.config/muse-bridge`. The login step and the auto-start entry
   both expect that path. If you move the folder, read “Move the
   folder” under Maintain first.
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

### B.2. Get the OAuth login

You do this one time. The login stays valid. The bridge mints fresh
keys from it on its own. Allow two minutes, most of it waiting on
the browser.

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

### B.3. Wire up OpenCode

1. Open `~/.config/opencode/opencode.json`. Add this provider block:
   ```json
   "provider": {
     "meta-bridge": {
       "npm": "@ai-sdk/openai",
       "name": "Meta via Muse bridge",
       "options": { "baseURL": "http://127.0.0.1:8915/v1",
         "forceReasoning": true },
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
             "max": { "reasoningEffort": "xhigh" }
           }
         }
       }
     }
   }
   ```
   The variants expose effort levels to OpenCode's switcher. There is
   deliberately no `off` variant: Meta rejects it.
2. In OpenCode, run `/connect`. Select `meta-bridge`. Type any dummy
   value, for example `bridge`. The bridge sets the real key itself.
   Your dummy value is ignored.
3. Run `/models`. Select `meta-bridge/muse-spark-1.3`.
4. Send a test message. Expected result: a normal answer.

### B.4. Wire up pi

1. Open `~/.pi/agent/models.json`. Add this provider:
   ```json
   { "providers": { "meta-bridge": {
     "baseUrl": "http://127.0.0.1:8915/v1",
     "apiKey": "bridge",
     "api": "openai-responses",
     "models": [{ "id": "muse-spark-1.3", "name": "Muse Spark 1.3",
       "reasoning": true, "input": ["text", "image"],
       "contextWindow": 1048576, "maxTokens": 256000,
       "thinkingLevelMap": { "off": null, "minimal": "minimal",
         "low": "low", "medium": "medium", "high": "high",
         "xhigh": "xhigh", "max": "max" } }]
   }}}
   ```
   The `apiKey` value can be any text. Pi requires the field. The
   bridge replaces it with the real key. `thinkingLevelMap` exposes
   pi's thinking levels to the picker. `null` hides a level: `off`
   is hidden because Meta rejects it.
2. Select the model with `/model` and send a test message.
3. Pick the thinking level in pi as usual. The bridge forwards the
   level to Meta untouched.

### B.5. Wire up anything else

Any tool that accepts a custom OpenAI-style server works. Set its
server address to `http://127.0.0.1:8915/v1` and its key to any dummy
text. Example with curl:
```bash
curl -s http://127.0.0.1:8915/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"muse-spark-1.3","input":"Reply exactly: META_OK"}'
```
Note: this call spends a small number of tokens. The `/models` call
above is free. Use `/models` for connection checks.

---

## Maintain the bridge

- **Read the log.** Run `tail -f ~/.config/muse-bridge/bridge.log`.
  Normal lines say `minted key via identity.json`. Upstream failures
  show as `upstream <method> <path> -> <status>`. No line ever
  contains a key.
- **Restart it.** Run:
  ```bash
  launchctl kickstart -k gui/$(id -u)/com.jeffhuen.muse-bridge
  ```
- **Stop it (keeps everything).** Run:
  ```bash
  launchctl unload ~/Library/LaunchAgents/com.jeffhuen.muse-bridge.plist
  ```
  Start again with the kickstart command above.
- **Uninstall it.** Run:
  ```bash
  ~/.config/muse-bridge/install.sh --uninstall
  ```
  This stops the daemon and removes auto-start. Your login and logs
  stay put, so reinstalling later just works. Add `--purge` to delete
  the whole folder including the login. Either way, delete the
  `meta-bridge` block from your tool configs by hand if you no
  longer want the entries.
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
| `upstream ... -> 429` in the log | The account rate limit is spent (often by a second tool on the same account) | Ease off the other tool, wait, retry |
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
environment. Upstream connections are pooled and reused across
requests. One lock guards minting so concurrent requests never mint
twice. It then forwards your request to `https://api.meta.ai/v1`,
swaps in the real key, and adds `prompt_cache_retention: 24h` to
Responses calls so prompt caching works. It streams the answer back
as it arrives.

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
