package protocols

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// TestCrossSessionIDIsolation verifies that when two separate sessions receive the same upstream tool call ID
// (e.g. call_987397), Session B's replay is not corrupted by Session A's cached tool record.
func TestCrossSessionIDIsolation(t *testing.T) {
	sharedCache := upstream.NewSignatureCache(100)

	// Session A (e.g. in shopify workspace): tool "bash" with ID "call_987397"
	sessionASig := "sig_session_a_bash_hmac"
	sharedCache.PutToolRecord(&upstream.NativeToolRecord{
		BridgeCallID:     "call_987397",
		OutputItemID:     "fc_session_a_1",
		ToolName:         "bash",
		Args:             map[string]any{"command": "git status"},
		ThoughtSignature: sessionASig,
		Model:            "gemini-2.5-pro",
		TurnID:           "turn_session_a",
		IsLegacy:         true,
	}, "fc_session_a_1")

	// Session B (e.g. in quickbooks_ex): tool "exec_command" with identical ID "call_987397"
	// Session B has its own native reasoning carrier proving that in this conversation, call_987397 was exec_command
	sessionBSig := "sig_session_b_exec_command_hmac"
	sessionBCarrier := EncodeReasoningEncryptedContent(ReasoningEncryptedState{
		Version: 1,
		Model:   "gemini-3.8-flash-high",
		TurnID:  "turn_session_b",
		Parts: []TurnPartRecord{
			{
				Index:            0,
				Kind:             PartKindToolCall,
				CallID:           "call_987397",
				ToolName:         "exec_command",
				Args:             map[string]any{"cmd": "npm test"},
				ThoughtSignature: sessionBSig,
				OutputItemID:     "fc_session_b_1",
			},
		},
		ToolSignatures: map[string]string{
			"call_987397":    sessionBSig,
			"fc_session_b_1": sessionBSig,
		},
	})

	// Session B next turn replay
	historySessionB := []any{
		map[string]any{"role": "user", "content": "run the tests"},
		map[string]any{
			"id":                "rs_session_b",
			"type":              "reasoning",
			"encrypted_content": sessionBCarrier,
		},
		map[string]any{
			"id":        "fc_session_b_1",
			"type":      "function_call",
			"call_id":   "call_987397",
			"name":      "exec_command",
			"arguments": `{"cmd":"npm test"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_987397",
			"output":  `{"status":"success"}`,
		},
	}

	rawInput, _ := json.Marshal(historySessionB)

	// Translate Session B request against the shared cache
	predReq, err := ConvertResponsesToPrediction(&ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: rawInput,
	}, sharedCache)

	if err != nil {
		t.Fatalf("Session B replay should succeed despite Session A cache collision, but got error: %v", err)
	}

	// Verify that the restored model part has Session B's signature and tool name
	var modelPart *upstream.Part
	for _, c := range predReq.Request.Contents {
		if c.Role == "model" && len(c.Parts) > 0 {
			modelPart = &c.Parts[0]
			break
		}
	}
	if modelPart == nil || modelPart.FunctionCall == nil {
		t.Fatalf("expected function call part in translated request")
	}
	if modelPart.FunctionCall.Name != "exec_command" {
		t.Errorf("got function name %q, want exec_command", modelPart.FunctionCall.Name)
	}
	if modelPart.ThoughtSignature != sessionBSig {
		t.Errorf("got signature %q, want %q", modelPart.ThoughtSignature, sessionBSig)
	}
}

// TestPiCompositeAliasResolution tests Pi's composite alias format (callID + "_" + itemID, 57 chars)
// recovering signature from cache when Pi strips the reasoning carrier.
func TestPiCompositeAliasResolution(t *testing.T) {
	cache := upstream.NewSignatureCache(100)

	callID := "call_0123456789abcdef01234567" // 29 chars
	itemID := "fc_abcdef0123456789abcdef01"   // 27 chars
	piComposite := callID + "_" + itemID        // 57 chars (safely under Pi's 64-char limit)

	piSig := "sig_pi_adapter_test"
	rec := &upstream.NativeToolRecord{
		BridgeCallID:     callID,
		OutputItemID:     itemID,
		ToolName:         "read_file",
		Args:             map[string]any{"path": "package.json"},
		ThoughtSignature: piSig,
	}
	cache.PutToolRecord(rec, itemID, piComposite)

	// Pi replays with only the composite alias as ID and call_id, without reasoning item
	historyPi := []any{
		map[string]any{"role": "user", "content": "show package"},
		map[string]any{
			"id":        piComposite,
			"type":      "function_call",
			"call_id":   piComposite,
			"name":      "read_file",
			"arguments": `{"path":"package.json"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": piComposite,
			"output":  `{"name":"my-app"}`,
		},
	}
	rawInput, _ := json.Marshal(historyPi)

	predReq, err := ConvertResponsesToPrediction(&ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: rawInput,
	}, cache)

	if err != nil {
		t.Fatalf("Pi composite alias lookup failed: %v", err)
	}

	var foundSig string
	for _, c := range predReq.Request.Contents {
		if c.Role == "model" {
			for _, p := range c.Parts {
				if p.FunctionCall != nil {
					foundSig = p.ThoughtSignature
				}
			}
		}
	}
	if foundSig != piSig {
		t.Errorf("got signature %q, want %q", foundSig, piSig)
	}
}

