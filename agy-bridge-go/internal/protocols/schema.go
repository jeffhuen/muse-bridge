package protocols

import "strings"

// Gemini's FunctionDeclaration.parameters is an OpenAPI subset that rejects
// unknown fields outright ("Invalid JSON payload received. Unknown name ..."),
// so harness schemas generated from Pydantic, Zod or TypeScript cannot be
// forwarded verbatim. geminiSchemaKeys is the accepted set, verified against
// the live endpoint; anything outside it is dropped rather than forwarded.
var geminiSchemaKeys = map[string]bool{
	"type":                 true,
	"format":               true,
	"title":                true,
	"description":          true,
	"nullable":             true,
	"default":              true,
	"enum":                 true,
	"example":              true,
	"items":                true,
	"minItems":             true,
	"maxItems":             true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"minProperties":        true,
	"maxProperties":        true,
	"minimum":              true,
	"maximum":              true,
	"minLength":            true,
	"maxLength":            true,
	"pattern":              true,
	"anyOf":                true,
	"propertyOrdering":     true,
}

// maxSchemaDepth bounds expansion so a pathological or deeply nested schema
// cannot exhaust the stack. Beyond it, subtrees collapse to a bare object.
const maxSchemaDepth = 64

// SanitizeToolSchema rewrites a client JSON Schema for Gemini's
// FunctionDeclaration.parameters.
//
// It always returns newly allocated maps and slices. Callers hold the client's
// own schema by reference (ToolDefinition.FunctionParameters returns the
// request's map, not a copy), and both context hashes serialize req.Tools, so
// mutating it in place would shift context keys and diverge replay lookups from
// the keys already persisted.
//
// $ref targets are inlined from $defs/definitions, draft metadata is dropped,
// const becomes a single-value enum, oneOf becomes anyOf, allOf members are
// merged, and reference cycles collapse to a bare object because Gemini's
// schema language cannot express recursion.
func SanitizeToolSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	defs := map[string]any{}
	for _, key := range []string{"$defs", "definitions"} {
		if raw, ok := schema[key].(map[string]any); ok {
			for name, def := range raw {
				if _, exists := defs[name]; !exists {
					defs[name] = def
				}
			}
		}
	}
	return sanitizeSchemaNode(schema, defs, nil, 0)
}

// bareObject is the fallback for a subtree Gemini cannot represent.
func bareObject(description string) map[string]any {
	out := map[string]any{"type": "object"}
	if description != "" {
		out["description"] = description
	}
	return out
}

func sanitizeSchemaNode(node map[string]any, defs map[string]any, refStack []string, depth int) map[string]any {
	if node == nil {
		return nil
	}
	description, _ := node["description"].(string)
	if depth > maxSchemaDepth {
		return bareObject(description)
	}

	// Resolve $ref first so sibling keys can overlay the inlined target, which
	// is how JSON Schema 2019-09 and later treat a $ref with adjacent keywords.
	out := map[string]any{}
	if ref, ok := node["$ref"].(string); ok && ref != "" {
		target, name, found := resolveSchemaRef(ref, defs)
		if !found {
			// An unresolvable or external reference carries no usable shape.
			resolved := sanitizeSiblings(node, defs, refStack, depth)
			if len(resolved) == 0 {
				return bareObject(description)
			}
			return resolved
		}
		for _, seen := range refStack {
			if seen == name {
				// Recursive schema: Gemini has no way to express the cycle.
				return bareObject(description)
			}
		}
		out = sanitizeSchemaNode(target, defs, append(refStack, name), depth+1)
		if out == nil {
			out = map[string]any{}
		}
	}

	for key, value := range sanitizeSiblings(node, defs, refStack, depth) {
		out[key] = value
	}
	if len(out) == 0 {
		return bareObject(description)
	}
	return out
}

