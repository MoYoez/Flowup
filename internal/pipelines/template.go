package pipelines

import (
	"fmt"
	"regexp"
	"strings"
)

// templateRe matches a single {{ dotted.path }} reference.
var templateRe = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// ResolveValue resolves a single YAML value against a run-state context.
//
//   - If the value is a string that is exactly one "{{ ref }}", the referenced
//     value is returned with its original type preserved (so a number stays a
//     number).
//   - If the value is a string with embedded "{{ ref }}" templates, each is
//     substituted and a string is returned (e.g. for idempotency keys).
//   - Arrays and maps are resolved element-wise (so a native node's argv like
//     ["sh","-c","{{ params.cmd }}"] templates correctly).
//   - Any other value is returned unchanged.
//
// The context is a nested map, e.g. {"params": {...}, "build": {...}}.
func ResolveValue(v any, ctx map[string]any) (any, error) {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			rv, err := ResolveValue(e, ctx)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	case map[string]any:
		return ResolveMap(t, ctx)
	case string:
		// handled below
	default:
		return v, nil
	}
	s := v.(string)
	matches := templateRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	// Whole-string single reference: preserve type.
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(s) {
		expr := s[matches[0][2]:matches[0][3]]
		return lookup(strings.TrimSpace(expr), ctx)
	}
	// Interpolation: stringify each reference.
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		expr := strings.TrimSpace(s[m[2]:m[3]])
		val, err := lookup(expr, ctx)
		if err != nil {
			return nil, err
		}
		b.WriteString(stringify(val))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

// ResolveMap resolves every value in a map against the context.
func ResolveMap(m map[string]any, ctx map[string]any) (map[string]any, error) {
	if m == nil {
		return map[string]any{}, nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		rv, err := ResolveValue(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", k, err)
		}
		out[k] = rv
	}
	return out, nil
}

// lookup walks a dotted path through a nested map context.
func lookup(path string, ctx map[string]any) (any, error) {
	parts := strings.Split(path, ".")
	var cur any = ctx
	for i, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot resolve {{ %s }}: %q is not an object", path, strings.Join(parts[:i], "."))
		}
		v, ok := m[p]
		if !ok {
			return nil, fmt.Errorf("unknown reference {{ %s }}", path)
		}
		cur = v
	}
	return cur, nil
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprintf("%v", v)
	}
}
