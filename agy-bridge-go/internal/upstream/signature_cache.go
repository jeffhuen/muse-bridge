package upstream

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SignatureCache preserves cryptographic thought signatures and function metadata
// attached to model outputs across multi-turn conversation steps.
type SignatureCache struct {
	mu           sync.RWMutex
	toolSigs     map[string]string          // tool_call_id / item_id -> thoughtSignature
	toolNames    map[string]string          // tool_call_id / item_id -> functionName
	toolArgs     map[string]map[string]any  // tool_call_id / item_id -> function arguments
	textSigs     map[string]string          // text -> thoughtSignature
	msgSigs      map[string]string          // msg_id / item_id -> thoughtSignature
	contextSigs  map[string]string          // context_key -> thoughtSignature
	ambiguous    map[string]bool            // context_key -> true if multiple signatures observed
	turnSiblings map[string]map[string]bool // lead_call_id -> set of sibling call IDs
	lastSig      string                     // most recently seen signature
	maxSize      int
	toolOrder    []string
	textOrder    []string
	msgOrder     []string
	contextOrder []string
	persistPath  string
	saving       atomic.Bool
}

// NewSignatureCache returns an initialized cache with a maximum capacity.
func NewSignatureCache(maxSize int) *SignatureCache {
	if maxSize <= 0 {
		maxSize = 2000
	}
	return &SignatureCache{
		toolSigs:     make(map[string]string),
		toolNames:    make(map[string]string),
		toolArgs:     make(map[string]map[string]any),
		textSigs:     make(map[string]string),
		msgSigs:      make(map[string]string),
		contextSigs:  make(map[string]string),
		ambiguous:    make(map[string]bool),
		turnSiblings: make(map[string]map[string]bool),
		maxSize:      maxSize,
		toolOrder:    make([]string, 0, maxSize),
		textOrder:    make([]string, 0, maxSize),
		msgOrder:     make([]string, 0, maxSize),
		contextOrder: make([]string, 0, maxSize),
	}
}

// SetPersistPath sets the path to automatically save cache contents to disk.
func (c *SignatureCache) SetPersistPath(path string) {
	c.mu.Lock()
	c.persistPath = path
	c.mu.Unlock()
}

func (c *SignatureCache) triggerSave() {
	c.mu.RLock()
	path := c.persistPath
	c.mu.RUnlock()
	if path == "" || flag.Lookup("test.v") != nil {
		return
	}
	if !c.saving.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.saving.Store(false)
		time.Sleep(200 * time.Millisecond)
		_ = c.SaveToFile(path)
	}()
}


// PutMessageSignature records the signature associated with a message or output item ID.
func (c *SignatureCache) PutMessageSignature(id, sig string) {
	if id == "" || sig == "" {
		return
	}
	c.mu.Lock()
	if _, exists := c.msgSigs[id]; !exists {
		if len(c.msgOrder) >= c.maxSize {
			oldest := c.msgOrder[0]
			c.msgOrder = c.msgOrder[1:]
			delete(c.msgSigs, oldest)
		}
		c.msgOrder = append(c.msgOrder, id)
	}
	c.msgSigs[id] = sig
	c.lastSig = sig
	c.mu.Unlock()
	c.triggerSave()
}

// GetMessageSignature retrieves the signature associated with a message or output item ID.
func (c *SignatureCache) GetMessageSignature(id string) string {
	if id == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.msgSigs[id]
}

// PutToolSignature records the signature associated with a tool call ID.
func (c *SignatureCache) PutToolSignature(callID, sig string) {
	c.PutToolDetails(callID, "", nil, sig)
}

// PutToolInfo records the function name and signature associated with a tool call ID.
func (c *SignatureCache) PutToolInfo(callID, name, sig string) {
	c.PutToolDetails(callID, name, nil, sig)
}

// PutToolDetails records the function name, arguments, and signature associated with a tool call ID.
func (c *SignatureCache) PutToolDetails(callID, name string, args map[string]any, sig string) {
	if callID == "" {
		return
	}
	c.mu.Lock()
	if _, exists := c.toolSigs[callID]; !exists {
		if len(c.toolOrder) >= c.maxSize {
			oldest := c.toolOrder[0]
			c.toolOrder = c.toolOrder[1:]
			delete(c.toolSigs, oldest)
			delete(c.toolNames, oldest)
			delete(c.toolArgs, oldest)
		}
		c.toolOrder = append(c.toolOrder, callID)
	}
	if sig != "" {
		c.toolSigs[callID] = sig
		c.lastSig = sig
	}
	if name != "" {
		c.toolNames[callID] = name
	}
	if args != nil {
		if c.toolArgs == nil {
			c.toolArgs = make(map[string]map[string]any)
		}
		c.toolArgs[callID] = args
	}
	c.mu.Unlock()
	if sig != "" {
		c.triggerSave()
	}
}

