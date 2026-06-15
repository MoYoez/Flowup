package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type OpenAI struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAI(baseURL, apiKey string, client *http.Client) *OpenAI {
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAI{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  client,
	}
}

func (c *OpenAI) Generate(ctx context.Context, request Request) (Response, error) {
	if c.apiKey == "" {
		return Response{}, &Error{Message: "OpenAI API key is required"}
	}
	if request.Model == "" {
		return Response{}, &Error{Message: "model is required"}
	}
	input := string(request.Input)
	if input == "" {
		input = "null"
	}
	payload := map[string]any{
		"model": request.Model,
		"messages": []any{
			map[string]any{"role": "system", "content": request.Prompt},
			map[string]any{"role": "user", "content": input},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "flowup_output",
				"strict": true,
				"schema": request.OutputSchema,
			},
		},
		"stream":      false,
		"store":       false,
		"temperature": request.Temperature,
	}
	if request.MaxTokens > 0 {
		payload["max_completion_tokens"] = request.MaxTokens
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("encode OpenAI request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/v1/chat/completions",
		bytes.NewReader(encoded),
	)
	if err != nil {
		return Response{}, fmt.Errorf("create OpenAI request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(httpRequest)
	if err != nil {
		return Response{}, &Error{Message: "OpenAI request failed", Transient: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return Response{}, &Error{
			Message:   fmt.Sprintf("OpenAI request returned status %d", response.StatusCode),
			Transient: response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500,
		}
	}
	var payloadResponse struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&payloadResponse); err != nil {
		return Response{}, &Error{Message: "decode OpenAI response", Transient: true}
	}
	if len(payloadResponse.Choices) == 0 {
		return Response{}, &Error{Message: "OpenAI response contained no choices", Transient: true}
	}
	raw := json.RawMessage(payloadResponse.Choices[0].Message.Content)
	if !json.Valid(raw) {
		return Response{}, &Error{Message: "OpenAI response was not valid JSON"}
	}
	return Response{
		Model: payloadResponse.Model,
		JSON:  append(json.RawMessage(nil), raw...),
		Usage: Usage{
			InputTokens:  payloadResponse.Usage.PromptTokens,
			OutputTokens: payloadResponse.Usage.CompletionTokens,
		},
	}, nil
}
