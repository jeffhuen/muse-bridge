package protocols

import (
	"encoding/json"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

func TestPhase2IdentityAliasesAreUnambiguous(t *testing.T) {
	for _, tc := range []struct {
		name, itemA, itemB, resultA, resultB string
		wantErr                              bool
	}{
		{"shared_item_alias", "fc_shared", "fc_shared", "call_a", "fc_shared", true},
		{"item_alias_shadows_another_call_id", "fc_a", "call_a", "fc_a", "call_b", true},
		{"valid_item_aliases", "fc_a", "fc_b", "fc_a", "fc_b", false},
		{"valid_canonical_ids", "fc_a", "fc_b", "call_a", "call_b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []map[string]any{
				{"role": "user", "content": "lookup both"},
				{"type": "function_call", "id": tc.itemA, "call_id": "call_a", "name": "lookup_alpha", "arguments": "{}"},
				{"type": "function_call", "id": tc.itemB, "call_id": "call_b", "name": "lookup_beta", "arguments": "{}"},
				{"type": "function_call_output", "call_id": tc.resultA, "output": "ALPHA_RESULT"},
				{"type": "function_call_output", "call_id": tc.resultB, "output": "BETA_RESULT"},
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
					var names []string
					for _, c := range pred.Request.Contents {
						for _, p := range c.Parts {
							if p.FunctionResponse != nil {
								names = append(names, p.FunctionResponse.ID+":"+p.FunctionResponse.Name)
							}
						}
					}
					t.Fatalf("ambiguous alias accepted; translated result identities: %v", names)
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
