package protocols

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestConvertChatToPrediction(t *testing.T) {
	sigCache := upstream.NewSignatureCache(100)
	sigCache.PutToolDetails("call_tokyo_123", "get_weather", map[string]any{"city": "Tokyo"}, "sig_tokyo_test_signature")

	req := &ChatRequest{
		Model: "gemini-3.8-flash",
		Messages: []ChatMessage{
			{
				Role:    "system",
				Content: json.RawMessage(`"You are a helpful assistant."`),
			},
			{
				Role:    "user",
				Content: json.RawMessage(`"What is the weather in Tokyo?"`),
			},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_tokyo_123",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "get_weather",
							Arguments: `{"city":"Tokyo"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_tokyo_123",
				Name:       "get_weather",
				Content:    json.RawMessage(`{"weather":"Sunny"}`),
			},
			{
				Role:    "user",
				Content: json.RawMessage(`"Thanks, and what about Kyoto?"`),
			},
		},
		Tools: []ToolDefinition{
			{
				Type: "function",
				Function: struct {
					Name        string         `json:"name"`
					Description string         `json:"description,omitempty"`
					Parameters  map[string]any `json:"parameters,omitempty"`
				}{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters: map[string]any{
						"type": "object",
					},
				},
			},
		},
	}

	predReq, err := ConvertChatToPrediction(req, sigCache)
	if err != nil {
		t.Fatalf("ConvertChatToPrediction failed: %v", err)
	}

	// 1. Check default model has high reasoning
	if predReq.Model != "gemini-3.8-flash-high" {
		t.Errorf("got model %q, want gemini-3.8-flash-high", predReq.Model)
	}
	if predReq.Request.GenerationConfig.ThinkingConfig.ThinkingLevel != "high" {
		t.Errorf("got thinkingLevel %q, want high", predReq.Request.GenerationConfig.ThinkingConfig.ThinkingLevel)
	}

	// 2. Check system instruction
	if predReq.Request.SystemInstruction == nil || len(predReq.Request.SystemInstruction.Parts) == 0 {
		t.Fatalf("missing system instruction")
	}
	if predReq.Request.SystemInstruction.Parts[0].Text != "You are a helpful assistant." {
		t.Errorf("system text mismatch: %v", predReq.Request.SystemInstruction.Parts[0].Text)
	}

	// 3. Check tool definitions
	if len(predReq.Request.Tools) != 1 || len(predReq.Request.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools not converted properly")
	}
	if predReq.Request.Tools[0].FunctionDeclarations[0].Name != "get_weather" {
		t.Errorf("tool name mismatch: %s", predReq.Request.Tools[0].FunctionDeclarations[0].Name)
	}

	// 4. Check contents: should alternate user, model, user
	contents := predReq.Request.Contents
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents after normalization, got %d", len(contents))
	}
	if contents[0].Role != "user" || contents[1].Role != "model" || contents[2].Role != "user" {
		t.Errorf("contents roles not alternating: %v, %v, %v", contents[0].Role, contents[1].Role, contents[2].Role)
	}

	// 5. Verify thought signature was restored for the tool call!
	modelContent := contents[1]
	if len(modelContent.Parts) != 1 || modelContent.Parts[0].FunctionCall == nil {
		t.Fatalf("model turn does not contain functionCall")
	}
	fcPart := modelContent.Parts[0]
	if fcPart.ThoughtSignature != "sig_tokyo_test_signature" {
		t.Errorf("thoughtSignature was not restored from cache! Got %q", fcPart.ThoughtSignature)
	}
}

func TestNormalizeAlternatingContents(t *testing.T) {
	input := []upstream.Content{
		{Role: "user", Parts: []upstream.Part{{Text: "msg 1"}}},
		{Role: "user", Parts: []upstream.Part{{Text: "msg 2"}}},
		{Role: "model", Parts: []upstream.Part{{Text: "reply 1"}}},
		{Role: "user", Parts: []upstream.Part{{FunctionResponse: &upstream.FunctionResponse{Name: "fn"}}}},
		{Role: "user", Parts: []upstream.Part{{Text: "msg 3"}}},
	}

	merged := NormalizeAlternatingContents(input)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged turns, got %d", len(merged))
	}

	if len(merged[0].Parts) != 2 {
		t.Errorf("expected 2 parts in first user turn, got %d", len(merged[0].Parts))
	}
	if len(merged[1].Parts) != 1 {
		t.Errorf("expected 1 part in model turn, got %d", len(merged[1].Parts))
	}
	if len(merged[2].Parts) != 2 {
		t.Errorf("expected 2 parts in final user turn, got %d", len(merged[2].Parts))
	}
}

func TestConvertResponsesToPrediction(t *testing.T) {
	sigCache := upstream.NewSignatureCache(100)
	sigCache.PutToolDetails("call_calc_999", "calc", map[string]any{"expr": "5+5"}, "sig_calc_signature_xyz")

	req := &ResponsesRequest{
		Model:        "gemini-3.8-flash",
		Instructions: "System instruction for codex",
		Input: json.RawMessage(`[
			{"role": "user", "content": "Calculate 5+5"},
			{"type": "function_call", "name": "calc", "call_id": "call_calc_999", "arguments": "{\"expr\":\"5+5\"}"},
			{"type": "function_call_output", "call_id": "call_calc_999", "output": "10"}
		]`),
	}

	predReq, err := ConvertResponsesToPrediction(req, sigCache)
	if err != nil {
		t.Fatalf("ConvertResponsesToPrediction failed: %v", err)
	}

	if predReq.Model != "gemini-3.8-flash-high" {
		t.Errorf("got model %q, want gemini-3.8-flash-high", predReq.Model)
	}
	if predReq.Request.SystemInstruction == nil || predReq.Request.SystemInstruction.Parts[0].Text != "System instruction for codex" {
		t.Errorf("instructions not set")
	}

	// Should have: user ("Calculate 5+5"), model (function_call with restored sig), user (function_call_output)
	contents := predReq.Request.Contents
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(contents))
	}

	modelTurn := contents[1]
	if len(modelTurn.Parts) != 1 || modelTurn.Parts[0].FunctionCall == nil {
		t.Fatalf("missing functionCall in model turn")
	}
	if modelTurn.Parts[0].ThoughtSignature != "sig_calc_signature_xyz" {
		t.Errorf("failed to restore thoughtSignature on Responses tool call: got %q", modelTurn.Parts[0].ThoughtSignature)
	}
}

func TestConvertResponsesToPredictionMissingSignatureFailsExplicitly(t *testing.T) {
	// Empty cache - simulates restart or client replay of uncompleted tool call without output
	sigCache := upstream.NewSignatureCache(100)

	req := &ResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: json.RawMessage(`[
			{"role": "user", "content": "Run bash command"},
			{"type": "function_call", "name": "default_api:bash", "call_id": "call_uncached_86", "arguments": "{\"command\":\"df -h\"}"}
		]`),
	}

	_, err := ConvertResponsesToPrediction(req, sigCache)
	if err == nil {
		t.Fatalf("expected error for unresolvable tool call signature, got nil")
	}
	if !strings.Contains(err.Error(), "missing required cryptographic thought signature") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestConvertChatToPredictionMissingSignatureFailsExplicitly(t *testing.T) {
	// Empty cache - simulates restart or client replay of uncompleted tool call without output
	sigCache := upstream.NewSignatureCache(100)

	req := &ChatRequest{
		Model: "gemini-3.8-flash",
		Messages: []ChatMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Run bash command"`),
			},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_uncached_chat",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "default_api:bash",
							Arguments: `{"command":"df -h"}`,
						},
					},
				},
			},
		},
	}

	_, err := ConvertChatToPrediction(req, sigCache)
	if err == nil {
		t.Fatalf("expected error for unresolvable tool call signature, got nil")
	}
	if !strings.Contains(err.Error(), "missing required cryptographic thought signature") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestConvertResponsesToPredictionCompletedEarlierTurnWithoutSignature(t *testing.T) {
	sigCache := upstream.NewSignatureCache(100)
	req := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "List files"},
			{"type": "function_call", "name": "bash", "call_id": "call_past_1", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_past_1", "output": "a.txt\nb.txt"},
			{"role": "user", "content": "What did you find?"}
		]`),
	}

	pred, err := ConvertResponsesToPrediction(req, sigCache)
	if err != nil {
		t.Fatalf("completed earlier turn without signature should succeed, got: %v", err)
	}

	// Verify structured history is preserved
	if len(pred.Request.Contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(pred.Request.Contents))
	}
	if pred.Request.Contents[1].Role != "model" || pred.Request.Contents[1].Parts[0].FunctionCall == nil {
		t.Errorf("contents[1] want model functionCall, got %+v", pred.Request.Contents[1])
	}
	if got := pred.Request.Contents[1].Parts[0].ThoughtSignature; got != "skip_thought_signature_validator" {
		t.Errorf("contents[1] want thoughtSignature skip_thought_signature_validator, got %q", got)
	}
	if pred.Request.Contents[2].Role != "user" || pred.Request.Contents[2].Parts[0].FunctionResponse == nil {
		t.Errorf("contents[2] want user functionResponse, got %+v", pred.Request.Contents[2])
	}
}

func TestConvertChatToPredictionCompletedEarlierTurnWithoutSignature(t *testing.T) {
	sigCache := upstream.NewSignatureCache(100)
	req := &ChatRequest{
		Model: "gemini-3.8-flash-high",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"List files"`)},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_past_chat_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "bash",
							Arguments: `{"cmd":"ls"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_past_chat_1", Name: "bash", Content: json.RawMessage(`"a.txt\nb.txt"`)},
			{Role: "user", Content: json.RawMessage(`"What did you find?"`)},
		},
	}

	pred, err := ConvertChatToPrediction(req, sigCache)
	if err != nil {
		t.Fatalf("completed earlier turn without signature should succeed in chat, got: %v", err)
	}

	if len(pred.Request.Contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(pred.Request.Contents))
	}
	if pred.Request.Contents[1].Role != "model" || pred.Request.Contents[1].Parts[0].FunctionCall == nil {
		t.Errorf("contents[1] want model functionCall, got %+v", pred.Request.Contents[1])
	}
	if got := pred.Request.Contents[1].Parts[0].ThoughtSignature; got != "skip_thought_signature_validator" {
		t.Errorf("contents[1] want thoughtSignature skip_thought_signature_validator, got %q", got)
	}
	if pred.Request.Contents[2].Role != "user" || pred.Request.Contents[2].Parts[0].FunctionResponse == nil {
		t.Errorf("contents[2] want user functionResponse, got %+v", pred.Request.Contents[2])
	}
}

func TestConvertResponsesToPredictionMigrationMarkerInCurrentTurn(t *testing.T) {
	sigCache := upstream.NewSignatureCache(100)
	req := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "Execute tool"},
			{"type": "function_call", "name": "bash", "call_id": "call_migrated_1", "arguments": "{\"cmd\":\"ls\"}", "thought_signature": "skip_thought_signature_validator"},
			{"type": "function_call_output", "call_id": "call_migrated_1", "output": "file.txt"}
		]`),
	}

	pred, err := ConvertResponsesToPrediction(req, sigCache)
	if err != nil {
		t.Fatalf("current turn with skip_thought_signature_validator should succeed, got: %v", err)
	}

	if got := pred.Request.Contents[1].Parts[0].ThoughtSignature; got != "skip_thought_signature_validator" {
		t.Errorf("got thoughtSignature %q, want skip_thought_signature_validator", got)
	}
}

