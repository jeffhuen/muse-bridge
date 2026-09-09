package protocols

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/config"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/models"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// ReasoningEncryptedState carries opaque turn state across Responses client turns.
type ReasoningEncryptedState struct {
	Version        int                 `json:"v"`
	Parts          []TurnPartRecord    `json:"parts,omitempty"`
	ToolSignatures map[string]string   `json:"tool_sigs,omitempty"`
	TurnSiblings   map[string][]string `json:"turn_siblings,omitempty"`
	TextSignatures map[string]string   `json:"text_sigs,omitempty"`
	OutputOrder    []string            `json:"output_order,omitempty"`
}

func EncodeReasoningEncryptedContent(state ReasoningEncryptedState) string {
	raw, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func DecodeReasoningEncryptedContent(encrypted string) *ReasoningEncryptedState {
	if encrypted == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		raw = []byte(encrypted)
	}
	var state ReasoningEncryptedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil
	}
	return &state
}

// ConvertChatToPrediction converts an OpenAI ChatRequest into a PredictionRequest.
func ConvertChatToPrediction(req *ChatRequest, sigCache *upstream.SignatureCache) (*upstream.PredictionRequest, error) {
	if err := validateChatToolExchange(req.Messages); err != nil {
		return nil, err
	}

	effort := models.ExtractEffort(req.Reasoning, req.ReasoningEffort, req.Thinking)
	if effort == "" {
		effort = "high"
	}
	resolvedModel := models.ResolveModelWithReasoning(req.Model, effort)
	if resolvedModel == "" {
		resolvedModel = config.DefaultModel
	}


	predReq := &upstream.PredictionRequest{
		Project:   "aicode-consumers",
		RequestID: RandomID("req"),
		Model:     resolvedModel,
		UserAgent: "antigravity",
		Request: upstream.GenerateContentRequest{
			GenerationConfig: &upstream.GenerationConfig{
				MaxOutputTokens: 65536,
				ThinkingConfig: &upstream.ThinkingConfig{
					ThinkingLevel:   effort,
					IncludeThoughts: true,
				},
			},
		},
	}

	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		predReq.Request.GenerationConfig.MaxOutputTokens = *req.MaxTokens
	}
	if req.Temperature != nil {
		predReq.Request.GenerationConfig.Temperature = req.Temperature
	}

	// 1. Convert tools if specified
	if len(req.Tools) > 0 {
		var fds []upstream.FunctionDeclaration
		seen := make(map[string]bool)
		for _, t := range req.Tools {
			switch t.Type {
			case "function", "":
				fnName := t.FunctionName()
				if fnName != "" && !seen[fnName] {
					seen[fnName] = true
					fds = append(fds, upstream.FunctionDeclaration{
						Name:        fnName,
						Description: t.FunctionDescription(),
						Parameters:  t.FunctionParameters(),
					})
				}
			case "namespace":
				for _, sub := range t.Tools {
					fnName := sub.FunctionName()
					if fnName != "" {
						declName := fnName
						if t.Name != "" {
							declName = fmt.Sprintf("%s__%s", t.Name, fnName)
						}
						if !seen[declName] {
							seen[declName] = true
							fds = append(fds, upstream.FunctionDeclaration{
								Name:        declName,
								Description: sub.FunctionDescription(),
								Parameters:  sub.FunctionParameters(),
							})
						}
					}
				}
			case "web_search":
				continue
			case "custom":
				return nil, fmt.Errorf("unsupported tool type %q; custom text tools are not supported", t.Type)
			case "mcp":
				return nil, fmt.Errorf("unsupported tool type %q; remote hosted MCP tools are not supported", t.Type)
			default:
				continue
			}
		}
		if len(fds) > 0 {
			predReq.Request.Tools = []upstream.Tool{{FunctionDeclarations: fds}}
		}
	}

	// Build map of call_id -> function name from assistant messages and track tool results
	callNames := make(map[string]string)
	hasMatchingOutputChat := make(map[string]bool)
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				callNames[tc.ID] = tc.Function.Name
			}
		}
		if strings.ToLower(strings.TrimSpace(m.Role)) == "tool" && m.ToolCallID != "" {
			hasMatchingOutputChat[m.ToolCallID] = true
		}
	}

	// Find the most recent standard user message (start of current turn)
	lastUserIdx := -1
	for idx := len(req.Messages) - 1; idx >= 0; idx-- {
		if strings.ToLower(strings.TrimSpace(req.Messages[idx].Role)) == "user" {
			lastUserIdx = idx
			break
		}
	}

	// 2. Convert messages to systemInstruction and contents
	var rawContents []upstream.Content
	for i, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "system", "developer":
			text := extractMessageText(m.Content)
			if text != "" {
				if predReq.Request.SystemInstruction == nil {
					predReq.Request.SystemInstruction = &upstream.Content{Role: "user"}
				}
				predReq.Request.SystemInstruction.Parts = append(predReq.Request.SystemInstruction.Parts, upstream.Part{Text: text})
			}

		case "assistant":
			var parts []upstream.Part
			text := extractMessageText(m.Content)
			prefixHash := ComputeChatContextHash(req.Messages[:i], req.Model, req.Tools)
			ctxKey := ChatContextKey(prefixHash, text)
			if text != "" {
				textPart := upstream.Part{Text: text}
				if m.ThoughtSignature != "" {
					textPart.ThoughtSignature = m.ThoughtSignature
				} else if sig := extractReasoningDetailsSignature(m.ReasoningDetails, sigCache); sig != "" {
					textPart.ThoughtSignature = sig
				} else if m.ID != "" && sigCache != nil && sigCache.GetMessageSignature(m.ID) != "" {
					textPart.ThoughtSignature = sigCache.GetMessageSignature(m.ID)
				} else if sigCache != nil && sigCache.GetContextSignature(ctxKey) != "" {
					textPart.ThoughtSignature = sigCache.GetContextSignature(ctxKey)
				}
				// If not unambiguously known, textPart.ThoughtSignature is omitted.
				parts = append(parts, textPart)
			}
			for _, tc := range m.ToolCalls {
				if tc.Function.Name != "" {
					var args map[string]any
					if len(tc.Function.Arguments) > 0 {
						_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
					}
					callPart := upstream.Part{
						FunctionCall: &upstream.FunctionCall{
							Name: tc.Function.Name,
							Args: args,
							ID:   tc.ID,
						},
					}

					ev := ProvenanceEvidence{
						CallID:               tc.ID,
						Name:                 tc.Function.Name,
						Args:                 args,
						HasMatchingOutput:    hasMatchingOutputChat[tc.ID],
						IsAffirmativeForeign: IsChatMessageAffirmativelyForeign(&m) || IsChatToolCallAffirmativelyForeign(&tc),
						IsCurrentTurn:        (lastUserIdx == -1 || i > lastUserIdx),
					}
					if tc.ThoughtSignature != "" {
						ev.CarrierSig = tc.ThoughtSignature
						ev.HasCarrier = true
					}
					if sigCache != nil && tc.ID != "" {
						cachedName := sigCache.GetToolName(tc.ID)
						cachedArgs := sigCache.GetToolArgs(tc.ID)
						if cachedName != "" || cachedArgs != nil {
							ev.HasCache = true
							if (cachedName != "" && cachedName != tc.Function.Name) || (cachedArgs != nil && !equalToolArgs(cachedArgs, args)) {
								ev.CacheMismatch = true
							} else {
								ev.CacheSig = sigCache.GetToolSignature(tc.ID)
							}
						} else {
							if sig := sigCache.GetToolSignature(tc.ID); sig != "" {
								ev.CacheSig = sig
								ev.HasCache = true
							} else if sig := sigCache.GetMessageSignature(tc.ID); sig != "" {
								ev.CacheSig = sig
								ev.HasCache = true
							}
						}
					}
					if ev.CarrierSig == "" && len(m.ToolCalls) == 1 {
						if sig := extractReasoningDetailsSignature(m.ReasoningDetails, sigCache); sig != "" {
							ev.CarrierSig = sig
							ev.HasCarrier = true
						}
					}

					// Sibling inheritance for parallel tool calls in the same chat turn
					if !ev.CarrierMismatch && !ev.CacheMismatch && ev.CarrierSig == "" && ev.CacheSig == "" && len(m.ToolCalls) > 1 {
						hasNativeOther := false
						for _, other := range m.ToolCalls {
							if other.ID == "" || other.ID == tc.ID {
								continue
							}
							var otherSig string
							if other.ThoughtSignature != "" {
								otherSig = other.ThoughtSignature
							} else if sigCache != nil {
								otherSig = sigCache.GetToolSignature(other.ID)
								if otherSig == "" {
									otherSig = sigCache.GetMessageSignature(other.ID)
								}
							}
							if otherSig != "" {
								hasNativeOther = true
								if sigCache != nil && sigCache.IsVerifiedSibling(other.ID, tc.ID) {
									ev.IsRecordedSib = true
									ev.HasSiblingLead = true
									ev.SiblingLeadSig = otherSig
									break
								}
							}
						}
						if hasNativeOther && !ev.IsRecordedSib && !ev.IsAffirmativeForeign {
							ev.NativeTurnUnverified = true
						}
					}

					res, err := ResolveToolSignature(ev)
					if err != nil {
						return nil, err
					}
					callPart.ThoughtSignature = res.ThoughtSignature
					parts = append(parts, callPart)
				}
			}
			if len(parts) > 0 {
				rawContents = append(rawContents, upstream.Content{Role: "model", Parts: parts})
			}

		case "tool":
			// Tool output in Gemini is sent under role "user" with functionResponse
			text := extractMessageText(m.Content)
			var respObj map[string]any
			if err := json.Unmarshal([]byte(text), &respObj); err != nil {
				respObj = map[string]any{"response": text}
			}
			funcName := m.Name
			if funcName == "" {
				funcName = callNames[m.ToolCallID]
			}
			if funcName == "" && sigCache != nil {
				funcName = sigCache.GetToolName(m.ToolCallID)
			}
			if funcName == "" {
				funcName = m.ToolCallID
			}
			rawContents = append(rawContents, upstream.Content{
				Role: "user",
				Parts: []upstream.Part{
					{
						FunctionResponse: &upstream.FunctionResponse{
							Name:     funcName,
							Response: respObj,
							ID:       m.ToolCallID,
						},
					},
				},
			})

		default: // "user"
			text := extractMessageText(m.Content)
			if text != "" {
				rawContents = append(rawContents, upstream.Content{
					Role:  "user",
					Parts: []upstream.Part{{Text: text}},
				})
			}
		}
	}

	// 3. Normalize contents to ensure alternating roles (merge consecutive same-role messages)
	predReq.Request.Contents = NormalizeAlternatingContents(rawContents)
	if len(predReq.Request.Contents) == 0 {
		return nil, fmt.Errorf("conversation has no user or model messages")
	}

	return predReq, nil
}

