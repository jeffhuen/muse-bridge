// Package proxy implements the localhost HTTP server routing OpenAI and Codex requests to Google PredictionService.
package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/config"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/models"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/protocols"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// Handler routes incoming HTTP requests to PredictionService.
type Handler struct {
	client     *upstream.Client
	sem        chan struct{}
	maxFlights int
	debug      bool
}

// New constructs an initialized proxy Handler.
func New(client *upstream.Client, maxFlights int, debug bool) *Handler {
	if maxFlights <= 0 {
		maxFlights = config.DefaultMaxFlights
	}
	return &Handler{
		client:     client,
		sem:        make(chan struct{}, maxFlights),
		maxFlights: maxFlights,
		debug:      debug,
	}
}

// ServeHTTP handles routing, bare-path normalization, and concurrency limits.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// 1. Local liveness check
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}

	// Normalize bare paths without /v1 prefix
	path := r.URL.Path
	if !strings.HasPrefix(path, "/v1/") && path != "/v1" {
		path = "/v1" + strings.TrimSuffix(path, "/")
	}

	// 2. Models listing
	if r.Method == http.MethodGet && (path == "/v1/models" || path == "/v1/models/") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.ListOpenAIModels())
		return
	}

	// Only POST endpoints below this point
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// 3. Acquire concurrency semaphore
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-r.Context().Done():
		http.Error(w, `{"error":"client cancelled"}`, 499)
		return
	case <-time.After(60 * time.Second):
		http.Error(w, `{"error":"max concurrent flights exceeded"}`, http.StatusTooManyRequests)
		return
	}

	// 4. Route payload handlers
	switch path {
	case "/v1/responses":
		protocols.HandleResponses(w, r, h.client)
	case "/v1/chat/completions":
		protocols.HandleChatCompletions(w, r, h.client)
	default:
		http.Error(w, fmt.Sprintf(`{"error":"not found: %s"}`, path), http.StatusNotFound)
	}

	if h.debug {
		log.Printf("served %s %s in %v", r.Method, path, time.Since(start))
	}
}
