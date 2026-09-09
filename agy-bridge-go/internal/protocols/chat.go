package protocols

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

type chatContextKey struct{}

// ToolDefinition represents a function tool declaration in OpenAI Chat or Responses format.
type ToolDefinition struct {
	Type        string           `json:"type"`
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Parameters  map[string]any   `json:"parameters,omitempty"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	Function    struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	} `json:"function"`
}

func (t *ToolDefinition) FunctionName() string {
	if t.Function.Name != "" {
		return t.Function.Name
	}
	return t.Name
}

func (t *ToolDefinition) FunctionDescription() string {
	if t.Function.Description != "" {
		return t.Function.Description
	}
	return t.Description
}

func (t *ToolDefinition) FunctionParameters() map[string]any {
	if t.Function.Parameters != nil {
		return t.Function.Parameters
	}
	return t.Parameters
}

// ToolCall represents a tool invocation requested by the assistant.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	ThoughtSignature string `json:"thought_signature,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Origin           string `json:"origin,omitempty"`
}

// ChatRequest represents the standard OpenAI /v1/chat/completions payload.
type ChatRequest struct {
	Model           string           `json:"model"`
	Messages        []ChatMessage    `json:"messages"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	Stream          bool             `json:"stream"`
	Temperature     *float64         `json:"temperature,omitempty"`
	MaxTokens       *int             `json:"max_tokens,omitempty"`
	Reasoning       any              `json:"reasoning,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	Thinking        any              `json:"thinking,omitempty"`
}

// ChatMessage represents one turn in OpenAI chat format.
type ChatMessage struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall       `json:"tool_calls,omitempty"`
	ThoughtSignature string           `json:"thought_signature,omitempty"`
	ID               string           `json:"id,omitempty"`
	ReasoningDetails []map[string]any `json:"reasoning_details,omitempty"`
	Provider         string           `json:"provider,omitempty"`
	Model            string           `json:"model,omitempty"`
	Origin           string           `json:"origin,omitempty"`
}

// writeJSONError formats an error response as valid JSON conforming to OpenAI error schema.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": strings.TrimSpace(msg),
			"type":    "invalid_request_error",
		},
	})
}

// HandleChatCompletions handles POST /v1/chat/completions by proxying directly to PredictionService.
func HandleChatCompletions(w http.ResponseWriter, r *http.Request, client *upstream.Client) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %s", err.Error()))
		return
	}

	predReq, err := ConvertChatToPrediction(&req, client.SigCache())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("translate request: %s", err.Error()))
		return
	}

	stream, err := client.StreamGenerateContent(r.Context(), predReq)
	if err != nil {
		log.Printf("[chat] upstream error: %v", err)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream call failed: %s", err.Error()))
		return
	}
	defer stream.Close()

	cmplID := RandomID("chatcmpl")
	created := time.Now().Unix()
	prefixHash := ComputeChatContextHash(req.Messages, req.Model, req.Tools)

	if !req.Stream {
		handleNonStreamingChat(w, stream, cmplID, created, predReq.Model, client.SigCache(), prefixHash)
		return
	}

	r = r.WithContext(context.WithValue(r.Context(), chatContextKey{}, prefixHash))
	handleStreamingChat(w, r, stream, cmplID, created, predReq.Model, client.SigCache())
}

