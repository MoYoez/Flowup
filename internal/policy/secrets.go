package policy

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/moyoez/flowup/internal/workflow"
)

type SecretSource interface {
	Lookup(name string) (string, bool)
}

type MapSecrets map[string]string

func (m MapSecrets) Lookup(name string) (string, bool) {
	value, ok := m[name]
	return value, ok
}

type EnvSecrets struct{}

func (EnvSecrets) Lookup(name string) (string, bool) {
	return os.LookupEnv(name)
}

func ResolveSecrets(
	input map[string]any,
	allowedPaths []string,
	source SecretSource,
) (map[string]any, map[string]any, error) {
	resolved, redacted, err := resolveSecretsValue(input, "", allowedPaths, source)
	if err != nil {
		return nil, nil, err
	}
	return resolved.(map[string]any), redacted.(map[string]any), nil
}

func resolveSecretsValue(
	value any,
	path string,
	allowedPaths []string,
	source SecretSource,
) (any, any, error) {
	switch typed := value.(type) {
	case workflow.SecretRef:
		if !pathAllowed(path, allowedPaths) {
			return nil, nil, fmt.Errorf("secret reference is not allowed at %q", path)
		}
		secret, ok := source.Lookup(typed.Name)
		if !ok {
			return nil, nil, fmt.Errorf("secret %q is not available", typed.Name)
		}
		return secret, "[REDACTED]", nil
	case map[string]any:
		resolved := make(map[string]any, len(typed))
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			resolvedItem, redactedItem, err := resolveSecretsValue(item, childPath, allowedPaths, source)
			if err != nil {
				return nil, nil, err
			}
			resolved[key] = resolvedItem
			redacted[key] = redactedItem
		}
		return resolved, redacted, nil
	case []any:
		resolved := make([]any, len(typed))
		redacted := make([]any, len(typed))
		for index, item := range typed {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			resolvedItem, redactedItem, err := resolveSecretsValue(item, childPath, allowedPaths, source)
			if err != nil {
				return nil, nil, err
			}
			resolved[index] = resolvedItem
			redacted[index] = redactedItem
		}
		return resolved, redacted, nil
	default:
		return value, value, nil
	}
}

func pathAllowed(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == path {
			return true
		}
		if strings.HasSuffix(pattern, ".*") {
			prefix := strings.TrimSuffix(pattern, "*")
			remainder := strings.TrimPrefix(path, prefix)
			if strings.HasPrefix(path, prefix) && remainder != "" && !strings.Contains(remainder, ".") {
				return true
			}
		}
	}
	return false
}

// SecretValues uses the same field and array paths as ResolveSecrets.
func SecretValues(value any, allowedPaths []string) []string {
	var values []string
	var visit func(any, string)
	visit = func(value any, path string) {
		switch v := value.(type) {
		case string:
			if v != "" && pathAllowed(path, allowedPaths) {
				values = append(values, v)
			}
		case map[string]any:
			for key, item := range v {
				child := key
				if path != "" {
					child = path + "." + key
				}
				visit(item, child)
			}
		case []any:
			for i, item := range v {
				visit(item, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	visit(value, "")
	return values
}

func RedactText(text string, values []string) string {
	values = append([]string(nil), values...)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		if value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	return text
}

var sensitiveKeyPattern = regexp.MustCompile(`(?i)(token|secret|password|authorization|api[_-]?key|private[_-]?key)`)

func Redact(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveKeyPattern.MatchString(key) {
				result[key] = "[REDACTED]"
			} else {
				result[key] = Redact(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = Redact(item)
		}
		return result
	default:
		return value
	}
}
