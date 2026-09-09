package upstream

import (
	"fmt"
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
