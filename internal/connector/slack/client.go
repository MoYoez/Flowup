package slackconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c Client) Call(ctx context.Context, method, token string, input, output any) error {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://slack.com"
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode Slack request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, baseURL+"/api/"+method, bytes.NewReader(raw),
	)
	if err != nil {
		return fmt.Errorf("create Slack request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Slack request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("read Slack response: %w", err)
	}
	if len(body) > 1<<20 {
		return fmt.Errorf("Slack response exceeds 1048576 bytes")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Slack request returned status %d", response.StatusCode)
	}
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode Slack response: %w", err)
	}
	if !envelope.OK {
		return fmt.Errorf("Slack API error: %s", envelope.Error)
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode Slack response payload: %w", err)
	}
	return nil
}
