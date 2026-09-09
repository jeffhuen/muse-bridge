package protocols

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestStateOwnershipExactToolSignatureRequired(t *testing.T) {
	cache := upstream.NewSignatureCache(100)

	// 1. Chat format with missing tool signature (incomplete pairing)
	chatReqMissing := &ChatRequest{
		Model: "gemini-3.8-flash",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Execute command"`)},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_ownership_chat_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "run_terminal",
							Arguments: `{"cmd":"ls"}`,
						},
					},
				},
			},
		},
	}

	_, err := ConvertChatToPrediction(chatReqMissing, cache)
	if err == nil {
		t.Fatal("expected error for Chat tool call missing signature, got nil")
	}
	if !strings.Contains(err.Error(), "missing required cryptographic thought signature") {
		t.Fatalf("unexpected error message: %v", err)
	}

	chatReq := &ChatRequest{
		Model: "gemini-3.8-flash",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Execute command"`)},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_ownership_chat_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "run_terminal",
							Arguments: `{"cmd":"ls"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_ownership_chat_1",
				Name:       "run_terminal",
				Content:    json.RawMessage(`{"output":"file.txt"}`),
			},
		},
	}

	// Supply signature in cache and verify it succeeds
	cache.PutToolInfo("call_ownership_chat_1", "run_terminal", "sig_verified_tool_1")
	predChat, err := ConvertChatToPrediction(chatReq, cache)
	if err != nil {
		t.Fatalf("ConvertChatToPrediction should succeed with cached signature: %v", err)
	}
	if predChat.Request.Contents[1].Parts[0].ThoughtSignature != "sig_verified_tool_1" {
		t.Errorf("got %q, want sig_verified_tool_1", predChat.Request.Contents[1].Parts[0].ThoughtSignature)
	}

	// 2. Responses format with missing tool signature (incomplete pairing)
	respReqMissing := &ResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: json.RawMessage(`[
			{"role": "user", "content": "Execute command"},
			{"type": "function_call", "name": "run_terminal", "call_id": "call_ownership_resp_1", "arguments": "{\"cmd\":\"ls\"}"}
		]`),
	}

	_, err = ConvertResponsesToPrediction(respReqMissing, cache)
	if err == nil {
		t.Fatal("expected error for Responses tool call missing signature, got nil")
	}
	if !strings.Contains(err.Error(), "missing required cryptographic thought signature") {
		t.Fatalf("unexpected error message: %v", err)
	}

	respReq := &ResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: json.RawMessage(`[
			{"role": "user", "content": "Execute command"},
			{"type": "function_call", "name": "run_terminal", "call_id": "call_ownership_resp_1", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_ownership_resp_1", "output": "file.txt"}
		]`),
	}

	// Supply signature in cache and verify it succeeds
	cache.PutToolInfo("call_ownership_resp_1", "run_terminal", "sig_verified_tool_2")
	predResp, err := ConvertResponsesToPrediction(respReq, cache)
	if err != nil {
		t.Fatalf("ConvertResponsesToPrediction should succeed with cached signature: %v", err)
	}
	if predResp.Request.Contents[1].Parts[0].ThoughtSignature != "sig_verified_tool_2" {
		t.Errorf("got %q, want sig_verified_tool_2", predResp.Request.Contents[1].Parts[0].ThoughtSignature)
	}
}

func TestStateOwnershipTextSignatureAmbiguityDetection(t *testing.T) {
	cache := upstream.NewSignatureCache(100)

	messages := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`"Ambiguous Prompt"`)},
	}
	ctxHash := ComputeChatContextHash(messages, "gemini-3.8-flash", nil)
	ctxKey := ChatContextKey(ctxHash, "Ambiguous Answer")

	// Generation 1 returns signature A
	cache.PutContextSignature(ctxKey, "signature_gen_A")
	if got := cache.GetContextSignature(ctxKey); got != "signature_gen_A" {
		t.Fatalf("got %q, want signature_gen_A", got)
	}

	// Identical context generation 2 returns conflicting signature B
	cache.PutContextSignature(ctxKey, "signature_gen_B")

	// Must be marked ambiguous and return empty string
	if got := cache.GetContextSignature(ctxKey); got != "" {
		t.Fatalf("expected empty signature for ambiguous context, got %q", got)
	}

	// Replay request with ambiguous answer
	req := &ChatRequest{
		Model: "gemini-3.8-flash",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Ambiguous Prompt"`)},
			{Role: "assistant", Content: json.RawMessage(`"Ambiguous Answer"`)},
			{Role: "user", Content: json.RawMessage(`"Next Turn"`)},
		},
	}
	pred, err := ConvertChatToPrediction(req, cache)
	if err != nil {
		t.Fatal(err)
	}
	// The assistant text part must NOT have a guessed signature
	if got := pred.Request.Contents[1].Parts[0].ThoughtSignature; got != "" {
		t.Errorf("expected empty ThoughtSignature on ambiguous text part, got %q", got)
	}
}

