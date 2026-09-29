package store

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record conflict")
)

type RunStatus string

const (
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunSucceeded       RunStatus = "succeeded"
	RunFailed          RunStatus = "failed"
	RunRejected        RunStatus = "rejected"
	RunCancelled       RunStatus = "cancelled"
)

type StepStatus string

const (
	StepPending         StepStatus = "pending"
	StepRunning         StepStatus = "running"
	StepSucceeded       StepStatus = "succeeded"
	StepSkipped         StepStatus = "skipped"
	StepWaitingApproval StepStatus = "waiting_approval"
	StepApproved        StepStatus = "approved"
	StepRejected        StepStatus = "rejected"
	StepFailed          StepStatus = "failed"
)

type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
)

type EffectStatus string

const (
	EffectStarted   EffectStatus = "started"
	EffectCompleted EffectStatus = "completed"
)

type RunRecord struct {
	ID              string
	WorkflowName    string
	WorkflowVersion int
	WorkflowYAML    []byte
	PluginBindings  json.RawMessage
	Inputs          json.RawMessage
	Output          json.RawMessage
	CurrentStep     int
	Status          RunStatus
	ErrorCode       string
	ErrorMessage    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type StepRecord struct {
	RunID        string
	StepID       string
	StepIndex    int
	ActionName   string
	Input        json.RawMessage
	Output       json.RawMessage
	Status       StepStatus
	Attempt      int
	ErrorCode    string
	ErrorMessage string
	StartedAt    time.Time
	FinishedAt   time.Time
}

type EventRecord struct {
	ID        int64
	RunID     string
	StepID    string
	Type      string
	Data      json.RawMessage
	CreatedAt time.Time
}

type ApprovalRecord struct {
	ID        string
	RunID     string
	StepID    string
	Message   string
	Preview   json.RawMessage
	Status    ApprovalStatus
	Reason    string
	CreatedAt time.Time
	DecidedAt time.Time
}

type EffectRecord struct {
	Key         string
	RunID       string
	StepID      string
	ActionName  string
	Status      EffectStatus
	Output      json.RawMessage
	CreatedAt   time.Time
	CompletedAt time.Time
}
