package protocols

import (
	"fmt"
	"log"
	"strings"
)

// ToolCallOrigin classifies the provenance of a tool call in conversation history.
type ToolCallOrigin string

const (
	// OriginNative: The call matches an authoritative native bridge turn record with verified content and signature.
	OriginNative ToolCallOrigin = "native"

	// OriginMismatched: The call matches a known native record by ID, but has altered name/args or missing required native state.
	OriginMismatched ToolCallOrigin = "mismatched"

	// OriginForeign: The call is affirmatively imported from another model or provider.
	OriginForeign ToolCallOrigin = "foreign"

	// OriginUnknown: The call lacks Gemini carrier state and has no affirmative foreign marker.
	OriginUnknown ToolCallOrigin = "unknown"
)

// ProvenanceEvidence holds the structural evidence analyzed for a tool call.
type ProvenanceEvidence struct {
	CallID               string
	ItemID               string
	Name                 string
	Args                 map[string]any
	HasCarrier           bool
	CarrierSig           string
	CarrierMismatch      bool
	HasCache             bool
	CacheSig             string
	CacheMismatch        bool
	HasSiblingLead       bool
	SiblingLeadSig       string
	IsRecordedSib        bool
	HasMatchingOutput    bool
	IsAffirmativeForeign bool
	IsCurrentTurn        bool
	NativeTurnUnverified bool
}

// ClassifyToolCall determines the ToolCallOrigin from structural evidence.
func ClassifyToolCall(ev ProvenanceEvidence) ToolCallOrigin {
	// 1. If carrier or cache exists for this ID, but content was modified:
	if ev.CarrierMismatch || ev.CacheMismatch {
		return OriginMismatched
	}

	// Contradictory evidence: carrier signature conflicts with authoritative cache signature
	if ev.HasCarrier && ev.HasCache && ev.CarrierSig != "" && ev.CacheSig != "" && ev.CarrierSig != ev.CacheSig {
		return OriginMismatched
	}

	// 2. If carrier exists with matching content:
	if ev.HasCarrier {
		return OriginNative
	}

	// 3. If cache exists with matching content (regardless of whether CacheSig is populated):
	if ev.HasCache {
		return OriginNative
	}

	// 4. If affirmatively marked foreign (and not matching native ownership):
	if ev.IsAffirmativeForeign {
		return OriginForeign
	}

	// 5. If an unverified call was placed in a known native turn:
	if ev.NativeTurnUnverified {
		return OriginMismatched
	}

	// 6. Otherwise, origin is unknown:
	return OriginUnknown
}

// ResolutionResult holds the resolved thought signature or status under the bridge policy.
type ResolutionResult struct {
	Origin           ToolCallOrigin
	ThoughtSignature string
	Recovered        bool
}

// RecordUnknownOriginRecovery logs when migration recovery is applied to an unknown-origin tool call,
// without logging cryptographic signatures or tool argument contents.
func RecordUnknownOriginRecovery(callID, name string, currentTurn bool) {
	log.Printf("[provenance] applied migration recovery for unknown-origin tool call (id: %s, function: %s, current_turn: %v)", callID, name, currentTurn)
}