func TestStateOwnershipModelAndToolsContextHashSensitivity(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`"Hello"`)},
	}
	toolsA := []ToolDefinition{
		{Type: "function", Function: struct {
			Name        string         `json:"name"`
			Description string         `json:"description,omitempty"`
			Parameters  map[string]any `json:"parameters,omitempty"`
		}{Name: "search"}},
	}
	toolsB := []ToolDefinition{
		{Type: "function", Function: struct {
			Name        string         `json:"name"`
			Description string         `json:"description,omitempty"`
			Parameters  map[string]any `json:"parameters,omitempty"`
		}{Name: "calculate"}},
	}

	// Chat context hash sensitivity
	h1 := ComputeChatContextHash(messages, "gemini-3.8-flash", toolsA)
	h2 := ComputeChatContextHash(messages, "gemini-3.8-pro", toolsA)
	h3 := ComputeChatContextHash(messages, "gemini-3.8-flash", toolsB)
	h4 := ComputeChatContextHash(messages, "gemini-3.8-flash", nil)

	if h1 == h2 {
		t.Error("Chat context hashes should differ when models differ")
	}
	if h1 == h3 {
		t.Error("Chat context hashes should differ when tools differ")
	}
	if h1 == h4 {
		t.Error("Chat context hashes should differ between tools present and nil")
	}

	// Responses context hash sensitivity
	rawInput := json.RawMessage(`[{"role":"user","content":"Hello"}]`)
	rh1 := ComputeResponsesContextHash(rawInput, "instruction-A", "gemini-3.8-flash", toolsA)
	rh2 := ComputeResponsesContextHash(rawInput, "instruction-B", "gemini-3.8-flash", toolsA)
	rh3 := ComputeResponsesContextHash(rawInput, "instruction-A", "gemini-3.8-pro", toolsA)
	rh4 := ComputeResponsesContextHash(rawInput, "instruction-A", "gemini-3.8-flash", toolsB)

	if rh1 == rh2 {
		t.Error("Responses context hashes should differ when instructions differ")
	}
	if rh1 == rh3 {
		t.Error("Responses context hashes should differ when models differ")
	}
	if rh1 == rh4 {
		t.Error("Responses context hashes should differ when tools differ")
	}
}

