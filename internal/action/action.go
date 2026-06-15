package action

import (
	"context"
	"encoding/json"
	"time"
)

type EffectClass string

const (
	EffectReadOnly EffectClass = "read_only"
	EffectExternal EffectClass = "external"
)

type Definition struct {
	Name         string
	InputSchema  map[string]any
	OutputSchema map[string]any
	SecretPaths  []string
	Timeout      time.Duration
}

type Invocation struct {
	RunID          string
	StepID         string
	Attempt        int
	Input          map[string]any
	IdempotencyKey string
	Events         EventSink
}

type Result struct {
	Output json.RawMessage
	Pause  *Pause
}

type Pause struct {
	Kind    string
	Message string
	Preview json.RawMessage
}

type Event struct {
	Type string
	Data map[string]any
}

type EventSink interface {
	Emit(context.Context, Event) error
}

type Action interface {
	Definition() Definition
	Effect(input map[string]any) EffectClass
	Execute(context.Context, Invocation) (Result, error)
}
