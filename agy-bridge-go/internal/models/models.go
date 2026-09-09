// Package models catalogs Antigravity models, aliases, and OpenAI model descriptors.
package models

import (
	"fmt"
	"strings"
	"time"
)

// ModelInfo describes a model's limits and metadata.
type ModelInfo struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	ContextWindow int    `json:"context_window"`
	MaxTokens     int    `json:"max_tokens"`
}

// CanonicalModels contains supported Antigravity models.
var CanonicalModels = []ModelInfo{
	{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.8-flash-medium", DisplayName: "Gemini 3.8 Flash (Medium)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.8-flash-low", DisplayName: "Gemini 3.8 Flash (Low)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.7-flash-high", DisplayName: "Gemini 3.7 Flash (High)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.7-flash-medium", DisplayName: "Gemini 3.7 Flash (Medium)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.7-flash-low", DisplayName: "Gemini 3.7 Flash (Low)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.6-flash-high", DisplayName: "Gemini 3.6 Flash (High)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.6-flash-medium", DisplayName: "Gemini 3.6 Flash (Medium)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.6-flash-low", DisplayName: "Gemini 3.6 Flash (Low)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.1-pro-high", DisplayName: "Gemini 3.1 Pro (High)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "gemini-3.1-pro-low", DisplayName: "Gemini 3.1 Pro (Low)", ContextWindow: 1048576, MaxTokens: 256000},
	{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6 (Thinking)", ContextWindow: 200000, MaxTokens: 64000},
	{ID: "claude-opus-4-6-thinking", DisplayName: "Claude Opus 4.6 (Thinking)", ContextWindow: 200000, MaxTokens: 64000},
	{ID: "gpt-oss-120b-medium", DisplayName: "GPT-OSS 120B (Medium)", ContextWindow: 131072, MaxTokens: 32768},
}

// Aliases maps common shorthand names and unadorned model IDs to default canonical IDs.
var Aliases = map[string]string{
	"flash":            "gemini-3.8-flash-high",
	"flash-high":       "gemini-3.8-flash-high",
	"flash-medium":     "gemini-3.8-flash-medium",
	"flash-low":        "gemini-3.8-flash-low",
	"gemini-flash":     "gemini-3.8-flash-high",
	"gemini-3.8-flash": "gemini-3.8-flash-high",
	"gemini-3.7-flash": "gemini-3.7-flash-high",
	"gemini-3.6-flash": "gemini-3.6-flash-high",
	"pro":              "gemini-3.1-pro-high",
	"pro-high":         "gemini-3.1-pro-high",
	"pro-low":          "gemini-3.1-pro-low",
	"gemini-pro":       "gemini-3.1-pro-high",
	"gemini-3.1-pro":   "gemini-3.1-pro-high",
	"sonnet":           "claude-sonnet-4-6",
	"claude-sonnet":    "claude-sonnet-4-6",
	"opus":             "claude-opus-4-6-thinking",
	"claude-opus":      "claude-opus-4-6-thinking",
	"gpt-oss":          "gpt-oss-120b-medium",
}

// NormalizeEffort cleans and maps varied client reasoning effort strings to "low", "medium", or "high".
func NormalizeEffort(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || s == "none" || s == "off" || s == "0" || s == "false" || s == "null" {
		return ""
	}
	switch s {
	case "minimal", "min", "low", "1":
		return "low"
	case "medium", "med", "default", "2":
		return "medium"
	case "high", "xhigh", "extra-high", "max", "3":
		return "high"
	}
	if strings.Contains(s, "min") || strings.Contains(s, "low") {
		return "low"
	}
	if strings.Contains(s, "med") {
		return "medium"
	}
	if strings.Contains(s, "max") || strings.Contains(s, "high") {
		return "high"
	}
	return ""
}

// ExtractReasoningEffort pulls effort from any client representation (string, map, number).
func ExtractReasoningEffort(val any) string {
	if val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return NormalizeEffort(v)
	case map[string]any:
		if effort, ok := v["effort"].(string); ok {
			return NormalizeEffort(effort)
		}
		if level, ok := v["level"].(string); ok {
			return NormalizeEffort(level)
		}
		if eff, ok := v["reasoning_effort"].(string); ok {
			return NormalizeEffort(eff)
		}
		if eff, ok := v["reasoningEffort"].(string); ok {
			return NormalizeEffort(eff)
		}
		// If map has type enabled
		if t, ok := v["type"].(string); ok && t == "enabled" {
			return "high"
		}
	case fmt.Stringer:
		return NormalizeEffort(v.String())
	}
	return ""
}