func handleStreamingChat(w http.ResponseWriter, r *http.Request, stream io.Reader, cmplID string, created int64, model string, sigCache *upstream.SignatureCache) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sendChunk := func(delta map[string]any, finishReason any, usage any) {
		choice := map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}
		chunk := map[string]any{
			"id":      cmplID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{choice},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		bytes, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", string(bytes))
		flusher.Flush()
	}

	decoder := NewUpstreamDecoder(stream)
	hasToolCalls := false
	var lastUsage map[string]any
	toolCallIndexes := make(map[string]int)
	nextToolIndex := 0
	var readErr error
	terminalReceived := false
	lastTextThoughtSig := ""
	var fullText strings.Builder
	var prefixHash string
	if ph, ok := r.Context().Value(chatContextKey{}).(string); ok {
		prefixHash = ph
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
					"prompt_tokens":     event.Usage.PromptTokenCount,
					"completion_tokens": event.Usage.CandidatesTokenCount,
					"total_tokens":      event.Usage.TotalTokenCount,
				}
			}
		case StreamEventPart:
			part := event.Part
			if part.FunctionCall != nil {
				hasToolCalls = true
				fc := part.FunctionCall
				if fc.ID == "" {
					fc.ID = RandomID("call")
				}
				sig := part.ThoughtSignature
				if sigCache != nil {
					sigCache.PutToolInfo(fc.ID, fc.Name, sig)
				}
				idx, exists := toolCallIndexes[fc.ID]
				if !exists {
					idx = nextToolIndex
					toolCallIndexes[fc.ID] = idx
					nextToolIndex++
				}
				argsBytes, _ := json.Marshal(fc.Args)
				tcChunk := map[string]any{
					"index": idx,
					"id":    fc.ID,
					"type":  "function",
					"function": map[string]any{
						"name":      fc.Name,
						"arguments": string(argsBytes),
					},
				}
				if sig != "" {
					tcChunk["thought_signature"] = sig
				}
				sendChunk(map[string]any{
					"tool_calls": []map[string]any{tcChunk},
				}, nil, nil)
			} else {
				if part.ThoughtSignature != "" {
					lastTextThoughtSig = part.ThoughtSignature
				}
				if part.Thought && part.Text != "" {
					sendChunk(map[string]any{
						"reasoning_content": part.Text,
					}, nil, nil)
				} else if part.Text != "" {
					fullText.WriteString(part.Text)
					delta := map[string]any{
						"content": part.Text,
					}
					if part.ThoughtSignature != "" {
						delta["thought_signature"] = part.ThoughtSignature
					}
					sendChunk(delta, nil, nil)
				}
			}
		case StreamEventTerminal:
			terminalReceived = true
			finishReason := "stop"
			if hasToolCalls {
				finishReason = "tool_calls"
			}
			delta := map[string]any{}
			if lastTextThoughtSig != "" {
				delta["thought_signature"] = lastTextThoughtSig
				delta["reasoning_details"] = []map[string]any{
					{
						"type": "reasoning.encrypted",
						"id":   cmplID,
						"data": lastTextThoughtSig,
					},
				}
			}
			sendChunk(delta, finishReason, lastUsage)
		}
		if readErr != nil {
			break
		}
	}

	if readErr == nil && !terminalReceived {
		readErr = fmt.Errorf("upstream stream ended prematurely without finish reason")
	}

	if readErr != nil {
		errChunk, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("upstream stream interrupted: %v", readErr),
				"type":    "upstream_error",
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", string(errChunk))
		flusher.Flush()
		return
	}

	if fullText.Len() > 0 && lastTextThoughtSig != "" && sigCache != nil {
		if prefixHash != "" {
			sigCache.PutContextSignature(ChatContextKey(prefixHash, fullText.String()), lastTextThoughtSig)
		}
	}
	if sigCache != nil && cmplID != "" && lastTextThoughtSig != "" {
		sigCache.PutMessageSignature(cmplID, lastTextThoughtSig)
	}

	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func handleNonStreamingChat(w http.ResponseWriter, stream io.Reader, cmplID string, created int64, model string, sigCache *upstream.SignatureCache, prefixHash ...string) {
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

	var toolCalls []map[string]any
	for _, part := range acc.Parts {
		if part.FunctionCall != nil {
			fc := part.FunctionCall
			if fc.ID == "" {
				fc.ID = RandomID("call")
			}
			sig := part.ThoughtSignature
			if sigCache != nil {
				sigCache.PutToolInfo(fc.ID, fc.Name, sig)
			}
			argsBytes, _ := json.Marshal(fc.Args)
			tcItem := map[string]any{
				"id":   fc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      fc.Name,
					"arguments": string(argsBytes),
				},
			}
			if sig != "" {
				tcItem["thought_signature"] = sig
			}
			toolCalls = append(toolCalls, tcItem)
		}
	}

	if acc.VisibleText != "" && acc.LastTextSig != "" && sigCache != nil {
		if pHash != "" {
			sigCache.PutContextSignature(ChatContextKey(pHash, acc.VisibleText), acc.LastTextSig)
		}
	}
	if sigCache != nil && cmplID != "" && acc.LastTextSig != "" {
		sigCache.PutMessageSignature(cmplID, acc.LastTextSig)
	}

	msg := map[string]any{
		"id":      cmplID,
		"role":    "assistant",
		"content": acc.VisibleText,
	}
	if acc.LastTextSig != "" {
		msg["thought_signature"] = acc.LastTextSig
		msg["reasoning_details"] = []map[string]any{
			{
				"type": "reasoning.encrypted",
				"id":   cmplID,
				"data": acc.LastTextSig,
			},
		}
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		finishReason = "tool_calls"
	}

	res := map[string]any{
		"id":      cmplID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finishReason,
			},
		},
	}
	if acc.Usage != nil {
		res["usage"] = map[string]any{
			"prompt_tokens":     acc.Usage.PromptTokenCount,
			"completion_tokens": acc.Usage.CandidatesTokenCount,
			"total_tokens":      acc.Usage.TotalTokenCount,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// Ensure unused import bytes compiles cleanly if needed.
var _ = bytes.Buffer{}
