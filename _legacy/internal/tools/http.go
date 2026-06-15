package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/ratelimit"
)

// HTTPGet fetches a URL (bounded). It is the verify-style tool: read-only,
// size-capped, timeout-bounded, and optionally rate-limited.
type HTTPGet struct {
	Client   *http.Client
	MaxBytes int64
	Limiter  *ratelimit.Bucket // optional: throttle outbound requests
}

// NewHTTPGet builds an http_get tool with sane bounds.
func NewHTTPGet() *HTTPGet {
	return &HTTPGet{Client: &http.Client{Timeout: 15 * time.Second}, MaxBytes: 64 << 10}
}

func (*HTTPGet) Name() string { return "http_get" }

func (*HTTPGet) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "http_get",
		Description: "HTTP GET a URL and return the status code and (truncated) body.",
		InputSchema: objSchema("url", `{"url":{"type":"string","description":"the URL to GET"}}`),
	}
}

func (h *HTTPGet) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := sonic.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad args: %w", err)
	}
	if a.URL == "" {
		return "", fmt.Errorf("http_get: empty url")
	}
	if h.Limiter != nil {
		if err := h.Limiter.Wait(ctx); err != nil {
			return "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, h.MaxBytes))
	return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, string(body)), nil
}
