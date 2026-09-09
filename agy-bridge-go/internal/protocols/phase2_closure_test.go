package protocols

import (
	"encoding/json"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestPhase2ClosureAliasRegistrationOrder(t *testing.T) {
	for _, tc := range []struct {
		name, itemA, itemB string
		wantErr            bool
	}{
		{"alias_registered_before_canonical_id", "call_b", "fc_b", true},
		{"canonical_id_registered_before_alias", "fc_a", "call_a", true},
		{"same_call_item_and_canonical_ids", "call_a", "call_b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []map[string]any{
				{"role": "user", "content": "lookup both"},
				{"type": "function_call", "id": tc.itemA, "call_id": "call_a", "name": "lookup_alpha", "arguments": "{}"},
				{"type": "function_call", "id": tc.itemB, "call_id": "call_b", "name": "lookup_beta", "arguments": "{}"},
				{"type": "function_call_output", "call_id": "call_a", "output": "ALPHA_RESULT"},
				{"type": "function_call_output", "call_id": "call_b", "output": "BETA_RESULT"},
			}
			raw, err := json.Marshal(items)
			if err != nil {
				t.Fatal(err)
			}
			cache := upstream.NewSignatureCache(100)
			cache.PutToolDetails("call_a", "lookup_alpha", map[string]any{}, "synthetic_alpha_signature")
			cache.PutToolDetails("call_b", "lookup_beta", map[string]any{}, "synthetic_beta_signature")
			pred, err := phase2ReviewConvert(t, "responses", string(raw), cache)
			if tc.wantErr {
				if err == nil {
					t.Fatal("conflicting alias accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"call_a": "lookup_alpha", "call_b": "lookup_beta"}
			count := 0
			for _, c := range pred.Request.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil {
						count++
						if want[p.FunctionResponse.ID] != p.FunctionResponse.Name {
							t.Errorf("wrong result identity: %s:%s", p.FunctionResponse.ID, p.FunctionResponse.Name)
						}
					}
				}
			}
			if count != 2 {
				t.Fatalf("got %d results, want 2", count)
			}
		})
	}
}
