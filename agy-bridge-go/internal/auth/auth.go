// Package auth loads Google Antigravity credentials from macOS Keychain and refreshes them via OAuth2.
package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/config"
)

var (
	ErrNoCredentials = errors.New("no antigravity credentials found in keychain or token file")
	ErrRefreshFailed = errors.New("failed to refresh oauth2 token")
)

// TokenData holds the OAuth2 token payload.
type TokenData struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

// StoredCredential represents the full credential structure stored in Keychain.
type StoredCredential struct {
	Token      TokenData `json:"token"`
	AuthMethod string    `json:"auth_method"`
}

// Provider provides access to a valid OAuth2 Bearer token.
type Provider interface {
	CurrentToken(ctx context.Context) (string, error)
	Invalidate(failedToken string)
}

// TokenStore caches and automatically refreshes Google Antigravity OAuth tokens.
type TokenStore struct {
	mu       sync.Mutex
	cred     *StoredCredential
	client   *http.Client
	rejected map[string]bool
}

// NewStore creates a TokenStore with the given HTTP client.
func NewStore(client *http.Client) *TokenStore {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &TokenStore{
		client:   client,
		rejected: make(map[string]bool),
	}
}

// CurrentToken returns a valid Bearer access token, refreshing if expired or expiring within 2 minutes.
func (s *TokenStore) CurrentToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. If we have a cached credential and it's valid for > 2 minutes and not rejected, return it.
	if s.cred != nil && s.cred.Token.AccessToken != "" && !s.rejected[s.cred.Token.AccessToken] {
		if time.Until(s.cred.Token.Expiry) > 2*time.Minute {
			return s.cred.Token.AccessToken, nil
		}
	}

	// 2. Load fresh credential from Keychain or file.
	cred, err := LoadCredential()
	if err != nil && s.cred == nil {
		return "", fmt.Errorf("load credentials: %w", err)
	}
	if cred != nil {
		if s.rejected[cred.Token.AccessToken] {
			cred.Token.AccessToken = ""
		}
		s.cred = cred
	}

	// Check if loaded credential is still valid and not rejected.
	if s.cred != nil && s.cred.Token.AccessToken != "" && !s.rejected[s.cred.Token.AccessToken] && time.Until(s.cred.Token.Expiry) > 2*time.Minute {
		return s.cred.Token.AccessToken, nil
	}

	// 3. Need refresh.
	if s.cred == nil || s.cred.Token.RefreshToken == "" {
		return "", ErrNoCredentials
	}

	log.Printf("[auth] refreshing access token via %s", config.OAuthTokenURL)
	newCred, err := RefreshToken(ctx, s.client, s.cred.Token.RefreshToken)
	if err != nil {
		// If refresh fails but we still have an unexpired, non-rejected access token, log and use it as fallback.
		if s.cred != nil && s.cred.Token.AccessToken != "" && !s.rejected[s.cred.Token.AccessToken] && time.Now().Before(s.cred.Token.Expiry) {
			log.Printf("[auth] refresh failed (%v), using remaining valid token until %v", err, s.cred.Token.Expiry)
			return s.cred.Token.AccessToken, nil
		}
		return "", fmt.Errorf("%w: %v", ErrRefreshFailed, err)
	}

	s.cred = newCred
	// Best-effort persist refreshed token back to keychain.
	go func(c StoredCredential) {
		_ = SaveCredential(&c)
	}(*newCred)

	return s.cred.Token.AccessToken, nil
}

// Invalidate clears the cached token if it matches failedToken and records it as rejected.
func (s *TokenStore) Invalidate(failedToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if failedToken != "" {
		if s.rejected == nil {
			s.rejected = make(map[string]bool)
		}
		s.rejected[failedToken] = true
	}
	if s.cred != nil && s.cred.Token.AccessToken == failedToken {
		s.cred.Token.AccessToken = ""
	}
}

// LoadCredential reads Antigravity credentials from env, macOS Keychain, or fallback token file.
func LoadCredential() (*StoredCredential, error) {
	// 1. Env override
	if envToken := strings.TrimSpace(os.Getenv("AGY_BRIDGE_TOKEN")); envToken != "" {
		return &StoredCredential{
			Token: TokenData{
				AccessToken: envToken,
				TokenType:   "Bearer",
				Expiry:      time.Now().Add(24 * time.Hour),
			},
			AuthMethod: "env",
		}, nil
	}

	// 2. macOS Keychain
	cmd := exec.Command("security", "find-generic-password", "-s", "gemini", "-a", "antigravity", "-w")
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		cred, parseErr := parseCredentialBytes(out)
		if parseErr == nil && cred.Token.AccessToken != "" {
			return cred, nil
		}
	}

	// 3. Fallback token file (~/.gemini/antigravity-cli/antigravity-oauth-token)
	home, _ := os.UserHomeDir()
	filePath := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	data, err := os.ReadFile(filePath)
	if err == nil {
		var cred StoredCredential
		if json.Unmarshal(data, &cred) == nil && cred.Token.AccessToken != "" {
			return &cred, nil
		}
	}

	return nil, ErrNoCredentials
}

func parseCredentialBytes(raw []byte) (*StoredCredential, error) {
	str := strings.TrimSpace(string(raw))
	if strings.HasPrefix(str, "go-keyring-base64:") {
		b64 := strings.TrimPrefix(str, "go-keyring-base64:")
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, err
		}
		str = string(decoded)
	}

	var cred StoredCredential
	if err := json.Unmarshal([]byte(str), &cred); err != nil {
		return nil, err
	}
	return &cred, nil
}

// RefreshToken exchanges a refresh token for a new access token at Google's OAuth2 endpoint.
func RefreshToken(ctx context.Context, client *http.Client, refreshToken string) (*StoredCredential, error) {
	form := url.Values{
		"client_id":     {config.OAuthClientID},
		"client_secret": {config.OAuthClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.OAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, errors.New("no access_token in refresh response")
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	cred := &StoredCredential{
		Token: TokenData{
			AccessToken:  tokenResp.AccessToken,
			TokenType:    tokenResp.TokenType,
			RefreshToken: refreshToken,
			Expiry:       time.Now().Add(time.Duration(expiresIn) * time.Second),
		},
		AuthMethod: "consumer",
	}
	return cred, nil
}

// SaveCredential writes the refreshed token back to macOS Keychain and fallback file.
func SaveCredential(cred *StoredCredential) error {
	if (flag.Lookup("test.v") != nil || os.Getenv("AGY_BRIDGE_TEST") != "") && os.Getenv("AGY_BRIDGE_FORCE_SAVE_IN_TEST") == "" {
		return nil
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return err
	}

	b64Val := "go-keyring-base64:" + base64.StdEncoding.EncodeToString(data)
	// Update keychain password
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", "gemini", "-a", "antigravity", "-w", b64Val)
	_ = cmd.Run()

	// Update file
	home, _ := os.UserHomeDir()
	filePath := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	var buf bytes.Buffer
	_ = json.Indent(&buf, data, "", "  ")
	_ = os.WriteFile(filePath, buf.Bytes(), 0o600)
	return nil
}
