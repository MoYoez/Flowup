package policy

import (
	"fmt"
	"net/url"
	"strings"
)

type NetworkPolicy struct {
	AllowedSchemes   []string
	AllowedHosts     []string
	MaxResponseBytes int64
}

func DefaultNetworkPolicy() NetworkPolicy {
	return NetworkPolicy{
		AllowedSchemes:   []string{"https", "http"},
		MaxResponseBytes: 1 << 20,
	}
}

func (p NetworkPolicy) ValidateURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Hostname() == "" {
		return fmt.Errorf("URL requires a scheme and host")
	}
	if parsed.User != nil {
		return fmt.Errorf("URL credentials are not allowed")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("URL fragment is not allowed")
	}
	if !containsFold(p.AllowedSchemes, parsed.Scheme) {
		return fmt.Errorf("URL scheme %q is not allowed", parsed.Scheme)
	}
	if len(p.AllowedHosts) > 0 && !containsFold(p.AllowedHosts, parsed.Hostname()) {
		return fmt.Errorf("URL host %q is not allowed", parsed.Hostname())
	}
	return nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