func TestConvertResponsesToPredictionHistoricalUnsignedChain(t *testing.T) {
	sigCache := upstream.NewSignatureCache(100)
	// Simulate multi-turn session with historical tool calls from a foreign model (e.g., OpenCode position 86)
	req := &ResponsesRequest{
		Model: "gemini-3.8-flash-high",
		Input: json.RawMessage(`[
			{"role": "user", "content": "Initial prompt"},
			{"type": "function_call", "name": "default_api:bash", "call_id": "call_foreign_1", "arguments": "{\"command\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_foreign_1", "output": "output 1"},
			{"type": "function_call", "name": "default_api:bash", "call_id": "call_foreign_2", "arguments": "{\"command\":\"cat file\"}"},
			{"type": "function_call_output", "call_id": "call_foreign_2", "output": "output 2"},
			{"role": "user", "content": "Follow up user message"}
		]`),
	}

	pred, err := ConvertResponsesToPrediction(req, sigCache)
	if err != nil {
		t.Fatalf("historical chain without signatures should succeed, got: %v", err)
	}

	// Verify all historical functionCall parts have skip_thought_signature_validator
	callCount := 0
	for _, c := range pred.Request.Contents {
		if c.Role == "model" {
			for _, p := range c.Parts {
				if p.FunctionCall != nil {
					callCount++
					if p.ThoughtSignature != "skip_thought_signature_validator" {
						t.Errorf("call %s has signature %q, want skip_thought_signature_validator", p.FunctionCall.Name, p.ThoughtSignature)
					}
				}
			}
		}
	}
	if callCount != 2 {
		t.Errorf("expected 2 historical function calls, found %d", callCount)
	}
}


