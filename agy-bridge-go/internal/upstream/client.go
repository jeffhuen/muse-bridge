package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/auth"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/config"
)

// Client handles streaming HTTP requests to Google's internal PredictionService.
type Client struct {
	auth        auth.Provider
	httpClient  *http.Client
	sigCache    *SignatureCache
	upstreamURL string
	userAgent   string
}

// NewClient creates a PredictionService client.
func NewClient(authProv auth.Provider, httpClient *http.Client, upstreamURL string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 0, // No wall-clock timeout on streaming HTTP body; context controls cancellation
		}
	}
	if upstreamURL == "" {
		upstreamURL = config.UpstreamURL()
	}
	sigCache := NewSignatureCache(2000)
	if flag.Lookup("test.v") == nil {
		sigPath := config.SignatureCachePath()
		sigCache.SetPersistPath(sigPath)
		_ = sigCache.LoadFromFile(sigPath)
	}
	return &Client{
		auth:        authProv,
		httpClient:  httpClient,
		sigCache:    sigCache,
		upstreamURL: upstreamURL,
		userAgent:   config.DefaultUserAgent,
	}
}

// SigCache exposes the signature cache for inspection and manipulation.
func (c *Client) SigCache() *SignatureCache {
	return c.sigCache
}

// StreamGenerateContent sends a prediction request and returns the SSE response stream.
func (c *Client) StreamGenerateContent(ctx context.Context, predReq *PredictionRequest) (io.ReadCloser, error) {
	bodyBytes, err := json.Marshal(predReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Attempt up to 2 times (retry on 401 unauthorized to refresh token).
	for attempt := 1; attempt <= 2; attempt++ {
		token, err := c.auth.CurrentToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("get auth token: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.upstreamURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("create http request: %w", err)
		}

		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", c.userAgent)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("do http request: %w", err)
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 1 {
			log.Printf("[upstream] 401 unauthorized, invalidating token and retrying...")
			c.auth.Invalidate(token)
			resp.Body.Close()
			continue
		}

		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			errBody, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, string(errBody))
		}

		return resp.Body, nil
	}

	return nil, fmt.Errorf("upstream request failed after token retry")
}
