// Package protocols handles serialization and SSE streaming for OpenAI Protocols.
package protocols

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// RandomID generates a standard hex ID with a prefix.
func RandomID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// ResponsesRequest represents OpenAI's /v1/responses payload used by Codex CLI and pi.
type ResponsesRequest struct {
	Model              string           `json:"model"`
	Input              json.RawMessage  `json:"input"`
	Instructions       string           `json:"instructions"`
	PreviousResponseID string           `json:"previous_response_id,omitempty"`
	Tools              []ToolDefinition `json:"tools,omitempty"`
	Stream             bool             `json:"stream"`
	Reasoning          any              `json:"reasoning,omitempty"`
	ReasoningEffort    string           `json:"reasoning_effort,omitempty"`
	Thinking           any              `json:"thinking,omitempty"`
}

// HandleResponses handles POST /v1/responses by proxying directly to PredictionService.
func HandleResponses(w http.ResponseWriter, r *http.Request, client *upstream.Client) {
	var req ResponsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %s", err.Error()))
		return
	}

	predReq, err := ConvertResponsesToPrediction(&req, client.SigCache())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("translate request: %s", err.Error()))
		return
	}

	log.Printf("[responses] total contents: %d", len(predReq.Request.Contents))
	if len(predReq.Request.Contents) > 86 {
		for i := 84; i <= 88 && i < len(predReq.Request.Contents); i++ {
			c := predReq.Request.Contents[i]
			var partTypes []string
			for _, p := range c.Parts {
				if p.FunctionCall != nil {
					partTypes = append(partTypes, fmt.Sprintf("FC:%s:sig=%q", p.FunctionCall.Name, p.ThoughtSignature))
				} else if p.FunctionResponse != nil {
					partTypes = append(partTypes, fmt.Sprintf("FR:%s", p.FunctionResponse.Name))
				} else if p.Text != "" {
					partTypes = append(partTypes, fmt.Sprintf("Text:%d:sig=%q", len(p.Text), p.ThoughtSignature))
				}
			}
			log.Printf("[responses] Content[%d] role=%s parts=%v", i, c.Role, partTypes)
		}
	}

	stream, err := client.StreamGenerateContent(r.Context(), predReq)
	if err != nil {
		log.Printf("[responses] upstream error: %v", err)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream call failed: %s", err.Error()))
		return
	}
	defer stream.Close()

	respID := RandomID("resp")
	itemID := RandomID("msg")
	created := time.Now().Unix()
	prefixHash := ComputeResponsesContextHash(req.Input, req.Instructions, req.Model, req.Tools)

	if !req.Stream {
		handleNonStreamingResponses(w, stream, respID, itemID, created, predReq.Model, client.SigCache(), prefixHash)
		return
	}

	handleStreamingResponses(w, stream, respID, itemID, created, predReq.Model, client.SigCache(), prefixHash)
}

