package store

import (
	"context"
	"encoding/json"
	"time"
)

type Store interface {
	Close() error

	CreateRun(context.Context, RunRecord) error
	GetRun(context.Context, string) (RunRecord, error)
	UpdateRun(context.Context, RunRecord) error

	PutStep(context.Context, StepRecord) error
	GetStep(context.Context, string, string) (StepRecord, error)
	ListSteps(context.Context, string) ([]StepRecord, error)

	AppendEvent(context.Context, EventRecord) error
	ListEvents(context.Context, string) ([]EventRecord, error)

	CreateApproval(context.Context, ApprovalRecord) error
	GetApproval(context.Context, string) (ApprovalRecord, error)
	DecideApproval(context.Context, string, ApprovalStatus, string, time.Time) (ApprovalRecord, error)

	BeginEffect(context.Context, EffectRecord) (EffectRecord, bool, error)
	GetEffect(context.Context, string) (EffectRecord, error)
	CompleteEffect(context.Context, string, json.RawMessage, time.Time) error
}
