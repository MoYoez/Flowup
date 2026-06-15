// Package contracts is the frozen wire/type contract shared across the three
// layers (trigger -> engine -> worker). It is the single source of truth for the
// shapes that cross a boundary, mirroring the appendix of
// docs/agent-pipeline-tech-research.md.
//
// Nothing in here knows *how* a node is executed (which model / Worker / sandbox)
// — that is an implementation detail behind these shapes. The whole point is
// that the boundary only ever sees the three-state result, never a raw error.
package contracts

import "encoding/json"

// Status is the three-state contract. Node-level and run-level results are
// isomorphic, so the fault boundary composes recursively.
type Status string

const (
	// StatusOK — typed output is available.
	StatusOK Status = "ok"
	// StatusFailed — a classified reason is available; never a raw stack trace.
	StatusFailed Status = "failed"
	// StatusNeedsHuman — a handoff + resume_token is available; first-class pause.
	StatusNeedsHuman Status = "needs_human"
)

// Kind is how a node is executed.
type Kind string

const (
	KindNative   Kind = "native"   // deterministic code, no model
	KindSemantic Kind = "semantic" // single model call, schema-shaped output
	KindAgent    Kind = "agent"    // multi-step tool-using model in a sandbox
)

// Reason classifications for Status == failed. A deliberately small, stable enum
// that is both agent-actionable and human-diagnosable; a raw error or stack is
// never surfaced — anything a node reports that isn't a short classified token is
// collapsed to ReasonNodeError. Node-level failures read as "<node_id>:<reason>".
const (
	ReasonRetriesExhausted = "retries_exhausted"       // retries used up
	ReasonTimeout          = "node_timeout"            // node exceeded limits.timeout_sec
	ReasonSchemaViolation  = "output_schema_violation" // output failed output_schema
	ReasonDispatchFailed   = "dispatch_failed"         // transport/worker delivery failure
	ReasonInputResolve     = "input_resolve_failed"    // could not resolve node inputs
	ReasonNodeError        = "node_error"              // catch-all for an unclassified node failure (raw text scrubbed)
	ReasonInvalidParams    = "invalid_params"          // params failed input_schema
	ReasonUnknownPipeline  = "unknown_pipeline"        // no such pipeline
	ReasonUnauthorized     = "unauthorized"            // caller not allowed this pipeline
	ReasonInternal         = "internal_error"          // engine/infra fault
)

// trigger -> engine

// Invocation is what a trusted host (OpenClaw / AstrBot) sends to start (or
// resume) a pipeline run.
type Invocation struct {
	PipelineID     string          `json:"pipeline_id"`
	Params         json.RawMessage `json:"params"`                 // validated against the pipeline input_schema before the run
	IdempotencyKey string          `json:"idempotency_key"`        // dedup + replay: same key never runs twice / never doubles side effects
	Caller         Caller          `json:"caller"`                 // auth scope + result delivery (from trusted host config)
	ResumeToken    string          `json:"resume_token,omitempty"` // resume from needs_human
}

// Caller identifies who is invoking, for auth scoping and result delivery.
type Caller struct {
	Host      string `json:"host"` // "openclaw" | "astrbot"
	UserRef   string `json:"user_ref"`
	SessionID string `json:"session_id"`
}

// InvocationResult is the three-state result returned to the host.
type InvocationResult struct {
	RunID   string          `json:"run_id"`
	Status  Status          `json:"status"`
	Output  json.RawMessage `json:"output,omitempty"`  // set when Status == ok
	Reason  string          `json:"reason,omitempty"`  // set when Status == failed (classified, never raw)
	Handoff *Handoff        `json:"handoff,omitempty"` // set when Status == needs_human
}

// Handoff is the human-facing explanation + the token to resume with.
type Handoff struct {
	What        string `json:"what"`
	ResumeToken string `json:"resume_token"`
}

// engine -> worker

// NodeTask is one unit of work dispatched to a capability-matched Worker.
type NodeTask struct {
	RunID        string          `json:"run_id"`
	NodeID       string          `json:"node_id"`
	Kind         Kind            `json:"kind"`
	Profile      ExecProfile     `json:"profile"`       // casting: primary+backup models, role, tool whitelist
	Inputs       json.RawMessage `json:"inputs"`        // resolved from run state
	OutputSchema json.RawMessage `json:"output_schema"` // sent with the task; validated on the Worker side before returning
	RequiresCaps []string        `json:"requires_caps"` // chrome/x11/gpu/os — routing key
	Limits       Limits          `json:"limits"`
	Attempt      int             `json:"attempt"`  // 1-based; selects Profile.Models[attempt-1] and counts retries
	LeaseID      string          `json:"lease_id"` // lease: visibility timeout → reclaim + redispatch
}

// ExecProfile is the casting for a node: who plays the part and with what tools.
type ExecProfile struct {
	Models []string `json:"models"` // primary → backups; Attempt slides down this list
	Role   string   `json:"role"`
	Tools  []string `json:"tools"`
	Temp   float64  `json:"temp"`
}

// Limits bound a node's cost.
type Limits struct {
	MaxSteps   int     `json:"max_steps"`
	BudgetUSD  float64 `json:"budget_usd"`
	TimeoutSec int     `json:"timeout_sec"`
}

// ModelForAttempt returns the model to use on a given 1-based attempt, sliding
// down the primary→backup chain and clamping at the last entry. This is the one
// mechanism the doc calls out: attempt drives both "retry" and "slide to the
// next model" — they are the same knob.
func (p ExecProfile) ModelForAttempt(attempt int) string {
	if len(p.Models) == 0 {
		return ""
	}
	i := attempt - 1
	if i < 0 {
		i = 0
	}
	if i >= len(p.Models) {
		i = len(p.Models) - 1
	}
	return p.Models[i]
}

// worker -> engine

// NodeResult is the three-state result of one node attempt.
type NodeResult struct {
	RunID     string          `json:"run_id"`
	NodeID    string          `json:"node_id"`
	LeaseID   string          `json:"lease_id"`
	Status    Status          `json:"status"`
	Output    json.RawMessage `json:"output,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Retriable bool            `json:"retriable,omitempty"`
	Handoff   *Handoff        `json:"handoff,omitempty"`
}

// NodeEvent is a streamed observation from inside a node execution. The event
// log built from these is the business/replay truth source (distinct from OTel,
// which is the performance/correlation view).
type NodeEvent struct {
	RunID  string          `json:"run_id"`
	NodeID string          `json:"node_id"`
	Type   string          `json:"type"` // node_started | tool_call | tool_result | token | ...
	Data   json.RawMessage `json:"data"`
}
