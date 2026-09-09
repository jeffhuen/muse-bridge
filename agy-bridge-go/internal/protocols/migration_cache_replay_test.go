package protocols

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestImportedParallelCallsReplayAfterCacheUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	// The old translator persisted a migration marker for only the first call.
	if err := os.WriteFile(path, []byte(`{"v":2,"records":{"call_one":{"call_id":"call_one","tool_name":"exec_command","args":{"cmd":"one"},"sig":"skip_thought_signature_validator"}},"aliases":{"call_one":"call_one"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"responses", "chat"} {
		t.Run(endpoint, func(t *testing.T) {
			cache := upstream.NewSignatureCache(100)
			if err := cache.LoadFromFile(path); err != nil {
				t.Fatal(err)
			}
			var pred *upstream.PredictionRequest
			var err error
			if endpoint == "responses" {
				pred, err = ConvertResponsesToPrediction(&ResponsesRequest{Model: "gemini-3.8-flash-high", Input: json.RawMessage(`[
					{"role":"user","content":"run commands"},
					{"type":"function_call","call_id":"call_one","name":"exec_command","arguments":"{\"cmd\":\"one\"}"},
					{"type":"function_call","call_id":"call_two","name":"exec_command","arguments":"{\"cmd\":\"two\"}"},
					{"type":"function_call_output","call_id":"call_one","output":"ok"},
					{"type":"function_call_output","call_id":"call_two","output":"ok"}
				]`)}, cache)
			} else {
				var req ChatRequest
				if err := json.Unmarshal([]byte(`{"model":"gemini-3.8-flash-high","messages":[
					{"role":"user","content":"run commands"},
					{"role":"assistant","tool_calls":[
						{"id":"call_one","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"one\"}"}},
						{"id":"call_two","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"two\"}"}}
					]},
					{"role":"tool","tool_call_id":"call_one","content":"ok"},
					{"role":"tool","tool_call_id":"call_two","content":"ok"}
				]}`), &req); err != nil {
					t.Fatal(err)
				}
				pred, err = ConvertChatToPrediction(&req, cache)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			for _, content := range pred.Request.Contents {
				for _, part := range content.Parts {
					if part.FunctionCall != nil {
						calls++
						if part.ThoughtSignature != "skip_thought_signature_validator" {
							t.Fatal("imported call did not receive migration recovery")
						}
					}
				}
			}
			if calls != 2 {
				t.Fatalf("got %d calls, want 2", calls)
			}
			for _, id := range []string{"call_one", "call_two"} {
				if _, ok := cache.GetToolRecord(id); ok {
					t.Fatalf("translation created native ownership for %s", id)
				}
			}
		})
	}
}