// ConvertResponsesToPrediction converts an OpenAI ResponsesRequest into a PredictionRequest.
func ConvertResponsesToPrediction(req *ResponsesRequest, sigCache *upstream.SignatureCache) (*upstream.PredictionRequest, error) {
	effort := models.ExtractEffort(req.Reasoning, req.ReasoningEffort, req.Thinking)
	if effort == "" {
		effort = "high"
	}
	resolvedModel := models.ResolveModelWithReasoning(req.Model, effort)
	if resolvedModel == "" {
		resolvedModel = config.DefaultModel
	}

	predReq := &upstream.PredictionRequest{
		Project:   "aicode-consumers",
		RequestID: RandomID("req"),
		Model:     resolvedModel,
		UserAgent: "antigravity",
		Request: upstream.GenerateContentRequest{
			GenerationConfig: &upstream.GenerationConfig{
				MaxOutputTokens: 65536,
				ThinkingConfig: &upstream.ThinkingConfig{
					ThinkingLevel:   effort,
					IncludeThoughts: true,
				},
			},
		},
	}

	if strings.TrimSpace(req.PreviousResponseID) != "" {
		return nil, fmt.Errorf("previous_response_id is unsupported; full history must be provided in input")
	}

	if strings.TrimSpace(req.Instructions) != "" {
		predReq.Request.SystemInstruction = &upstream.Content{
			Role:  "user",
			Parts: []upstream.Part{{Text: strings.TrimSpace(req.Instructions)}},
		}
	}

	if len(req.Tools) > 0 {
		var fds []upstream.FunctionDeclaration
		seen := make(map[string]bool)
		for _, t := range req.Tools {
			switch t.Type {
			case "function", "":
				fnName := t.FunctionName()
				if fnName != "" && !seen[fnName] {
					seen[fnName] = true
					fds = append(fds, upstream.FunctionDeclaration{
						Name:        fnName,
						Description: t.FunctionDescription(),
						Parameters:  t.FunctionParameters(),
					})
				}
			case "namespace":
				for _, sub := range t.Tools {
					fnName := sub.FunctionName()
					if fnName != "" {
						declName := fnName
						if t.Name != "" {
							declName = fmt.Sprintf("%s__%s", t.Name, fnName)
						}
						if !seen[declName] {
							seen[declName] = true
							fds = append(fds, upstream.FunctionDeclaration{
								Name:        declName,
								Description: sub.FunctionDescription(),
								Parameters:  sub.FunctionParameters(),
							})
						}
					}
				}
			case "web_search":
				continue
			case "custom":
				return nil, fmt.Errorf("unsupported tool type %q; custom text tools are not supported", t.Type)
			case "mcp":
				return nil, fmt.Errorf("unsupported tool type %q; remote hosted MCP tools are not supported", t.Type)
			default:
				continue
			}
		}
		if len(fds) > 0 {
			predReq.Request.Tools = []upstream.Tool{{FunctionDeclarations: fds}}
		}
	}

	var rawContents []upstream.Content

	var singleStr string
	if err := json.Unmarshal(req.Input, &singleStr); err == nil && singleStr != "" {
		rawContents = append(rawContents, upstream.Content{
			Role:  "user",
			Parts: []upstream.Part{{Text: singleStr}},
		})
	} else {
		var items []map[string]any
		if err := json.Unmarshal(req.Input, &items); err == nil {
			if err := validateResponsesToolExchange(items); err != nil {
				return nil, err
			}
			callNames := make(map[string]string)
			hasMatchingOutput := make(map[string]bool)
			callAlias := make(map[string]string)
			for _, item := range items {
				itemType, _ := item["type"].(string)
				if itemType == "function_call" {
					name, _ := item["name"].(string)
					namespace, _ := item["namespace"].(string)
					if namespace != "" {
						name = fmt.Sprintf("%s__%s", namespace, name)
					}
					callID, _ := item["call_id"].(string)
					itemID, _ := item["id"].(string)
					canonicalID := callID
					if canonicalID == "" {
						canonicalID = itemID
					}
					if canonicalID != "" {
						callAlias[canonicalID] = canonicalID
						if callID != "" {
							callAlias[callID] = canonicalID
						}
						if itemID != "" {
							callAlias[itemID] = canonicalID
						}
					}
					if name != "" {
						if canonicalID != "" {
							callNames[canonicalID] = name
						}
						if callID != "" {
							callNames[callID] = name
						}
						if itemID != "" {
							callNames[itemID] = name
						}
					}
				} else if itemType == "function_call_output" {
					if cID, _ := item["call_id"].(string); cID != "" {
						canonical := callAlias[cID]
						if canonical == "" {
							canonical = cID
						}
						hasMatchingOutput[canonical] = true
					}
				}
			}

			// Pre-scan for reasoning items carrying opaque turn state
			type verifiedTextPart struct {
				Text string
				Sig  string
			}
			type verifiedToolPart struct {
				Name string
				Args map[string]any
				Sig  string
			}
			reasoningVerifiedText := make(map[string]verifiedTextPart)
			reasoningVerifiedTools := make(map[string]verifiedToolPart)
			carrierSiblings := make(map[string]map[string]bool)

			for _, item := range items {
				itemType, _ := item["type"].(string)
				if itemType == "reasoning" {
					if enc, ok := item["encrypted_content"].(string); ok && enc != "" {
						if state := DecodeReasoningEncryptedContent(enc); state != nil {
							for _, p := range state.Parts {
								if p.Kind == PartKindText && p.ThoughtSignature != "" {
									vp := verifiedTextPart{Text: p.Text, Sig: p.ThoughtSignature}
									if p.OutputItemID != "" {
										reasoningVerifiedText[p.OutputItemID] = vp
									}
								} else if p.Kind == PartKindToolCall {
									tp := verifiedToolPart{Name: p.ToolName, Args: p.Args, Sig: p.ThoughtSignature}
									if p.CallID != "" {
										reasoningVerifiedTools[p.CallID] = tp
									}
									if p.OutputItemID != "" {
										reasoningVerifiedTools[p.OutputItemID] = tp
									}
								}
							}
							for lead, sibs := range state.TurnSiblings {
								if carrierSiblings[lead] == nil {
									carrierSiblings[lead] = make(map[string]bool)
								}
								for _, s := range sibs {
									carrierSiblings[lead][s] = true
								}
								if sigCache != nil {
									sigCache.RecordTurnSiblings(lead, sibs)
								}
							}
						}
					}
				}
			}

			// checkToolContent validates a tool call against the reasoning carrier and sigCache.
			// Returns isMismatched=true if the tool ID was recognized in carrier or cache but had different name/args.
			checkToolContent := func(cID, itID, name string, args map[string]any) (isMismatched bool, carrierSig string, hasCarrier bool) {
				var vtp *verifiedToolPart
				if cID != "" {
					if p, ok := reasoningVerifiedTools[cID]; ok {
						vtp = &p
					}
				}
				if vtp == nil && itID != "" {
					if p, ok := reasoningVerifiedTools[itID]; ok {
						vtp = &p
					}
				}
				if vtp != nil {
					hasCarrier = true
					if vtp.Name != name || !equalToolArgs(vtp.Args, args) {
						return true, "", true
					}
					return false, vtp.Sig, true
				}

				// If not in carrier, check sigCache
				if sigCache != nil {
					cachedName := ""
					var cachedArgs map[string]any
					if cID != "" {
						cachedName = sigCache.GetToolName(cID)
						cachedArgs = sigCache.GetToolArgs(cID)
					}
					if cachedName == "" && itID != "" {
						cachedName = sigCache.GetToolName(itID)
					}
					if cachedArgs == nil && itID != "" {
						cachedArgs = sigCache.GetToolArgs(itID)
					}
					if cachedName != "" && cachedName != name {
						return true, "", false
					}
					if cachedArgs != nil && !equalToolArgs(cachedArgs, args) {
						return true, "", false
					}
				}
				return false, "", false
			}

			// Pre-check tool call groups for signature presence
			type toolGroupInfo struct {
				signedCallID string
			}
			groupInfo := make(map[int]*toolGroupInfo)
			for i := 0; i < len(items); i++ {
				itemType, _ := items[i]["type"].(string)
				if itemType == "function_call" {
					j := i
					var signedLead string
					for j < len(items) {
						jType, _ := items[j]["type"].(string)
						if jType == "reasoning" {
							j++
							continue
						}
						if jType != "function_call" {
							break
						}
						cID, _ := items[j]["call_id"].(string)
						itID, _ := items[j]["id"].(string)
						if cID == "" {
							cID = itID
						}
						jName, _ := items[j]["name"].(string)
						jNs, _ := items[j]["namespace"].(string)
						if jNs != "" {
							jName = fmt.Sprintf("%s__%s", jNs, jName)
						}
						jArgsStr, _ := items[j]["arguments"].(string)
						var jArgs map[string]any
						if jArgsStr != "" {
							_ = json.Unmarshal([]byte(jArgsStr), &jArgs)
						}

						mismatched, carrierSig, _ := checkToolContent(cID, itID, jName, jArgs)
						if !mismatched {
							sig := carrierSig
							if sig == "" {
								if s, ok := items[j]["thought_signature"].(string); ok && s != "" {
									sig = s
								} else if s, ok := items[j]["thoughtSignature"].(string); ok && s != "" {
									sig = s
								}
							}
							if sig == "" && sigCache != nil {
								if cID != "" {
									sig = sigCache.GetToolSignature(cID)
									if sig == "" {
										sig = sigCache.GetMessageSignature(cID)
									}
								}
								if sig == "" && itID != "" {
									sig = sigCache.GetToolSignature(itID)
									if sig == "" {
										sig = sigCache.GetMessageSignature(itID)
									}
								}
							}
							if sig != "" && signedLead == "" {
								signedLead = cID
							}
						}
						j++
					}
					info := &toolGroupInfo{signedCallID: signedLead}
					for k := i; k < j; k++ {
						groupInfo[k] = info
					}
					i = j - 1
				}
			}

			// Find the most recent standard user message (start of current turn)
			lastUserTextIdx := -1
			for idx := len(items) - 1; idx >= 0; idx-- {
				itType, _ := items[idx]["type"].(string)
				r, _ := items[idx]["role"].(string)
				if itType == "function_call" || itType == "function_call_output" || itType == "reasoning" {
					continue
				}
				if r == "user" || (itType == "message" && r == "user") {
					lastUserTextIdx = idx
					break
				}
			}

			for idx, item := range items {
				itemType, _ := item["type"].(string)
				role, _ := item["role"].(string)

				if itemType == "reasoning" {
					continue
				}

				if itemType == "function_call" {
					name, _ := item["name"].(string)
					namespace, _ := item["namespace"].(string)
					if namespace != "" {
						name = fmt.Sprintf("%s__%s", namespace, name)
					}
					callID, _ := item["call_id"].(string)
					itemID, _ := item["id"].(string)
					if callID == "" {
						callID = itemID
					}
					argsStr, _ := item["arguments"].(string)
					var args map[string]any
					if argsStr != "" {
						_ = json.Unmarshal([]byte(argsStr), &args)
					}
					callPart := upstream.Part{
						FunctionCall: &upstream.FunctionCall{
							Name: name,
							Args: args,
							ID:   callID,
						},
					}

					canonicalID := callID
					if canonicalID == "" {
						canonicalID = itemID
					}
					ev := ProvenanceEvidence{
						CallID:               callID,
						ItemID:               itemID,
						Name:                 name,
						Args:                 args,
						HasMatchingOutput:    hasMatchingOutput[canonicalID],
						IsAffirmativeForeign: IsItemAffirmativelyForeign(item),
						IsCurrentTurn:        (lastUserTextIdx == -1 || idx > lastUserTextIdx),
					}

					var vtp *verifiedToolPart
					if callID != "" {
						if p, ok := reasoningVerifiedTools[callID]; ok {
							vtp = &p
						}
					}
					if vtp == nil && itemID != "" {
						if p, ok := reasoningVerifiedTools[itemID]; ok {
							vtp = &p
						}
					}
					if vtp != nil {
						ev.HasCarrier = true
						if vtp.Name != name || !equalToolArgs(vtp.Args, args) {
							ev.CarrierMismatch = true
						} else {
							ev.CarrierSig = vtp.Sig
						}
					}

					if sigCache != nil {
						cachedName := ""
						var cachedArgs map[string]any
						if callID != "" {
							cachedName = sigCache.GetToolName(callID)
							cachedArgs = sigCache.GetToolArgs(callID)
						}
						if cachedName == "" && itemID != "" {
							cachedName = sigCache.GetToolName(itemID)
						}
						if cachedArgs == nil && itemID != "" {
							cachedArgs = sigCache.GetToolArgs(itemID)
						}
						if cachedName != "" || cachedArgs != nil {
							ev.HasCache = true
							if (cachedName != "" && cachedName != name) || (cachedArgs != nil && !equalToolArgs(cachedArgs, args)) {
								ev.CacheMismatch = true
							} else {
								sig := sigCache.GetToolSignature(callID)
								if sig == "" && itemID != "" {
									sig = sigCache.GetToolSignature(itemID)
								}
								if sig == "" && callID != "" {
									sig = sigCache.GetMessageSignature(callID)
								}
								if sig == "" && itemID != "" {
									sig = sigCache.GetMessageSignature(itemID)
								}
								ev.CacheSig = sig
							}
						} else {
							sig := sigCache.GetToolSignature(callID)
							if sig == "" && itemID != "" {
								sig = sigCache.GetToolSignature(itemID)
							}
							if sig == "" && callID != "" {
								sig = sigCache.GetMessageSignature(callID)
							}
							if sig == "" && itemID != "" {
								sig = sigCache.GetMessageSignature(itemID)
							}
							if sig != "" {
								ev.HasCache = true
								ev.CacheSig = sig
							}
						}
					}

					if !ev.CarrierMismatch && !ev.CacheMismatch {
						if sig, ok := item["thought_signature"].(string); ok && sig != "" {
							ev.CarrierSig = sig
							ev.HasCarrier = true
						} else if sig, ok := item["thoughtSignature"].(string); ok && sig != "" {
							ev.CarrierSig = sig
							ev.HasCarrier = true
						}
					}

					if !ev.CarrierMismatch && !ev.CacheMismatch {
						info := groupInfo[idx]
						if info != nil && info.signedCallID != "" && info.signedCallID != callID {
							leadCallID := info.signedCallID
							isRecordedSibling := carrierSiblings[leadCallID][callID] ||
								(itemID != "" && carrierSiblings[leadCallID][itemID]) ||
								(sigCache != nil && (sigCache.IsVerifiedSibling(leadCallID, callID) || (itemID != "" && sigCache.IsVerifiedSibling(leadCallID, itemID))))
							if isRecordedSibling {
								ev.IsRecordedSib = true
								ev.HasSiblingLead = true
								var leadSig string
								if lp, ok := reasoningVerifiedTools[leadCallID]; ok && lp.Sig != "" {
									leadSig = lp.Sig
								}
								if leadSig == "" && sigCache != nil {
									leadSig = sigCache.GetToolSignature(leadCallID)
									if leadSig == "" {
										leadSig = sigCache.GetMessageSignature(leadCallID)
									}
								}
								ev.SiblingLeadSig = leadSig
							} else if !ev.IsAffirmativeForeign && !ev.HasCarrier && (!ev.HasCache || ev.CacheSig == "") {
								ev.NativeTurnUnverified = true
							}
						}
					}

					res, err := ResolveToolSignature(ev)
					if err != nil {
						return nil, err
					}
					callPart.ThoughtSignature = res.ThoughtSignature
					rawContents = append(rawContents, upstream.Content{
						Role:  "model",
						Parts: []upstream.Part{callPart},
					})
					continue
				}

				if itemType == "function_call_output" {
					callID, _ := item["call_id"].(string)
					outputStr, _ := item["output"].(string)
					var respObj map[string]any
					if err := json.Unmarshal([]byte(outputStr), &respObj); err != nil {
						respObj = map[string]any{"response": outputStr}
					}
					canonicalID := callAlias[callID]
					if canonicalID == "" {
						canonicalID = callID
					}
					funcName := callNames[canonicalID]
					if funcName == "" {
						funcName = callNames[callID]
					}
					if funcName == "" && sigCache != nil {
						funcName = sigCache.GetToolName(canonicalID)
						if funcName == "" && callID != canonicalID {
							funcName = sigCache.GetToolName(callID)
						}
					}
					if funcName == "" {
						funcName = canonicalID
					}
					rawContents = append(rawContents, upstream.Content{
						Role: "user",
						Parts: []upstream.Part{
							{
								FunctionResponse: &upstream.FunctionResponse{
									Name:     funcName,
									Response: respObj,
									ID:       canonicalID,
								},
							},
						},
					})
					continue
				}

				contentVal := item["content"]
				var text string
				switch c := contentVal.(type) {
				case string:
					text = c
				case []any:
					var parts []string
					for _, p := range c {
						if pm, ok := p.(map[string]any); ok {
							if t, ok := pm["text"].(string); ok {
								parts = append(parts, t)
							}
						}
					}
					text = strings.Join(parts, "\n")
				}

				if role == "system" || role == "developer" || itemType == "system" || itemType == "developer" {
					if predReq.Request.SystemInstruction == nil {
						predReq.Request.SystemInstruction = &upstream.Content{Role: "user"}
					}
					predReq.Request.SystemInstruction.Parts = append(predReq.Request.SystemInstruction.Parts, upstream.Part{Text: text})
					continue
				}

				if role == "" {
					role = "user"
				}

				if role == "assistant" {
					if text != "" {
						p := upstream.Part{Text: text}
						id, _ := item["id"].(string)
						itemID, _ := item["item_id"].(string)
						if sig, ok := item["thought_signature"].(string); ok && sig != "" {
							p.ThoughtSignature = sig
						} else if sig, ok := item["thoughtSignature"].(string); ok && sig != "" {
							p.ThoughtSignature = sig
						} else if vtp, ok := reasoningVerifiedText[id]; ok && vtp.Text == text {
							p.ThoughtSignature = vtp.Sig
						} else if vtp, ok := reasoningVerifiedText[itemID]; ok && vtp.Text == text {
							p.ThoughtSignature = vtp.Sig
						} else if id != "" && sigCache != nil && sigCache.GetMessageSignature(id) != "" {
							prefixBytes, _ := json.Marshal(items[:idx])
							prefixHash := ComputeResponsesContextHash(prefixBytes, req.Instructions, req.Model, req.Tools)
							if sig := sigCache.GetContextSignature(ResponsesContextKey(prefixHash, text)); sig != "" {
								p.ThoughtSignature = sig
							}
						}
						// If not unambiguously known, ThoughtSignature is omitted.
						rawContents = append(rawContents, upstream.Content{
							Role:  "model",
							Parts: []upstream.Part{p},
						})
					}
				} else {
					if text != "" {
						rawContents = append(rawContents, upstream.Content{
							Role:  "user",
							Parts: []upstream.Part{{Text: text}},
						})
					}
				}
			}
		}
	}

	predReq.Request.Contents = NormalizeAlternatingContents(rawContents)
	if len(predReq.Request.Contents) == 0 {
		return nil, fmt.Errorf("responses conversation has no valid input contents")
	}

	return predReq, nil
}

