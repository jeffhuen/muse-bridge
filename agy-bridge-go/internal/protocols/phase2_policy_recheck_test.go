package protocols

import (
	"encoding/json"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestPhase2RecheckNativeEvidenceBeatsForeignLabel(t *testing.T) {
	for _, api := range []string{"chat", "responses"} {
		for _, source := range []string{"cache", "carrier"} {
			if api == "chat" && source == "carrier" {
				continue
			}
			for _, state := range []string{"valid", "missing", "edited"} {
				t.Run(api+"/"+source+"/"+state, func(t *testing.T) {
					var items []map[string]any
					if err := json.Unmarshal([]byte(phase2ReviewSingleHistory[api]), &items); err != nil {
						t.Fatal(err)
					}
					call := items[1]
					if api == "chat" {
						call = items[1]["tool_calls"].([]any)[0].(map[string]any)
					}
					call["provider"] = "anthropic"
					if state == "edited" {
						if api == "responses" {
							call["arguments"] = `{"key":"changed"}`
						} else {
							call["function"].(map[string]any)["arguments"] = `{"key":"changed"}`
						}
					}
					sig := "synthetic_native_signature"
					if state == "missing" {
						sig = ""
					}
					cache := upstream.NewSignatureCache(100)
					if source == "cache" {
						cache.PutToolDetails("call_known", "lookup", map[string]any{"key": "a"}, sig)
					} else {
						carrier := EncodeReasoningEncryptedContent(ReasoningEncryptedState{Version: 1, Parts: []TurnPartRecord{{Kind: PartKindToolCall, CallID: "call_known", OutputItemID: "fc_known", ToolName: "lookup", Args: map[string]any{"key": "a"}, ThoughtSignature: sig}}})
						items = append(items[:1], append([]map[string]any{{"type": "reasoning", "id": "rs_known", "encrypted_content": carrier, "summary": []any{}}}, items[1:]...)...)
					}
					raw, err := json.Marshal(items)
					if err != nil {
						t.Fatal(err)
					}
					pred, err := phase2ReviewConvert(t, api, string(raw), cache)
					if state != "valid" {
						if err == nil {
							t.Fatalf("foreign label bypassed %s native-state rejection", state)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, c := range pred.Request.Contents {
						for _, p := range c.Parts {
							if p.FunctionCall != nil && p.FunctionCall.ID == "call_known" {
								found = true
								if p.ThoughtSignature != sig {
									t.Errorf("foreign label overrode matching native signature: got %q", p.ThoughtSignature)
								}
							}
						}
					}
					if !found {
						t.Fatal("native call disappeared")
					}
				})
			}
		}
	}
}

func TestPhase2RecheckResponsesPairingKeepsIDsDistinct(t *testing.T) {
	cases := []struct {
		name, history string
		wantErr       bool
	}{
		{"duplicate_via_call_item_id", `[{"role":"user","content":"lookup"},{"type":"function_call","id":"fc_a","call_id":"call_a","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_a","output":"first"},{"type":"function_call_output","call_id":"fc_a","output":"second"}]`, true},
		{"missing_result_hidden_by_output_item_id", `[{"role":"user","content":"lookup"},{"type":"function_call","id":"fc_a","call_id":"call_a","name":"lookup","arguments":"{}"},{"type":"function_call","id":"fc_b","call_id":"call_b","name":"lookup","arguments":"{}"},{"type":"function_call_output","id":"fc_b","call_id":"call_a","output":"only a"}]`, true},
		{"valid_parallel_results", `[{"role":"user","content":"lookup"},{"type":"function_call","id":"fc_a","call_id":"call_a","name":"lookup","arguments":"{}"},{"type":"function_call","id":"fc_b","call_id":"call_b","name":"lookup","arguments":"{}"},{"type":"function_call_output","id":"out_a","call_id":"call_a","output":"a"},{"type":"function_call_output","id":"out_b","call_id":"call_b","output":"b"}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := upstream.NewSignatureCache(100)
			for _, id := range []string{"call_a", "call_b"} {
				cache.PutToolDetails(id, "lookup", map[string]any{}, "synthetic_native_signature")
			}
			_, err := phase2ReviewConvert(t, "responses", tc.history, cache)
			if tc.wantErr && err == nil {
				t.Fatal("invalid exchange accepted through item ID/call ID aliasing")
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}