// ExtractEffort combines all potential sources of reasoning flags across different client protocols.
func ExtractEffort(reasoning any, reasoningEffort string, thinking any) string {
	if eff := NormalizeEffort(reasoningEffort); eff != "" {
		return eff
	}
	if eff := ExtractReasoningEffort(reasoning); eff != "" {
		return eff
	}
	if eff := ExtractReasoningEffort(thinking); eff != "" {
		return eff
	}
	return ""
}

// ResolveModel maps any alias or exact name to a canonical model ID.
func ResolveModel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "gemini-3.8-flash-high"
	}
	lower := strings.ToLower(raw)
	if target, ok := Aliases[lower]; ok {
		return target
	}
	for _, m := range CanonicalModels {
		if strings.EqualFold(m.ID, raw) {
			return m.ID
		}
	}
	return raw
}

// ResolveModelWithReasoning re-maps a model based on the client's requested reasoning effort.
// If the client requests low/medium/high, it maps to the exact AGY model variant.
func ResolveModelWithReasoning(rawModel string, effort string) string {
	model := ResolveModel(rawModel)
	normEffort := NormalizeEffort(effort)
	if normEffort == "" {
		return model
	}

	lower := strings.ToLower(model)

	// Gemini 3.8 Flash family
	if strings.HasPrefix(lower, "gemini-3.8-flash") {
		switch normEffort {
		case "low":
			return "gemini-3.8-flash-low"
		case "medium":
			return "gemini-3.8-flash-medium"
		case "high":
			return "gemini-3.8-flash-high"
		}
	}

	// Gemini 3.7 Flash family
	if strings.HasPrefix(lower, "gemini-3.7-flash") {
		switch normEffort {
		case "low":
			return "gemini-3.7-flash-low"
		case "medium":
			return "gemini-3.7-flash-medium"
		case "high":
			return "gemini-3.7-flash-high"
		}
	}

	// Gemini 3.6 Flash family
	if strings.HasPrefix(lower, "gemini-3.6-flash") {
		switch normEffort {
		case "low":
			return "gemini-3.6-flash-low"
		case "medium":
			return "gemini-3.6-flash-medium"
		case "high":
			return "gemini-3.6-flash-high"
		}
	}

	// Gemini 3.1 Pro family (AGY only offers -high and -low)
	if strings.HasPrefix(lower, "gemini-3.1-pro") {
		switch normEffort {
		case "low":
			return "gemini-3.1-pro-low"
		case "medium", "high":
			return "gemini-3.1-pro-high"
		}
	}

	return model
}

// OpenAIModel is the OpenAI /v1/models representation.
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// OpenAIModelList is the envelope returned by GET /v1/models.
type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

// ListOpenAIModels returns the /v1/models listing.
func ListOpenAIModels() OpenAIModelList {
	now := time.Now().Unix()
	data := make([]OpenAIModel, len(CanonicalModels))
	for i, m := range CanonicalModels {
		data[i] = OpenAIModel{
			ID:      m.ID,
			Object:  "model",
			Created: now,
			OwnedBy: "google-antigravity",
		}
	}
	return OpenAIModelList{
		Object: "list",
		Data:   data,
	}
}
