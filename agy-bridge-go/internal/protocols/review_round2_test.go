package protocols

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func round2Foreign(t *testing.T, name string) *ResponsesRequest {
	t.Helper()
	items := []any{
		map[string]any{"role": "user", "content": "run"},
		map[string]any{"type": "function_call", "call_id": "call_foreign_shared", "name": name, "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "call_foreign_shared", "output": "done"},
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return &ResponsesRequest{Model: "gemini-3.8-flash-high", Input: raw}
}

func TestRound2MigrationDoesNotCreateNativeOwnership(t *testing.T) {
	cache := upstream.NewSignatureCache(100)
	if _, err := ConvertResponsesToPrediction(round2Foreign(t, "foreign_tool_a"), cache); err != nil {
		t.Fatal(err)
	}
	if rec, ok := cache.GetToolRecord("call_foreign_shared"); ok {
		t.Errorf("migration marker stored as native ownership: signature=%q legacy=%v", rec.ThoughtSignature, rec.IsLegacy)
	}
	if _, err := ConvertResponsesToPrediction(round2Foreign(t, "foreign_tool_b"), cache); err != nil {
		t.Errorf("unrelated imported history now rejected: %v", err)
	}
}

func TestRound2CarrierCannotReplaceOwnedSignature(t *testing.T) {
	cache := upstream.NewSignatureCache(100)
	turn := reviewTurn("exec_command", "pwd", "sig_original")
	turn.PopulateCache(cache, "")
	altered := turn.Parts[0]
	altered.ThoughtSignature = "unverified_client_replacement"
	carrier := EncodeReasoningEncryptedContent(ReasoningEncryptedState{Version: 1, Parts: []TurnPartRecord{altered}})
	_, _ = ConvertResponsesToPrediction(reviewReplay(t, turn, false, carrier), cache)
	rec, ok := cache.GetToolRecord(turn.Parts[0].CallID)
	if !ok || rec.ThoughtSignature != "sig_original" {
		t.Fatalf("client carrier replaced authoritative signature: %+v", rec)
	}
}

func TestRound2NewUpstreamAliasDoesNotHideLegacyOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	snapshot := `{"tool_sigs":{"call_987397":"sig_legacy"},"tool_names":{"call_987397":"lookup"},"tool_args":{"call_987397":{"cmd":"original"}}}`
	if err := os.WriteFile(path, []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	cache := upstream.NewSignatureCache(100)
	if err := cache.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	reviewTurn("exec_command", "new", "sig_new").PopulateCache(cache, "")
	if _, ok := cache.GetToolRecord("call_987397"); !ok {
		t.Errorf("new call's upstream alias hid an existing legacy canonical record")
	}
	legacy := &AuthoritativeTurn{Parts: []TurnPartRecord{{Kind: PartKindToolCall, CallID: "call_987397", ToolName: "lookup", Args: map[string]any{"cmd": "ALTERED"}}}}
	pred, err := ConvertResponsesToPrediction(reviewReplay(t, legacy, false, ""), cache)
	if err == nil {
		for _, c := range pred.Request.Contents {
			for _, p := range c.Parts {
				if p.FunctionCall != nil {
					t.Fatalf("altered legacy call accepted with signature %q", p.ThoughtSignature)
				}
			}
		}
		t.Fatal("altered legacy call not rejected")
	}
}

func TestRound2RepeatedStreamCallHasOneOutputItem(t *testing.T) {
	var buf strings.Builder
	for i, cmd := range []string{"", "pwd"} {
		event := upstream.SSEStreamEvent{Response: &upstream.PredictionResponse{Candidates: []upstream.Candidate{{Content: upstream.Content{Role: "model", Parts: []upstream.Part{{FunctionCall: &upstream.FunctionCall{ID: "call_upstream_one", Name: "exec_command", Args: map[string]any{"cmd": cmd}}, ThoughtSignature: "sig_one"}}}}}}}
		if i == 1 {
			event.Response.Candidates[0].FinishReason = "STOP"
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		buf.WriteString("data: " + string(raw) + "\n\n")
	}
	w := httptest.NewRecorder()
	handleStreamingResponses(w, strings.NewReader(buf.String()), "resp_round2", "msg_round2", 0, "gemini-3.8-flash-high", upstream.NewSignatureCache(100))
	started, finalCalls := 0, 0
	for _, e := range auditEvents(t, w.Body.String()) {
		if e["type"] == "response.output_item.added" && e["item"].(map[string]any)["type"] == "function_call" {
			started++
		}
		if e["type"] == "response.completed" {
			for _, raw := range e["response"].(map[string]any)["output"].([]any) {
				if raw.(map[string]any)["type"] == "function_call" {
					finalCalls++
				}
			}
		}
	}
	if started != 1 || finalCalls != 1 {
		t.Fatalf("one logical call emitted %d added events but %d final calls", started, finalCalls)
	}
}
