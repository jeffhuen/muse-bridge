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
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/config"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/keys"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/rewrite"
)

// forwardedHeaders pass upstream metadata that clients need for backoff
// (Retry-After) and tracing. Everything else stays dropped, as before.
var forwardedHeaders = []string{"Retry-After", "X-Request-Id", "X-Ratelimit-Remaining"}

// streamBufs recycles copy buffers so each stream does not allocate 64KB.
var streamBufs = sync.Pool{New: func() any {
	buf := make([]byte, config.StreamChunk)
	return &buf
}}

// NewUpstreamClient builds the shared upstream client. There is
// deliberately no Client.Timeout: it is a wall clock over the whole
// body read and would abort legitimate long generations. Stream
// lifetime is governed by the caller's context instead; the transport
// still bounds time-to-first-byte and handshake phases.
func NewUpstreamClient(maxFlights int) *http.Client {
	if maxFlights <= 0 {
		maxFlights = config.DefaultMaxFlights
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          2 * maxFlights,
		MaxIdleConnsPerHost:   maxFlights,
		IdleConnTimeout:       config.IdleConnTTL,
		ResponseHeaderTimeout: config.UpstreamHeaderTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Identity encoding keeps chunked streams flowing with minimum
		// latency instead of batching them through a gzip framer.
		DisableCompression: true,
	}}
}

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
	// Normalize before the rewrite check so bare paths (e.g. /responses
	// without the /v1 prefix) get the same munging as prefixed ones.
	upstreamPath := r.URL.Path
	if !strings.HasPrefix(upstreamPath, "/v1") {
		upstreamPath = "/v1" + upstreamPath
	}
	if strings.HasPrefix(upstreamPath, "/v1/responses") && len(body) > 0 {
		body = rewrite.Responses(body, h.debug)
	}

	key, err := h.keys.CurrentKey()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	target := h.upstream + upstreamPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	timer := time.NewTimer(config.SemaphoreWait)
	select {
	case h.sem <- struct{}{}:
		timer.Stop()
		defer func() { <-h.sem }()
	case <-timer.C:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bridge busy; retry"})
		return
	case <-r.Context().Done():
		timer.Stop()
		return // Client gone; nothing left to answer.
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
				h.keys.Invalidate(key)
				key, err = h.keys.CurrentKey()
				if err != nil {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
					return
				}
				continue
			}
			forwardHeaders(w, resp)
			var obj any
			if err := rewrite.Decode(errBody, &obj); err != nil {
				m := map[string]string{"error": fmt.Sprintf("upstream HTTP %d", resp.StatusCode)}
				if s := snippet(errBody); s != "" {
					m["body"] = s
				}
				obj = m
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
// long generations arrive incrementally. Headers flush first so the
// client sees the response line even during a long reasoning phase with
// no body bytes yet. A client write failure ends the copy and closes
// the upstream body, releasing its connection.
func (h *Handler) stream(w http.ResponseWriter, resp *http.Response) {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	forwardHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	bufPtr := streamBufs.Get().(*[]byte)
	defer streamBufs.Put(bufPtr)
	buf := *bufPtr
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

// forwardHeaders copies the allowlisted upstream metadata headers.
func forwardHeaders(w http.ResponseWriter, resp *http.Response) {
	for _, name := range forwardedHeaders {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
}

// snippet renders raw bytes for error strings, capped at 500 bytes.
func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

func writeJSON(w http.ResponseWriter, code int, obj any) {
	out, _ := json.Marshal(obj)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(out)
}
