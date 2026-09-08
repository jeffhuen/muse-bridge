package keys

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/auth"
)

// isolateEnv redirects every credential file lookup at an empty temp dir
// and clears static-key env vars, so tests never touch the real home.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MUSE_BRIDGE_DIR", "")
	t.Setenv("MUSE_BRIDGE_IDENTITY", "")
	t.Setenv("MUSE_BRIDGE_KEY", "")
	t.Setenv("META_API_KEY", "")
	t.Setenv("MODEL_API_KEY", "")
}

// mintStub redirects auth.MintURL at a local stub. The stub replies with
// status and body on each hit and counts hits.
func mintStub(t *testing.T, status int, body string) (*http.Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") == "" {
			t.Error("mint request missing Authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	old := auth.MintURL
	auth.MintURL = srv.URL
	t.Cleanup(func() { auth.MintURL = old })
	return srv.Client(), &hits
}

func TestCurrentKeyMintsAndCaches(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MUSE_BRIDGE_IDENTITY", "test-identity-long-enough-to-count")
	client, hits := mintStub(t, 200, `{"api_key":"LLM_one"}`)

	s := NewStore(client, time.Hour)
	first, err := s.CurrentKey()
	if err != nil {
		t.Fatalf("CurrentKey: %v", err)
	}
	second, err := s.CurrentKey()
	if err != nil {
		t.Fatalf("CurrentKey again: %v", err)
	}
	if first != "LLM_one" || second != "LLM_one" {
		t.Fatalf("keys = %q, %q", first, second)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("mint hits = %d, want 1 (second call must use cache)", n)
	}
}

func TestCurrentKeyRefetchesAfterTTL(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MUSE_BRIDGE_IDENTITY", "test-identity-long-enough-to-count")
	client, hits := mintStub(t, 200, `{"api_key":"LLM_one"}`)

	s := NewStore(client, 20*time.Millisecond)
	if _, err := s.CurrentKey(); err != nil {
		t.Fatalf("CurrentKey: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := s.CurrentKey(); err != nil {
		t.Fatalf("CurrentKey after TTL: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("mint hits = %d, want 2", n)
	}
}

func TestInvalidateForcesRefetch(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MUSE_BRIDGE_IDENTITY", "test-identity-long-enough-to-count")
	client, hits := mintStub(t, 200, `{"api_key":"LLM_one"}`)

	s := NewStore(client, time.Hour)
	if _, err := s.CurrentKey(); err != nil {
		t.Fatalf("CurrentKey: %v", err)
	}
	s.Invalidate("LLM_one")
	if _, err := s.CurrentKey(); err != nil {
		t.Fatalf("CurrentKey after invalidate: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("mint hits = %d, want 2", n)
	}
}

func TestInvalidateIgnoresStaleKey(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MUSE_BRIDGE_IDENTITY", "test-identity-long-enough-to-count")
	client, hits := mintStub(t, 200, `{"api_key":"LLM_one"}`)

	s := NewStore(client, time.Hour)
	key, err := s.CurrentKey()
	if err != nil {
		t.Fatalf("CurrentKey: %v", err)
	}
	// A 401 for an already-rotated key must not evict the good one.
	s.Invalidate("LLM_stale")
	if got, err := s.CurrentKey(); err != nil || got != key {
		t.Fatalf("key=%q err=%v after stale invalidate", got, err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("mint hits = %d, want 1 (no re-mint)", n)
	}
	s.Invalidate(key)
	if _, err := s.CurrentKey(); err != nil {
		t.Fatalf("CurrentKey after matching invalidate: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("mint hits = %d, want 2", n)
	}
}

func TestConcurrentResolveMintsOnce(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MUSE_BRIDGE_IDENTITY", "test-identity-long-enough-to-count")
	client, hits := mintStub(t, 200, `{"api_key":"LLM_one"}`)

	s := NewStore(client, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.CurrentKey(); err != nil {
				t.Errorf("CurrentKey: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Fatalf("mint hits = %d, want 1 (stampede)", n)
	}
}

func TestDirectKeyFallback(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MUSE_BRIDGE_KEY", "LLM_static-long-enough-to-count")
	client, hits := mintStub(t, 200, `{"api_key":"LLM_one"}`)

	s := NewStore(client, time.Hour)
	key, err := s.CurrentKey()
	if err != nil {
		t.Fatalf("CurrentKey: %v", err)
	}
	if key != "LLM_static-long-enough-to-count" {
		t.Fatalf("key = %q, want static fallback", key)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("mint hits = %d, want 0 (no identity, skip mint)", n)
	}
}

func TestMintFailureFallsBackToDirectKey(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MUSE_BRIDGE_IDENTITY", "test-identity-long-enough-to-count")
	t.Setenv("MUSE_BRIDGE_KEY", "LLM_static-long-enough-to-count")
	client, _ := mintStub(t, 500, `{"error":"boom"}`)

	s := NewStore(client, time.Hour)
	key, err := s.CurrentKey()
	if err != nil {
		t.Fatalf("CurrentKey: %v", err)
	}
	if key != "LLM_static-long-enough-to-count" {
		t.Fatalf("key = %q, want static fallback", key)
	}
}

func TestNoCredential(t *testing.T) {
	isolateEnv(t)
	client, hits := mintStub(t, 200, `{"api_key":"LLM_one"}`)

	s := NewStore(client, time.Hour)
	if _, err := s.CurrentKey(); !errors.Is(err, auth.ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("mint hits = %d, want 0", n)
	}
}
