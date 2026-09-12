package rewrite

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	return out
}

func TestResponses(t *testing.T) {
	cases := []struct {
		name          string
		in            string
		wantRetention string
		wantReasoning any // nil means the key must be absent
	}{
		{"adds retention", `{"model":"muse-spark-1.3"}`, "24h", nil},
		{"keeps retention", `{"prompt_cache_retention":"1h"}`, "1h", nil},
		{"drops reasoning none", `{"reasoning":{"effort":"none"}}`, "24h", nil},
		{"drops reasoning null", `{"reasoning":{"effort":null}}`, "24h", nil},
		{"drops reasoning missing effort", `{"reasoning":{}}`, "24h", nil},
		{"keeps reasoning effort", `{"reasoning":{"effort":"max"}}`, "24h", map[string]any{"effort": "max"}},
		{"keeps non-object reasoning", `{"reasoning":"xhigh"}`, "24h", "xhigh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := decode(t, Responses([]byte(tc.in), false))
			if out["prompt_cache_retention"] != tc.wantRetention {
				t.Fatalf("retention = %v, want %s", out["prompt_cache_retention"], tc.wantRetention)
			}
			got, ok := out["reasoning"]
			if tc.wantReasoning == nil {
				if ok {
					t.Fatalf("reasoning present, want absent: %v", got)
				}
				return
			}
			if !ok || !reflect.DeepEqual(got, tc.wantReasoning) {
				t.Fatalf("reasoning = %v, want %v", got, tc.wantReasoning)
			}
		})
	}
}

func TestResponsesSuppliesMissingToolParameters(t *testing.T) {
	in := `{"tools":[{"type":"function","name":"no_params"},{"type":"function","name":"null_params","parameters":null},{"type":"function","name":"has_params","parameters":{"type":"object"}}]}`
	out := decode(t, Responses([]byte(in), false))
	tools := out["tools"].([]any)
	for i, name := range []string{"no_params", "null_params", "has_params"} {
		tool := tools[i].(map[string]any)
		if tool["name"] != name {
			t.Fatalf("tool[%d] name = %v, want %s", i, tool["name"], name)
		}
		params, ok := tool["parameters"].(map[string]any)
		if !ok || params["type"] != "object" {
			t.Fatalf("tool[%d] parameters = %v, want {\"type\":\"object\"}", i, tool["parameters"])
		}
	}
}

func TestResponsesInvalidJSONPassthrough(t *testing.T) {
	in := []byte(`{not json`)
	if out := Responses(in, false); string(out) != string(in) {
		t.Fatalf("invalid JSON not passed through: %q", out)
	}
}

func TestResponsesPreservesBigIntegers(t *testing.T) {
	const big = "9223372036854775807" // 2^63-1: unrepresentable as float64
	out := Responses([]byte(`{"model":"m","seed":`+big+`}`), false)
	if !strings.Contains(string(out), `"seed":`+big) {
		t.Fatalf("seed mangled: %s", out)
	}
}
