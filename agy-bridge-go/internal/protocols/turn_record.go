package protocols

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// TurnPartKind identifies the kind of part within a model turn.
type TurnPartKind string

const (
	PartKindThought  TurnPartKind = "thought"
	PartKindToolCall TurnPartKind = "function_call"
	PartKindText     TurnPartKind = "text"
)

// TurnPartRecord represents a single discrete part in an upstream model turn.
type TurnPartRecord struct {
	Index            int            `json:"index"`
	Kind             TurnPartKind   `json:"kind"`
	Text             string         `json:"text,omitempty"`
	ThoughtSignature string         `json:"sig,omitempty"`
	CallID           string         `json:"call_id,omitempty"`     // bridge tool call id (e.g. call_...)
	UpstreamID       string         `json:"upstream_id,omitempty"` // original upstream tool call id (e.g. call_987397)
	ToolName         string         `json:"tool_name,omitempty"`   // upstream tool name
	Args             map[string]any `json:"args,omitempty"`
	OutputItemID     string         `json:"item_id,omitempty"` // client output item id (e.g. fc_... or msg_...)
}

// AuthoritativeTurn is the single authoritative turn representation for both streaming
// and non-streaming, carrying ordered upstream parts, their signatures, and their
// corresponding client item/call IDs.
type AuthoritativeTurn struct {
	Version        int                 `json:"v"`
	TurnID         string              `json:"turn_id,omitempty"`
	Model          string              `json:"model,omitempty"`
	ReasoningID    string              `json:"reasoning_id,omitempty"`
	ThoughtSummary string              `json:"thought_summary,omitempty"`
	OutputOrder    []string            `json:"output_order,omitempty"`
	Parts          []TurnPartRecord    `json:"parts"`
	ToolSignatures map[string]string   `json:"tool_sigs,omitempty"` // callID -> sig, itemID -> sig
	TextSignatures map[string]string   `json:"text_sigs,omitempty"` // itemID -> sig
	TurnSiblings   map[string][]string `json:"turn_siblings,omitempty"`
}

// AuthoritativeTurnAccumulator incrementally constructs an AuthoritativeTurn from upstream parts
// as they arrive (in streaming or batch), preserving exact logical part boundaries, signatures,
// and item/call identifiers.
type AuthoritativeTurnAccumulator struct {
	turn               *AuthoritativeTurn
	baseItemID         string
	thoughtBuilder     strings.Builder
	hasThought         bool
	currentText        strings.Builder
	currentTextSig     string
	currentTextID      string
	textCount          int
	toolCallIDs        []string
	recordedItems      map[string]bool
	upstreamToBridgeID map[string]string
	upstreamToItemID   map[string]string
}

// NewAuthoritativeTurnAccumulator returns a new incremental turn accumulator.
func NewAuthoritativeTurnAccumulator(baseItemID string) *AuthoritativeTurnAccumulator {
	return &AuthoritativeTurnAccumulator{
		turn: &AuthoritativeTurn{
			Version:        1,
			ReasoningID:    "rs_" + RandomID("rs"),
			ToolSignatures: make(map[string]string),
			TextSignatures: make(map[string]string),
			TurnSiblings:   make(map[string][]string),
		},
		baseItemID:         baseItemID,
		recordedItems:      make(map[string]bool),
		upstreamToBridgeID: make(map[string]string),
		upstreamToItemID:   make(map[string]string),
	}
}

// SetModel records the model name on the turn.
func (acc *AuthoritativeTurnAccumulator) SetModel(model string) {
	acc.turn.Model = model
}

// SetTurnID records the turn identifier on the turn.
func (acc *AuthoritativeTurnAccumulator) SetTurnID(turnID string) {
	acc.turn.TurnID = turnID
}

func (acc *AuthoritativeTurnAccumulator) recordOutputItemID(id string) {
	if id == "" || acc.recordedItems[id] {
		return
	}
	if acc.recordedItems == nil {
		acc.recordedItems = make(map[string]bool)
	}
	acc.recordedItems[id] = true
	acc.turn.OutputOrder = append(acc.turn.OutputOrder, id)
}

// Turn returns the underlying AuthoritativeTurn.
func (acc *AuthoritativeTurnAccumulator) Turn() *AuthoritativeTurn {
	return acc.turn
}

// ReasoningID returns the assigned reasoning item ID.
func (acc *AuthoritativeTurnAccumulator) ReasoningID() string {
	return acc.turn.ReasoningID
}

// ThoughtSummary returns the accumulated thought text.
func (acc *AuthoritativeTurnAccumulator) ThoughtSummary() string {
	return acc.thoughtBuilder.String()
}