// NormalizeAlternatingContents combines adjacent same-role messages into a single Content with multiple Parts.
func NormalizeAlternatingContents(contents []upstream.Content) []upstream.Content {
	var cleanContents []upstream.Content
	for _, c := range contents {
		var cleanParts []upstream.Part
		for _, p := range c.Parts {
			if p.Text == "" && p.FunctionCall == nil && p.FunctionResponse == nil {
				continue
			}
			cleanParts = append(cleanParts, p)
		}
		if len(cleanParts) > 0 {
			cleanContents = append(cleanContents, upstream.Content{
				Role:  c.Role,
				Parts: cleanParts,
			})
		}
	}

	if len(cleanContents) <= 1 {
		return cleanContents
	}

	var merged []upstream.Content
	for _, c := range cleanContents {
		if len(c.Parts) == 0 {
			continue
		}
		if len(merged) == 0 {
			merged = append(merged, c)
			continue
		}

		last := &merged[len(merged)-1]
		if last.Role == c.Role {
			// Merge parts into existing turn
			last.Parts = append(last.Parts, c.Parts...)
		} else {
			merged = append(merged, c)
		}
	}
	return merged
}

func extractMessageText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		return str
	}
	var parts []map[string]any
	if err := json.Unmarshal(content, &parts); err == nil {
		var textParts []string
		for _, p := range parts {
			if t, ok := p["text"].(string); ok && t != "" {
				textParts = append(textParts, t)
			}
		}
		return strings.Join(textParts, "\n")
	}
	return strings.TrimSpace(string(content))
}

