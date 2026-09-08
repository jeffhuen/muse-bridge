// Package auth talks to Meta's OAuth and key-mint endpoints and loads
// local credentials. It never caches: caching lives in internal/keys.
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/config"
)

// MintURL is a var (not a const) so tests can redirect minting at a
// local httptest server.
var MintURL = "https://api.meta.ai/muse-code/key"

// ErrNoCredential reports that no identity minted and no static key exists.
var ErrNoCredential = errors.New("no usable credential; run login once")

// LoadIdentity returns the stored Meta identity and where it came from,
// or empty strings when none is usable.
func LoadIdentity() (ident, src string) {
	if val := strings.TrimSpace(os.Getenv("MUSE_BRIDGE_IDENTITY")); len(val) > 20 {
		return val, "env:MUSE_BRIDGE_IDENTITY"
	}
	data, err := os.ReadFile(config.IdentityPath())
	if err != nil {
		return "", ""
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", ""
	}
	ident, _ = doc["identity"].(string)
	if len(ident) > 20 {
		return ident, "identity.json"
	}
	return "", ""
}

// DirectKey is a static API key and the name of its source.
type DirectKey struct {
	From string
	Key  string
}

// LoadDirectKeys gathers static keys from the environment and, as a last
// resort, from the Muse app's own token file.
func LoadDirectKeys() []DirectKey {
	var keys []DirectKey
	for _, name := range []string{"MUSE_BRIDGE_KEY", "META_API_KEY", "MODEL_API_KEY"} {
		if val := strings.TrimSpace(os.Getenv(name)); len(val) > 20 {
			keys = append(keys, DirectKey{From: name, Key: val})
		}
	}
	data, err := os.ReadFile(config.MuseAuthPath())
	if err != nil {
		log.Println("muse auth not readable:", err)
		return keys
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return keys
	}
	seen := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case string:
			if (strings.HasPrefix(n, "LLM_") || strings.HasPrefix(n, "LLM|")) && !seen[n] {
				seen[n] = true
				keys = append(keys, DirectKey{From: "muse-auth.json", Key: n})
			}
		case map[string]any:
			for _, v := range n {
				walk(v)
			}
		case []any:
			for _, v := range n {
				walk(v)
			}
		}
	}
	if m, ok := doc.(map[string]any); ok {
		if providers, ok := m["providers"]; ok {
			walk(providers)
		} else {
			walk(doc)
		}
	} else {
		walk(doc)
	}
	return keys
}

// PostForm POSTs form fields and returns the status code with the decoded
// JSON body. Transport failure yields status 0 with the error in the body.
func PostForm(client *http.Client, urlStr string, fields map[string]string) (int, map[string]any) {
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	req, err := http.NewRequest("POST", urlStr, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, map[string]any{"error": err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return 0, map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()
	return resp.StatusCode, decodeJSON(resp.Body)
}

// MintAPIKey exchanges a Meta identity for a short-lived Model API key.
func MintAPIKey(client *http.Client, identity string) (string, error) {
	req, err := http.NewRequest("POST", MintURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+identity)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-version", "1.0.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mint unreachable: %w", err)
	}
	defer resp.Body.Close()
	body := decodeJSON(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("mint HTTP %d: %s", resp.StatusCode, ShortJSON(body))
	}
	key, _ := body["api_key"].(string)
	return key, nil
}

func decodeJSON(r io.Reader) map[string]any {
	out := map[string]any{}
	body, _ := io.ReadAll(r)
	if len(body) == 0 {
		return out
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// ShortJSON renders obj for error strings, capped at 200 bytes.
func ShortJSON(obj map[string]any) string {
	raw, _ := json.Marshal(obj)
	if len(raw) > 200 {
		raw = raw[:200]
	}
	return string(raw)
}
