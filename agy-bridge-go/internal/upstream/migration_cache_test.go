package upstream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationMarkersNeverOwnNativeRecords(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			data := persistedCacheData{
				Version:      version,
				TurnSiblings: map[string][]string{"imported": {"unknown"}, "native": {"unsigned", "imported"}},
			}
			if version == 1 {
				data.ToolSigs = map[string]string{"imported": migrationRecoverySignature, "native": "native_signature"}
				data.ToolNames = map[string]string{"imported": "exec_command", "native": "exec_command", "unsigned": "exec_command"}
				data.ToolArgs = map[string]map[string]any{"imported": {"cmd": "one"}, "unsigned": {"cmd": "two"}}
			} else {
				data.Records = map[string]*NativeToolRecord{
					"imported": {BridgeCallID: "imported", ThoughtSignature: migrationRecoverySignature},
					"native":   {BridgeCallID: "native", ThoughtSignature: "native_signature"},
					"unsigned": {BridgeCallID: "unsigned", ToolName: "exec_command", Args: map[string]any{"cmd": "two"}},
				}
				data.Aliases = map[string]string{"imported": "imported", "fc_imported": "imported", "imported_fc_imported": "imported", "fc_native": "native"}
			}
			path := filepath.Join(t.TempDir(), "cache.json")
			raw, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cache := NewSignatureCache(100)
			for restart := 0; restart < 2; restart++ {
				if err := cache.LoadFromFile(path); err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"imported", "fc_imported", "imported_fc_imported"} {
					if _, ok := cache.GetToolRecord(id); ok {
						t.Fatalf("migration marker owns %s", id)
					}
					if _, ok := cache.aliases[id]; ok {
						t.Fatalf("migration alias survived: %s", id)
					}
				}
				if got := cache.GetToolSignature("native"); got != "native_signature" {
					t.Fatalf("native signature lost: %q", got)
				}
				if rec, ok := cache.GetToolRecord("unsigned"); !ok || rec.Args["cmd"] != "two" {
					t.Fatal("unsigned native metadata lost")
				}
				if !cache.IsVerifiedSibling("native", "unsigned") {
					t.Fatal("native sibling relation lost")
				}
				if cache.IsVerifiedSibling("imported", "unknown") || cache.IsVerifiedSibling("native", "imported") {
					t.Fatal("migration ownership relation survived")
				}
				if err := cache.SaveToFile(path); err != nil {
					t.Fatal(err)
				}
				cache = NewSignatureCache(100)
			}
		})
	}
	cache := NewSignatureCache(10)
	cache.PutToolDetails("native", "exec_command", nil, "native_signature")
	for _, id := range []string{"imported", "native"} {
		cache.PutToolRecord(&NativeToolRecord{BridgeCallID: id, ThoughtSignature: migrationRecoverySignature}, "fc_imported")
	}
	if _, ok := cache.GetToolRecord("imported"); ok {
		t.Fatal("new migration record accepted")
	}
	if _, ok := cache.GetToolRecord("fc_imported"); ok {
		t.Fatal("new migration alias accepted")
	}
	if cache.GetToolSignature("native") != "native_signature" {
		t.Fatal("migration marker replaced native signature")
	}
}