// GetToolSignature retrieves the signature associated with a tool call ID.
func (c *SignatureCache) GetToolSignature(callID string) string {
	if callID == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.toolSigs[callID]
}

// GetToolName retrieves the function name associated with a tool call ID.
func (c *SignatureCache) GetToolName(callID string) string {
	if callID == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.toolNames[callID]
}

// GetToolArgs retrieves the function arguments associated with a tool call ID.
func (c *SignatureCache) GetToolArgs(callID string) map[string]any {
	if callID == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.toolArgs == nil {
		return nil
	}
	return c.toolArgs[callID]
}

// RecordTurnSiblings records that siblingCallIDs were issued in the same turn as leadCallID.
func (c *SignatureCache) RecordTurnSiblings(leadCallID string, siblingCallIDs []string) {
	if leadCallID == "" || len(siblingCallIDs) == 0 {
		return
	}
	c.mu.Lock()
	if c.turnSiblings == nil {
		c.turnSiblings = make(map[string]map[string]bool)
	}
	if c.turnSiblings[leadCallID] == nil {
		c.turnSiblings[leadCallID] = make(map[string]bool)
	}
	for _, id := range siblingCallIDs {
		if id != "" && id != leadCallID {
			c.turnSiblings[leadCallID][id] = true
		}
	}
	c.mu.Unlock()
	c.triggerSave()
}

