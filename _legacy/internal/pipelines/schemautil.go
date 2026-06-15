package pipelines

import "sort"

// schemaRequired returns the ordered `required` list of a JSON-schema map.
// The order is meaningful (it is a YAML sequence) and is what edge labels use.
func schemaRequired(schema map[string]any) []string {
	if schema == nil {
		return nil
	}
	r, ok := schema["required"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(r))
	for _, v := range r {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// schemaPropertyKeys returns property keys deterministically: required ones
// first in their declared order, then any remaining properties sorted. (Go maps
// have no order, so we impose one to keep derived views stable.)
func schemaPropertyKeys(schema map[string]any) []string {
	if schema == nil {
		return nil
	}
	req := schemaRequired(schema)
	seen := make(map[string]bool, len(req))
	out := make([]string, 0, len(req))
	for _, k := range req {
		out = append(out, k)
		seen[k] = true
	}
	props, _ := schema["properties"].(map[string]any)
	rest := make([]string, 0, len(props))
	for k := range props {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}
