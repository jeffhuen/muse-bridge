package protocols

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// Gemini rejects unknown fields in FunctionDeclaration.parameters, so every
// draft keyword it does not implement must be resolved or dropped before the
// declaration is sent.
func TestSanitizeToolSchemaRewritesUnsupportedKeywords(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"inlines $ref from $defs": {
			in:   `{"type":"object","properties":{"c":{"$ref":"#/$defs/Config"}},"$defs":{"Config":{"type":"object","properties":{"x":{"type":"string"}}}}}`,
			want: `{"properties":{"c":{"properties":{"x":{"type":"string"}},"type":"object"}},"type":"object"}`,
		},
		"inlines $ref from definitions": {
			in:   `{"type":"object","properties":{"c":{"$ref":"#/definitions/C"}},"definitions":{"C":{"type":"integer"}}}`,
			want: `{"properties":{"c":{"type":"integer"}},"type":"object"}`,
		},
		"sibling keys overlay the inlined target": {
			in:   `{"$ref":"#/$defs/C","description":"local","$defs":{"C":{"type":"string","description":"shared"}}}`,
			want: `{"description":"local","type":"string"}`,
		},
		"drops draft metadata": {
			in:   `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"x","$comment":"c","type":"object"}`,
			want: `{"type":"object"}`,
		},
		"const becomes single-value enum": {
			in:   `{"type":"object","properties":{"a":{"const":"x"}}}`,
			want: `{"properties":{"a":{"enum":["x"]}},"type":"object"}`,
		},
		"oneOf becomes anyOf": {
			in:   `{"oneOf":[{"type":"string"},{"type":"integer"}]}`,
			want: `{"anyOf":[{"type":"string"},{"type":"integer"}]}`,
		},
		"allOf members merge into the node": {
			in:   `{"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"properties":{"b":{"type":"integer"}},"required":["b"]}]}`,
			want: `{"properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a","b"],"type":"object"}`,
		},
		"additionalProperties survives": {
			in:   `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`,
			want: `{"additionalProperties":false,"properties":{"a":{"type":"string"}},"type":"object"}`,
		},
		"unknown vendor keywords are dropped": {
			in:   `{"type":"object","unevaluatedProperties":false,"x-vendor":1,"if":{"type":"string"}}`,
			want: `{"type":"object"}`,
		},
		"tuple items keep the first schema": {
			in:   `{"type":"array","items":[{"type":"string"},{"type":"integer"}]}`,
			want: `{"items":{"type":"string"},"type":"array"}`,
		},
		"unresolvable ref degrades to a bare object": {
			in:   `{"type":"object","properties":{"a":{"$ref":"https://example.com/Other"}}}`,
			want: `{"properties":{"a":{"type":"object"}},"type":"object"}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var in map[string]any
			if err := json.Unmarshal([]byte(tc.in), &in); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(SanitizeToolSchema(in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("sanitized schema mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// A schema that references itself cannot be expressed in Gemini's schema
// language, so the cycle must terminate rather than expand forever.
func TestSanitizeToolSchemaCollapsesCycles(t *testing.T) {
	in := map[string]any{}
	raw := `{"type":"object","properties":{"child":{"$ref":"#/$defs/Node"}},
	          "$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}}}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	done := make(chan map[string]any, 1)
	go func() { done <- SanitizeToolSchema(in) }()
	var out map[string]any
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sanitizing a recursive schema did not terminate")
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"properties":{"child":{"properties":{"next":{"type":"object"}},"type":"object"}},"type":"object"}`
	if string(encoded) != want {
		t.Fatalf("recursive schema mismatch\n got: %s\nwant: %s", encoded, want)
	}
}

// The client's schema map is shared by reference: ToolDefinition.FunctionParameters
// returns the request's own map, and both context hashes serialize req.Tools. If
// sanitizing aliased that map, later mutation of the outgoing declaration would
// shift context keys and diverge replay lookups from already-persisted keys.
func TestSanitizeToolSchemaNeverAliasesClientSchema(t *testing.T) {
	raw := `{"type":"object","properties":{"c":{"$ref":"#/$defs/Config"}},
	         "$defs":{"Config":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}}}`
	var in map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	out := SanitizeToolSchema(in)

	// Mutate the sanitized copy as aggressively as a later transform might.
	delete(out, "type")
	out["properties"].(map[string]any)["c"].(map[string]any)["properties"].(map[string]any)["x"] = "clobbered"
	out["properties"].(map[string]any)["injected"] = map[string]any{"type": "number"}

	after, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("client schema mutated by sanitize+mutate\nbefore: %s\n after: %s", before, after)
	}
}

// The outgoing declaration must not share storage with the client tools, so the
// context hash computed from req.Tools stays stable across conversion.
func TestConversionLeavesToolsContextHashStable(t *testing.T) {
	raw := `{"model":"gemini-3.8-flash-high",
	         "tools":[{"type":"function","name":"cfg","description":"d",
	           "parameters":{"type":"object","properties":{"c":{"$ref":"#/$defs/Config"}},
	             "$defs":{"Config":{"type":"object","properties":{"x":{"type":"string"}}}}}}],
	         "messages":[{"role":"user","content":"hi"}]}`
	var req ChatRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	cache := upstream.NewSignatureCache(16)
	hashBefore := ComputeChatContextHash(req.Messages, req.Model, req.Tools)

	prediction, err := ConvertChatToPrediction(&req, cache)
	if err != nil {
		t.Fatal(err)
	}

	decls := prediction.Request.Tools[0].FunctionDeclarations
	if len(decls) != 1 {
		t.Fatalf("want one declaration, got %d", len(decls))
	}
	if _, leaked := decls[0].Parameters["$defs"]; leaked {
		t.Fatal("$defs reached the outgoing declaration; Google rejects it")
	}
	// Mutating the outgoing schema must not disturb the client's tools.
	decls[0].Parameters["injected"] = true
	delete(decls[0].Parameters, "properties")

	if hashAfter := ComputeChatContextHash(req.Messages, req.Model, req.Tools); hashAfter != hashBefore {
		t.Fatalf("mutating the outgoing declaration changed the client tools hash\nbefore: %s\n after: %s", hashBefore, hashAfter)
	}
}
