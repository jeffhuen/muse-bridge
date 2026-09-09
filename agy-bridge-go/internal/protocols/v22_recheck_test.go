package protocols

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// Exercise the existing serializers and translators with client-visible history.
func v22Reply(t *testing.T, api string, streaming bool, id, signature string, cache *upstream.SignatureCache) map[string]any {
	t.Helper()
	event := upstream.SSEStreamEvent{Response: &upstream.PredictionResponse{Candidates: []upstream.Candidate{{Content: upstream.Content{Role: "model", Parts: []upstream.Part{{Text: "Same reply", ThoughtSignature: signature}}}, FinishReason: "STOP"}}}}
	raw, _ := json.Marshal(event)
	r := strings.NewReader("data: " + string(raw) + "\n\n")
	w := httptest.NewRecorder()
	if api == "chat" {
		if streaming {
			handleStreamingChat(w, httptest.NewRequest("POST", "/", nil), r, id, 0, "gemini", cache)
		} else {
			handleNonStreamingChat(w, r, id, 0, "gemini", cache)
		}
	} else {
		if streaming {
			handleStreamingResponses(w, r, id, "msg_"+id, 0, "gemini", cache)
		} else {
			handleNonStreamingResponses(w, r, id, "msg_"+id, 0, "gemini", cache)
		}
	}
	if !streaming {
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if api == "chat" {
			return out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
		}
		for _, item := range out["output"].([]any) {
			m := item.(map[string]any)
			if m["type"] == "message" {
				return m
			}
		}
		return out["output"].([]any)[0].(map[string]any)
	}
	message := map[string]any{"role": "assistant", "content": ""}
	for _, e := range auditEvents(t, w.Body.String()) {
		if api == "responses" {
			if e["type"] == "response.completed" {
				for _, item := range e["response"].(map[string]any)["output"].([]any) {
					m := item.(map[string]any)
					if m["type"] == "message" {
						return m
					}
				}
				return e["response"].(map[string]any)["output"].([]any)[0].(map[string]any)
			}
			continue
		}
		for _, choice := range e["choices"].([]any) {
			delta := choice.(map[string]any)["delta"].(map[string]any)
			if content, ok := delta["content"].(string); ok {
				message["content"] = message["content"].(string) + content
			}
			if sig, ok := delta["thought_signature"]; ok {
				message["thought_signature"] = sig
			}
		}
	}
	return message
}

func TestV22SignatureIsolationAllModes(t *testing.T) {
	for _, api := range []string{"chat", "responses"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", api, streaming), func(t *testing.T) {
				cache := upstream.NewSignatureCache(10)
				a := v22Reply(t, api, streaming, "a", "signature-A", cache)
				v22Reply(t, api, streaming, "b", "signature-B", cache)
				history := []any{map[string]any{"role": "user", "content": "conversation A"}, a, map[string]any{"role": "user", "content": "continue A"}}
				var pred *upstream.PredictionRequest
				var err error
				if api == "chat" {
					raw, _ := json.Marshal(map[string]any{"messages": history})
					var req ChatRequest
					if err = json.Unmarshal(raw, &req); err != nil {
						t.Fatal(err)
					}
					pred, err = ConvertChatToPrediction(&req, cache)
				} else {
					raw, _ := json.Marshal(history)
					pred, err = ConvertResponsesToPrediction(&ResponsesRequest{Input: raw}, cache)
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := pred.Request.Contents[1].Parts[0].ThoughtSignature; got != "signature-A" {
					t.Fatalf("conversation A replay got %q, want signature-A", got)
				}
			})
		}
	}
}

func TestV22ResponsesOutputIndexesMatchCompleted(t *testing.T) {
	// Lazy message creation assigns tool index 0 and message index 1.
	body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"lookup","args":{}}},{"text":"Looking that up."}]},"finishReason":"STOP"}]}}` + "\n\n"
	w := httptest.NewRecorder()
	handleStreamingResponses(w, strings.NewReader(body), "resp", "msg", 0, "gemini", upstream.NewSignatureCache(10))
	added := map[int]string{}
	for _, e := range auditEvents(t, w.Body.String()) {
		if e["type"] == "response.output_item.added" {
			added[int(e["output_index"].(float64))] = e["item"].(map[string]any)["id"].(string)
		}
		if e["type"] == "response.completed" {
			for index, raw := range e["response"].(map[string]any)["output"].([]any) {
				item := raw.(map[string]any)
				if item["id"] != added[index] {
					t.Errorf("output[%d] id=%v but streaming output_index %d belongs to %s", index, item["id"], index, added[index])
				}
			}
		}
	}
}
