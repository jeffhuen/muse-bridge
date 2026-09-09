package models

import (
	"testing"
)

func TestNormalizeEffort(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"none", ""},
		{"off", ""},
		{"0", ""},
		{"false", ""},
		{"null", ""},
		{"low", "low"},
		{"minimal", "low"},
		{"min", "low"},
		{"1", "low"},
		{"medium", "medium"},
		{"med", "medium"},
		{"default", "medium"},
		{"2", "medium"},
		{"high", "high"},
		{"xhigh", "high"},
		{"extra-high", "high"},
		{"max", "high"},
		{"3", "high"},
	}

	for _, tt := range tests {
		got := NormalizeEffort(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeEffort(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractEffort(t *testing.T) {
	// From reasoning_effort string
	if got := ExtractEffort(nil, "low", nil); got != "low" {
		t.Fatalf("want low, got %s", got)
	}

	// From reasoning map with "effort"
	m1 := map[string]any{"effort": "max"}
	if got := ExtractEffort(m1, "", nil); got != "high" {
		t.Fatalf("want high, got %s", got)
	}

	// From reasoning map with "level"
	m2 := map[string]any{"level": "minimal"}
	if got := ExtractEffort(m2, "", nil); got != "low" {
		t.Fatalf("want low, got %s", got)
	}

	// From thinking map
	m3 := map[string]any{"type": "enabled"}
	if got := ExtractEffort(nil, "", m3); got != "high" {
		t.Fatalf("want high, got %s", got)
	}

	// From reasoning string
	if got := ExtractEffort("xhigh", "", nil); got != "high" {
		t.Fatalf("want high, got %s", got)
	}
}

func TestResolveModelWithReasoning(t *testing.T) {
	tests := []struct {
		model  string
		effort string
		want   string
	}{
		// Gemini 3.8 Flash remappings
		{"gemini-3.8-flash-high", "low", "gemini-3.8-flash-low"},
		{"gemini-3.8-flash-high", "minimal", "gemini-3.8-flash-low"},
		{"gemini-3.8-flash-high", "medium", "gemini-3.8-flash-medium"},
		{"gemini-3.8-flash-high", "high", "gemini-3.8-flash-high"},
		{"gemini-3.8-flash-high", "max", "gemini-3.8-flash-high"},
		{"gemini-3.8-flash-high", "", "gemini-3.8-flash-high"},
		{"gemini-3.8-flash-high", "none", "gemini-3.8-flash-high"},

		// Base flash aliases
		{"flash", "low", "gemini-3.8-flash-low"},
		{"gemini-3.8-flash", "medium", "gemini-3.8-flash-medium"},
		{"gemini-3.8-flash", "", "gemini-3.8-flash-high"},

		// Gemini 3.1 Pro remappings
		{"gemini-3.1-pro-high", "low", "gemini-3.1-pro-low"},
		{"gemini-3.1-pro-low", "high", "gemini-3.1-pro-high"},
		{"gemini-3.1-pro-high", "medium", "gemini-3.1-pro-high"}, // Pro maps medium to high
		{"pro", "low", "gemini-3.1-pro-low"},
		{"pro", "max", "gemini-3.1-pro-high"},

		// Claude and external models
		{"claude-sonnet-4-6", "high", "claude-sonnet-4-6"},
		{"sonnet", "low", "claude-sonnet-4-6"},
		{"gpt-oss", "low", "gpt-oss-120b-medium"},
	}

	for _, tt := range tests {
		got := ResolveModelWithReasoning(tt.model, tt.effort)
		if got != tt.want {
			t.Errorf("ResolveModelWithReasoning(%q, %q) = %q, want %q", tt.model, tt.effort, got, tt.want)
		}
	}
}
