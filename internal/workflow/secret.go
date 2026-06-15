package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

type SecretRef struct {
	Name string
}

var secretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ParseSecretReference(value string) (SecretRef, bool, error) {
	if !strings.Contains(value, "secrets.") {
		return SecretRef{}, false, nil
	}
	matches := embeddedExpressionPattern.FindStringSubmatch(value)
	if len(matches) != 2 || matches[0] != value {
		return SecretRef{}, false, fmt.Errorf("secret reference must occupy the complete value")
	}
	source := strings.TrimSpace(matches[1])
	parts := strings.Split(source, ".")
	if len(parts) != 2 || parts[0] != "secrets" || !secretNamePattern.MatchString(parts[1]) {
		return SecretRef{}, false, fmt.Errorf("invalid secret reference %q", source)
	}
	return SecretRef{Name: parts[1]}, true, nil
}

func ContainsSecret(value any) bool {
	switch typed := value.(type) {
	case SecretRef:
		return true
	case map[string]any:
		for _, item := range typed {
			if ContainsSecret(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if ContainsSecret(item) {
				return true
			}
		}
	}
	return false
}
