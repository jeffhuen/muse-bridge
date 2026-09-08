// Package rewrite applies the bridge's /v1/responses payload munging.
// It is pure (input bytes in, output bytes out) so it is trivially tested.
package rewrite

import (
	"bytes"
	"encoding/json"
	"log"
)

// Decode unmarshals JSON while preserving number literals exactly.
// Plain json.Unmarshal decodes every number as float64, corrupting
// integers above 2^53 (seeds, IDs); UseNumber keeps them verbatim.
func Decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// Responses defaults prompt_cache_retention to 24h and drops reasoning
// blocks Meta rejects (missing, null, or "none" effort). Invalid JSON
// passes through untouched, matching bridge.py. When debug is true the
// model and original reasoning are logged.
func Responses(body []byte, debug bool) []byte {
	var payload map[string]any
	if err := Decode(body, &payload); err != nil {
		return body
	}
	if _, ok := payload["prompt_cache_retention"]; !ok {
		payload["prompt_cache_retention"] = "24h"
	}
	if debug {
		log.Printf("req model=%v reasoning=%v", payload["model"], payload["reasoning"])
	}
	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		effort, exists := reasoning["effort"]
		if !exists || effort == nil || effort == "none" {
			delete(payload, "reasoning")
		}
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}