// sanitizeSiblings converts every keyword on a node except $ref.
func sanitizeSiblings(node map[string]any, defs map[string]any, refStack []string, depth int) map[string]any {
	out := map[string]any{}
	for key, value := range node {
		switch key {
		case "$ref", "$defs", "definitions", "$schema", "$id", "$comment", "$anchor":
			// Draft metadata and already-inlined references.
			continue

		case "const":
			// Gemini rejects const; a single-value enum is equivalent.
			out["enum"] = []any{value}

		case "oneOf", "allOf":
			members, ok := value.([]any)
			if !ok {
				continue
			}
			if key == "oneOf" {
				// Gemini offers anyOf only; the validation difference is that
				// anyOf permits more than one branch to match.
				if converted := sanitizeSchemaList(members, defs, refStack, depth); len(converted) > 0 {
					out["anyOf"] = converted
				}
				continue
			}
			// allOf is an intersection, which Gemini cannot express either, so
			// merge the members into this node. Generated schemas overwhelmingly
			// emit a single member wrapping a $ref.
			for _, member := range members {
				m, ok := member.(map[string]any)
				if !ok {
					continue
				}
				mergeSchemaInto(out, sanitizeSchemaNode(m, defs, refStack, depth+1))
			}

		case "properties":
			props, ok := value.(map[string]any)
			if !ok {
				continue
			}
			converted := map[string]any{}
			for name, raw := range props {
				child, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				converted[name] = sanitizeSchemaNode(child, defs, refStack, depth+1)
			}
			if len(converted) > 0 {
				out["properties"] = converted
			}

		case "items":
			switch typed := value.(type) {
			case map[string]any:
				out["items"] = sanitizeSchemaNode(typed, defs, refStack, depth+1)
			case []any:
				// Tuple form: Gemini takes a single item schema, so keep the first.
				if converted := sanitizeSchemaList(typed, defs, refStack, depth); len(converted) > 0 {
					out["items"] = converted[0]
				}
			}

		case "anyOf":
			members, ok := value.([]any)
			if !ok {
				continue
			}
			if converted := sanitizeSchemaList(members, defs, refStack, depth); len(converted) > 0 {
				out["anyOf"] = converted
			}

		case "additionalProperties":
			// Accepted as a boolean; a schema-valued form is not supported.
			if b, ok := value.(bool); ok {
				out["additionalProperties"] = b
			}

		case "required", "enum", "propertyOrdering":
			if list, ok := value.([]any); ok {
				out[key] = append([]any(nil), list...)
			}

		default:
			if geminiSchemaKeys[key] {
				out[key] = deepCopySchemaValue(value)
			}
		}
	}
	return out
}

func sanitizeSchemaList(members []any, defs map[string]any, refStack []string, depth int) []any {
	var out []any
	for _, member := range members {
		m, ok := member.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, sanitizeSchemaNode(m, defs, refStack, depth+1))
	}
	return out
}

// mergeSchemaInto folds an allOf member into the accumulating node. Properties
// and required unions accumulate; other keywords keep the first value seen so an
// explicit sibling on the parent is not overwritten by a merged member.
func mergeSchemaInto(dst, src map[string]any) {
	for key, value := range src {
		switch key {
		case "properties":
			existing, _ := dst["properties"].(map[string]any)
			if existing == nil {
				existing = map[string]any{}
			}
			if incoming, ok := value.(map[string]any); ok {
				for name, prop := range incoming {
					if _, taken := existing[name]; !taken {
						existing[name] = prop
					}
				}
			}
			if len(existing) > 0 {
				dst["properties"] = existing
			}
		case "required":
			existing, _ := dst["required"].([]any)
			incoming, _ := value.([]any)
			seen := map[any]bool{}
			for _, r := range existing {
				seen[r] = true
			}
			for _, r := range incoming {
				if !seen[r] {
					existing = append(existing, r)
					seen[r] = true
				}
			}
			if len(existing) > 0 {
				dst["required"] = existing
			}
		default:
			if _, taken := dst[key]; !taken {
				dst[key] = value
			}
		}
	}
}

// resolveSchemaRef looks up a local JSON pointer in the collected definitions.
// Only same-document $defs/definitions pointers are resolvable; anything else
// (an external URL, a deep pointer into properties) reports not found.
func resolveSchemaRef(ref string, defs map[string]any) (map[string]any, string, bool) {
	for _, prefix := range []string{"#/$defs/", "#/definitions/"} {
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		name := strings.TrimPrefix(ref, prefix)
		if strings.Contains(name, "/") {
			return nil, ref, false
		}
		if target, ok := defs[name].(map[string]any); ok {
			return target, name, true
		}
		return nil, ref, false
	}
	return nil, ref, false
}

func deepCopySchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = deepCopySchemaValue(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = deepCopySchemaValue(v)
		}
		return out
	default:
		return value
	}
}
