package policy

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

type NetworkPolicy struct {
	AllowedSchemes   []string
	AllowedHosts     []string
	MaxResponseBytes int64
	// BlockPrivateNetworks rejects requests that resolve to loopback, private,
	// link-local, or otherwise internal addresses. It defends against SSRF when
	// untrusted data (AI output, external responses) flows into a request URL.
	BlockPrivateNetworks bool
}

func DefaultNetworkPolicy() NetworkPolicy {
	return NetworkPolicy{
		AllowedSchemes:       []string{"https", "http"},
		MaxResponseBytes:     1 << 20,
		BlockPrivateNetworks: true,
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
	if p.BlockPrivateNetworks {
		if ip := net.ParseIP(parsed.Hostname()); ip != nil && isBlockedIP(ip) {
			return fmt.Errorf("URL host %q is a blocked private address", parsed.Hostname())
		}
	}
	return nil
}

// GuardedHTTPClient builds an HTTP client that enforces the network policy at
// connection time. When BlockPrivateNetworks is set, every resolved address is
// checked before the socket connects, which also stops DNS rebinding because the
// check runs against the post-resolution IP rather than the hostname.
func GuardedHTTPClient(p NetworkPolicy) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if p.BlockPrivateNetworks {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
				return fmt.Errorf("connection to private address %s is blocked", host)
			}
			return nil
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast()
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
