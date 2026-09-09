package protocols

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestRound3OwnedContentCannotBeRewrittenViaCarrier(t *testing.T) {
	for _, tc := range []struct{ name, tool, cmd, sig string }{
		{"arguments", "exec_command", "ALTERED", "sig_original"},
		{"name", "other_tool", "pwd", "sig_original"},
		{"arguments_and_signature", "exec_command", "ALTERED", "unverified_replacement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := upstream.NewSignatureCache(100)
			original := reviewTurn("exec_command", "pwd", "sig_original")
			original.PopulateCache(cache, "")
			altered := original.Parts[0]
			altered.ToolName = tc.tool
			altered.Args = map[string]any{"cmd": tc.cmd}
			altered.ThoughtSignature = tc.sig
			carrier := EncodeReasoningEncryptedContent(ReasoningEncryptedState{Version: 1, Parts: []TurnPartRecord{altered}})
			req := reviewReplay(t, &AuthoritativeTurn{Parts: []TurnPartRecord{altered}}, false, carrier)
			pred, err := ConvertResponsesToPrediction(req, cache)
			if err == nil {
				for _, c := range pred.Request.Contents {
					for _, p := range c.Parts {
						if p.FunctionCall != nil {
							t.Fatalf("modified owned call forwarded: name=%s args=%v signature=%s", p.FunctionCall.Name, p.FunctionCall.Args, p.ThoughtSignature)
						}
					}
				}
				t.Fatal("expected owned-content mismatch rejection")
			}
		})
	}
}

func TestRound3InterleavedStreamDoneMatchesFinalArguments(t *testing.T) {
	var buf strings.Builder
	updates := []struct{ id, cmd string }{{"call_A", "draft"}, {"call_B", "b"}, {"call_A", "final"}}
	for i, u := range updates {
		event := upstream.SSEStreamEvent{Response: &upstream.PredictionResponse{Candidates: []upstream.Candidate{{Content: upstream.Content{Role: "model", Parts: []upstream.Part{{FunctionCall: &upstream.FunctionCall{ID: u.id, Name: "exec_command", Args: map[string]any{"cmd": u.cmd}}, ThoughtSignature: "sig_" + u.id}}}}}}}
		if i == len(updates)-1 {
			event.Response.Candidates[0].FinishReason = "STOP"
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		buf.WriteString("data: " + string(raw) + "\n\n")
	}
	w := httptest.NewRecorder()
	handleStreamingResponses(w, strings.NewReader(buf.String()), "resp_round3", "msg_round3", 0, "gemini-3.8-flash-high", upstream.NewSignatureCache(100))
	done := map[string]string{}
	var final map[string]any
	for _, e := range auditEvents(t, w.Body.String()) {
		if e["type"] == "response.function_call_arguments.done" {
			done[e["call_id"].(string)] = e["arguments"].(string)
		}
		if e["type"] == "response.completed" {
			final = e["response"].(map[string]any)
		}
	}
	if final == nil {
		t.Fatal("missing completed response")
	}
	for _, raw := range final["output"].([]any) {
		item := raw.(map[string]any)
		if item["type"] == "function_call" {
			id := item["call_id"].(string)
			if done[id] != item["arguments"] {
				t.Errorf("call %s: streamed done arguments=%s, final response arguments=%s", id, done[id], item["arguments"])
			}
		}
	}
}
