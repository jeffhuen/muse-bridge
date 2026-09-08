# muse-bridge-go

Go port of the muse-bridge Python daemon. Same behavior, same files
(`identity.json`, logs, `debug` flag): either daemon can serve the same
bridge home. One static binary per platform, standard library only.

## Layout

| Path | Purpose |
|------|---------|
| `cmd/muse-bridge-go/` | Entry point: flags, wiring, graceful shutdown. No logic. |
| `internal/config/` | Endpoints, paths, limits. One place for every tunable. |
| `internal/auth/` | Meta OAuth device flow, key mint, credential loading. No caching. |
| `internal/keys/` | Cached key store behind a `Provider` interface. |
| `internal/rewrite/` | Pure `/v1/responses` payload munging (input bytes in, output bytes out). |
| `internal/proxy/` | The reverse-proxy handler. Keys and upstream are injected. |
| `package.sh` | Cross-compiles stripped static binaries for all six targets into `dist/`. |

Rules: `internal/` packages never import `cmd/`; `proxy` depends on the
`keys.Provider` interface, not the concrete store; endpoint URLs that
tests must redirect (`auth.MintURL`) are vars, everything else is const.

## Build, test, run

```bash
go test ./...              # offline unit tests (httptest stubs, no network)
go test -race ./...        # same with the race detector
go vet ./... && gofmt -l . # must be silent
go build -o /tmp/muse-bridge-go ./cmd/muse-bridge-go
/tmp/muse-bridge-go -port 8916   # side-by-side with the Python daemon
./package.sh v1.2.3        # dist/ binaries for all platforms, version-stamped
```

`login` runs the same device flow as `bridge.py login` and writes the
same `identity.json`, so logging in from either daemon authorizes both.

## Persistent install (side-by-side with the Python daemon)

The Go daemon runs on port 8916 with its own log, leaving the Python
daemon on 8915 untouched. Both share `identity.json` (read-only) and
mint their own keys.

1. Build into the bridge home, stamped with the commit:

   ```bash
   mkdir -p ~/.config/muse-bridge/bin && cd ~/.config/muse-bridge/muse-bridge-go
   go build -trimpath -ldflags "-s -w -X main.version=$(git rev-parse --short HEAD)" \
     -o ../bin/muse-bridge-go ./cmd/muse-bridge-go
   ```

2. Persist it. macOS (`~/Library/LaunchAgents/com.jeffhuen.muse-bridge-go.plist`).
   Paths inside must be absolute and literal (launchd does not expand
   `~`); substitute your own home directory below:

   ```xml
   <dict>
       <key>Label</key>
       <string>com.jeffhuen.muse-bridge-go</string>
       <key>ProgramArguments</key>
       <array>
           <string>/Users/jeffhuen/.config/muse-bridge/bin/muse-bridge-go</string>
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
       <string>/Users/jeffhuen/.config/muse-bridge/bridge-go.log</string>
       <key>StandardErrorPath</key>
       <string>/Users/jeffhuen/.config/muse-bridge/bridge-go.log</string>
   </dict>
   ```

   ```bash
   launchctl load ~/Library/LaunchAgents/com.jeffhuen.muse-bridge-go.plist
   ```

   Linux (`~/.config/systemd/user/muse-bridge-go.service`):

   ```ini
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
   ```

   ```bash
   systemctl --user daemon-reload
   systemctl --user enable --now muse-bridge-go.service
   ```

3. Verify both daemons (outputs must match byte-for-byte):

   ```bash
   curl -s http://127.0.0.1:8916/healthz
   curl -s http://127.0.0.1:8916/v1/models -o /tmp/go.json
   curl -s http://127.0.0.1:8915/v1/models -o /tmp/py.json
   cmp /tmp/go.json /tmp/py.json && echo PARITY_OK
   ```

4. Wire the harnesses. In each config, duplicate the `meta-bridge`
   block as `meta-bridge-go` with exactly three changes: the provider
   key, ` (Go)` appended to the display name, and port `8915` →
   `8916`. Everything else (reasoning variants, limits,
   `forceReasoning`, `thinkingLevelMap`) stays identical. Then:
   - OpenCode: `/connect` → `meta-bridge-go` → any dummy text (stored
     per provider id; the bridge ignores it), then `/models` to select.
   - pi: `/model` → `meta-bridge-go/muse-spark-1.3`.

Maintain:

```bash
tail -f ~/.config/muse-bridge/bridge-go.log   # its log
# restart (macOS):
launchctl kickstart -k gui/$(id -u)/com.jeffhuen.muse-bridge-go
# after pulling new Go code: repeat step 1, then restart.
```

## Platform matrix

`package.sh` builds (all verified): `darwin/amd64`, `darwin/arm64`,
`linux/amd64`, `linux/arm64`, `windows/amd64`, `windows/arm64`.
`CGO_ENABLED=0` keeps each binary dependency-free, including libc.

Portability notes:

- Paths mirror `bridge.py` exactly (`$XDG_CONFIG_HOME` or `~/.config`,
  via `os.UserHomeDir`), so both daemons share one bridge home on every
  OS. `MUSE_BRIDGE_DIR` overrides the location outright.
- Signal handling (`os.Interrupt` + `SIGTERM`) compiles on all six
  targets; graceful shutdown degrades to plain exit where a signal
  never arrives (Windows services).
- Only TCP sockets are used — no Unix sockets, no `flock`, no syslog.
- Windows file modes are best-effort: Go cannot express ACLs, so
  `identity.json` relies on the user's profile directory being private.
  Persistence on Windows is Task Scheduler or NSSM (no service wrapper
  is bundled yet).

## Parity with bridge.py (and deliberate divergences)

Ported one-to-one: device login, mint + 20h cache, direct-key fallback,
conditional 401 invalidate-and-retry-once, `/v1` path prefixing with
bare-path rewriting, 25MB body cap, 64-flight bound,
`prompt_cache_retention: 24h`, reasoning-effort stripping, error JSON
passthrough with snippets, `Retry-After` forwarding, chunked streaming.

Divergences, all intentional:

- `GET /healthz` answers locally (`{"status":"ok"}`) without touching
  keys or network. bridge.py would forward the path upstream.
- Log lines carry timestamps (`log.LstdFlags | log.Lmicroseconds`).
- Upstream requests bind to the client connection's context: a harness
  disconnect cancels the in-flight upstream fetch. Python lets it run.
- No wall-clock timeout on streams (client context governs); only
  time-to-first-byte is bounded. Python's socket timeout is per-read,
  which behaves the same way.
- No `WriteTimeout` on the server: it is a wall clock over the whole
  handler and would kill legitimate long streams. Header read and idle
  timeouts are set instead.