func handleStreamingResponses(w http.ResponseWriter, stream io.Reader, respID, itemID string, created int64, model string, sigCache *upstream.SignatureCache, prefixHash ...string) {
	var pHash string
	if len(prefixHash) > 0 {
		pHash = prefixHash[0]
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sequence := 0
	sendSSE := func(event string, data map[string]any) {
		data["sequence_number"] = sequence
		sequence++
		bytes, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(bytes))
		flusher.Flush()
	}

	// 1. response.created
	sendSSE("response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": created,
			"model":      model,
			"status":     "in_progress",
		},
	})

	decoder := NewUpstreamDecoder(stream)
	var lastUsage map[string]any
	outputIndex := 0

	turnAcc := NewAuthoritativeTurnAccumulator(itemID)
	turnAcc.SetModel(model)
	if respID != "" {
		turnAcc.SetTurnID(respID)
	}

	var (
		currentMsgStarted    bool
		currentMsgIndex      int
		reasoningStarted     bool
		reasoningOutputIndex int
		lastFinishReason     string
		terminalReceived     bool
		readErr              error
	)

	emitFlushedMsg := func(flushed *TurnPartRecord) {
		if flushed == nil {
			return
		}
		sendSSE("response.output_text.done", map[string]any{
			"type":          "response.output_text.done",
			"item_id":       flushed.OutputItemID,
			"output_index":  currentMsgIndex,
			"content_index": 0,
			"text":          flushed.Text,
		})
		sendSSE("response.content_part.done", map[string]any{
			"type":          "response.content_part.done",
			"item_id":       flushed.OutputItemID,
			"output_index":  currentMsgIndex,
			"content_index": 0,
			"part": map[string]any{
				"type": "output_text",
				"text": flushed.Text,
			},
		})
		msgItem := map[string]any{
			"id":     flushed.OutputItemID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{
				{
					"type": "output_text",
					"text": flushed.Text,
				},
			},
		}
		if flushed.ThoughtSignature != "" {
			msgItem["thought_signature"] = flushed.ThoughtSignature
		}
		sendSSE("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": currentMsgIndex,
			"item":         msgItem,
		})
		currentMsgStarted = false
	}

	flushCurrentMsg := func() {
		if !currentMsgStarted {
			return
		}
		flushed := turnAcc.FlushPendingText()
		emitFlushedMsg(flushed)
	}

	for {
		event, err := decoder.Next()
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
		switch event.Type {
		case StreamEventError:
			readErr = event.Error
		case StreamEventUsage:
			if event.Usage != nil {
				lastUsage = map[string]any{
					"input_tokens":  event.Usage.PromptTokenCount,
					"output_tokens": event.Usage.CandidatesTokenCount,
					"total_tokens":  event.Usage.TotalTokenCount,
				}
			}
		case StreamEventPart:
			part := event.Part
			if part.Thought {
				flushCurrentMsg()
				if !reasoningStarted {
					reasoningStarted = true
					reasoningOutputIndex = outputIndex
					outputIndex++
					sendSSE("response.output_item.added", map[string]any{
						"type":         "response.output_item.added",
						"output_index": reasoningOutputIndex,
						"item": map[string]any{
							"id":      turnAcc.ReasoningID(),
							"type":    "reasoning",
							"status":  "in_progress",
							"summary": []any{},
						},
					})
				}
				turnAcc.ProcessPart(part)
				if part.Text != "" {
					sendSSE("response.reasoning_summary_text.delta", map[string]any{
						"type":         "response.reasoning_summary_text.delta",
						"item_id":      turnAcc.ReasoningID(),
						"output_index": reasoningOutputIndex,
						"delta":        part.Text,
					})
				}
			} else if part.FunctionCall != nil {
				flushCurrentMsg()
				_, added := turnAcc.ProcessPart(part)
				if added != nil {
					fcOutputIndex := outputIndex
					outputIndex++
					wireNamespace, wireName := splitWireFunctionName(added.ToolName)
					argsBytes, _ := json.Marshal(added.Args)

					addedItem := map[string]any{
						"id":        added.OutputItemID,
						"type":      "function_call",
						"status":    "in_progress",
						"call_id":   added.CallID,
						"name":      wireName,
						"arguments": "",
					}
					if wireNamespace != "" {
						addedItem["namespace"] = wireNamespace
					}
					if added.ThoughtSignature != "" {
						addedItem["thought_signature"] = added.ThoughtSignature
					}
					sendSSE("response.output_item.added", map[string]any{
						"type":         "response.output_item.added",
						"output_index": fcOutputIndex,
						"item":         addedItem,
					})

					argsDone := map[string]any{
						"type":         "response.function_call_arguments.done",
						"item_id":      added.OutputItemID,
						"output_index": fcOutputIndex,
						"call_id":      added.CallID,
						"name":         wireName,
						"arguments":    string(argsBytes),
					}
					if wireNamespace != "" {
						argsDone["namespace"] = wireNamespace
					}
					sendSSE("response.function_call_arguments.done", argsDone)

					fcItem := map[string]any{
						"id":        added.OutputItemID,
						"type":      "function_call",
						"status":    "completed",
						"call_id":   added.CallID,
						"name":      wireName,
						"arguments": string(argsBytes),
					}
					if wireNamespace != "" {
						fcItem["namespace"] = wireNamespace
					}
					if added.ThoughtSignature != "" {
						fcItem["thought_signature"] = added.ThoughtSignature
					}
					sendSSE("response.output_item.done", map[string]any{
						"type":         "response.output_item.done",
						"output_index": fcOutputIndex,
						"item":         fcItem,
					})
				}
			} else if part.Text != "" || part.ThoughtSignature != "" {
				if part.Text == "" && part.ThoughtSignature != "" {
					turnAcc.ProcessPart(part)
					continue
				}

				if currentMsgStarted && turnAcc.CurrentTextSig() != part.ThoughtSignature {
					flushCurrentMsg()
				}
				if !currentMsgStarted && part.Text != "" {
					currentMsgStarted = true
					currentMsgIndex = outputIndex
					outputIndex++

					sendSSE("response.output_item.added", map[string]any{
						"type":         "response.output_item.added",
						"output_index": currentMsgIndex,
						"item": map[string]any{
							"id":      turnAcc.CurrentTextID(),
							"type":    "message",
							"status":  "in_progress",
							"role":    "assistant",
							"content": []any{},
						},
					})
					sendSSE("response.content_part.added", map[string]any{
						"type":          "response.content_part.added",
						"item_id":       turnAcc.CurrentTextID(),
						"output_index":  currentMsgIndex,
						"content_index": 0,
						"part": map[string]any{
							"type":        "output_text",
							"text":        "",
							"annotations": []any{},
						},
					})
				}
				msgID := turnAcc.CurrentTextID()
				turnAcc.ProcessPart(part)
				if part.Text != "" {
					sendSSE("response.output_text.delta", map[string]any{
						"type":          "response.output_text.delta",
						"item_id":       msgID,
						"output_index":  currentMsgIndex,
						"content_index": 0,
						"delta":         part.Text,
					})
				}
			}
		case StreamEventTerminal:
			terminalReceived = true
			lastFinishReason = event.FinishReason
		}
		if readErr != nil {
			break
		}
	}

	if readErr == nil && !terminalReceived {
		readErr = fmt.Errorf("upstream stream ended prematurely without finish reason")
	}

	if readErr != nil {
		sendSSE("response.failed", map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"id":     respID,
				"status": "failed",
				"error": map[string]any{
					"message": fmt.Sprintf("upstream stream interrupted: %v", readErr),
				},
			},
		})
		return
	}

	flushCurrentMsg()

	turn := turnAcc.Finish()
	turn.PopulateCache(sigCache, pHash)

	if turn.NeedsReasoningItem() {
		encContent := EncodeReasoningEncryptedContent(ReasoningEncryptedState{
			Version:        turn.Version,
			Parts:          turn.Parts,
			ToolSignatures: turn.ToolSignatures,
			TurnSiblings:   turn.TurnSiblings,
			TextSignatures: turn.TextSignatures,
			OutputOrder:    turn.OutputOrder,
		})
		doneItem := map[string]any{
			"id":                turn.ReasoningID,
			"type":              "reasoning",
			"status":            "completed",
			"encrypted_content": encContent,
			"summary": []map[string]any{
				{"type": "summary_text", "text": turn.ThoughtSummary},
			},
		}

		if !reasoningStarted {
			reasoningStarted = true
			reasoningOutputIndex = outputIndex
			outputIndex++
			sendSSE("response.output_item.added", map[string]any{
				"type":         "response.output_item.added",
				"output_index": reasoningOutputIndex,
				"item": map[string]any{
					"id":      turn.ReasoningID,
					"type":    "reasoning",
					"status":  "in_progress",
					"summary": []any{},
				},
			})
			sendSSE("response.output_item.done", map[string]any{
				"type":         "response.output_item.done",
				"output_index": reasoningOutputIndex,
				"item":         doneItem,
			})
		} else {
			sendSSE("response.output_item.done", map[string]any{
				"type":         "response.output_item.done",
				"output_index": reasoningOutputIndex,
				"item":         doneItem,
			})
		}
	}

	if outputIndex == 0 {
		msgOutputIndex := outputIndex
		outputIndex++
		emptyItem := map[string]any{
			"id":     turn.Parts[0].OutputItemID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{
				{
					"type": "output_text",
					"text": "",
				},
			},
		}
		sendSSE("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": msgOutputIndex,
			"item":         emptyItem,
		})
		sendSSE("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": msgOutputIndex,
			"item":         emptyItem,
		})
	}

	allOutputs := turn.ToOutputItems()

	respStatus := "completed"
	var incompleteDetails map[string]any
	if lastFinishReason == "MAX_TOKENS" {
		respStatus = "incomplete"
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	}

	respPayload := map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": created,
		"model":      model,
		"status":     respStatus,
		"output":     allOutputs,
		"usage":      lastUsage,
	}
	if incompleteDetails != nil {
		respPayload["incomplete_details"] = incompleteDetails
	}

	eventName := "response.completed"
	if respStatus == "incomplete" {
		eventName = "response.incomplete"
	}
	sendSSE(eventName, map[string]any{
		"type":     eventName,
		"response": respPayload,
	})
}

