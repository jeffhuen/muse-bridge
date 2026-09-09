package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/config"
)

func TestParseCredentialBytes(t *testing.T) {
	rawJSON := `{"token":{"access_token":"ya29.test12345","token_type":"Bearer","refresh_token":"1//testref","expiry":"2026-09-08T20:00:00Z"},"auth_method":"consumer"}`

	// Test direct JSON
	cred, err := parseCredentialBytes([]byte(rawJSON))
	if err != nil {
		t.Fatalf("parse raw JSON: %v", err)
	}
	if cred.Token.AccessToken != "ya29.test12345" {
		t.Errorf("got access token %q, want ya29.test12345", cred.Token.AccessToken)
	}
	if cred.Token.RefreshToken != "1//testref" {
		t.Errorf("got refresh token %q, want 1//testref", cred.Token.RefreshToken)
	}

	// Test go-keyring-base64 format
	b64 := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(rawJSON))
	cred2, err := parseCredentialBytes([]byte(b64))
	if err != nil {
		t.Fatalf("parse base64: %v", err)
	}
	if cred2.Token.AccessToken != "ya29.test12345" {
		t.Errorf("got access token %q, want ya29.test12345", cred2.Token.AccessToken)
	}
}

func TestTokenStoreEnvOverride(t *testing.T) {
	t.Setenv("AGY_BRIDGE_TOKEN", "ya29.override_token")
	store := NewStore(nil)
	token, err := store.CurrentToken(context.Background())
	if err != nil {
		t.Fatalf("CurrentToken: %v", err)
	}
	if token != "ya29.override_token" {
		t.Errorf("got token %q, want ya29.override_token", token)
	}
}

func TestRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type=refresh_token, got %s", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "my-refresh-token" {
			t.Errorf("expected refresh_token=my-refresh-token, got %s", r.Form.Get("refresh_token"))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ya29.new_refreshed_token",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	defer server.Close()

	origURL := config.OAuthTokenURL
	config.OAuthTokenURL = server.URL
	defer func() { config.OAuthTokenURL = origURL }()

	cred, err := RefreshToken(context.Background(), server.Client(), "my-refresh-token")
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if cred.Token.AccessToken != "ya29.new_refreshed_token" {
		t.Errorf("got access_token %q, want ya29.new_refreshed_token", cred.Token.AccessToken)
	}
	if time.Until(cred.Token.Expiry) < 3500*time.Second {
		t.Errorf("expiry too soon: %v", cred.Token.Expiry)
	}
}