// HasPendingText reports whether uncommitted text exists in the current text part.
func (acc *AuthoritativeTurnAccumulator) HasPendingText() bool {
	return acc.currentText.Len() > 0 || acc.currentTextSig != ""
}

// CurrentTextID returns the client item ID for the currently accumulating text part.
func (acc *AuthoritativeTurnAccumulator) CurrentTextID() string {
	if acc.currentTextID == "" {
		if acc.textCount == 0 && acc.baseItemID != "" {
			acc.currentTextID = acc.baseItemID
		} else if acc.baseItemID != "" {
			acc.currentTextID = fmt.Sprintf("%s_%d", acc.baseItemID, acc.textCount)
		} else {
			acc.currentTextID = RandomID("msg")
		}
	}
	return acc.currentTextID
}

// CurrentTextSig returns the thought signature on the accumulating text part.
func (acc *AuthoritativeTurnAccumulator) CurrentTextSig() string {
	return acc.currentTextSig
}

// CurrentTextContent returns the accumulating text string.
func (acc *AuthoritativeTurnAccumulator) CurrentTextContent() string {
	return acc.currentText.String()
}

// FlushPendingText commits the accumulated text into an AuthoritativeTurn part and returns it.
func (acc *AuthoritativeTurnAccumulator) FlushPendingText() *TurnPartRecord {
	if acc.currentText.Len() > 0 || acc.currentTextSig != "" {
		msgID := acc.CurrentTextID()
		acc.textCount++
		record := TurnPartRecord{
			Index:            len(acc.turn.Parts),
			Kind:             PartKindText,
			Text:             acc.currentText.String(),
			ThoughtSignature: acc.currentTextSig,
			OutputItemID:     msgID,
		}
		acc.turn.Parts = append(acc.turn.Parts, record)
		if acc.currentTextSig != "" {
			acc.turn.TextSignatures[msgID] = acc.currentTextSig
		}
		acc.currentText.Reset()
		acc.currentTextSig = ""
		acc.currentTextID = ""
		return &record
	}
	return nil
}

// ProcessPart processes an upstream Part incrementally.
// It returns:
// - flushed: if a prior text part had to be committed due to a boundary change or tool call.
// - added: if a new non-text part (e.g. tool call) was created and committed.
func (acc *AuthoritativeTurnAccumulator) ProcessPart(part upstream.Part) (flushed *TurnPartRecord, added *TurnPartRecord) {
	if part.Thought {
		flushed = acc.FlushPendingText()
		acc.hasThought = true
		acc.recordOutputItemID(acc.turn.ReasoningID)
		if part.Text != "" {
			acc.thoughtBuilder.WriteString(part.Text)
		}
		return flushed, nil
	}

	if part.FunctionCall != nil {
		flushed = acc.FlushPendingText()
		fc := part.FunctionCall
		callID := fc.ID
		upstreamID := fc.ID
		if callID == "" {
			callID = RandomID("call")
			fc.ID = callID
		}
		fcID := RandomID("fc")
		sig := part.ThoughtSignature

		record := TurnPartRecord{
			Index:            len(acc.turn.Parts),
			Kind:             PartKindToolCall,
			CallID:           callID,
			UpstreamID:       upstreamID,
			ToolName:         fc.Name,
			Args:             fc.Args,
			ThoughtSignature: sig,
			OutputItemID:     fcID,
		}
		acc.turn.Parts = append(acc.turn.Parts, record)
		acc.recordOutputItemID(fcID)
		acc.toolCallIDs = append(acc.toolCallIDs, callID)
		if sig != "" {
			acc.turn.ToolSignatures[callID] = sig
			acc.turn.ToolSignatures[fcID] = sig
		}
		return flushed, &record
	}

	if part.Text != "" || part.ThoughtSignature != "" {
		if part.Text == "" && part.ThoughtSignature != "" {
			if acc.currentText.Len() > 0 {
				acc.currentTextSig = part.ThoughtSignature
			} else if len(acc.turn.Parts) > 0 {
				for idx := len(acc.turn.Parts) - 1; idx >= 0; idx-- {
					if acc.turn.Parts[idx].Kind == PartKindText {
						acc.turn.Parts[idx].ThoughtSignature = part.ThoughtSignature
						acc.turn.TextSignatures[acc.turn.Parts[idx].OutputItemID] = part.ThoughtSignature
						break
					}
				}
			}
			return nil, nil
		}

		// If signature status changed while text was accumulating, flush prior text part
		if acc.currentText.Len() > 0 && acc.currentTextSig != part.ThoughtSignature {
			flushed = acc.FlushPendingText()
		}

		msgID := acc.CurrentTextID()
		acc.recordOutputItemID(msgID)
		if part.Text != "" {
			acc.currentText.WriteString(part.Text)
		}
		if part.ThoughtSignature != "" {
			acc.currentTextSig = part.ThoughtSignature
		}
		return flushed, nil
	}

	return nil, nil
}

