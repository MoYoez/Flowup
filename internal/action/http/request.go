package httpaction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/policy"
)

type Request struct {
	client  *http.Client
	network policy.NetworkPolicy
}

func New(client *http.Client, network policy.NetworkPolicy) *Request {
	if network.MaxResponseBytes <= 0 {
		network.MaxResponseBytes = 1 << 20
	}
	if len(network.AllowedSchemes) == 0 {
		network.AllowedSchemes = []string{"https", "http"}
	}
	if client == nil {
		client = policy.GuardedHTTPClient(network)
	}
	return &Request{client: client, network: network}
}

func (*Request) Definition() action.Definition {
	return action.Definition{
		Name: "http.request",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"method", "url"},
			"properties": map[string]any{
				"method":          map[string]any{"type": "string", "enum": []any{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}},
				"url":             map[string]any{"type": "string"},
				"headers":         map[string]any{"type": "object"},
				"query":           map[string]any{"type": "object"},
				"body":            map[string]any{},
				"idempotency_key": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []any{"status", "headers", "body"},
			"properties": map[string]any{
				"status":  map[string]any{"type": "integer"},
				"headers": map[string]any{"type": "object"},
				"body":    map[string]any{},
			},
		},
		SecretPaths: []string{"headers.*"},
		Timeout:     30 * time.Second,
	}
}

func (*Request) Effect(input map[string]any) action.EffectClass {
	method, _ := input["method"].(string)
	if method == http.MethodGet || method == http.MethodHead {
		return action.EffectReadOnly
	}
	return action.EffectExternal
}

// Prepare validates everything that can be checked without contacting the
// network so a malformed request fails before the engine records a durable
// effect. This keeps unsendable requests from later being reported as
// indeterminate during recovery.
func (a *Request) Prepare(input map[string]any) error {
	rawURL, _ := input["url"].(string)
	if err := a.network.ValidateURL(rawURL); err != nil {
		return err
	}
	if _, err := url.Parse(rawURL); err != nil {
		return fmt.Errorf("parse request URL: %w", err)
	}
	if query, ok := input["query"].(map[string]any); ok {
		for key, value := range query {
			if _, err := scalarString(value); err != nil {
				return fmt.Errorf("query %q: %w", key, err)
			}
		}
	}
	if headers, ok := input["headers"].(map[string]any); ok {
		for key, value := range headers {
			if _, err := scalarString(value); err != nil {
				return fmt.Errorf("header %q: %w", key, err)
			}
		}
	}
	if value, ok := input["body"]; ok {
		if _, err := json.Marshal(value); err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
	}
	return nil
}

func (a *Request) Execute(ctx context.Context, invocation action.Invocation) (action.Result, error) {
	method, _ := invocation.Input["method"].(string)
	rawURL, _ := invocation.Input["url"].(string)
	if err := a.network.ValidateURL(rawURL); err != nil {
		return action.Result{}, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return action.Result{}, fmt.Errorf("parse request URL: %w", err)
	}
	if query, ok := invocation.Input["query"].(map[string]any); ok {
		values := parsed.Query()
		for key, value := range query {
			text, err := scalarString(value)
			if err != nil {
				return action.Result{}, fmt.Errorf("query %q: %w", key, err)
			}
			values.Set(key, text)
		}
		parsed.RawQuery = values.Encode()
	}
	var body io.Reader
	if value, ok := invocation.Input["body"]; ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return action.Result{}, fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return action.Result{}, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if headers, ok := invocation.Input["headers"].(map[string]any); ok {
		for key, value := range headers {
			text, err := scalarString(value)
			if err != nil {
				return action.Result{}, fmt.Errorf("header %q: %w", key, err)
			}
			request.Header.Set(key, text)
		}
	}
	client := *a.client
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if checkRedirect != nil {
			if err := checkRedirect(request, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return a.network.ValidateURL(request.URL.String())
	}
	response, err := client.Do(request)
	if err != nil {
		return action.Result{}, action.Transient(fmt.Errorf("HTTP request failed: %w", err))
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, a.network.MaxResponseBytes+1)
	rawBody, err := io.ReadAll(limited)
	if err != nil {
		return action.Result{}, fmt.Errorf("read HTTP response: %w", err)
	}
	if int64(len(rawBody)) > a.network.MaxResponseBytes {
		return action.Result{}, fmt.Errorf("response exceeds %d bytes", a.network.MaxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		statusErr := fmt.Errorf("HTTP request returned status %d", response.StatusCode)
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return action.Result{}, action.Transient(statusErr)
		}
		return action.Result{}, statusErr
	}
	var responseBody any
	if len(rawBody) == 0 {
		responseBody = nil
	} else if json.Valid(rawBody) {
		if err := json.Unmarshal(rawBody, &responseBody); err != nil {
			return action.Result{}, fmt.Errorf("decode HTTP JSON response: %w", err)
		}
	} else {
		responseBody = string(rawBody)
	}
	output, err := json.Marshal(map[string]any{
		"status":  response.StatusCode,
		"headers": response.Header,
		"body":    responseBody,
	})
	if err != nil {
		return action.Result{}, fmt.Errorf("encode HTTP response: %w", err)
	}
	return action.Result{Output: output}, nil
}

func scalarString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case int:
		return fmt.Sprintf("%d", typed), nil
	default:
		return "", fmt.Errorf("value must be scalar, got %T", value)
	}
}
