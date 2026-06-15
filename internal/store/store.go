// Package store is the single source of truth: the durable event log, the run
// state projection, the step-memoization table (so completed steps are replayed,
// not re-run), and the side-effect dedup table (outbox/exactly-once primitive).
//
// Decision #4 locks Postgres for production. The MVP runs on pure-Go SQLite with
// no Docker; both sit behind this one Store interface and share a generic
// database/sql implementation, so Postgres drops in by swapping the constructor.
package store

import "context"

// Run lifecycle statuses. "pending" is the only internal-only one; the other
// three are the external three-state contract.
const (
	RunPending    = "pending"
	RunOK         = "ok"
	RunFailed     = "failed"
	RunNeedsHuman = "needs_human"
)

// RunRecord is the run-level state projection.
type RunRecord struct {
	RunID          string
	PipelineID     string
	IdempotencyKey string
	Status         string
	Input          []byte
	Output         []byte
	Reason         string
	HandoffWhat    string
	ResumeToken    string
	SuspendedNode  string
	CreatedAt      string
	UpdatedAt      string
}

// StepRecord is one memoized step output. The (RunID, StepKey) pair is the
// primary key; writes are idempotent (write-once), which is what makes
// "don't re-run committed nodes on recovery" work.
type StepRecord struct {
	RunID     string
	StepKey   string
	Output    []byte
	CreatedAt string
}

// EventRecord is one entry in the event log — the business/replay truth source
// (distinct from OTel's performance/correlation view). run_id/node_id/attempt
// are the correlation IDs.
type EventRecord struct {
	ID      int64
	RunID   string
	NodeID  string
	Attempt int
	Type    string
	Data    []byte
	TS      string
}

// Task queue statuses.
const (
	TaskQueued = "queued"
	TaskLeased = "leased"
	TaskDone   = "done"
	TaskFailed = "failed"
)

// TaskRecord is one queued unit of node work — the durable substrate for
// engine→Worker dispatch. Workers pull matching tasks (capability routing), hold
// a lease with a visibility timeout, heartbeat to extend it, and write back a
// result. A dead worker's lease expires and the task is reclaimed for another
// worker — exactly-once at the result/side-effect level.
type TaskRecord struct {
	TaskID        string // run_id:node_id:attempt (enqueue is idempotent on this)
	RunID         string
	NodeID        string
	Attempt       int
	Caps          string // comma-separated required caps; the routing key
	Payload       []byte // the NodeTask JSON
	Status        string
	LeaseOwner    string
	LeaseExpiryMs int64
	Result        []byte // the NodeResult JSON
	CreatedAt     string
	UpdatedAt     string
}

// OutboxEntry is a durable delivery intent (the transactional-outbox row).
type OutboxEntry struct {
	Key       string // the external idempotency key (dedup at the destination)
	Payload   []byte
	Status    string // pending | delivered
	CreatedAt string
}

// Store is the durable substrate. All methods are safe for the engine's
// (low) concurrency; the SQLite backend serializes writers.
type Store interface {
	// Init creates tables if absent (idempotent).
	Init(ctx context.Context) error

	// CreateRun inserts a run, deduped by IdempotencyKey. If a run with that key
	// already exists, created=false and the existing record is returned (this is
	// call-level exactly-once / replay).
	CreateRun(ctx context.Context, r RunRecord) (created bool, existing RunRecord, err error)
	GetRun(ctx context.Context, runID string) (RunRecord, bool, error)
	GetRunByResumeToken(ctx context.Context, token string) (RunRecord, bool, error)
	GetRunByIdempotencyKey(ctx context.Context, key string) (RunRecord, bool, error)
	UpdateRun(ctx context.Context, r RunRecord) error
	ListPendingRuns(ctx context.Context) ([]RunRecord, error)

	// GetStep returns a memoized step output if present.
	GetStep(ctx context.Context, runID, stepKey string) (StepRecord, bool, error)
	// PutStep records a step output write-once (no-op if it already exists).
	PutStep(ctx context.Context, r StepRecord) error

	AppendEvent(ctx context.Context, e EventRecord) error
	ListEvents(ctx context.Context, runID string) ([]EventRecord, error)

	// ClaimSideEffect reserves a side-effect key. claimed=true means the caller
	// is the first and should perform the effect; claimed=false returns the
	// previously recorded result (the outbox/dedup primitive).
	ClaimSideEffect(ctx context.Context, key string) (claimed bool, existing []byte, err error)
	// FinishSideEffect stores the result for a previously claimed key.
	FinishSideEffect(ctx context.Context, key string, result []byte) error

	// PutHumanInput / GetHumanInput carry resume-time human input to the waiting node.
	PutHumanInput(ctx context.Context, runID, nodeID string, data []byte) error
	GetHumanInput(ctx context.Context, runID, nodeID string) ([]byte, bool, error)

	// PurgeRuns deletes runs created strictly before the given RFC3339 timestamp
	// (and their events/steps/human_inputs). Retention/data-minimization guardrail.
	PurgeRuns(ctx context.Context, beforeRFC3339 string) (deleted int, err error)

	// Outbox: record a delivery intent durably; a deliverer drains it
	// at-least-once and the external API dedups on the key (exactly-once).
	EnqueueOutbox(ctx context.Context, key string, payload []byte) error
	ClaimPendingOutbox(ctx context.Context, limit int) ([]OutboxEntry, error)
	MarkDelivered(ctx context.Context, key string) error

	// Task queue: pull-based dispatch with leases.
	// EnqueueTask adds a task; idempotent on TaskID.
	EnqueueTask(ctx context.Context, t TaskRecord) error
	// ClaimTask atomically leases the oldest queued task whose required caps are a
	// subset of workerCaps. ok=false means nothing matched (backpressure).
	ClaimTask(ctx context.Context, workerID string, workerCaps []string, leaseMS int64) (TaskRecord, bool, error)
	// HeartbeatTask extends the lease iff still owned. ok=false means the lease was lost.
	HeartbeatTask(ctx context.Context, taskID, workerID string, leaseMS int64) (bool, error)
	// CompleteTask records a result iff the caller still owns the lease — a
	// reclaimed worker's late result is rejected (accepted=false).
	CompleteTask(ctx context.Context, taskID, workerID, status string, result []byte) (accepted bool, err error)
	GetTask(ctx context.Context, taskID string) (TaskRecord, bool, error)
	// ReclaimExpiredTasks requeues leased tasks whose lease expired (dead workers).
	ReclaimExpiredTasks(ctx context.Context) (reclaimed int, err error)

	Close() error
}
