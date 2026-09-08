// Package proxy is the localhost reverse proxy: it resolves a key,
// forwards the request to the Model API, and streams the reply back.
// Key resolution and the upstream address are injected so tests run
// fully offline against httptest servers.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/config"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/keys"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/rewrite"
)

// Handler proxies one bridge API surface. The zero value is not usable;
// construct with New.
type Handler struct {
	keys     keys.Provider
	client   *http.Client
	upstream string
	sem      chan struct{}
	debug    bool
}

// New wires a Handler. maxFlights bounds concurrent upstream requests;
// non-positive values fall back to the default.
func New(k keys.Provider, client *http.Client, upstream string, maxFlights int, debug bool) *Handler {
	if maxFlights <= 0 {
		maxFlights = config.DefaultMaxFlights
	}
	return &Handler{
		keys:     k,
		client:   client,
		upstream: strings.TrimSuffix(upstream, "/"),
		sem:      make(chan struct{}, maxFlights),
		debug:    debug,
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Liveness probe. Deliberately local-only: it never touches keys
	// or the network, so supervisors can poll it freely. bridge.py has
	// no equivalent (it would forward this path upstream).
	if r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	h.serveProxy(w, r)
}

func (h *Handler) serveProxy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, config.MaxBody+1))
	r.Body.Close()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed reading request body"})
		return
	}
	if len(body) > config.MaxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge,
			map[string]string{"error": "request body exceeds bridge limit"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/responses") && len(body) > 0 {
		body = rewrite.Responses(body, h.debug)
	}

	key, err := h.keys.CurrentKey()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	upstreamPath := r.URL.Path
	if !strings.HasPrefix(upstreamPath, "/v1") {
		upstreamPath = "/v1" + upstreamPath
	}
	target := h.upstream + upstreamPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-time.After(config.SemaphoreWait):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bridge busy; retry"})
		return
	}

	for attempt := 0; attempt < 2; attempt++ {
		resp, err := h.roundTrip(r, target, body, key)
		if err != nil {
			writeJSON(w, http.StatusBadGateway,
				map[string]string{"error": "upstream unreachable: " + err.Error()})
			return
		}
		if resp.StatusCode >= 400 {
			log.Printf("upstream %s %s -> %d", r.Method, upstreamPath, resp.StatusCode)
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, config.MaxErrorBody))
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
				h.keys.Invalidate()
				key, err = h.keys.CurrentKey()
				if err != nil {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
					return
				}
				continue
			}
			var obj any
			if err := json.Unmarshal(errBody, &obj); err != nil {
				obj = map[string]string{"error": fmt.Sprintf("upstream HTTP %d", resp.StatusCode)}
			}
			writeJSON(w, resp.StatusCode, obj)
			return
		}
		h.stream(w, resp)
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream retry exhausted"})
}

// roundTrip performs one upstream request. It binds the upstream call to
// the client's context, so a disconnect cancels the upstream fetch.
func (h *Handler) roundTrip(r *http.Request, target string, body []byte, key string) (*http.Response, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	if len(body) > 0 {
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	return h.client.Do(req)
}

// stream copies the upstream body to the client, flushing each chunk so
// long generations arrive incrementally. A client write failure ends the
// copy and closes the upstream body, releasing its connection.
func (h *Handler) stream(w http.ResponseWriter, resp *http.Response) {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, config.StreamChunk)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
	resp.Body.Close()
}

func writeJSON(w http.ResponseWriter, code int, obj any) {
	out, _ := json.Marshal(obj)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(out)
}
