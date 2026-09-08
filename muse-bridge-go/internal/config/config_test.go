package config

import (
	"net/url"
	"testing"
)

// UpstreamBase must stay scheme+host: request paths already carry /v1,
// so a path here would double it (…/v1/v1/models) and 404 upstream.
func TestUpstreamBaseHasNoPath(t *testing.T) {
	u, err := url.Parse(UpstreamBase)
	if err != nil {
		t.Fatalf("UpstreamBase does not parse: %v", err)
	}
	if u.Scheme == "" || u.Host == "" {
		t.Fatalf("UpstreamBase needs scheme and host: %q", UpstreamBase)
	}
	if u.Path != "" && u.Path != "/" {
		t.Fatalf("UpstreamBase must not carry a path: %q", UpstreamBase)
	}
}