func TestCodexResponsesOutputOrdering(t *testing.T) {
	// 1. Tool call BEFORE text: verify streaming events and completed output maintain [tool, message]
	t.Run("ToolBeforeMessage", func(t *testing.T) {
		events := []string{
			`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"execute_tool","args":{"x":10},"id":"call_order_1"},"thoughtSignature":"sig_order_tool"}]}}]}}`,
			`data: {"response":{"candidates":[{"content":{"parts":[{"text":"Done executing"}]},"finishReason":"STOP"}]}}`,
		}
		streamData := strings.Join(events, "\n\n") + "\n\n"

		w := httptest.NewRecorder()
		sigCache := upstream.NewSignatureCache(100)
		handleStreamingResponses(w, strings.NewReader(streamData), "resp_order_1", "msg_order_1", 1700000000, "gemini-3.8-flash", sigCache)

		body := w.Body.String()
		lines := strings.Split(body, "\n")
		var sseEvents []struct {
			Event string
			Data  map[string]any
		}
		var currentEvent string
		for _, line := range lines {
			if strings.HasPrefix(line, "event: ") {
				currentEvent = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				dataStr := strings.TrimPrefix(line, "data: ")
				var data map[string]any
				if err := json.Unmarshal([]byte(dataStr), &data); err == nil {
					sseEvents = append(sseEvents, struct {
						Event string
						Data  map[string]any
					}{Event: currentEvent, Data: data})
				}
			}
		}

		// Verify output_index ordering in streaming events
		var toolAddedIndex, msgAddedIndex float64
		var foundCompleted bool
		for _, ev := range sseEvents {
			if ev.Event == "response.output_item.added" {
				item, _ := ev.Data["item"].(map[string]any)
				if item["type"] == "function_call" {
					toolAddedIndex = ev.Data["output_index"].(float64)
				}
				if item["type"] == "message" {
					msgAddedIndex = ev.Data["output_index"].(float64)
				}
			}
			if ev.Event == "response.completed" {
				foundCompleted = true
				resp, _ := ev.Data["response"].(map[string]any)
				outputs, _ := resp["output"].([]any)
				var nonReasoning []map[string]any
				for _, o := range outputs {
					om := o.(map[string]any)
					if om["type"] != "reasoning" {
						nonReasoning = append(nonReasoning, om)
					}
				}
				if len(nonReasoning) != 2 {
					t.Fatalf("expected 2 non-reasoning output items, got %d", len(nonReasoning))
				}
				if nonReasoning[0]["type"] != "function_call" {
					t.Errorf("output[0] type = %v, want function_call", nonReasoning[0]["type"])
				}
				if nonReasoning[1]["type"] != "message" {
					t.Errorf("output[1] type = %v, want message", nonReasoning[1]["type"])
				}
			}
		}

		if toolAddedIndex >= msgAddedIndex {
			t.Errorf("expected tool before message, got tool at %v and message at %v", toolAddedIndex, msgAddedIndex)
		}
		if !foundCompleted {
			t.Fatal("response.completed event not received")
		}

		// Non-streaming equivalence
		wNonStream := httptest.NewRecorder()
		handleNonStreamingResponses(wNonStream, strings.NewReader(streamData), "resp_order_1_ns", "msg_order_1_ns", 1700000000, "gemini-3.8-flash", sigCache)
		var nonStreamResp map[string]any
		if err := json.Unmarshal(wNonStream.Body.Bytes(), &nonStreamResp); err != nil {
			t.Fatal(err)
		}
		nsOutputs, _ := nonStreamResp["output"].([]any)
		var nonReasoningNS []map[string]any
		for _, o := range nsOutputs {
			om := o.(map[string]any)
			if om["type"] != "reasoning" {
				nonReasoningNS = append(nonReasoningNS, om)
			}
		}
		if len(nonReasoningNS) != 2 {
			t.Fatalf("expected 2 non-reasoning outputs in non-streaming, got %d", len(nonReasoningNS))
		}
		if nonReasoningNS[0]["type"] != "function_call" || nonReasoningNS[1]["type"] != "message" {
			t.Errorf("non-streaming output order mismatch: [0]=%v, [1]=%v", nonReasoningNS[0]["type"], nonReasoningNS[1]["type"])
		}
	})

	// 2. Text BEFORE tool call: verify streaming events and completed output maintain [message, tool]
	t.Run("MessageBeforeTool", func(t *testing.T) {
		events := []string{
			`data: {"response":{"candidates":[{"content":{"parts":[{"text":"Thinking before call"}]}}]}}`,
			`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search","args":{"q":"test"},"id":"call_order_2"},"thoughtSignature":"sig_order_tool_2"}]},"finishReason":"STOP"}]}}`,
		}
		streamData := strings.Join(events, "\n\n") + "\n\n"

		w := httptest.NewRecorder()
		sigCache := upstream.NewSignatureCache(100)
		handleStreamingResponses(w, strings.NewReader(streamData), "resp_order_2", "msg_order_2", 1700000000, "gemini-3.8-flash", sigCache)

		var toolAddedIndex, msgAddedIndex float64
		for _, line := range strings.Split(w.Body.String(), "\n") {
			if strings.HasPrefix(line, "data: ") {
				var data map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err == nil {
					if data["type"] == "response.output_item.added" {
						item, _ := data["item"].(map[string]any)
						if item["type"] == "function_call" {
							toolAddedIndex = data["output_index"].(float64)
						}
						if item["type"] == "message" {
							msgAddedIndex = data["output_index"].(float64)
						}
					}
				}
			}
		}
		if msgAddedIndex >= toolAddedIndex {
			t.Errorf("expected message before tool, got msg at %v and tool at %v", msgAddedIndex, toolAddedIndex)
		}

		// Non-streaming equivalence
		wNonStream := httptest.NewRecorder()
		handleNonStreamingResponses(wNonStream, strings.NewReader(streamData), "resp_order_2_ns", "msg_order_2_ns", 1700000000, "gemini-3.8-flash", sigCache)
		var nonStreamResp map[string]any
		if err := json.Unmarshal(wNonStream.Body.Bytes(), &nonStreamResp); err != nil {
			t.Fatal(err)
		}
		nsOutputs, _ := nonStreamResp["output"].([]any)
		var nonReasoningNS []map[string]any
		for _, o := range nsOutputs {
			om := o.(map[string]any)
			if om["type"] != "reasoning" {
				nonReasoningNS = append(nonReasoningNS, om)
			}
		}
		if len(nonReasoningNS) != 2 {
			t.Fatalf("expected 2 non-reasoning outputs in non-streaming, got %d", len(nonReasoningNS))
		}
		if nonReasoningNS[0]["type"] != "message" || nonReasoningNS[1]["type"] != "function_call" {
			t.Errorf("non-streaming output order mismatch: [0]=%v, [1]=%v", nonReasoningNS[0]["type"], nonReasoningNS[1]["type"])
		}
	})
}
