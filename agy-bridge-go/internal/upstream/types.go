package upstream

import "encoding/json"

// PredictionRequest is the top-level payload sent to Google's internal PredictionService.
type PredictionRequest struct {
	Project     string                 `json:"project"`
	RequestID   string                 `json:"requestId"`
	Request     GenerateContentRequest `json:"request"`
	Model       string                 `json:"model"`
	UserAgent   string                 `json:"userAgent"`
	RequestType string                 `json:"requestType,omitempty"`
}

// GenerateContentRequest contains the core generative parameters.
type GenerateContentRequest struct {
	Contents          []Content         `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	SessionID         string            `json:"sessionId,omitempty"`
}

// Content represents a single message in the conversation.
type Content struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

// Part is a single element within a Content message.
type Part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

// FunctionCall represents a model-generated tool call.
type FunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
	ID   string         `json:"id,omitempty"`
}

// FunctionResponse represents a client tool execution result.
type FunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	ID       string         `json:"id,omitempty"`
}

// Tool defines functions available to the model.
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
}

// FunctionDeclaration defines a callable function schema.
type FunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// GenerationConfig controls output constraints and reasoning effort.
type GenerationConfig struct {
	MaxOutputTokens int             `json:"maxOutputTokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ThinkingConfig  *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

// ThinkingConfig sets the reasoning parameters.
type ThinkingConfig struct {
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

// StreamError represents an error object returned in upstream SSE data.
type StreamError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// SSEStreamEvent represents an SSE event line from PredictionService.
type SSEStreamEvent struct {
	Response *PredictionResponse `json:"response,omitempty"`
	Error    *StreamError        `json:"error,omitempty"`
	TraceID  string              `json:"traceId,omitempty"`
}

// PredictionResponse represents the response payload inside an SSE event.
type PredictionResponse struct {
	Candidates    []Candidate    `json:"candidates,omitempty"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
}

// Candidate represents a generated model completion.
type Candidate struct {
	Content      Content `json:"content"`
	FinishReason string  `json:"finishReason,omitempty"`
	Index        int     `json:"index,omitempty"`
}

// UsageMetadata captures token counts including reasoning tokens.
type UsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
}

// RawJSONHelper decodes arbitrary JSON values.
func ToMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
