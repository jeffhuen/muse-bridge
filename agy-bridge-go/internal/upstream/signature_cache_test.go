package upstream

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestSignatureCacheBasic(t *testing.T) {
	cache := NewSignatureCache(10)

	cache.PutToolSignature("call_1", "sig_1")
	cache.PutToolSignature("call_2", "sig_2")

	if got := cache.GetToolSignature("call_1"); got != "sig_1" {
		t.Errorf("expected sig_1, got %q", got)
	}
	if got := cache.GetToolSignature("call_2"); got != "sig_2" {
		t.Errorf("expected sig_2, got %q", got)
	}
	if got := cache.GetToolSignature("call_nonexistent"); got != "" {
		t.Errorf("expected empty string for nonexistent key, got %q", got)
	}
	if got := cache.GetToolSignature(""); got != "" {
		t.Errorf("expected empty string for empty key, got %q", got)
	}
}

func TestSignatureCacheEviction(t *testing.T) {
	cache := NewSignatureCache(3)

	cache.PutToolSignature("c1", "s1")
	cache.PutToolSignature("c2", "s2")
	cache.PutToolSignature("c3", "s3")

	// Capacity full. Adding c4 should evict c1.
	cache.PutToolSignature("c4", "s4")

	if got := cache.GetToolSignature("c1"); got != "" {
		t.Errorf("expected c1 to be evicted, got %q", got)
	}
	if got := cache.GetToolSignature("c2"); got != "s2" {
		t.Errorf("expected c2 to still exist, got %q", got)
	}
	if got := cache.GetToolSignature("c4"); got != "s4" {
		t.Errorf("expected c4 to exist, got %q", got)
	}
}

func TestSignatureCacheConcurrency(t *testing.T) {
	cache := NewSignatureCache(100)
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("call_%d", id)
			val := fmt.Sprintf("sig_%d", id)
			cache.PutToolSignature(key, val)
			_ = cache.GetToolSignature(key)
		}(i)
	}
	wg.Wait()
}

func TestSignatureCachePersistence(t *testing.T) {
	dir := t.TempDir()
	filePath := dir + "/signatures.json"

	c1 := NewSignatureCache(10)
	c1.PutToolInfo("call_test_1", "bash", "sig_bash_123")
	c1.PutMessageSignature("msg_test_1", "sig_msg_456")
	c1.PutTextSignature("hello world", "sig_text_789")
	c1.PutContextSignature("ctx_123:hello world", "sig_ctx_999")

	if err := c1.SaveToFile(filePath); err != nil {
		t.Fatalf("SaveToFile failed: %v", err)
	}

	c2 := NewSignatureCache(10)
	if err := c2.LoadFromFile(filePath); err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if got := c2.GetToolSignature("call_test_1"); got != "sig_bash_123" {
		t.Errorf("got %q, want sig_bash_123", got)
	}
	if got := c2.GetToolName("call_test_1"); got != "bash" {
		t.Errorf("got %q, want bash", got)
	}
	if got := c2.GetMessageSignature("msg_test_1"); got != "sig_msg_456" {
		t.Errorf("got %q, want sig_msg_456", got)
	}
	if got := c2.GetTextSignature("hello world"); got != "sig_text_789" {
		t.Errorf("got %q, want sig_text_789", got)
	}
	if got := c2.GetContextSignature("ctx_123:hello world"); got != "sig_ctx_999" {
		t.Errorf("got %q, want sig_ctx_999", got)
	}
}

func TestSignatureCacheContextIsolation(t *testing.T) {
	c := NewSignatureCache(10)
	// Conversation A has identical assistant text to Conversation B
	c.PutContextSignature("convA:Same reply", "signature-A")
	c.PutTextSignature("Same reply", "signature-B") // overwritten by conversation B global text
	c.PutContextSignature("convB:Same reply", "signature-B")

	if got := c.GetContextSignature("convA:Same reply"); got != "signature-A" {
		t.Errorf("convA got %q, want signature-A", got)
	}
	if got := c.GetContextSignature("convB:Same reply"); got != "signature-B" {
		t.Errorf("convB got %q, want signature-B", got)
	}
}

func TestSignatureCacheAmbiguityDetection(t *testing.T) {
	c := NewSignatureCache(10)
	// Generation 1
	c.PutContextSignature("identical_context:same_text", "sig-1")
	if got := c.GetContextSignature("identical_context:same_text"); got != "sig-1" {
		t.Errorf("got %q, want sig-1", got)
	}

	// Generation 2 with identical context & text generates a different signature
	c.PutContextSignature("identical_context:same_text", "sig-2")

	// Must be marked ambiguous: returns empty so client omits rather than guessing wrong
	if got := c.GetContextSignature("identical_context:same_text"); got != "" {
		t.Errorf("ambiguous key returned %q, want empty string", got)
	}
}

