package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/config"
)

// DoLogin runs the one-time Meta device flow, verifies the login by
// minting a key, and stores the identity mode 0600. It blocks until the
// user approves in the browser, the device code expires, or ctx cancels.
func DoLogin(ctx context.Context, client *http.Client) error {
	status, auth := PostForm(client, config.DeviceAuthURL, map[string]string{"client_id": config.ClientID})
	deviceCode, _ := auth["device_code"].(string)
	userCode, _ := auth["user_code"].(string)
	verifyURI, _ := auth["verification_uri"].(string)
	if status != 200 || deviceCode == "" || userCode == "" || verifyURI == "" {
		return fmt.Errorf("device auth start failed HTTP %d: %s", status, ShortJSON(auth))
	}
	if complete, _ := auth["verification_uri_complete"].(string); complete != "" {
		verifyURI = complete
	}
	interval := 5.0
	if v, ok := auth["interval"].(float64); ok && v > 0 {
		interval = v
	}
	expires := 900.0
	if v, ok := auth["expires_in"].(float64); ok && v > 0 {
		expires = v
	}
	fmt.Printf("Approve in browser: %s\n", verifyURI)
	fmt.Printf("User code: %s (expires in %ds)\n", userCode, int(expires))
	deadline := time.Now().Add(time.Duration(expires) * time.Second)
	identity := ""
	for time.Now().Before(deadline) {
		timer := time.NewTimer(time.Duration(interval * float64(time.Second)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("login interrupted: %w", ctx.Err())
		case <-timer.C:
		}
		status, tok := PostForm(client, config.DeviceTokenURL, map[string]string{
			"grant_type":  config.DeviceGrant,
			"device_code": deviceCode,
			"client_id":   config.ClientID,
		})
		if access, _ := tok["access_token"].(string); status == 200 && access != "" {
			identity = access
			break
		}
		errStr, _ := tok["error"].(string)
		if (errStr == "authorization_pending" || errStr == "") && (status == 200 || status == 400) {
			if errStr == "" && status == 200 {
				return fmt.Errorf("unexpected token response: %s", ShortJSON(tok))
			}
			continue
		}
		switch errStr {
		case "slow_down":
			interval += 5
			continue
		case "access_denied":
			return fmt.Errorf("login denied in browser")
		case "expired_token":
			return fmt.Errorf("device code expired; run login again")
		}
		return fmt.Errorf("login failed HTTP %d: %s", status, ShortJSON(tok))
	}
	if identity == "" {
		return fmt.Errorf("login timed out; run login again")
	}
	key, err := MintAPIKey(client, identity)
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("login approved but no API key minted; check billing at https://dev.meta.ai/billing")
	}
	doc, _ := json.Marshal(map[string]any{"identity": identity, "stored_at": time.Now().Unix()})
	if err := os.WriteFile(config.IdentityPath(), doc, 0o600); err != nil {
		return err
	}
	fmt.Println("identity stored (0600). Mint works.")
	return nil
}
