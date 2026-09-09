package upstream

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type mockAuthProv struct {
	tokens        []string
	tokenIdx      int32
	invalidated   []string
}

func (m *mockAuthProv) CurrentToken(ctx context.Context) (string, error) {
	idx := atomic.LoadInt32(&m.tokenIdx)
	if int(idx) < len(m.tokens) {
		return m.tokens[idx], nil
	}
	return "fallback-token", nil
}

func (m *mockAuthProv) Invalidate(token string) {
	m.invalidated = append(m.invalidated, token)
	atomic.AddInt32(&m.tokenIdx, 1)
}

func TestClientStreamSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "missing user agent", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"response\":{}}\n\n")
	}))
	defer server.Close()

	authProv := &mockAuthProv{tokens: []string{"valid-token"}}
	client := NewClient(authProv, server.Client(), server.URL)

	stream, err := client.StreamGenerateContent(context.Background(), &PredictionRequest{
		Model: "gemini-3.8-flash-high",
	})
	if err != nil {
		t.Fatalf("StreamGenerateContent failed: %v", err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "data: {\"response\":{}}") {
		t.Fatalf("unexpected stream lines: %v", lines)
	}
}

func TestClientStreamRetryOn401(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count == 1 {
			// First attempt has expired token
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		// Second attempt has refreshed token
		if r.Header.Get("Authorization") != "Bearer refreshed-token" {
			http.Error(w, "still unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"response\":{\"candidates\":[]}}\n\n")
	}))
	defer server.Close()

	authProv := &mockAuthProv{tokens: []string{"expired-token", "refreshed-token"}}
	client := NewClient(authProv, server.Client(), server.URL)

	stream, err := client.StreamGenerateContent(context.Background(), &PredictionRequest{
		Model: "gemini-3.8-flash-high",
	})
	if err != nil {
		t.Fatalf("StreamGenerateContent should succeed on retry: %v", err)
	}
	defer stream.Close()

	if len(authProv.invalidated) != 1 || authProv.invalidated[0] != "expired-token" {
		t.Fatalf("expected expired-token to be invalidated, got: %v", authProv.invalidated)
	}
	if atomic.LoadInt32(&requestCount) != 2 {
		t.Fatalf("expected 2 attempts, got %d", requestCount)
	}
}