func TestNativeToolRecordAndAliases(t *testing.T) {
	c := NewSignatureCache(10)

	callID := "call_0123456789abcdef01234567"
	itemID := "fc_abcdef0123456789abcdef01"
	piAlias := callID + "_" + itemID

	origArgs := map[string]any{"cmd": "git status", "timeout": 30}
	rec := &NativeToolRecord{
		BridgeCallID:     callID,
		OutputItemID:     itemID,
		UpstreamID:       "call_987397",
		ToolName:         "exec_command",
		Args:             origArgs,
		ThoughtSignature: "sig_atomic_test",
		Model:            "gemini-3.8-flash-high",
		TurnID:           "turn_xyz",
	}

	c.PutToolRecord(rec, itemID, piAlias)

	// Mutate origArgs to verify atomic cloning
	origArgs["cmd"] = "rm -rf /"

	// Look up by BridgeCallID
	rec1, ok := c.GetToolRecord(callID)
	if !ok || rec1 == nil {
		t.Fatalf("expected record for callID, got not found")
	}
	if rec1.ToolName != "exec_command" {
		t.Errorf("got ToolName %q, want exec_command", rec1.ToolName)
	}
	if rec1.Args["cmd"] != "git status" {
		t.Errorf("expected cloned args not mutated, got %v", rec1.Args["cmd"])
	}

	// Look up by itemID alias
	rec2, ok := c.GetToolRecord(itemID)
	if !ok || rec2 == nil {
		t.Fatalf("expected record for itemID alias, got not found")
	}
	if rec2.BridgeCallID != callID || rec2.ThoughtSignature != "sig_atomic_test" {
		t.Errorf("itemID alias did not resolve to canonical record: %+v", rec2)
	}

	// Look up by Pi composite alias (57 chars)
	rec3, ok := c.GetToolRecord(piAlias)
	if !ok || rec3 == nil {
		t.Fatalf("expected record for piAlias, got not found")
	}
	if rec3.BridgeCallID != callID || rec3.UpstreamID != "call_987397" {
		t.Errorf("piAlias did not resolve to canonical record: %+v", rec3)
	}

	// Sibling tracking with aliases
	otherCallID := "call_other_sibling_99"
	otherRec := &NativeToolRecord{
		BridgeCallID:     otherCallID,
		ToolName:         "read_file",
		ThoughtSignature: "sig_other",
	}
	c.PutToolRecord(otherRec, "fc_other_alias")

	c.RecordTurnSiblings(piAlias, []string{"fc_other_alias"})

	if !c.IsVerifiedSibling(callID, otherCallID) {
		t.Errorf("expected callID and otherCallID to be verified siblings")
	}
	if !c.IsVerifiedSibling(piAlias, otherCallID) {
		t.Errorf("expected piAlias and otherCallID to be verified siblings")
	}
	if !c.IsVerifiedSibling(itemID, "fc_other_alias") {
		t.Errorf("expected itemID and fc_other_alias to be verified siblings")
	}
}

func TestLegacyV1CacheSnapshotMigration(t *testing.T) {
	dir := t.TempDir()
	legacyFile := dir + "/legacy_signatures.json"

	// Construct legacy v0/v1 JSON with tool_sigs, tool_names, tool_args
	legacyJSON := `{
		"tool_sigs": {"call_legacy_1": "sig_leg_1"},
		"tool_names": {"call_legacy_1": "bash"},
		"tool_args": {"call_legacy_1": {"flag": "-la"}},
		"last_sig": "sig_leg_1"
	}`

	if err := os.WriteFile(legacyFile, []byte(legacyJSON), 0600); err != nil {
		t.Fatalf("failed to write legacy snapshot: %v", err)
	}

	c := NewSignatureCache(10)
	if err := c.LoadFromFile(legacyFile); err != nil {
		t.Fatalf("LoadFromFile failed on legacy snapshot: %v", err)
	}

	rec, ok := c.GetToolRecord("call_legacy_1")
	if !ok || rec == nil {
		t.Fatalf("expected legacy record to be loaded, got not found")
	}
	if !rec.IsLegacy {
		t.Errorf("expected IsLegacy to be true for migrated record")
	}
	if rec.ToolName != "bash" {
		t.Errorf("got ToolName %q, want bash", rec.ToolName)
	}
	if rec.ThoughtSignature != "sig_leg_1" {
		t.Errorf("got ThoughtSignature %q, want sig_leg_1", rec.ThoughtSignature)
	}
	if rec.UpstreamID != "" || rec.Model != "" || rec.TurnID != "" {
		t.Errorf("migrated record should not invent metadata: %+v", rec)
	}
}
