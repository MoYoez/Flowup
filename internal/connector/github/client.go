package githubconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type Ref struct {
	Owner  string
	Repo   string
	Number int
	Kind   string
}

func ParseURL(raw string) (Ref, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return Ref{}, fmt.Errorf("parse GitHub URL: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return Ref{}, fmt.Errorf("GitHub URL must use https://github.com")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 {
		return Ref{}, fmt.Errorf("unsupported GitHub URL path")
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return Ref{}, fmt.Errorf("invalid GitHub item number")
	}
	kind := ""
	switch parts[2] {
	case "issues":
		kind = "issue"
	case "pull":
		kind = "pull_request"
	default:
		return Ref{}, fmt.Errorf("unsupported GitHub item kind %q", parts[2])
	}
	return Ref{Owner: parts[0], Repo: parts[1], Number: number, Kind: kind}, nil
}

func (c Client) DoJSON(
	ctx context.Context,
	method string,
	path string,
	token string,
	input any,
	output any,
) error {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode GitHub request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create GitHub request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("GitHub request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("GitHub request returned status %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}
