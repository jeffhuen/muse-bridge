package protocols

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestAuditRequestTranslation(t *testing.T) {
	t.Run("ResponsesStandardToolSchema", func(t *testing.T) {
		var req ResponsesRequest
		json.Unmarshal([]byte(`{"input":"lookup alpha","tools":[{"type":"function","name":"lookup_code","parameters":{"type":"object","properties":{"key":{"type":"string"}}}}]}`), &req)
		p, err := ConvertResponsesToPrediction(&req, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Request.Tools) != 1 {
			t.Fatalf("standard Responses function declaration dropped: got %d tools", len(p.Request.Tools))
		}
	})
	for _, api := range []string{"chat", "responses"} {
		t.Run(api+"ToolResultName", func(t *testing.T) {
			var p *upstream.PredictionRequest
			var err error
			if api == "chat" {
				var req ChatRequest
				json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"lookup alpha"},{"role":"assistant","tool_calls":[{"id":"call_123","type":"function","thought_signature":"sig_123","function":{"name":"lookup_code","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_123","content":"{\"code\":\"MARKER\"}"}]}`), &req)
				p, err = ConvertChatToPrediction(&req, nil)
			} else {
				var req ResponsesRequest
				json.Unmarshal([]byte(`{"input":[{"role":"user","content":"lookup alpha"},{"type":"function_call","name":"lookup_code","call_id":"call_123","thought_signature":"sig_123","arguments":"{}"},{"type":"function_call_output","call_id":"call_123","output":"{\"code\":\"MARKER\"}"}]}`), &req)
				p, err = ConvertResponsesToPrediction(&req, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := p.Request.Contents[2].Parts[0].FunctionResponse.Name
			if got != "lookup_code" {
				t.Fatalf("functionResponse.name=%q, want lookup_code", got)
			}
		})
		t.Run(api+"DeveloperInstructions", func(t *testing.T) {
			var p *upstream.PredictionRequest
			if api == "chat" {
				var req ChatRequest
				json.Unmarshal([]byte(`{"messages":[{"role":"developer","content":"DEVELOPER_RULE"},{"role":"user","content":"hello"}]}`), &req)
				p, _ = ConvertChatToPrediction(&req, nil)
			} else {
				var req ResponsesRequest
				json.Unmarshal([]byte(`{"input":[{"role":"developer","content":"DEVELOPER_RULE"},{"role":"user","content":"hello"}]}`), &req)
				p, _ = ConvertResponsesToPrediction(&req, nil)
			}
			if p.Request.SystemInstruction == nil {
				t.Fatal("developer instruction demoted into user conversation")
			}
		})
	}
}

func auditEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "[DONE]" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	return events
}

func TestAuditOutputContract(t *testing.T) {
	chunks := []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"lookup_code","args":{"key":"alpha"}},"thoughtSignature":"signature-a"},{"functionCall":{"id":"call_b","name":"lookup_code","args":{"key":"beta"}}}]},"finishReason":"STOP"}]}}`}
	server := newMockUpstreamServer(t, chunks, http.StatusOK)
	defer server.Close()
	client := upstream.NewClient(&mockAuthProvider{token: "mock-token-123"}, server.Client(), server.URL)
	t.Run("ChatParallelCallIndexes", func(t *testing.T) {
		rec := httptest.NewRecorder()
		HandleChatCompletions(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"lookup"}],"stream":true}`)), client)
		var indexes []float64
		for _, e := range auditEvents(t, rec.Body.String()) {
			for _, choice := range e["choices"].([]any) {
				d := choice.(map[string]any)["delta"].(map[string]any)
				if calls, ok := d["tool_calls"].([]any); ok {
					for _, c := range calls {
						indexes = append(indexes, c.(map[string]any)["index"].(float64))
					}
				}
			}
		}
		if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
			t.Fatalf("parallel call indexes = %v, want [0 1]", indexes)
		}
	})
	t.Run("ResponsesToolCompletionEvents", func(t *testing.T) {
		rec := httptest.NewRecorder()
		HandleResponses(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"input":"lookup","stream":true}`)), client)
		var argsDone, toolDone, finalOutputs int
		for _, e := range auditEvents(t, rec.Body.String()) {
			switch e["type"] {
			case "response.function_call_arguments.done":
				argsDone++
			case "response.output_item.done":
				if e["item"].(map[string]any)["type"] == "function_call" {
					toolDone++
				}
			case "response.completed":
				out, _ := e["response"].(map[string]any)["output"].([]any)
				finalOutputs = len(out)
			}
		}
		if argsDone != 2 || toolDone != 2 || finalOutputs < 2 {
			t.Fatalf("args.done=%d tool item.done=%d final outputs=%d; need both calls completed and retained", argsDone, toolDone, finalOutputs)
		}
	})
	for _, api := range []string{"chat", "responses"} {
		t.Run(api+"MissingUpstreamCallID", func(t *testing.T) {
			body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup_code","args":{}},"thoughtSignature":"signed-call"}]},"finishReason":"STOP"}]}}` + "\n\n"
			rec := httptest.NewRecorder()
			cache := upstream.NewSignatureCache(10)
			var id string
			if api == "chat" {
				handleNonStreamingChat(rec, strings.NewReader(body), "chatcmpl_test", 0, "gemini", cache)
				var response struct {
					Choices []struct {
						Message ChatMessage `json:"message"`
					} `json:"choices"`
				}
				json.Unmarshal(rec.Body.Bytes(), &response)
				id = response.Choices[0].Message.ToolCalls[0].ID
			} else {
				handleNonStreamingResponses(rec, strings.NewReader(body), "resp_test", "msg_test", 0, "gemini", cache)
				var response struct {
					Output []struct {
						Type   string `json:"type"`
						CallID string `json:"call_id"`
					} `json:"output"`
				}
				json.Unmarshal(rec.Body.Bytes(), &response)
				for _, item := range response.Output {
					if item.Type == "function_call" {
						id = item.CallID
					}
				}
			}
			if id == "" {
				t.Fatal("empty client tool-call ID; signature cannot be cached or replayed")
			}
			if cache.GetToolSignature(id) != "signed-call" {
				t.Fatal("signature lost")
			}
		})
	}
}