// Finish finalizes the turn record and returns the completed AuthoritativeTurn.
func (acc *AuthoritativeTurnAccumulator) Finish() *AuthoritativeTurn {
	acc.FlushPendingText()

	if len(acc.turn.Parts) == 0 && !acc.hasThought {
		msgID := acc.baseItemID
		if msgID == "" {
			msgID = RandomID("msg")
		}
		acc.turn.Parts = append(acc.turn.Parts, TurnPartRecord{
			Index:        0,
			Kind:         PartKindText,
			Text:         "",
			OutputItemID: msgID,
		})
		acc.recordOutputItemID(msgID)
	}

	acc.turn.ThoughtSummary = acc.thoughtBuilder.String()

	if len(acc.toolCallIDs) > 1 {
		acc.turn.TurnSiblings[acc.toolCallIDs[0]] = acc.toolCallIDs[1:]
	}

	return acc.turn
}

// BuildAuthoritativeTurn constructs an AuthoritativeTurn from upstream parts.
func BuildAuthoritativeTurn(parts []upstream.Part, baseItemID string, model ...string) *AuthoritativeTurn {
	acc := NewAuthoritativeTurnAccumulator(baseItemID)
	if len(model) > 0 && model[0] != "" {
		acc.SetModel(model[0])
	}
	for _, part := range parts {
		acc.ProcessPart(part)
	}
	return acc.Finish()
}

// NeedsReasoningItem returns true if the turn produced thoughts or any signed parts.
func (t *AuthoritativeTurn) NeedsReasoningItem() bool {
	return len(t.ThoughtSummary) > 0 || len(t.ToolSignatures) > 0 || len(t.TextSignatures) > 0
}

// ToOutputItems converts the authoritative turn into standard Responses output items.
func (t *AuthoritativeTurn) ToOutputItems() []map[string]any {
	itemsByID := make(map[string]map[string]any)

	var reasoningItem map[string]any
	if t.NeedsReasoningItem() {
		encContent := EncodeReasoningEncryptedContent(ReasoningEncryptedState{
			Version:        t.Version,
			Model:          t.Model,
			TurnID:         t.TurnID,
			Parts:          t.Parts,
			ToolSignatures: t.ToolSignatures,
			TurnSiblings:   t.TurnSiblings,
			TextSignatures: t.TextSignatures,
			OutputOrder:    t.OutputOrder,
		})
		reasoningItem = map[string]any{
			"id":                t.ReasoningID,
			"type":              "reasoning",
			"status":            "completed",
			"encrypted_content": encContent,
			"summary": []map[string]any{
				{"type": "summary_text", "text": t.ThoughtSummary},
			},
		}
		itemsByID[t.ReasoningID] = reasoningItem
	}

	for _, p := range t.Parts {
		switch p.Kind {
		case PartKindToolCall:
			wireNamespace, wireName := splitWireFunctionName(p.ToolName)
			argsBytes, _ := json.Marshal(p.Args)
			fcItem := map[string]any{
				"id":        p.OutputItemID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   p.CallID,
				"name":      wireName,
				"arguments": string(argsBytes),
			}
			if wireNamespace != "" {
				fcItem["namespace"] = wireNamespace
			}
			if p.ThoughtSignature != "" {
				fcItem["thought_signature"] = p.ThoughtSignature
			}
			itemsByID[p.OutputItemID] = fcItem

		case PartKindText:
			msgItem := map[string]any{
				"id":     p.OutputItemID,
				"type":   "message",
				"status": "completed",
				"role":   "assistant",
				"content": []map[string]any{
					{
						"type":        "output_text",
						"text":        p.Text,
						"annotations": []any{},
					},
				},
			}
			if p.ThoughtSignature != "" {
				msgItem["thought_signature"] = p.ThoughtSignature
			}
			itemsByID[p.OutputItemID] = msgItem
		}
	}

	var items []map[string]any
	used := make(map[string]bool)

	// Follow OutputOrder if recorded
	for _, id := range t.OutputOrder {
		if it, ok := itemsByID[id]; ok && !used[id] {
			items = append(items, it)
			used[id] = true
		}
	}

	// Fallback/remaining items:
	// If reasoningItem was needed but not in OutputOrder, prepend if thoughts present, else append.
	if reasoningItem != nil && !used[t.ReasoningID] {
		if len(t.ThoughtSummary) > 0 {
			items = append([]map[string]any{reasoningItem}, items...)
		} else {
			items = append(items, reasoningItem)
		}
		used[t.ReasoningID] = true
	}

	for _, p := range t.Parts {
		if it, ok := itemsByID[p.OutputItemID]; ok && !used[p.OutputItemID] {
			items = append(items, it)
			used[p.OutputItemID] = true
		}
	}

	return items
}

