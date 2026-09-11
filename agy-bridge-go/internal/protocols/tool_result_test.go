package protocols

import (
	"encoding/json"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// Textual tool results must remain data. In particular, a JSON Schema $ref
// returned by webfetch must not become a Gemini function-response attachment
// reference. Exercise both production converters and the serialized request.
func TestToolResultTextPreserved(t *testing.T) {
	cases := map[string]string{
		"plain":     "a.txt\nb.txt",
		"schema":    `{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"#/$defs/Config","$defs":{"Config":{"type":"object"}}}`,
		"nested":    `{"definitions":[{"properties":{"config":{"$ref":"#/$defs/Config"}}}]}`,
		"precision": "{\n  \"id\": 9007199254740993, \"value\": 1.2300\n}",
		"array":     `[{"$ref":"literal-tool-data"}]`,
		"null":      "null",
		"string":    `"quoted JSON text"`,
		"empty":     "",
	}
	for _, protocol := range []string{"responses", "chat"} {
		for name, output := range cases {
			t.Run(protocol+"/"+name, func(t *testing.T) {
				const callID = "call_tool_result_data"
				const toolName = "load_schema"
				cache := upstream.NewSignatureCache(100)
				cache.PutToolDetails(callID, toolName, map[string]any{}, "native-tool-signature")
				var prediction *upstream.PredictionRequest
				var err error
				if protocol == "responses" {
					input, marshalErr := json.Marshal([]any{
						map[string]any{"role": "user", "content": "Load the schema."},
						map[string]any{"type": "function_call", "call_id": callID, "name": toolName, "arguments": "{}"},
						map[string]any{"type": "function_call_output", "call_id": callID, "output": output},
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					prediction, err = ConvertResponsesToPrediction(&ResponsesRequest{
						Model: "gemini-3.8-flash-high", Input: input,
					}, cache)
				} else {
					raw, marshalErr := json.Marshal(map[string]any{
						"model": "gemini-3.8-flash-high",
						"messages": []any{
							map[string]any{"role": "user", "content": "Load the schema."},
							map[string]any{"role": "assistant", "tool_calls": []any{
								map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": toolName, "arguments": "{}"}},
							}},
							map[string]any{"role": "tool", "tool_call_id": callID, "name": toolName, "content": output},
						},
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					var req ChatRequest
					if err := json.Unmarshal(raw, &req); err != nil {
						t.Fatal(err)
					}
					prediction, err = ConvertChatToPrediction(&req, cache)
				}
				if err != nil {
					t.Fatal(err)
				}

				wire, err := json.Marshal(prediction)
				if err != nil {
					t.Fatal(err)
				}
				var decoded upstream.PredictionRequest
				if err := json.Unmarshal(wire, &decoded); err != nil {
					t.Fatal(err)
				}
				calls, responses := 0, 0
				for _, content := range decoded.Request.Contents {
					for _, part := range content.Parts {
						if call := part.FunctionCall; call != nil {
							calls++
							if call.ID != callID || call.Name != toolName || part.ThoughtSignature != "native-tool-signature" {
								t.Fatalf("tool identity or signature changed: %+v", part)
							}
						}
						if result := part.FunctionResponse; result != nil {
							responses++
							if content.Role != "user" || result.ID != callID || result.Name != toolName {
								t.Fatalf("tool-result pairing changed: %+v", result)
							}
							text, ok := result.Response["response"].(string)
							if !ok || len(result.Response) != 1 || text != output {
								t.Fatalf("tool text must survive unchanged without promoting JSON keys: got %#v, want %q", result.Response, output)
							}
						}
					}
				}
				if calls != 1 || responses != 1 {
					t.Fatalf("want one call and result, got %d calls and %d results", calls, responses)
				}
			})
		}
	}
}