// IsVerifiedSibling checks if siblingCallID was recorded as an emitted turn sibling of leadCallID.
func (c *SignatureCache) IsVerifiedSibling(leadCallID, siblingCallID string) bool {
	if leadCallID == "" || siblingCallID == "" || leadCallID == siblingCallID {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.turnSiblings == nil {
		return false
	}
	if siblings, ok := c.turnSiblings[leadCallID]; ok && siblings[siblingCallID] {
		return true
	}
	if siblings, ok := c.turnSiblings[siblingCallID]; ok && siblings[leadCallID] {
		return true
	}
	return false
}


// PutTextSignature records the signature associated with an assistant text message.
func (c *SignatureCache) PutTextSignature(text, sig string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || sig == "" {
		return
	}
	c.mu.Lock()
	if _, exists := c.textSigs[trimmed]; !exists {
		if len(c.textOrder) >= c.maxSize {
			oldest := c.textOrder[0]
			c.textOrder = c.textOrder[1:]
			delete(c.textSigs, oldest)
		}
		c.textOrder = append(c.textOrder, trimmed)
	}
	c.textSigs[trimmed] = sig
	c.lastSig = sig
	c.mu.Unlock()
	c.triggerSave()
}

// GetTextSignature retrieves the signature associated with an assistant text message.
func (c *SignatureCache) GetTextSignature(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.textSigs[trimmed]
}

// PutContextSignature records the signature associated with a conversation context key (e.g. prefix_hash + text).
// If two independent generations with identical context produce different signatures, the key is marked
// ambiguous and subsequent lookups return empty rather than guessing an incorrect signature.
func (c *SignatureCache) PutContextSignature(contextKey, sig string) {
	trimmedKey := strings.TrimSpace(contextKey)
	if trimmedKey == "" || sig == "" {
		return
	}
	c.mu.Lock()
	if c.contextSigs == nil {
		c.contextSigs = make(map[string]string)
	}
	if c.ambiguous == nil {
		c.ambiguous = make(map[string]bool)
	}
	if existing, exists := c.contextSigs[trimmedKey]; exists {
		if existing != sig && existing != "" {
			c.ambiguous[trimmedKey] = true
			c.contextSigs[trimmedKey] = ""
			c.mu.Unlock()
			c.triggerSave()
			return
		}
	}
	if c.ambiguous[trimmedKey] {
		c.mu.Unlock()
		return
	}
	if _, exists := c.contextSigs[trimmedKey]; !exists {
		if len(c.contextOrder) >= c.maxSize {
			oldest := c.contextOrder[0]
			c.contextOrder = c.contextOrder[1:]
			delete(c.contextSigs, oldest)
		}
		c.contextOrder = append(c.contextOrder, trimmedKey)
	}
	c.contextSigs[trimmedKey] = sig
	c.lastSig = sig
	c.mu.Unlock()
	c.triggerSave()
}

// GetContextSignature retrieves the signature associated with a conversation context key.
// Returns empty string if the context is ambiguous or not recorded.
func (c *SignatureCache) GetContextSignature(contextKey string) string {
	trimmedKey := strings.TrimSpace(contextKey)
	if trimmedKey == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ambiguous != nil && c.ambiguous[trimmedKey] {
		return ""
	}
	if c.contextSigs == nil {
		return ""
	}
	return c.contextSigs[trimmedKey]
}

type persistedCacheData struct {
	ToolSigs     map[string]string          `json:"tool_sigs"`
	ToolNames    map[string]string          `json:"tool_names"`
	ToolArgs     map[string]map[string]any  `json:"tool_args,omitempty"`
	TextSigs     map[string]string          `json:"text_sigs"`
	MsgSigs      map[string]string          `json:"msg_sigs"`
	ContextSigs  map[string]string          `json:"context_sigs,omitempty"`
	Ambiguous    map[string]bool            `json:"ambiguous,omitempty"`
	TurnSiblings map[string][]string        `json:"turn_siblings,omitempty"`
	LastSig      string                     `json:"last_sig"`
}

// SaveToFile saves the cache snapshot to disk atomically.
func (c *SignatureCache) SaveToFile(filePath string) error {
	if filePath == "" {
		return nil
	}
	c.mu.RLock()
	data := persistedCacheData{
		ToolSigs:     make(map[string]string, len(c.toolSigs)),
		ToolNames:    make(map[string]string, len(c.toolNames)),
		ToolArgs:     make(map[string]map[string]any, len(c.toolArgs)),
		TextSigs:     make(map[string]string, len(c.textSigs)),
		MsgSigs:      make(map[string]string, len(c.msgSigs)),
		ContextSigs:  make(map[string]string, len(c.contextSigs)),
		Ambiguous:    make(map[string]bool, len(c.ambiguous)),
		TurnSiblings: make(map[string][]string, len(c.turnSiblings)),
		LastSig:      c.lastSig,
	}
	for k, v := range c.toolSigs {
		data.ToolSigs[k] = v
	}
	for k, v := range c.toolNames {
		data.ToolNames[k] = v
	}
	for k, v := range c.toolArgs {
		data.ToolArgs[k] = v
	}
	for k, v := range c.textSigs {
		data.TextSigs[k] = v
	}
	for k, v := range c.msgSigs {
		data.MsgSigs[k] = v
	}
	for k, v := range c.contextSigs {
		data.ContextSigs[k] = v
	}
	for k, v := range c.ambiguous {
		data.Ambiguous[k] = v
	}
	for lead, sibs := range c.turnSiblings {
		list := make([]string, 0, len(sibs))
		for sib := range sibs {
			list = append(list, sib)
		}
		data.TurnSiblings[lead] = list
	}
	c.mu.RUnlock()

	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	tmp := filePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filePath)
}

// LoadFromFile restores cache entries from a file snapshot.
func (c *SignatureCache) LoadFromFile(filePath string) error {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	var data persistedCacheData
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range data.ToolSigs {
		c.toolSigs[k] = v
		c.toolOrder = append(c.toolOrder, k)
	}
	for k, v := range data.ToolNames {
		c.toolNames[k] = v
	}
	if c.toolArgs == nil {
		c.toolArgs = make(map[string]map[string]any)
	}
	for k, v := range data.ToolArgs {
		c.toolArgs[k] = v
	}
	for k, v := range data.TextSigs {
		c.textSigs[k] = v
		c.textOrder = append(c.textOrder, k)
	}
	for k, v := range data.MsgSigs {
		c.msgSigs[k] = v
		c.msgOrder = append(c.msgOrder, k)
	}
	for k, v := range data.ContextSigs {
		c.contextSigs[k] = v
		c.contextOrder = append(c.contextOrder, k)
	}
	if c.ambiguous == nil {
		c.ambiguous = make(map[string]bool)
	}
	for k, v := range data.Ambiguous {
		c.ambiguous[k] = v
	}
	if c.turnSiblings == nil {
		c.turnSiblings = make(map[string]map[string]bool)
	}
	for lead, list := range data.TurnSiblings {
		if c.turnSiblings[lead] == nil {
			c.turnSiblings[lead] = make(map[string]bool)
		}
		for _, sib := range list {
			c.turnSiblings[lead][sib] = true
		}
	}
	if data.LastSig != "" {
		c.lastSig = data.LastSig
	}
	return nil
}
