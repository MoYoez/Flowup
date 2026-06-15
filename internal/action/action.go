package action

import (
	"context"
	"encoding/json"
	"errors"
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

// Preparer is an optional capability for external actions. The engine calls
// Prepare with the resolved input before recording the durable effect, so any
// input that could never have reached the network fails without leaving a
// "started" effect record that recovery would treat as indeterminate.
type Preparer interface {
	Prepare(input map[string]any) error
}

// Transient wraps an error to mark it as worth retrying. The engine only retries
// transient failures from read-only actions; external effects are never retried
// automatically because a repeat could duplicate a side effect.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return transientError{err: err}
}

// IsTransient reports whether err (or anything it wraps) was marked transient.
func IsTransient(err error) bool {
	var transient interface{ Transient() bool }
	if errors.As(err, &transient) {
		return transient.Transient()
	}
	return false
}

type transientError struct{ err error }

func (t transientError) Error() string   { return t.err.Error() }
func (t transientError) Unwrap() error   { return t.err }
func (t transientError) Transient() bool { return true }