func TestAuditSignedPartsReplay(t *testing.T) {
 // A signed text part and a signed call must keep their own signatures and order.
 body := "data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Checking alpha","thoughtSignature":"signed-text"},{"functionCall":{"id":"call_signed","name":"lookup_code","args":{}},"thoughtSignature":"signed-call"}]},"finishReason":"STOP"}]}}` + "\n\n"
 cache := upstream.NewSignatureCache(10)
 rec := httptest.NewRecorder()
 handleNonStreamingChat(rec, strings.NewReader(body), "chatcmpl_test", 0, "gemini", cache)
 var reply struct { Choices []struct { Message ChatMessage `json:"message"` } `json:"choices"` }
 if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil { t.Fatal(err) }
 req := &ChatRequest{Messages: []ChatMessage{
  {Role: "user", Content: json.RawMessage(`"lookup alpha"`)},
  reply.Choices[0].Message,
  {Role: "tool", ToolCallID: "call_signed", Name: "lookup_code", Content: json.RawMessage(`"ALPHA"`)},
 }}
 pred, err := ConvertChatToPrediction(req, cache)
 if err != nil { t.Fatal(err) }
 parts := pred.Request.Contents[1].Parts
 if len(parts)!=2 || parts[0].ThoughtSignature!="signed-text" || parts[1].ThoughtSignature!="signed-call" {
  t.Fatal("assistant text signature was discarded while only the tool-call signature was replayed")
 }
}

type auditBrokenStream struct{ read bool }

func (r *auditBrokenStream) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.ErrUnexpectedEOF
	}
	r.read = true
	return copy(p, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}}`+"\n\n"), nil
}

func TestAuditStreamFailures(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, api := range []string{"chat", "responses"} {
			name := api + "NonStreaming"
			if streaming {
				name = api + "Streaming"
			}
			t.Run(name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				broken := &auditBrokenStream{}
				if api == "chat" {
					if streaming {
						handleStreamingChat(rec, httptest.NewRequest("POST", "/", nil), broken, "cmpl", 0, "gemini", nil)
					} else {
						handleNonStreamingChat(rec, broken, "cmpl", 0, "gemini", nil)
					}
				} else {
					if streaming {
						handleStreamingResponses(rec, broken, "resp", "msg", 0, "gemini", nil)
					} else {
						handleNonStreamingResponses(rec, broken, "resp", "msg", 0, "gemini", nil)
					}
				}
				body := rec.Body.String()
				if rec.Code < 400 && !strings.Contains(body, `"error"`) && !strings.Contains(body, `"response.failed"`) {
					t.Fatalf("broken upstream reported successful HTTP %d response", rec.Code)
				}
			})
		}
	}
	t.Run("ErrorResponseIsValidJSON", func(t *testing.T) {
		rec := httptest.NewRecorder()
		server := newMockUpstreamServer(t, nil, http.StatusBadRequest)
		defer server.Close()
		client := upstream.NewClient(&mockAuthProvider{token: "mock-token-123"}, server.Client(), server.URL)
		HandleChatCompletions(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"messages":[{"role":"user","content":"test"}]}`)), client)
		if !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("error response is malformed JSON: %s", rec.Body.String())
		}
	})
}
