package protocols

import (
	"encoding/json"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// The handler hashes the client's raw request bytes while the replay lookup
// hashes a re-marshaled item prefix. Those are two spellings of one history, so
// the hash must ignore whitespace and key order or the lookup silently misses.
func TestResponsesContextHashIgnoresFormatting(t *testing.T) {
	compact := json.RawMessage(`[{"role":"user","content":"hello"},{"id":"msg_1","role":"assistant","content":"hi"}]`)
	pretty := json.RawMessage("[\n  {\n    \"role\": \"user\",\n    \"content\": \"hello\"\n  },\n  {\n    \"role\": \"assistant\",\n    \"id\": \"msg_1\",\n    \"content\": \"hi\"\n  }\n]")
	reordered := json.RawMessage(`[{"content":"hello","role":"user"},{"content":"hi","role":"assistant","id":"msg_1"}]`)

	base := ComputeResponsesContextHash(compact, "inst", "gemini-3.8-flash-high", nil)
	if base == "" {
		t.Fatal("expected a hash for non-empty input")
	}
	for name, variant := range map[string]json.RawMessage{"pretty": pretty, "reordered": reordered} {
		if got := ComputeResponsesContextHash(variant, "inst", "gemini-3.8-flash-high", nil); got != base {
			t.Fatalf("%s spelling hashed differently\n got: %s\nwant: %s", name, got, base)
		}
	}

	// Distinct histories must still separate.
	other := json.RawMessage(`[{"role":"user","content":"goodbye"}]`)
	if got := ComputeResponsesContextHash(other, "inst", "gemini-3.8-flash-high", nil); got == base {
		t.Fatal("different histories collided")
	}
}

// Canonicalizing must not round large integers through float64, which would
// make two distinct histories hash identically.
func TestResponsesContextHashPreservesLargeIntegers(t *testing.T) {
	a := json.RawMessage(`[{"role":"user","content":"9007199254740993"},{"n":9007199254740993}]`)
	b := json.RawMessage(`[{"role":"user","content":"9007199254740993"},{"n":9007199254740992}]`)
	if ComputeResponsesContextHash(a, "", "m", nil) == ComputeResponsesContextHash(b, "", "m", nil) {
		t.Fatal("large integers collided after canonicalization")
	}
}

// End to end: a signature stored against a pretty-printed prefix must be found
// when the replay path re-marshals that same prefix compactly.
func TestResponsesReplayFindsSignatureAcrossFormatting(t *testing.T) {
	const assistantText = "cached answer"
	const assistantID = "msg_cached"
	const wantSig = "cached-thought-signature"

	// What the client sent on the earlier turn, pretty-printed as a harness would.
	prettyPrefix := json.RawMessage("[\n  {\n    \"role\": \"user\",\n    \"content\": \"question\"\n  }\n]")

	cache := upstream.NewSignatureCache(16)
	cache.PutMessageSignature(assistantID, wantSig)
	prefixHash := ComputeResponsesContextHash(prettyPrefix, "", "gemini-3.8-flash-high", nil)
	cache.PutContextSignature(ResponsesContextKey(prefixHash, assistantText), wantSig)

	// The next turn replays that history; conversion re-marshals the prefix.
	input, err := json.Marshal([]any{
		map[string]any{"role": "user", "content": "question"},
		map[string]any{"role": "assistant", "id": assistantID, "content": assistantText},
		map[string]any{"role": "user", "content": "follow up"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prediction, err := ConvertResponsesToPrediction(&ResponsesRequest{
		Model: "gemini-3.8-flash-high", Input: input,
	}, cache)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, content := range prediction.Request.Contents {
		if content.Role != "model" {
			continue
		}
		for _, part := range content.Parts {
			if part.Text != assistantText {
				continue
			}
			found = true
			if part.ThoughtSignature != wantSig {
				t.Fatalf("replayed assistant part lost its cached signature: got %q, want %q",
					part.ThoughtSignature, wantSig)
			}
		}
	}
	if !found {
		t.Fatal("assistant part was not replayed at all")
	}
}