// ComputeChatContextHash creates a deterministic hash of the conversation messages preceding a turn,
// including the model and tool definitions to prevent collisions across different configurations.
func ComputeChatContextHash(messages []ChatMessage, model string, tools []ToolDefinition) string {
	if len(messages) == 0 && model == "" && len(tools) == 0 {
		return ""
	}
	h := sha256.New()
	if model != "" {
		h.Write([]byte("model:"))
		h.Write([]byte(model))
		h.Write([]byte("\n"))
	}
	if len(tools) > 0 {
		h.Write([]byte("tools:"))
		toolsBytes, _ := json.Marshal(tools)
		h.Write(toolsBytes)
		h.Write([]byte("\n"))
	}
	for _, m := range messages {
		h.Write([]byte(m.Role))
		h.Write([]byte(":"))
		h.Write([]byte(extractMessageText(m.Content)))
		for _, tc := range m.ToolCalls {
			h.Write([]byte(tc.ID))
			h.Write([]byte(tc.Function.Name))
			h.Write([]byte(tc.Function.Arguments))
		}
		if m.ToolCallID != "" {
			h.Write([]byte(m.ToolCallID))
		}
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ChatContextKey returns a unique cache key combining conversation prefix hash and message text.
func ChatContextKey(prefixHash, text string) string {
	trimmed := strings.TrimSpace(text)
	if prefixHash == "" {
		return trimmed
	}
	return prefixHash + ":" + trimmed
}

// ComputeResponsesContextHash creates a deterministic hash of the responses context preceding a turn.
func ComputeResponsesContextHash(input json.RawMessage, instructions string, model string, tools []ToolDefinition) string {
	if len(input) == 0 && instructions == "" && model == "" && len(tools) == 0 {
		return ""
	}
	h := sha256.New()
	if model != "" {
		h.Write([]byte("model:"))
		h.Write([]byte(model))
		h.Write([]byte("\n"))
	}
	if instructions != "" {
		h.Write([]byte("instructions:"))
		h.Write([]byte(instructions))
		h.Write([]byte("\n"))
	}
	if len(tools) > 0 {
		h.Write([]byte("tools:"))
		toolsBytes, _ := json.Marshal(tools)
		h.Write(toolsBytes)
		h.Write([]byte("\n"))
	}
	if len(input) > 0 {
		h.Write([]byte("input:"))
		h.Write(input)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ResponsesContextKey returns a unique cache key combining responses prefix hash and message text.
func ResponsesContextKey(prefixHash, text string) string {
	trimmed := strings.TrimSpace(text)
	if prefixHash == "" {
		return trimmed
	}
	return prefixHash + ":" + trimmed
}

func extractReasoningDetailsSignature(details []map[string]any, sigCache *upstream.SignatureCache) string {
	for _, d := range details {
		if d == nil {
			continue
		}
		if id, ok := d["id"].(string); ok && sigCache != nil && strings.TrimSpace(id) != "" {
			if sig := sigCache.GetMessageSignature(strings.TrimSpace(id)); sig != "" {
				return sig
			}
		}
		if data, ok := d["data"].(string); ok && strings.TrimSpace(data) != "" {
			return strings.TrimSpace(data)
		}
		if sig, ok := d["signature"].(string); ok && strings.TrimSpace(sig) != "" {
			return strings.TrimSpace(sig)
		}
	}
	return ""
}

func equalToolArgs(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	bytesA, errA := json.Marshal(a)
	bytesB, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(bytesA) == string(bytesB)
}

func validateResponsesToolExchange(items []map[string]any) error {
	callAlias := make(map[string]string)
	canonicalCalls := make(map[string]bool)
	for _, item := range items {
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			callID, _ := item["call_id"].(string)
			itemID, _ := item["id"].(string)
			canonicalID := callID
			if canonicalID == "" {
				canonicalID = itemID
			}
			if canonicalID == "" {
				return fmt.Errorf("function_call missing call_id and id")
			}
			if canonicalCalls[canonicalID] {
				return fmt.Errorf("duplicate function call id %q", canonicalID)
			}
			canonicalCalls[canonicalID] = true

			if existing, ok := callAlias[canonicalID]; ok && existing != canonicalID {
				return fmt.Errorf("conflicting tool call identifier %q: already bound to call %q", canonicalID, existing)
			}
			callAlias[canonicalID] = canonicalID

			if callID != "" && callID != canonicalID {
				if existing, ok := callAlias[callID]; ok && existing != canonicalID {
					return fmt.Errorf("conflicting tool call identifier %q: already bound to call %q", callID, existing)
				}
				callAlias[callID] = canonicalID
			}
			if itemID != "" && itemID != canonicalID {
				if existing, ok := callAlias[itemID]; ok && existing != canonicalID {
					return fmt.Errorf("conflicting tool call identifier %q: already bound to call %q", itemID, existing)
				}
				callAlias[itemID] = canonicalID
			}
		}
	}

	seenCalls := make(map[string]int)
	seenResults := make(map[string]int)

	for _, item := range items {
		itemType, _ := item["type"].(string)
		if itemType == "function_call" {
			callID, _ := item["call_id"].(string)
			itemID, _ := item["id"].(string)
			canonicalID := callID
			if canonicalID == "" {
				canonicalID = itemID
			}
			if seenResults[canonicalID] > 0 {
				return fmt.Errorf("invalid tool result for call %q: result precedes function call", canonicalID)
			}
			if seenCalls[canonicalID] > 0 {
				return fmt.Errorf("duplicate function call id %q", canonicalID)
			}
			seenCalls[canonicalID]++
		} else if itemType == "function_call_output" {
			refID, _ := item["call_id"].(string)
			if refID == "" {
				return fmt.Errorf("function_call_output item missing call_id")
			}
			canonicalID := callAlias[refID]
			if canonicalID == "" {
				canonicalID = refID
			}
			if seenCalls[canonicalID] == 0 {
				if canonicalCalls[canonicalID] {
					return fmt.Errorf("invalid tool result for call %q: result precedes function call", refID)
				}
				return fmt.Errorf("orphan tool result for call %q: no corresponding function call found", refID)
			}
			if seenResults[canonicalID] > 0 {
				return fmt.Errorf("duplicate tool result for call %q", refID)
			}
			seenResults[canonicalID]++
		}
	}
	return nil
}

func validateChatToolExchange(messages []ChatMessage) error {
	allCalls := make(map[string]bool)
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				allCalls[tc.ID] = true
			}
		}
	}

	seenCalls := make(map[string]int)
	seenResults := make(map[string]int)

	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					if seenResults[tc.ID] > 0 {
						return fmt.Errorf("invalid tool result for call %q: result precedes function call", tc.ID)
					}
					if seenCalls[tc.ID] > 0 {
						return fmt.Errorf("duplicate function call id %q", tc.ID)
					}
					seenCalls[tc.ID]++
				}
			}
		}
		if role == "tool" {
			callID := m.ToolCallID
			if callID == "" {
				return fmt.Errorf("tool message missing tool_call_id")
			}
			if seenCalls[callID] == 0 {
				if allCalls[callID] {
					return fmt.Errorf("invalid tool result for call %q: result precedes function call", callID)
				}
				return fmt.Errorf("orphan tool result for call %q: no corresponding function call found", callID)
			}
			if seenResults[callID] > 0 {
				return fmt.Errorf("duplicate tool result for call %q", callID)
			}
			seenResults[callID]++
		}
	}
	return nil
}