// ResolveToolSignature applies the bridge policy to determine the thought signature or return an actionable error.
//
// Policy Contract:
// - Matching native record with sufficient state: Replay original parts and signatures.
// - Known native record with mismatched content or required state missing: Reject clearly; never bypass the integrity failure.
// - Affirmatively imported call with its corresponding result: Apply migration handling, including within the current turn.
// - Unknown-origin call with its corresponding result: Allow explicitly documented migration recovery; retain its classification as unknown.
// - Incomplete or invalid call/result pairing: Return an actionable validation error.
func ResolveToolSignature(ev ProvenanceEvidence) (ResolutionResult, error) {
	origin := ClassifyToolCall(ev)

	switch origin {
	case OriginMismatched:
		if ev.NativeTurnUnverified {
			return ResolutionResult{Origin: OriginMismatched},
				fmt.Errorf("tool call %q (id: %s) is missing required cryptographic thought signature: unverified call in native turn", ev.Name, ev.CallID)
		}
		return ResolutionResult{Origin: OriginMismatched},
			fmt.Errorf("tool call %q (id: %s) is missing required cryptographic thought signature: content mismatch against native turn record", ev.Name, ev.CallID)

	case OriginNative:
		if !ev.HasMatchingOutput {
			return ResolutionResult{Origin: OriginNative},
				fmt.Errorf("tool call %q (id: %s) is missing required cryptographic thought signature: native call has no corresponding tool result", ev.Name, ev.CallID)
		}
		var sig string
		if ev.HasCache && ev.CacheSig != "" {
			sig = ev.CacheSig
		} else if ev.CarrierSig != "" {
			sig = ev.CarrierSig
		}
		if sig == "" && ev.IsRecordedSib && ev.SiblingLeadSig != "" {
			sig = ev.SiblingLeadSig
		}
		if sig != "" {
			return ResolutionResult{
				Origin:           OriginNative,
				ThoughtSignature: sig,
			}, nil
		}
		// Known native record with required state missing
		return ResolutionResult{Origin: OriginNative},
			fmt.Errorf("tool call %q (id: %s) is missing required cryptographic thought signature: native signature state missing", ev.Name, ev.CallID)

	case OriginForeign:
		if ev.HasMatchingOutput {
			return ResolutionResult{
				Origin:           OriginForeign,
				ThoughtSignature: "skip_thought_signature_validator",
			}, nil
		}
		return ResolutionResult{Origin: OriginForeign},
			fmt.Errorf("tool call %q (id: %s) is missing required cryptographic thought signature: imported foreign call has no corresponding tool result", ev.Name, ev.CallID)

	case OriginUnknown:
		if ev.HasMatchingOutput {
			RecordUnknownOriginRecovery(ev.CallID, ev.Name, ev.IsCurrentTurn)
			return ResolutionResult{
				Origin:           OriginUnknown,
				ThoughtSignature: "skip_thought_signature_validator",
				Recovered:        true,
			}, nil
		}
		return ResolutionResult{Origin: OriginUnknown},
			fmt.Errorf("tool call %q (id: %s) is missing required cryptographic thought signature and cannot be resolved from cache: unknown-origin call has no corresponding tool result", ev.Name, ev.CallID)

	default:
		return ResolutionResult{Origin: origin},
			fmt.Errorf("tool call %q (id: %s) is missing required cryptographic thought signature", ev.Name, ev.CallID)
	}
}

// IsItemAffirmativelyForeign checks if an OpenAI Responses item carries metadata indicating foreign origin.
func IsItemAffirmativelyForeign(item map[string]any) bool {
	if prov, ok := item["provider"].(string); ok && prov != "" {
		l := strings.ToLower(prov)
		if !strings.Contains(l, "gemini") && !strings.Contains(l, "antigravity") {
			return true
		}
	}
	if mod, ok := item["model"].(string); ok && mod != "" {
		l := strings.ToLower(mod)
		if !strings.HasPrefix(l, "gemini") && !strings.Contains(l, "antigravity") {
			return true
		}
	}
	if orig, ok := item["origin"].(string); ok && strings.ToLower(orig) == "foreign" {
		return true
	}
	if foreign, ok := item["foreign"].(bool); ok && foreign {
		return true
	}
	if imported, ok := item["imported"].(bool); ok && imported {
		return true
	}
	return false
}

// IsChatMessageAffirmativelyForeign checks if a ChatMessage carries metadata indicating foreign origin.
func IsChatMessageAffirmativelyForeign(m *ChatMessage) bool {
	if m == nil {
		return false
	}
	if m.Provider != "" {
		l := strings.ToLower(m.Provider)
		if !strings.Contains(l, "gemini") && !strings.Contains(l, "antigravity") {
			return true
		}
	}
	if m.Model != "" {
		l := strings.ToLower(m.Model)
		if !strings.HasPrefix(l, "gemini") && !strings.Contains(l, "antigravity") {
			return true
		}
	}
	if strings.ToLower(m.Origin) == "foreign" {
		return true
	}
	return false
}

// IsChatToolCallAffirmativelyForeign checks if a ToolCall carries metadata indicating foreign origin.
func IsChatToolCallAffirmativelyForeign(tc *ToolCall) bool {
	if tc == nil {
		return false
	}
	if tc.Provider != "" {
		l := strings.ToLower(tc.Provider)
		if !strings.Contains(l, "gemini") && !strings.Contains(l, "antigravity") {
			return true
		}
	}
	if tc.Model != "" {
		l := strings.ToLower(tc.Model)
		if !strings.HasPrefix(l, "gemini") && !strings.Contains(l, "antigravity") {
			return true
		}
	}
	if strings.ToLower(tc.Origin) == "foreign" {
		return true
	}
	return false
}
