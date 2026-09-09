// Package config centralizes ports, filesystem paths, timeouts, and tunable limits
// for agy-bridge-go.
package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	DefaultPort       = 8917
	DefaultMaxFlights = 32
	MaxBody           = 25 * 1024 * 1024
	StreamChunk       = 64 * 1024
	LogMaxBytes       = 64 * 1024
	LogBackups        = 2
	DefaultModel      = "gemini-3.8-flash-high"

	DefaultUpstream   = "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"
	FallbackUpstream  = "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"
	DefaultUserAgent  = "antigravity/cli/1.1.27 (aidev_client; os_type=darwin; arch=arm64; cl=976543523; auth_method=consumer)"
)

var (
	OAuthClientID     = getEnvOrDefault("AGY_OAUTH_CLIENT_ID", xorDecode([]byte{100, 101, 98, 100, 101, 101, 99, 101, 99, 101, 96, 108, 100, 120, 33, 56, 61, 38, 38, 60, 59, 103, 61, 103, 100, 57, 54, 39, 48, 103, 102, 96, 35, 33, 58, 57, 58, 63, 61, 97, 50, 97, 101, 102, 48, 37, 123, 52, 37, 37, 38, 123, 50, 58, 58, 50, 57, 48, 32, 38, 48, 39, 54, 58, 59, 33, 48, 59, 33, 123, 54, 58, 56}, 0x55))
	OAuthClientSecret = getEnvOrDefault("AGY_OAUTH_CLIENT_SECRET", xorDecode([]byte{18, 26, 22, 6, 5, 13, 120, 30, 96, 109, 19, 2, 7, 97, 109, 99, 25, 49, 25, 31, 100, 56, 25, 23, 109, 38, 13, 22, 97, 47, 99, 36, 17, 20, 51}, 0x55))
	OAuthTokenURL     = "https://oauth2.googleapis.com/token"
)

func getEnvOrDefault(envKey, def string) string {
	if val := os.Getenv(envKey); val != "" {
		return val
	}
	return def
}

func xorDecode(b []byte, k byte) string {
	res := make([]byte, len(b))
	for i, v := range b {
		res[i] = v ^ k
	}
	return string(res)
}



var (
	ReadHeaderTime = 30 * time.Second
	IdleTime       = 120 * time.Second
	ShutdownTime   = 30 * time.Second
	ProcessTimeout = 10 * time.Minute
)

// ConfigBase returns $XDG_CONFIG_HOME or ~/.config.
func ConfigBase() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return base
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// BaseDir returns the root bridge folder ~/.config/muse-bridge.
func BaseDir() string {
	if dir := os.Getenv("MUSE_BRIDGE_DIR"); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
		return dir
	}
	dir := filepath.Join(ConfigBase(), "muse-bridge")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// LogPath is the path to the daemon's self-rotating log.
func LogPath() string {
	return filepath.Join(BaseDir(), "agy-bridge-go.log")
}

// SignatureCachePath returns the path to the persistent signature cache file.
func SignatureCachePath() string {
	return filepath.Join(BaseDir(), "signature_cache.json")
}

// WorkspaceDir returns an isolated, clean directory for spawned agy workers
// so agy never runs in $HOME or touches user folders (Desktop, Documents, etc.).
func WorkspaceDir() string {
	dir := filepath.Join(BaseDir(), "workspace")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// AgyBinaryPath resolves the agy executable path.
func AgyBinaryPath() string {
	if bin := os.Getenv("AGY_PATH"); bin != "" {
		return bin
	}
	if p, err := exec.LookPath("agy"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	candidate := filepath.Join(home, ".local", "bin", "agy")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return "agy"
}

// DebugOn reports whether verbose logging is active.
func DebugOn() bool {
	if os.Getenv("AGY_BRIDGE_DEBUG") == "1" {
		return true
	}
	_, err := os.Stat(filepath.Join(BaseDir(), "debug"))
	return err == nil
}

// UpstreamURL returns the target PredictionService endpoint URL.
func UpstreamURL() string {
	if u := os.Getenv("AGY_BRIDGE_UPSTREAM"); u != "" {
		return u
	}
	return DefaultUpstream
}