func handleNonStreamingResponses(w http.ResponseWriter, stream io.Reader, respID, itemID string, created int64, model string, sigCache *upstream.SignatureCache, prefixHash ...string) {
	var pHash string
	if len(prefixHash) > 0 {
		pHash = prefixHash[0]
	}
	decoder := NewUpstreamDecoder(stream)
	acc, err := AccumulateTurn(decoder)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream stream interrupted: %v", err))
		return
	}

	var allOutputs []map[string]any
	turn := BuildAuthoritativeTurn(acc.Parts, itemID, model)
	turn.TurnID = respID
	turn.PopulateCache(sigCache, pHash)
	allOutputs = turn.ToOutputItems()

	respStatus := "completed"
	var incompleteDetails map[string]any
	if acc.FinishReason == "MAX_TOKENS" {
		respStatus = "incomplete"
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	}

	responseObj := map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": created,
		"model":      model,
		"status":     respStatus,
		"output":     allOutputs,
	}
	if incompleteDetails != nil {
		responseObj["incomplete_details"] = incompleteDetails
	}
	if acc.Usage != nil {
		responseObj["usage"] = map[string]any{
			"input_tokens":  acc.Usage.PromptTokenCount,
			"output_tokens": acc.Usage.CandidatesTokenCount,
			"total_tokens":  acc.Usage.TotalTokenCount,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(responseObj)
}

func splitWireFunctionName(name string) (string, string) {
	if strings.Contains(name, "__") {
		parts := strings.SplitN(name, "__", 2)
		return parts[0], parts[1]
	}
	return "", name
}
