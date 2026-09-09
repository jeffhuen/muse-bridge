package protocols

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// TestPhase2_Row1_NativeValidReplay verifies that matching native records with sufficient state
// replay their authentic cryptographic thought signatures.
func TestPhase2_Row1_NativeValidReplay(t *testing.T) {
	cache := upstream.NewSignatureCache(100)
	cache.PutToolDetails("call_native_1", "lookup_file", map[string]any{"path": "main.go"}, "sig_authentic_gemini_123")

	// 1. Responses format
	respReq := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "read file"},
			{"type": "function_call", "name": "lookup_file", "call_id": "call_native_1", "arguments": "{\"path\":\"main.go\"}"},
			{"type": "function_call_output", "call_id": "call_native_1", "output": "package main"}
		]`),
	}
	predResp, err := ConvertResponsesToPrediction(respReq, cache)
	if err != nil {
		t.Fatalf("native Responses replay failed: %v", err)
	}
	if got := predResp.Request.Contents[1].Parts[0].ThoughtSignature; got != "sig_authentic_gemini_123" {
		t.Errorf("expected authentic signature %q, got %q", "sig_authentic_gemini_123", got)
	}

	// 2. Chat format
	chatReq := &ChatRequest{
		Model: "gemini-3.8-flash-high",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"read file"`)},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_native_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "lookup_file",
							Arguments: `{"path":"main.go"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_native_1", Name: "lookup_file", Content: json.RawMessage(`"package main"`)},
		},
	}
	predChat, err := ConvertChatToPrediction(chatReq, cache)
	if err != nil {
		t.Fatalf("native Chat replay failed: %v", err)
	}
	if got := predChat.Request.Contents[1].Parts[0].ThoughtSignature; got != "sig_authentic_gemini_123" {
		t.Errorf("expected authentic signature %q, got %q", "sig_authentic_gemini_123", got)
	}
}

// TestPhase2_Row2_KnownNativeMismatchRejection verifies that known native records with altered content
// or unverified adjacent injections are strictly rejected and never fall back to migration markers.
func TestPhase2_Row2_KnownNativeMismatchRejection(t *testing.T) {
	cache := upstream.NewSignatureCache(100)
	cache.PutToolDetails("call_tampered_1", "lookup_file", map[string]any{"path": "main.go"}, "sig_authentic_123")

	// A: Content Mismatch (altered arguments)
	respReq := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "read file"},
			{"type": "function_call", "name": "lookup_file", "call_id": "call_tampered_1", "arguments": "{\"path\":\"hacked.go\"}"},
			{"type": "function_call_output", "call_id": "call_tampered_1", "output": "package evil"}
		]`),
	}
	_, err := ConvertResponsesToPrediction(respReq, cache)
	if err == nil {
		t.Fatal("expected content mismatch rejection, got nil")
	}
	if !strings.Contains(err.Error(), "content mismatch against native turn record") {
		t.Errorf("unexpected error: %v", err)
	}

	// B: Content Mismatch in Chat
	chatReq := &ChatRequest{
		Model: "gemini-3.8-flash-high",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"read file"`)},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_tampered_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "lookup_file",
							Arguments: `{"path":"hacked.go"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_tampered_1", Name: "lookup_file", Content: json.RawMessage(`"package evil"`)},
		},
	}
	_, err = ConvertChatToPrediction(chatReq, cache)
	if err == nil {
		t.Fatal("expected Chat content mismatch rejection, got nil")
	}
	if !strings.Contains(err.Error(), "content mismatch against native turn record") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPhase2_Row3_AffirmativelyImportedCallWithResult verifies that foreign tool calls
