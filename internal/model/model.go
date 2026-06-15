package model

import (
	"context"
	"encoding/json"
	"errors"
)

type Request struct {
	Model        string
	Prompt       string
	Input        json.RawMessage
	OutputSchema map[string]any
	MaxTokens    int
	Temperature  float64
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type Response struct {
	Model string
	JSON  json.RawMessage
	Usage Usage
}

type Error struct {
	Message   string
	Transient bool
}

func (e *Error) Error() string {
	return e.Message
}

type Client interface {
	Generate(context.Context, Request) (Response, error)
}

func IsTransient(err error) bool {
	var modelError *Error
	if !errors.As(err, &modelError) {
		return false
	}
	return modelError.Transient
}
