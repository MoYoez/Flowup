package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/model"
)

// HTTPPost POSTs a body to a URL — the generic "call an external API" tool
// (a real PaaS/webhook endpoint plugs in here). Bounded like http_get.
type HTTPPost struct {
	Client   *http.Client
	MaxBytes int64
}

// NewHTTPPost builds the tool with sane bounds.
func NewHTTPPost() *HTTPPost {
	return &HTTPPost{Client: &http.Client{Timeout: 20 * time.Second}, MaxBytes: 64 << 10}
}

func (*HTTPPost) Name() string { return "http_post" }

func (*HTTPPost) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "http_post",
		Description: "POST a body to a URL (e.g. call an external API/webhook). Returns the status code and (bounded) response body.",
		InputSchema: json.RawMessage(`{"type":"object","required":["url"],"properties":{"url":{"type":"string"},"body":{"type":"string"},"content_type":{"type":"string"}}}`),
	}
}

func (h *HTTPPost) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		URL, Body, ContentType string
	}
	if err := sonic.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad args: %w", err)
	}
	if a.URL == "" {
		return "", fmt.Errorf("http_post: empty url")
	}
	ct := a.ContentType
	if ct == "" {
		ct = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, bytes.NewReader([]byte(a.Body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", ct)
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, h.MaxBytes))
	return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, string(body)), nil
}