// PopulateCache registers turn signatures and provenance in the SignatureCache.
func (t *AuthoritativeTurn) PopulateCache(sigCache *upstream.SignatureCache, prefixHash string) {
	if sigCache == nil {
		return
	}
	for _, p := range t.Parts {
		switch p.Kind {
		case PartKindToolCall:
			rec := &upstream.NativeToolRecord{
				BridgeCallID:     p.CallID,
				OutputItemID:     p.OutputItemID,
				UpstreamID:       p.UpstreamID,
				ToolName:         p.ToolName,
				Args:             p.Args,
				ThoughtSignature: p.ThoughtSignature,
				Model:            t.Model,
				TurnID:           t.TurnID,
			}
			var aliases []string
			if p.OutputItemID != "" {
				aliases = append(aliases, p.OutputItemID)
				if p.CallID != "" {
					aliases = append(aliases, p.CallID+"_"+p.OutputItemID)
				}
			}
			sigCache.PutToolRecord(rec, aliases...)
			if p.ThoughtSignature != "" {
				sigCache.PutMessageSignature(p.CallID, p.ThoughtSignature)
				if p.OutputItemID != "" {
					sigCache.PutMessageSignature(p.OutputItemID, p.ThoughtSignature)
				}
			}
		case PartKindText:
			if p.ThoughtSignature != "" {
				sigCache.PutMessageSignature(p.OutputItemID, p.ThoughtSignature)
				if prefixHash != "" {
					sigCache.PutContextSignature(ResponsesContextKey(prefixHash, p.Text), p.ThoughtSignature)
				}
			}
		}
	}
	for lead, siblings := range t.TurnSiblings {
		sigCache.RecordTurnSiblings(lead, siblings)
	}
}

// AuthoritativeTurnFromOutputItems constructs an AuthoritativeTurn from generated Responses output items.
func AuthoritativeTurnFromOutputItems(outputItems []map[string]any, thoughtSummary string) *AuthoritativeTurn {
	turn := &AuthoritativeTurn{
		Version:        1,
		ReasoningID:    "rs_" + RandomID("rs"),
		ThoughtSummary: thoughtSummary,
		ToolSignatures: make(map[string]string),
		TextSignatures: make(map[string]string),
		TurnSiblings:   make(map[string][]string),
	}

	var toolCallIDs []string

	for _, item := range outputItems {
		itemType, _ := item["type"].(string)
		switch itemType {
		case "function_call":
			callID, _ := item["call_id"].(string)
			fcID, _ := item["id"].(string)
			name, _ := item["name"].(string)
			if ns, ok := item["namespace"].(string); ok && ns != "" {
				name = fmt.Sprintf("%s__%s", ns, name)
			}
			sig, _ := item["thought_signature"].(string)
			var args map[string]any
			if argsStr, ok := item["arguments"].(string); ok && len(argsStr) > 0 {
				_ = json.Unmarshal([]byte(argsStr), &args)
			}
			turn.Parts = append(turn.Parts, TurnPartRecord{
				Index:            len(turn.Parts),
				Kind:             PartKindToolCall,
				CallID:           callID,
				ToolName:         name,
				Args:             args,
				ThoughtSignature: sig,
				OutputItemID:     fcID,
			})
			if callID != "" {
				toolCallIDs = append(toolCallIDs, callID)
			}
			if sig != "" {
				if callID != "" {
					turn.ToolSignatures[callID] = sig
				}
				if fcID != "" {
					turn.ToolSignatures[fcID] = sig
				}
			}

		case "message":
			msgID, _ := item["id"].(string)
			sig, _ := item["thought_signature"].(string)
			var text string
			if content, ok := item["content"].([]map[string]any); ok && len(content) > 0 {
				text, _ = content[0]["text"].(string)
			} else if contentSlice, ok := item["content"].([]any); ok && len(contentSlice) > 0 {
				if cm, ok := contentSlice[0].(map[string]any); ok {
					text, _ = cm["text"].(string)
				}
			}
			turn.Parts = append(turn.Parts, TurnPartRecord{
				Index:            len(turn.Parts),
				Kind:             PartKindText,
				Text:             text,
				ThoughtSignature: sig,
				OutputItemID:     msgID,
			})
			if sig != "" && msgID != "" {
				turn.TextSignatures[msgID] = sig
			}
		}
	}

	if len(toolCallIDs) > 1 {
		turn.TurnSiblings[toolCallIDs[0]] = toolCallIDs[1:]
	}

	return turn
}

