// Package config centralizes every endpoint, filesystem path, and tunable
// limit so behavior lives in one place and nothing is Magic-stringed
// across packages.
package config

import (
	"os"
	"path/filepath"
	"time"
)

const (
	// Scheme + host only: request paths already carry the /v1 prefix
	// (bridge.py's UPSTREAM const is descriptive; its requests likewise
	// go to host + /v1/... path).
	UpstreamBase   = "https://api.meta.ai"
	DeviceAuthURL  = "https://auth.meta.com/oidc/device/authorization/"
	DeviceTokenURL = "https://auth.meta.com/oidc/device/token/"
	ClientID       = "1031625952748946"
	DeviceGrant    = "urn:ietf:params:oauth:grant-type:device_code"

	DefaultPort       = 8915
	DefaultMaxFlights = 64
	MaxBody           = 25 * 1024 * 1024
	MaxErrorBody      = 1024*1024 + 1
	StreamChunk       = 64 * 1024
	// Log rotation matches muse-bridge-py: 64KB active file plus two
	// numbered backups (bridge-go.log.1, .2).
	LogMaxBytes = 64 * 1024
	LogBackups  = 2
)

var (
	// Durations are vars so long-lived processes could reload them;
	// nothing today mutates them outside tests.
	KeyTTL = 20 * time.Hour
	// UpstreamHeaderTimeout bounds time-to-first-byte only. Stream
	// bodies are unbounded by design: a wall clock there would kill
	// legitimate long generations.
	UpstreamHeaderTimeout = 300 * time.Second
	AuthTimeout           = 30 * time.Second
	SemaphoreWait         = 60 * time.Second
	ReadHeaderTime        = 30 * time.Second
	IdleTime              = 120 * time.Second
	ShutdownTime          = 30 * time.Second
	// IdleConnTTL caps pooled upstream keep-alive reuse, matching
	// bridge.py's pool.
	IdleConnTTL = 60 * time.Second
)

// ConfigBase returns $XDG_CONFIG_HOME or ~/.config.
func ConfigBase() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return base
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// BaseDir is the bridge home; it is created mode 0700 on demand.
// MUSE_BRIDGE_DIR overrides the location outright (portability escape
// hatch for machines with unusual layouts). Mode bits are best-effort on
// Windows, where Go cannot express ACLs; see the README.
func BaseDir() string {
	if dir := os.Getenv("MUSE_BRIDGE_DIR"); dir != "" {
		os.MkdirAll(dir, 0o700)
		return dir
	}
	dir := filepath.Join(ConfigBase(), "muse-bridge")
	os.MkdirAll(dir, 0o700)
	return dir
}

// IdentityPath holds the Meta login (mode 0600).
func IdentityPath() string { return filepath.Join(BaseDir(), "identity.json") }

// MuseAuthPath is the Muse app's own token file, scanned only as a
// fallback source of static keys.
func MuseAuthPath() string { return filepath.Join(ConfigBase(), "muse", "auth.json") }

// DebugOn reports whether verbose request logging is enabled.
func DebugOn() bool {
	_, err := os.Stat(filepath.Join(BaseDir(), "debug"))
	return err == nil
}
