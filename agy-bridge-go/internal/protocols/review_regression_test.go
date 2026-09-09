package protocols

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func reviewTurn(name, command, sig string) *AuthoritativeTurn {
	return BuildAuthoritativeTurn([]upstream.Part{{
		FunctionCall:     &upstream.FunctionCall{ID: "call_987397", Name: name, Args: map[string]any{"cmd": command}},
		ThoughtSignature: sig,
	}}, "", "gemini-3.8-flash-high")
}

func reviewReplay(t *testing.T, turn *AuthoritativeTurn, pi bool, carrier string) *ResponsesRequest {
	t.Helper()
	items := []any{map[string]any{"role": "user", "content": "run the command"}}
	if carrier != "" {
		items = append(items, map[string]any{"type": "reasoning", "encrypted_content": carrier})
	}
	for _, part := range turn.Parts {
		if part.Kind != PartKindToolCall {
			continue
		}
		id := part.CallID
		if pi {
			id += "_" + part.OutputItemID
		}
		args, err := json.Marshal(part.Args)
		if err != nil {
			t.Fatal(err)
		}
		call := map[string]any{"type": "function_call", "call_id": id, "name": part.ToolName, "arguments": string(args)}
		if !pi {
			call["id"] = part.OutputItemID
		}
		items = append(items, call, map[string]any{"type": "function_call_output", "call_id": id, "output": "done"})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return &ResponsesRequest{Model: "gemini-3.8-flash-high", Input: raw}
}

func TestReviewGeneratedIDsAreDistinct(t *testing.T) {
	a := reviewTurn("bash", "pwd", "sig_a")
	b := reviewTurn("exec_command", "ls", "sig_b")
	if a.Parts[0].CallID == b.Parts[0].CallID {
		t.Fatalf("independent turns emitted the same public ID: %s", a.Parts[0].CallID)
	}
}

func TestReviewPiReplayAfterOtherSession(t *testing.T) {
	for _, tc := range []struct{ name, tool, cmd string }{
		{"different_tools", "bash", "ls"},
		{"same_tool_different_arguments", "exec_command", "ls"},
		{"same_tool_same_arguments", "exec_command", "pwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := upstream.NewSignatureCache(100)
			a := reviewTurn("exec_command", "pwd", "sig_a")
			b := reviewTurn(tc.tool, tc.cmd, "sig_b")
			a.PopulateCache(cache, "")
			b.PopulateCache(cache, "")
			path := filepath.Join(t.TempDir(), "cache.json")
			if err := cache.SaveToFile(path); err != nil {
				t.Fatal(err)
			}
			cache = upstream.NewSignatureCache(100)
			if err := cache.LoadFromFile(path); err != nil {
				t.Fatal(err)
			}
			pred, err := ConvertResponsesToPrediction(reviewReplay(t, a, true, ""), cache)
			if err != nil {
				t.Fatalf("Session A failed after Session B and restart: %v", err)
			}
			for _, content := range pred.Request.Contents {
				for _, part := range content.Parts {
					if part.FunctionCall != nil && part.ThoughtSignature != "sig_a" {
						t.Fatalf("Session A received another session's signature: %q", part.ThoughtSignature)
					}
				}
			}
		})
	}
}

func TestReviewRejectedCarrierDoesNotOverwriteCache(t *testing.T) {
	cache := upstream.NewSignatureCache(100)
	original := reviewTurn("exec_command", "pwd", "sig_original")
	original.PopulateCache(cache, "")
	changed := original.Parts[0]
	changed.ToolName = "other_tool"
	changed.ThoughtSignature = "unverified_replacement"
	carrier := EncodeReasoningEncryptedContent(ReasoningEncryptedState{Version: 1, Parts: []TurnPartRecord{changed}})
	_, err := ConvertResponsesToPrediction(reviewReplay(t, original, false, carrier), cache)
	if err == nil {
		t.Fatal("expected carrier mismatch rejection")
	}
	rec, ok := cache.GetToolRecord(original.Parts[0].CallID)
	if !ok || rec.ToolName != "exec_command" || rec.ThoughtSignature != "sig_original" {
		t.Fatalf("rejected request overwrote native record: %+v", rec)
	}
}

func TestReviewLegacyUnsignedSiblingKeepsArgumentValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	snapshot := `{"tool_sigs":{"call_lead":"sig_lead"},"tool_names":{"call_lead":"lookup","call_sibling":"lookup"},"tool_args":{"call_lead":{"key":"lead"},"call_sibling":{"key":"original"}},"turn_siblings":{"call_lead":["call_sibling"]}}`
	if err := os.WriteFile(path, []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	cache := upstream.NewSignatureCache(100)
	if err := cache.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	var req ChatRequest
	raw := `{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"look up both"},{"role":"assistant","tool_calls":[{"id":"call_lead","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"lead\"}"}},{"id":"call_sibling","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"ALTERED\"}"}}]},{"role":"tool","tool_call_id":"call_lead","content":"done"},{"role":"tool","tool_call_id":"call_sibling","content":"done"}]}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	pred, err := ConvertChatToPrediction(&req, cache)
	if err == nil {
		for _, content := range pred.Request.Contents {
			for _, part := range content.Parts {
				if part.FunctionCall != nil && part.FunctionCall.ID == "call_sibling" {
					t.Fatalf("altered legacy sibling accepted with signature %q", part.ThoughtSignature)
				}
			}
		}
		t.Fatal("altered legacy sibling was not rejected")
	}
}