// TestIdentityConflictRejection verifies that if call_id and id resolve to contradictory records,
// the bridge rejects with an explicit identity conflict error.
func TestIdentityConflictRejection(t *testing.T) {
	cache := upstream.NewSignatureCache(100)

	recA := &upstream.NativeToolRecord{
		BridgeCallID:     "call_record_aaa",
		ToolName:         "tool_a",
		ThoughtSignature: "sig_a",
	}
	recB := &upstream.NativeToolRecord{
		BridgeCallID:     "call_record_bbb",
		ToolName:         "tool_b",
		ThoughtSignature: "sig_b",
	}

	cache.PutToolRecord(recA, "fc_record_aaa")
	cache.PutToolRecord(recB, "fc_record_bbb")

	// Craft conflicting tool call item where call_id points to record A, but id points to record B
	historyConflicting := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{
			"id":        "fc_record_bbb",   // resolves to call_record_bbb
			"call_id":   "call_record_aaa", // resolves to call_record_aaa
			"type":      "function_call",
			"name":      "tool_a",
			"arguments": "{}",
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_record_aaa",
			"output":  "ok",
		},
	}
	rawInput, _ := json.Marshal(historyConflicting)

	_, err := ConvertResponsesToPrediction(&ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: rawInput,
	}, cache)

	if err == nil {
		t.Fatalf("expected error on contradictory identity resolution, got nil")
	}
	if !strings.Contains(err.Error(), "tool call identity conflict") {
		t.Errorf("expected 'tool call identity conflict' in error, got %v", err)
	}
}

// TestStrictTamperingRejection verifies that modifying arguments or renaming the tool within
// a known native record triggers strict rejection.
func TestStrictTamperingRejection(t *testing.T) {
	cache := upstream.NewSignatureCache(100)

	callID := "call_strict_test_1"
	cache.PutToolRecord(&upstream.NativeToolRecord{
		BridgeCallID:     callID,
		ToolName:         "search_database",
		Args:             map[string]any{"query": "SELECT * FROM users"},
		ThoughtSignature: "sig_secret_db",
	})

	// Attacker modifies query to DROP TABLE users
	historyTampered := []any{
		map[string]any{"role": "user", "content": "search"},
		map[string]any{
			"type":      "function_call",
			"call_id":   callID,
			"name":      "search_database",
			"arguments": `{"query":"DROP TABLE users"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  "done",
		},
	}
	rawInput, _ := json.Marshal(historyTampered)

	_, err := ConvertResponsesToPrediction(&ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: rawInput,
	}, cache)

	if err == nil {
		t.Fatalf("expected tampering rejection, got success")
	}
	if !strings.Contains(err.Error(), "content mismatch against native turn record") {
		t.Errorf("expected 'content mismatch against native turn record', got: %v", err)
	}
}

// TestCarrierOnlyRecovery verifies that if the signature cache is completely lost,
// the encrypted reasoning carrier restores the record and allows successful execution.
func TestCarrierOnlyRecovery(t *testing.T) {
	emptyCache := upstream.NewSignatureCache(100)

	carrierSig := "sig_recovered_from_carrier_alone"
	carrier := EncodeReasoningEncryptedContent(ReasoningEncryptedState{
		Version: 1,
		Model:   "gemini-3.8-flash-high",
		TurnID:  "turn_carrier_recovery",
		Parts: []TurnPartRecord{
			{
				Index:            0,
				Kind:             PartKindToolCall,
				CallID:           "call_carrier_recover_1",
				ToolName:         "fetch_data",
				Args:             map[string]any{"url": "https://example.com"},
				ThoughtSignature: carrierSig,
				OutputItemID:     "fc_carrier_recover_1",
			},
		},
		ToolSignatures: map[string]string{
			"call_carrier_recover_1": carrierSig,
		},
	})

	history := []any{
		map[string]any{"role": "user", "content": "fetch"},
		map[string]any{
			"id":                "rs_recover",
			"type":              "reasoning",
			"encrypted_content": carrier,
		},
		map[string]any{
			"id":        "fc_carrier_recover_1",
			"type":      "function_call",
			"call_id":   "call_carrier_recover_1",
			"name":      "fetch_data",
			"arguments": `{"url":"https://example.com"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_carrier_recover_1",
			"output":  "data",
		},
	}
	rawInput, _ := json.Marshal(history)

	predReq, err := ConvertResponsesToPrediction(&ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: rawInput,
	}, emptyCache)

	if err != nil {
		t.Fatalf("carrier-only recovery failed: %v", err)
	}

	var foundSig string
	for _, c := range predReq.Request.Contents {
		if c.Role == "model" {
			for _, p := range c.Parts {
				if p.FunctionCall != nil {
					foundSig = p.ThoughtSignature
				}
			}
		}
	}
	if foundSig != carrierSig {
		t.Errorf("got signature %q, want %q", foundSig, carrierSig)
	}

	// Carrier-only recovery is request-local; translation must not mutate cache state
	if rec, ok := emptyCache.GetToolRecord("call_carrier_recover_1"); ok && rec != nil {
		t.Fatalf("carrier recovery should be request-local and not write to cache")
	}
}
