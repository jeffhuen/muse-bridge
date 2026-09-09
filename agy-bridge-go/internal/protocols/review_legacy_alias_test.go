package protocols

import (
	"encoding/json"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewLegacyResponsesAliasPair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	snapshot := `{"tool_sigs":{"call_legacy":"sig_original","fc_legacy":"sig_original"},"tool_names":{"call_legacy":"lookup","fc_legacy":"lookup"},"tool_args":{"call_legacy":{"key":"original"},"fc_legacy":{"key":"original"}}}`
	if err := os.WriteFile(path, []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	cache := upstream.NewSignatureCache(100)
	if err := cache.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	req := &ResponsesRequest{Model: "gemini-3.8-flash-high", Input: json.RawMessage(`[
		{"role":"user","content":"look up"},
		{"type":"function_call","call_id":"call_legacy","id":"fc_legacy","name":"lookup","arguments":"{\"key\":\"original\"}"},
		{"type":"function_call_output","call_id":"call_legacy","output":"done"}
	]`)}
	if _, err := ConvertResponsesToPrediction(req, cache); err != nil {
		t.Fatalf("valid legacy Responses alias pair rejected: %v", err)
	}
}
