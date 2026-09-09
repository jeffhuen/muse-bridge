package protocols

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestV23ExportClientFixtures(t *testing.T) {
	path := os.Getenv("V23_FIXTURE_OUTPUT")
	if path == "" { t.Skip("set V23_FIXTURE_OUTPUT to export bridge SSE") }
	body := "data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Same reply","thoughtSignature":"signature-A"}]},"finishReason":"STOP"}]}}` + "\n\n"
	fixtures := map[string]string{}
	for _, api := range []string{"chat", "responses"} {
		w := httptest.NewRecorder()
		cache := upstream.NewSignatureCache(10)
		if api == "chat" { handleStreamingChat(w, httptest.NewRequest("POST", "/", nil), strings.NewReader(body), "chatcmpl_A", 0, "gemini-3.8-flash-high", cache) } else { handleStreamingResponses(w, strings.NewReader(body), "resp_A", "msg_A", 0, "gemini-3.8-flash-high", cache) }
		fixtures[api] = w.Body.String()
	}
	raw, err := json.Marshal(fixtures)
	if err != nil { t.Fatal(err) }
	if err = os.WriteFile(path, raw, 0600); err != nil { t.Fatal(err) }
}

func TestV23InstalledPiReplay(t *testing.T) {
	path := os.Getenv("V23_PI_REPLAY")
	if path == "" { t.Skip("set V23_PI_REPLAY to installed pi adapter output") }
	raw, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	var requests map[string]json.RawMessage
	if err = json.Unmarshal(raw, &requests); err != nil { t.Fatal(err) }
	for _, api := range []string{"chat", "responses"} {
		t.Run(api, func(t *testing.T) {
			cache := upstream.NewSignatureCache(10)
			cache.PutTextSignature("Same reply", "signature-B")
			cache.PutMessageSignature("chatcmpl_A", "signature-A")
			cache.PutMessageSignature("msg_A", "signature-A")
			var pred *upstream.PredictionRequest
			var err error
			if api == "chat" {
				var req ChatRequest
				if err = json.Unmarshal(requests[api], &req); err != nil { t.Fatal(err) }
				pred, err = ConvertChatToPrediction(&req, cache)
			} else {
				var req ResponsesRequest
				if err = json.Unmarshal(requests[api], &req); err != nil { t.Fatal(err) }
				pred, err = ConvertResponsesToPrediction(&req, cache)
			}
			if err != nil { t.Fatal(err) }
			if got := pred.Request.Contents[1].Parts[0].ThoughtSignature; got != "signature-A" { t.Fatalf("installed pi %s replay attached %q to conversation A", api, got) }
		})
	}
}

func TestStandardChatContextIsolation(t *testing.T) {
	cache := upstream.NewSignatureCache(10)

	// Simulate Conversation A turn 1
	messagesA := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`"Prompt A"`)},
	}
	hashA := ComputeChatContextHash(messagesA, "", nil)
	cache.PutTextSignature("Same reply", "signature-A")
	cache.PutContextSignature(ChatContextKey(hashA, "Same reply"), "signature-A")

	// Simulate Conversation B turn 1 (overwriting global text cache with signature-B)
	messagesB := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`"Prompt B"`)},
	}
	hashB := ComputeChatContextHash(messagesB, "", nil)
	cache.PutTextSignature("Same reply", "signature-B")
	cache.PutContextSignature(ChatContextKey(hashB, "Same reply"), "signature-B")

	// Verify conversation A replay with standard messages (no ID, no signature, no reasoning_details)
	replayA := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Prompt A"`)},
			{Role: "assistant", Content: json.RawMessage(`"Same reply"`)},
			{Role: "user", Content: json.RawMessage(`"continue A"`)},
		},
	}
	predA, err := ConvertChatToPrediction(replayA, cache)
	if err != nil {
		t.Fatal(err)
	}
	if got := predA.Request.Contents[1].Parts[0].ThoughtSignature; got != "signature-A" {
		t.Fatalf("conversation A replayed with %q, want signature-A", got)
	}

	// Verify conversation B replay
	replayB := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Prompt B"`)},
			{Role: "assistant", Content: json.RawMessage(`"Same reply"`)},
			{Role: "user", Content: json.RawMessage(`"continue B"`)},
		},
	}
	predB, err := ConvertChatToPrediction(replayB, cache)
	if err != nil {
		t.Fatal(err)
	}
	if got := predB.Request.Contents[1].Parts[0].ThoughtSignature; got != "signature-B" {
		t.Fatalf("conversation B replayed with %q, want signature-B", got)
	}
}

