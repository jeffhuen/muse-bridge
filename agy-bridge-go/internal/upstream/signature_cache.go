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

// NativeToolRecord stores complete native turn state atomically.
type NativeToolRecord struct {
	BridgeCallID     string         `json:"call_id"`
	OutputItemID     string         `json:"item_id,omitempty"`
	UpstreamID       string         `json:"upstream_id,omitempty"`
	ToolName         string         `json:"tool_name"`
	Args             map[string]any `json:"args,omitempty"`
	ThoughtSignature string         `json:"sig,omitempty"`
	Model            string         `json:"model,omitempty"`
	TurnID           string         `json:"turn_id,omitempty"`
	IsLegacy         bool           `json:"is_legacy,omitempty"`
}

func cloneArgs(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil
	}
	return cloned
}

func (r *NativeToolRecord) Clone() *NativeToolRecord {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Args = cloneArgs(r.Args)
	return &cp
}

// SignatureCache preserves cryptographic thought signatures and function metadata
// attached to model outputs across multi-turn conversation steps.
type SignatureCache struct {
	mu           sync.RWMutex
	records      map[string]*NativeToolRecord // canonical BridgeCallID -> record
	aliases      map[string]string            // alias (call_id, item_id, pi_composite) -> canonical BridgeCallID
	textSigs     map[string]string            // text -> thoughtSignature
	msgSigs      map[string]string            // msg_id / item_id -> thoughtSignature
	contextSigs  map[string]string            // context_key -> thoughtSignature
	ambiguous    map[string]bool              // context_key -> true if multiple signatures observed
	turnSiblings map[string]map[string]bool   // canonical BridgeCallID -> set of sibling canonical BridgeCallIDs
	lastSig      string                       // most recently seen signature
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
		records:      make(map[string]*NativeToolRecord),
		aliases:      make(map[string]string),
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

// PutToolRecord registers a native tool record and binds all associated aliases.
func (c *SignatureCache) PutToolRecord(rec *NativeToolRecord, aliases ...string) {
	if rec == nil || rec.BridgeCallID == "" {
		return
	}
	c.mu.Lock()
	if c.records == nil {
		c.records = make(map[string]*NativeToolRecord)
	}
	if c.aliases == nil {
		c.aliases = make(map[string]string)
	}

	cloned := rec.Clone()
	c.records[rec.BridgeCallID] = cloned

	isNew := true
	for _, id := range c.toolOrder {
		if id == rec.BridgeCallID {
			isNew = false
			break
		}
	}
	if isNew {
		if len(c.toolOrder) >= c.maxSize {
			oldest := c.toolOrder[0]
			c.toolOrder = c.toolOrder[1:]
			delete(c.records, oldest)
			for a, target := range c.aliases {
				if target == oldest {
					delete(c.aliases, a)
				}
			}
		}
		c.toolOrder = append(c.toolOrder, rec.BridgeCallID)
	}

	c.aliases[rec.BridgeCallID] = rec.BridgeCallID
	for _, a := range aliases {
		if a != "" {
			c.aliases[a] = rec.BridgeCallID
		}
	}

	if rec.ThoughtSignature != "" {
		c.lastSig = rec.ThoughtSignature
	}
	c.mu.Unlock()

	if rec.ThoughtSignature != "" {
		c.triggerSave()
	}
}

// GetToolRecord retrieves the atomic native tool record by alias or callID.
func (c *SignatureCache) GetToolRecord(alias string) (*NativeToolRecord, bool) {
	if alias == "" {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	canonicalID := alias
	if c.aliases != nil {
		if id, ok := c.aliases[alias]; ok && id != "" {
			canonicalID = id
		}
	}

	if c.records != nil {
		if rec, ok := c.records[canonicalID]; ok && rec != nil {
			return rec.Clone(), true
		}
	}
	return nil, false
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
	rec := &NativeToolRecord{
		BridgeCallID:     callID,
		ToolName:         name,
		Args:             args,
		ThoughtSignature: sig,
	}
	c.PutToolRecord(rec, callID)
}

// GetToolSignature retrieves the signature associated with a tool call ID or alias.
func (c *SignatureCache) GetToolSignature(callID string) string {
	if rec, ok := c.GetToolRecord(callID); ok {
		return rec.ThoughtSignature
	}
	return ""
}

// GetToolName retrieves the function name associated with a tool call ID or alias.
func (c *SignatureCache) GetToolName(callID string) string {
	if rec, ok := c.GetToolRecord(callID); ok {
		return rec.ToolName
	}
	return ""
}

// GetToolArgs retrieves the function arguments associated with a tool call ID or alias.
func (c *SignatureCache) GetToolArgs(callID string) map[string]any {
	if rec, ok := c.GetToolRecord(callID); ok {
		return rec.Args
	}
	return nil
}

func (c *SignatureCache) resolveCanonicalIDLocked(alias string) string {
	if c.aliases != nil {
		if id, ok := c.aliases[alias]; ok && id != "" {
			return id
		}
	}
	return alias
}

// RecordTurnSiblings records that siblingCallIDs were issued in the same turn as leadCallID.
func (c *SignatureCache) RecordTurnSiblings(leadCallID string, siblingCallIDs []string) {
	if leadCallID == "" || len(siblingCallIDs) == 0 {
		return
	}
	c.mu.Lock()
	leadCanon := c.resolveCanonicalIDLocked(leadCallID)
	if c.turnSiblings == nil {
		c.turnSiblings = make(map[string]map[string]bool)
	}
	if c.turnSiblings[leadCanon] == nil {
		c.turnSiblings[leadCanon] = make(map[string]bool)
	}
	for _, id := range siblingCallIDs {
		if id != "" {
			sibCanon := c.resolveCanonicalIDLocked(id)
			if sibCanon != leadCanon {
				c.turnSiblings[leadCanon][sibCanon] = true
			}
		}
	}
	c.mu.Unlock()
	c.triggerSave()
}

// IsVerifiedSibling checks if siblingCallID was recorded as an emitted turn sibling of leadCallID.
func (c *SignatureCache) IsVerifiedSibling(leadCallID, siblingCallID string) bool {
	if leadCallID == "" || siblingCallID == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.turnSiblings == nil {
		return false
	}
	leadCanon := c.resolveCanonicalIDLocked(leadCallID)
	sibCanon := c.resolveCanonicalIDLocked(siblingCallID)
	if leadCanon == sibCanon {
		return false
	}
	if siblings, ok := c.turnSiblings[leadCanon]; ok && siblings[sibCanon] {
		return true
	}
	if siblings, ok := c.turnSiblings[sibCanon]; ok && siblings[leadCanon] {
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
	Version      int                          `json:"v,omitempty"`
	Records      map[string]*NativeToolRecord `json:"records,omitempty"`
	Aliases      map[string]string            `json:"aliases,omitempty"`
	TextSigs     map[string]string            `json:"text_sigs,omitempty"`
	MsgSigs      map[string]string            `json:"msg_sigs,omitempty"`
	ContextSigs  map[string]string            `json:"context_sigs,omitempty"`
	Ambiguous    map[string]bool              `json:"ambiguous,omitempty"`
	TurnSiblings map[string][]string          `json:"turn_siblings,omitempty"`
	LastSig      string                       `json:"last_sig,omitempty"`

	// Legacy fields (v0/v1) for backward compatibility:
	ToolSigs  map[string]string         `json:"tool_sigs,omitempty"`
	ToolNames map[string]string         `json:"tool_names,omitempty"`
	ToolArgs  map[string]map[string]any `json:"tool_args,omitempty"`
}

// SaveToFile saves the cache snapshot to disk atomically.
func (c *SignatureCache) SaveToFile(filePath string) error {
	if filePath == "" {
		return nil
	}
	c.mu.RLock()
	data := persistedCacheData{
		Version:      2,
		Records:      make(map[string]*NativeToolRecord, len(c.records)),
		Aliases:      make(map[string]string, len(c.aliases)),
		TextSigs:     make(map[string]string, len(c.textSigs)),
		MsgSigs:      make(map[string]string, len(c.msgSigs)),
		ContextSigs:  make(map[string]string, len(c.contextSigs)),
		Ambiguous:    make(map[string]bool, len(c.ambiguous)),
		TurnSiblings: make(map[string][]string, len(c.turnSiblings)),
		LastSig:      c.lastSig,
	}
	for k, v := range c.records {
		if v != nil {
			data.Records[k] = v.Clone()
		}
	}
	for k, v := range c.aliases {
		data.Aliases[k] = v
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

	if c.records == nil {
		c.records = make(map[string]*NativeToolRecord)
	}
	if c.aliases == nil {
		c.aliases = make(map[string]string)
	}

	if data.Version >= 2 && len(data.Records) > 0 {
		for k, rec := range data.Records {
			if rec != nil {
				c.records[k] = rec.Clone()
				c.toolOrder = append(c.toolOrder, k)
			}
		}
		for a, target := range data.Aliases {
			c.aliases[a] = target
		}
	} else if len(data.ToolSigs) > 0 {
		// Visibly legacy migration: do not invent upstream IDs, model ownership, or turn IDs
		for k, sig := range data.ToolSigs {
			rec := &NativeToolRecord{
				BridgeCallID:     k,
				ToolName:         data.ToolNames[k],
				Args:             cloneArgs(data.ToolArgs[k]),
				ThoughtSignature: sig,
				IsLegacy:         true,
			}
			c.records[k] = rec
			c.aliases[k] = k
			c.toolOrder = append(c.toolOrder, k)
		}
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
