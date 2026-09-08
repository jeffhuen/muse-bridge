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
401 invalidate-and-retry-once, `/v1` path prefixing, 25MB body cap,
64-flight bound, `prompt_cache_retention: 24h`, reasoning-effort
stripping, error JSON passthrough, chunked streaming.

Divergences, all intentional:

- `GET /healthz` answers locally (`{"status":"ok"}`) without touching
  keys or network. bridge.py would forward the path upstream.
- Log lines carry timestamps (`log.LstdFlags | log.Lmicroseconds`).
- Upstream requests bind to the client connection's context: a harness
  disconnect cancels the in-flight upstream fetch. Python lets it run.
- No `WriteTimeout` on the server: it is a wall clock over the whole
  handler and would kill legitimate long streams. Header read and idle
  timeouts are set instead.