// (including within the current turn during mid-tool-chain model switching) receive the migration marker.
func TestPhase2_Row3_AffirmativelyImportedCallWithResult(t *testing.T) {
	cache := upstream.NewSignatureCache(100)

	// 1. Current-turn Responses request with explicit foreign provider
	respReq := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "run calculation"},
			{"type": "function_call", "name": "calc", "call_id": "call_claude_1", "arguments": "{\"expr\":\"2+2\"}", "provider": "anthropic"},
			{"type": "function_call_output", "call_id": "call_claude_1", "output": "4"}
		]`),
	}
	predResp, err := ConvertResponsesToPrediction(respReq, cache)
	if err != nil {
		t.Fatalf("affirmatively foreign tool call failed: %v", err)
	}
	if got := predResp.Request.Contents[1].Parts[0].ThoughtSignature; got != "skip_thought_signature_validator" {
		t.Errorf("expected skip_thought_signature_validator, got %q", got)
	}

	// 2. Current-turn Chat request with explicit foreign model metadata
	chatReq := &ChatRequest{
		Model: "gemini-3.8-flash-high",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"run calculation"`)},
			{
				Role:  "assistant",
				Model: "claude-3-5-sonnet",
				ToolCalls: []ToolCall{
					{
						ID:   "call_claude_chat_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "calc",
							Arguments: `{"expr":"2+2"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_claude_chat_1", Name: "calc", Content: json.RawMessage(`"4"`)},
		},
	}
	predChat, err := ConvertChatToPrediction(chatReq, cache)
	if err != nil {
		t.Fatalf("affirmatively foreign Chat tool call failed: %v", err)
	}
	if got := predChat.Request.Contents[1].Parts[0].ThoughtSignature; got != "skip_thought_signature_validator" {
		t.Errorf("expected skip_thought_signature_validator, got %q", got)
	}
}

// TestPhase2_Row4_UnknownOriginCallWithResult verifies that unknown-origin completed calls
// (such as history from unmodified harnesses like OpenCode switching models) receive migration recovery.
func TestPhase2_Row4_UnknownOriginCallWithResult(t *testing.T) {
	cache := upstream.NewSignatureCache(100)

	// 1. Current-turn unknown origin in Responses
	respReq := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "what is disk space?"},
			{"type": "function_call", "name": "bash", "call_id": "call_unknown_resp_1", "arguments": "{\"cmd\":\"df -h\"}"},
			{"type": "function_call_output", "call_id": "call_unknown_resp_1", "output": "/dev/disk1s1 500GB"}
		]`),
	}
	predResp, err := ConvertResponsesToPrediction(respReq, cache)
	if err != nil {
		t.Fatalf("unknown origin with result should receive migration recovery: %v", err)
	}
	if got := predResp.Request.Contents[1].Parts[0].ThoughtSignature; got != "skip_thought_signature_validator" {
		t.Errorf("expected skip_thought_signature_validator, got %q", got)
	}

	// 2. Current-turn unknown origin in Chat
	chatReq := &ChatRequest{
		Model: "gemini-3.8-flash-high",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"what is disk space?"`)},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_unknown_chat_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "bash",
							Arguments: `{"cmd":"df -h"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_unknown_chat_1", Name: "bash", Content: json.RawMessage(`"/dev/disk1s1 500GB"`)},
		},
	}
	predChat, err := ConvertChatToPrediction(chatReq, cache)
	if err != nil {
		t.Fatalf("unknown origin in Chat with result should receive migration recovery: %v", err)
	}
	if got := predChat.Request.Contents[1].Parts[0].ThoughtSignature; got != "skip_thought_signature_validator" {
		t.Errorf("expected skip_thought_signature_validator, got %q", got)
	}
}

// TestPhase2_Row5_IncompleteCallResultPairingRejection verifies that incomplete pairings
// (tool calls without corresponding tool results) return actionable validation errors.
func TestPhase2_Row5_IncompleteCallResultPairingRejection(t *testing.T) {
	cache := upstream.NewSignatureCache(100)

	// 1. Incomplete pairing in Responses (call without output)
	respReq := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "run command"},
			{"type": "function_call", "name": "bash", "call_id": "call_orphaned_1", "arguments": "{\"cmd\":\"uptime\"}"}
		]`),
	}
	_, err := ConvertResponsesToPrediction(respReq, cache)
	if err == nil {
		t.Fatal("expected error for incomplete call/result pairing in Responses, got nil")
	}
	if !strings.Contains(err.Error(), "unknown-origin call has no corresponding tool result") {
		t.Errorf("expected actionable error message for incomplete pairing, got: %v", err)
	}

	// 2. Incomplete pairing for affirmatively foreign call
	respReqForeign := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "run command"},
			{"type": "function_call", "name": "bash", "call_id": "call_foreign_orphan", "arguments": "{\"cmd\":\"uptime\"}", "provider": "anthropic"}
		]`),
	}
	_, err = ConvertResponsesToPrediction(respReqForeign, cache)
	if err == nil {
		t.Fatal("expected error for incomplete foreign call/result pairing, got nil")
	}
	if !strings.Contains(err.Error(), "imported foreign call has no corresponding tool result") {
		t.Errorf("expected actionable error message for foreign incomplete pairing, got: %v", err)
	}

	// 3. Incomplete pairing in Chat (call without tool response message)
	chatReq := &ChatRequest{
		Model: "gemini-3.8-flash-high",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"run command"`)},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_orphaned_chat_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "bash",
							Arguments: `{"cmd":"uptime"}`,
						},
					},
				},
			},
		},
	}
	_, err = ConvertChatToPrediction(chatReq, cache)
	if err == nil {
		t.Fatal("expected error for incomplete call/result pairing in Chat, got nil")
	}
	if !strings.Contains(err.Error(), "unknown-origin call has no corresponding tool result") {
		t.Errorf("expected actionable error message for incomplete pairing in Chat, got: %v", err)
	}
}
