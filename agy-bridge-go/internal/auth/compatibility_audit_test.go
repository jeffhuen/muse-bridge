package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/config"
)

func TestAudit401RefreshesRejectedCredential(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	t.Setenv("AGY_BRIDGE_TOKEN", "")
	credential, _ := json.Marshal(StoredCredential{AuthMethod: "consumer", Token: TokenData{AccessToken: "rejected-token", RefreshToken: "test-refresh", Expiry: time.Now().Add(time.Hour)}})
	// Use the absolute cat path: PATH deliberately contains only our fake security helper.
	script := "#!/bin/sh\nif [ \"$1\" = find-generic-password ]; then\n/bin/cat <<'CREDENTIAL'\n" + string(credential) + "\nCREDENTIAL\nfi\n"
	if err := os.WriteFile(filepath.Join(dir, "security"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"refreshed-token","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer server.Close()
	oldURL := config.OAuthTokenURL
	config.OAuthTokenURL = server.URL
	t.Cleanup(func() { config.OAuthTokenURL = oldURL })
	store := NewStore(server.Client())
	first, err := store.CurrentToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store.Invalidate(first)
	next, err := store.CurrentToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if next == first || refreshes != 1 {
		t.Fatalf("401 replayed rejected credential: same_token=%v refresh_requests=%d", next == first, refreshes)
	}
}
