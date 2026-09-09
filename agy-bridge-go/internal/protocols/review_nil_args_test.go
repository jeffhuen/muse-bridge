package protocols

import (
	"encoding/json"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestReviewNativeNoArgsCannotAcquireArguments(t *testing.T) {
	for _, initial := range []struct {
		name string
		args map[string]any
	}{{"omitted", nil}, {"empty_object", map[string]any{}}} {
		for _, endpoint := range []string{"responses", "chat"} {
			for _, carrier := range []bool{false, true} {
				label := initial.name + "/" + endpoint
				if carrier {
					label += "/carrier"
				} else {
					label += "/cache_only"
				}
				t.Run(label, func(t *testing.T) {
					cache := upstream.NewSignatureCache(100)
					turn := BuildAuthoritativeTurn([]upstream.Part{{
						FunctionCall:     &upstream.FunctionCall{ID: "call_upstream", Name: "get_status", Args: initial.args},
						ThoughtSignature: "sig_original",
					}}, "", "gemini-3.8-flash-high")
					turn.PopulateCache(cache, "")
					part := turn.Parts[0]
					if rec, ok := cache.GetToolRecord(part.CallID); !ok || rec.IsLegacy {
						t.Fatal("fixture must own a non-legacy native record")
					}
					translate := func(p TurnPartRecord) (*upstream.PredictionRequest, error) {
						if endpoint == "responses" {
							enc := ""
							if carrier {
								enc = EncodeReasoningEncryptedContent(ReasoningEncryptedState{Version: 1, Parts: []TurnPartRecord{p}})
							}
							return ConvertResponsesToPrediction(reviewReplay(t, &AuthoritativeTurn{Parts: []TurnPartRecord{p}}, false, enc), cache)
						}
						args, _ := json.Marshal(p.Args)
						call := map[string]any{"id": p.CallID, "type": "function", "function": map[string]any{"name": p.ToolName, "arguments": string(args)}}
						if carrier {
							call["thought_signature"] = p.ThoughtSignature
						}
						raw, _ := json.Marshal(map[string]any{"model": "gemini-3.8-flash-high", "messages": []any{
							map[string]any{"role": "user", "content": "get status"},
							map[string]any{"role": "assistant", "tool_calls": []any{call}},
							map[string]any{"role": "tool", "tool_call_id": p.CallID, "content": "done"},
						}})
						var req ChatRequest
						if err := json.Unmarshal(raw, &req); err != nil {
							t.Fatal(err)
						}
						return ConvertChatToPrediction(&req, cache)
					}
					if _, err := translate(part); err != nil {
						t.Fatalf("unchanged replay rejected: %v", err)
					}
					part.Args = map[string]any{"extra": "changed"}
					pred, err := translate(part)
					if err == nil {
						for _, content := range pred.Request.Contents {
							for _, p := range content.Parts {
								if p.FunctionCall != nil {
									t.Fatalf("accepted added arguments %v with signature %q", p.FunctionCall.Args, p.ThoughtSignature)
								}
							}
						}
						t.Fatal("expected native argument mismatch")
					}
				})
			}
		}
	}
}
