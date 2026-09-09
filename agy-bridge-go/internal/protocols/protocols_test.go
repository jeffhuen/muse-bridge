package protocols

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

type mockAuthProvider struct {
	token string
}

func (m *mockAuthProvider) CurrentToken(ctx context.Context) (string, error) {
	return m.token, nil
}

func (m *mockAuthProvider) Invalidate(t string) {}

func newMockUpstreamServer(t *testing.T, chunks []string, statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-token-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if statusCode != 0 && statusCode != http.StatusOK {
			http.Error(w, "upstream server error", statusCode)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
}

func standardMockSSEChunks() []string {
	return []string{
		// 1. Thinking / reasoning delta
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"Analyzing code..."}]}}],"modelVersion":"gemini-3.8-flash"}}`,
		// 2. Regular content text delta
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello from bridge!"}]}}],"modelVersion":"gemini-3.8-flash"}}`,
		// 3. Tool call with thought signature
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_read_1","name":"read_file","args":{"path":"main.go"}},"thoughtSignature":"sig_secret_abc"}]}}],"modelVersion":"gemini-3.8-flash"}}`,
		// 4. Finish with STOP
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig_finish_xyz","text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50,"totalTokenCount":150},"modelVersion":"gemini-3.8-flash"}}`,
	}
}

func TestHandleChatCompletionsStreaming(t *testing.T) {
	mockServer := newMockUpstreamServer(t, standardMockSSEChunks(), http.StatusOK)
	defer mockServer.Close()

	client := upstream.NewClient(&mockAuthProvider{token: "mock-token-123"}, mockServer.Client(), mockServer.URL)

	reqBody := `{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"test"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	HandleChatCompletions(rec, req, client)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var hasReasoning, hasContent, hasToolCall, hasDone bool
	scanner := bufio.NewScanner(rec.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" {
			hasDone = true
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(line[6:]), &chunk); err != nil {
			t.Fatalf("failed to unmarshal chunk: %v", err)
		}
		choices := chunk["choices"].([]any)
		if len(choices) > 0 {
			c := choices[0].(map[string]any)
			delta := c["delta"].(map[string]any)
			if delta["reasoning_content"] == "Analyzing code..." {
				hasReasoning = true
			}
			if delta["content"] == "Hello from bridge!" {
				hasContent = true
			}
			if delta["tool_calls"] != nil {
				hasToolCall = true
			}
		}
	}

	if !hasReasoning {
		t.Error("expected reasoning_content chunk in stream")
	}
	if !hasContent {
		t.Error("expected content chunk in stream")
	}
	if !hasToolCall {
		t.Error("expected tool_calls chunk in stream")
	}
	if !hasDone {
		t.Error("expected [DONE] chunk at end of stream")
	}

	// Verify thought signature was cached
	cachedSig := client.SigCache().GetToolSignature("call_read_1")
	if cachedSig != "sig_secret_abc" {
		t.Errorf("thought signature was not properly cached: %q", cachedSig)
	}
}

func TestHandleChatCompletionsNonStreaming(t *testing.T) {
	mockServer := newMockUpstreamServer(t, standardMockSSEChunks(), http.StatusOK)
	defer mockServer.Close()

	client := upstream.NewClient(&mockAuthProvider{token: "mock-token-123"}, mockServer.Client(), mockServer.URL)

	reqBody := `{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"test"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	HandleChatCompletions(rec, req, client)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	choices := res["choices"].([]any)
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)

	if msg["content"] != "Hello from bridge!" {
		t.Errorf("expected content 'Hello from bridge!', got %v", msg["content"])
	}
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls', got %v", choice["finish_reason"])
	}
	toolCalls := msg["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	tc := toolCalls[0].(map[string]any)
	if tc["id"] != "call_read_1" {
		t.Errorf("expected tool call id 'call_read_1', got %v", tc["id"])
	}
}

func TestHandleResponsesStreaming(t *testing.T) {
	mockServer := newMockUpstreamServer(t, standardMockSSEChunks(), http.StatusOK)
	defer mockServer.Close()

	client := upstream.NewClient(&mockAuthProvider{token: "mock-token-123"}, mockServer.Client(), mockServer.URL)

	reqBody := `{"model":"gemini-3.8-flash-high","input":"hello","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	HandleResponses(rec, req, client)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var eventTypes []string
	var fullText string
	scanner := bufio.NewScanner(rec.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line[6:]), &evt); err != nil {
			t.Fatalf("failed to unmarshal SSE event: %v", err)
		}
		evtType, _ := evt["type"].(string)
		eventTypes = append(eventTypes, evtType)
		if evtType == "response.output_text.delta" {
			fullText += evt["delta"].(string)
		}
	}

	expectedEvents := []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_item.added",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}

	for _, expected := range expectedEvents {
		found := false
		for _, actual := range eventTypes {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected event %q in Responses stream, got: %v", expected, eventTypes)
		}
	}

	if fullText != "Hello from bridge!" {
		t.Errorf("expected text 'Hello from bridge!', got %q", fullText)
	}
}

func TestHandleResponsesNonStreaming(t *testing.T) {
	mockServer := newMockUpstreamServer(t, standardMockSSEChunks(), http.StatusOK)
	defer mockServer.Close()

	client := upstream.NewClient(&mockAuthProvider{token: "mock-token-123"}, mockServer.Client(), mockServer.URL)

	reqBody := `{"model":"gemini-3.8-flash-high","input":"hello","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	HandleResponses(rec, req, client)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if res["status"] != "completed" {
		t.Errorf("expected status 'completed', got %v", res["status"])
	}

	output := res["output"].([]any)
	if len(output) < 2 {
		t.Fatalf("expected at least 2 output items (message + function_call), got %d", len(output))
	}
}

func TestErrorHandling(t *testing.T) {
	mockServer := newMockUpstreamServer(t, nil, http.StatusInternalServerError)
	defer mockServer.Close()

	client := upstream.NewClient(&mockAuthProvider{token: "mock-token-123"}, mockServer.Client(), mockServer.URL)

	// 1. Invalid JSON body
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("invalid-json{"))
	rec := httptest.NewRecorder()
	HandleChatCompletions(rec, req, client)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on invalid JSON, got %d", rec.Code)
	}

	// 2. Upstream 500 error
	validBody := `{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"hi"}]}`
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validBody))
	rec2 := httptest.NewRecorder()
	HandleChatCompletions(rec2, req2, client)
	if rec2.Code != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway on upstream failure, got %d", rec2.Code)
	}
}
