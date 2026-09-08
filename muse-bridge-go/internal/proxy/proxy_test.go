package proxy

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/config"
)

// stubKeys hands out scripted keys and records invalidations.
type stubKeys struct {
	mu          sync.Mutex
	keys        []string
	calls       int
	invalidated int
	err         error
}

func (s *stubKeys) CurrentKey() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	i := s.invalidated
	if i >= len(s.keys) {
		i = len(s.keys) - 1
	}
	return s.keys[i], nil
}

func (s *stubKeys) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidated++
}

// setup wires a Handler in front of a stub upstream.
func setup(t *testing.T, k *stubKeys, upstream http.HandlerFunc) *Handler {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	return New(k, srv.Client(), srv.URL, 0, false)
}

func do(t *testing.T, h *Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthzIsLocal(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_x"}}
	hit := false
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) { hit = true })
	rec := do(t, h, "GET", "/healthz", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	if hit {
		t.Fatal("healthz reached upstream")
	}
	if k.calls != 0 {
		t.Fatal("healthz resolved a key")
	}
}

func TestForwardsAuthPathAndQuery(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	var gotAuth, gotPath, gotQuery string
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotQuery = r.Header.Get("Authorization"), r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[]}`))
	})
	rec := do(t, h, "GET", "/v1/models?x=1", "", "")
	if rec.Code != 200 || rec.Body.String() != `{"data":[]}` {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer LLM_test" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if gotPath != "/v1/models" || gotQuery != "x=1" {
		t.Fatalf("path=%q query=%q", gotPath, gotQuery)
	}
}

func TestPrefixesBarePaths(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	var gotPath string
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	})
	do(t, h, "GET", "/models", "", "")
	if gotPath != "/v1/models" {
		t.Fatalf("path=%q, want /v1/models", gotPath)
	}
}

func TestRewritesResponsesBody(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	var gotBody string
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Write([]byte(`{}`))
	})
	in := `{"model":"m","reasoning":{"effort":"none"}}`
	rec := do(t, h, "POST", "/v1/responses", "application/json", in)
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(gotBody, `"prompt_cache_retention":"24h"`) {
		t.Fatalf("retention missing: %s", gotBody)
	}
	if strings.Contains(gotBody, "reasoning") {
		t.Fatalf("reasoning not stripped: %s", gotBody)
	}
}

func TestLeavesOtherBodiesAlone(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	var gotBody string
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Write([]byte(`{}`))
	})
	in := `{"reasoning":{"effort":"none"}}`
	do(t, h, "POST", "/v1/other", "application/json", in)
	if gotBody != in {
		t.Fatalf("body rewritten: %s", gotBody)
	}
}

func TestRetryOn401ThenSucceed(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_old", "LLM_new"}}
	var calls int
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer LLM_old" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"expired"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	rec := do(t, h, "GET", "/v1/models", "", "")
	if rec.Code != 200 || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	if calls != 2 || k.invalidated != 1 {
		t.Fatalf("calls=%d invalidated=%d, want 2/1", calls, k.invalidated)
	}
}

func TestForwardsSecond401(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_old", "LLM_new"}}
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"denied"}`))
	})
	rec := do(t, h, "GET", "/v1/models", "", "")
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "denied") {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestForwardsUpstreamError(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":"slow down"}`))
	})
	rec := do(t, h, "GET", "/v1/models", "", "")
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "slow down") {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestNonJSONUpstreamError(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		w.Write([]byte(`<html>proxy woe</html>`))
	})
	rec := do(t, h, "GET", "/v1/models", "", "")
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "upstream HTTP 502") {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestStreamsChunks(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, chunk := range []string{"one-", "two-", "three"} {
			w.Write([]byte(chunk))
			flusher.Flush()
		}
	})
	rec := do(t, h, "GET", "/v1/stream", "", "")
	if rec.Body.String() != "one-two-three" {
		t.Fatalf("body=%q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q", ct)
	}
}

func TestRejectsOversizeBody(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	hit := false
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) { hit = true })
	rec := do(t, h, "POST", "/v1/responses", "application/json",
		strings.Repeat("x", config.MaxBody+1))
	if rec.Code != 413 {
		t.Fatalf("code=%d, want 413", rec.Code)
	}
	if hit {
		t.Fatal("oversize body reached upstream")
	}
}

func TestNoCredentialIs503(t *testing.T) {
	k := &stubKeys{err: errors.New("no usable credential")}
	hit := false
	h := setup(t, k, func(w http.ResponseWriter, r *http.Request) { hit = true })
	rec := do(t, h, "GET", "/v1/models", "", "")
	if rec.Code != 503 {
		t.Fatalf("code=%d, want 503", rec.Code)
	}
	if hit {
		t.Fatal("request without key reached upstream")
	}
}

func TestUpstreamUnreachableIs502(t *testing.T) {
	k := &stubKeys{keys: []string{"LLM_test"}}
	// Closed server: dialing it fails fast without touching the network.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	h := New(k, http.DefaultClient, url, 0, false)
	rec := do(t, h, "GET", "/v1/models", "", "")
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "upstream unreachable") {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}
