package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// bindType selects the placeholder style of the underlying driver.
type bindType int

const (
	bindQuestion bindType = iota // ? ?      (SQLite)
	bindDollar                   // $1 $2    (Postgres)
)

// dialect captures the few things that differ between SQLite and Postgres.
type dialect struct {
	bind     bindType
	serialPK string // column DDL for the auto-increment events.id
}

// sqlStore is the generic database/sql implementation shared by both backends.
// JSON payloads are stored as TEXT to stay dialect-neutral.
type sqlStore struct {
	db *sql.DB
	d  dialect
}

func newSQLStore(db *sql.DB, d dialect) *sqlStore { return &sqlStore{db: db, d: d} }

// rebind rewrites `?` placeholders to `$n` when the dialect needs it.
func (s *sqlStore) rebind(q string) string {
	if s.d.bind == bindQuestion {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *sqlStore) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.rebind(q), args...)
}

func (s *sqlStore) Init(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS workflow_status (
			run_id          TEXT PRIMARY KEY,
			pipeline_id     TEXT NOT NULL,
			idempotency_key TEXT NOT NULL UNIQUE,
			status          TEXT NOT NULL,
			input           TEXT,
			output          TEXT,
			reason          TEXT,
			handoff_what    TEXT,
			resume_token    TEXT,
			suspended_node  TEXT,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS operation_outputs (
			run_id     TEXT NOT NULL,
			step_key   TEXT NOT NULL,
			output     TEXT,
			created_at TEXT NOT NULL,
			PRIMARY KEY (run_id, step_key)
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			` + s.d.serialPK + `,
			run_id  TEXT NOT NULL,
			node_id TEXT,
			attempt INTEGER,
			type    TEXT NOT NULL,
			data    TEXT,
			ts      TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_run ON events (run_id)`,
		`CREATE TABLE IF NOT EXISTS side_effects (
			key        TEXT PRIMARY KEY,
			result     TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS human_inputs (
			run_id     TEXT NOT NULL,
			node_id    TEXT NOT NULL,
			data       TEXT,
			created_at TEXT NOT NULL,
			PRIMARY KEY (run_id, node_id)
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			task_id         TEXT PRIMARY KEY,
			run_id          TEXT NOT NULL,
			node_id         TEXT NOT NULL,
			attempt         INTEGER NOT NULL,
			caps            TEXT,
			payload         TEXT,
			status          TEXT NOT NULL,
			lease_owner     TEXT,
			lease_expiry_ms BIGINT,
			result          TEXT,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks (status, created_at)`,
		`CREATE TABLE IF NOT EXISTS outbox (
			key        TEXT PRIMARY KEY,
			payload    TEXT,
			status     TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, st := range stmts {
		if _, err := s.db.ExecContext(ctx, st); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *sqlStore) CreateRun(ctx context.Context, r RunRecord) (bool, RunRecord, error) {
	if r.CreatedAt == "" {
		r.CreatedAt = now()
	}
	r.UpdatedAt = now()
	res, err := s.exec(ctx, `INSERT INTO workflow_status
		(run_id, pipeline_id, idempotency_key, status, input, output, reason, handoff_what, resume_token, suspended_node, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		r.RunID, r.PipelineID, r.IdempotencyKey, r.Status,
		text(r.Input), text(r.Output), r.Reason, r.HandoffWhat, r.ResumeToken, r.SuspendedNode,
		r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return false, RunRecord{}, fmt.Errorf("create run: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return true, r, nil
	}
	existing, ok, err := s.getRunBy(ctx, "idempotency_key", r.IdempotencyKey)
	if err != nil {
		return false, RunRecord{}, err
	}
	if !ok {
		return false, RunRecord{}, errors.New("create run: conflict but existing row not found")
	}
	return false, existing, nil
}

func (s *sqlStore) GetRun(ctx context.Context, runID string) (RunRecord, bool, error) {
	return s.getRunBy(ctx, "run_id", runID)
}

func (s *sqlStore) GetRunByResumeToken(ctx context.Context, token string) (RunRecord, bool, error) {
	if token == "" {
		return RunRecord{}, false, nil
	}
	return s.getRunBy(ctx, "resume_token", token)
}

func (s *sqlStore) GetRunByIdempotencyKey(ctx context.Context, key string) (RunRecord, bool, error) {
	if key == "" {
		return RunRecord{}, false, nil
	}
	return s.getRunBy(ctx, "idempotency_key", key)
}

func (s *sqlStore) getRunBy(ctx context.Context, col, val string) (RunRecord, bool, error) {
	q := `SELECT run_id, pipeline_id, idempotency_key, status, input, output, reason,
		handoff_what, resume_token, suspended_node, created_at, updated_at
		FROM workflow_status WHERE ` + col + ` = ?`
	row := s.db.QueryRowContext(ctx, s.rebind(q), val)
	var r RunRecord
	var input, output []byte
	err := row.Scan(&r.RunID, &r.PipelineID, &r.IdempotencyKey, &r.Status, &input, &output,
		&r.Reason, &r.HandoffWhat, &r.ResumeToken, &r.SuspendedNode, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, false, nil
	}
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("get run: %w", err)
	}
	r.Input = bytesOrNil(input)
	r.Output = bytesOrNil(output)
	return r, true, nil
}

func (s *sqlStore) UpdateRun(ctx context.Context, r RunRecord) error {
	r.UpdatedAt = now()
	_, err := s.exec(ctx, `UPDATE workflow_status SET
		status=?, output=?, reason=?, handoff_what=?, resume_token=?, suspended_node=?, updated_at=?
		WHERE run_id=?`,
		r.Status, text(r.Output), r.Reason, r.HandoffWhat, r.ResumeToken, r.SuspendedNode, r.UpdatedAt, r.RunID)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	return nil
}

func (s *sqlStore) ListPendingRuns(ctx context.Context) ([]RunRecord, error) {
	q := `SELECT run_id, pipeline_id, idempotency_key, status, input, output, reason,
		handoff_what, resume_token, suspended_node, created_at, updated_at
		FROM workflow_status WHERE status = ? ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, s.rebind(q), RunPending)
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer rows.Close()
	var out []RunRecord
	for rows.Next() {
		var r RunRecord
		var input, output []byte
		if err := rows.Scan(&r.RunID, &r.PipelineID, &r.IdempotencyKey, &r.Status, &input, &output,
			&r.Reason, &r.HandoffWhat, &r.ResumeToken, &r.SuspendedNode, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Input = bytesOrNil(input)
		r.Output = bytesOrNil(output)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqlStore) GetStep(ctx context.Context, runID, stepKey string) (StepRecord, bool, error) {
	q := `SELECT run_id, step_key, output, created_at FROM operation_outputs WHERE run_id=? AND step_key=?`
	row := s.db.QueryRowContext(ctx, s.rebind(q), runID, stepKey)
	var r StepRecord
	var output []byte
	err := row.Scan(&r.RunID, &r.StepKey, &output, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StepRecord{}, false, nil
	}
	if err != nil {
		return StepRecord{}, false, fmt.Errorf("get step: %w", err)
	}
	r.Output = bytesOrNil(output)
	return r, true, nil
}

func (s *sqlStore) PutStep(ctx context.Context, r StepRecord) error {
	if r.CreatedAt == "" {
		r.CreatedAt = now()
	}
	_, err := s.exec(ctx, `INSERT INTO operation_outputs (run_id, step_key, output, created_at)
		VALUES (?,?,?,?) ON CONFLICT(run_id, step_key) DO NOTHING`,
		r.RunID, r.StepKey, text(r.Output), r.CreatedAt)
	if err != nil {
		return fmt.Errorf("put step: %w", err)
	}
	return nil
}

func (s *sqlStore) AppendEvent(ctx context.Context, e EventRecord) error {
	if e.TS == "" {
		e.TS = now()
	}
	_, err := s.exec(ctx, `INSERT INTO events (run_id, node_id, attempt, type, data, ts)
		VALUES (?,?,?,?,?,?)`,
		e.RunID, e.NodeID, e.Attempt, e.Type, text(e.Data), e.TS)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (s *sqlStore) ListEvents(ctx context.Context, runID string) ([]EventRecord, error) {
	q := `SELECT id, run_id, node_id, attempt, type, data, ts FROM events WHERE run_id=? ORDER BY id`
	rows, err := s.db.QueryContext(ctx, s.rebind(q), runID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var data []byte
		if err := rows.Scan(&e.ID, &e.RunID, &e.NodeID, &e.Attempt, &e.Type, &data, &e.TS); err != nil {
			return nil, err
		}
		e.Data = bytesOrNil(data)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqlStore) ClaimSideEffect(ctx context.Context, key string) (bool, []byte, error) {
	res, err := s.exec(ctx, `INSERT INTO side_effects (key, result, created_at)
		VALUES (?,?,?) ON CONFLICT(key) DO NOTHING`, key, "", now())
	if err != nil {
		return false, nil, fmt.Errorf("claim side effect: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return true, nil, nil // first claimant: perform the effect
	}
	q := `SELECT result FROM side_effects WHERE key=?`
	row := s.db.QueryRowContext(ctx, s.rebind(q), key)
	var result []byte
	if err := row.Scan(&result); err != nil {
		return false, nil, fmt.Errorf("load side effect: %w", err)
	}
	return false, bytesOrNil(result), nil
}

func (s *sqlStore) FinishSideEffect(ctx context.Context, key string, result []byte) error {
	_, err := s.exec(ctx, `UPDATE side_effects SET result=? WHERE key=?`, text(result), key)
	if err != nil {
		return fmt.Errorf("finish side effect: %w", err)
	}
	return nil
}

func (s *sqlStore) PutHumanInput(ctx context.Context, runID, nodeID string, data []byte) error {
	_, err := s.exec(ctx, `INSERT INTO human_inputs (run_id, node_id, data, created_at)
		VALUES (?,?,?,?) ON CONFLICT(run_id, node_id) DO UPDATE SET data=excluded.data`,
		runID, nodeID, text(data), now())
	if err != nil {
		return fmt.Errorf("put human input: %w", err)
	}
	return nil
}

func (s *sqlStore) GetHumanInput(ctx context.Context, runID, nodeID string) ([]byte, bool, error) {
	q := `SELECT data FROM human_inputs WHERE run_id=? AND node_id=?`
	row := s.db.QueryRowContext(ctx, s.rebind(q), runID, nodeID)
	var data []byte
	err := row.Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get human input: %w", err)
	}
	return bytesOrNil(data), true, nil
}

// Task queue.

func (s *sqlStore) EnqueueTask(ctx context.Context, t TaskRecord) error {
	ts := now()
	_, err := s.exec(ctx, `INSERT INTO tasks
		(task_id, run_id, node_id, attempt, caps, payload, status, lease_owner, lease_expiry_ms, result, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(task_id) DO NOTHING`,
		t.TaskID, t.RunID, t.NodeID, t.Attempt, t.Caps, text(t.Payload), TaskQueued, "", int64(0), "", ts, ts)
	if err != nil {
		return fmt.Errorf("enqueue task: %w", err)
	}
	return nil
}

func (s *sqlStore) ClaimTask(ctx context.Context, workerID string, workerCaps []string, leaseMS int64) (TaskRecord, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskRecord{}, false, fmt.Errorf("claim begin: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, s.rebind(`SELECT task_id, caps FROM tasks WHERE status=? ORDER BY created_at, task_id`), TaskQueued)
	if err != nil {
		return TaskRecord{}, false, fmt.Errorf("claim scan: %w", err)
	}
	type cand struct{ id, caps string }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.caps); err != nil {
			rows.Close()
			return TaskRecord{}, false, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TaskRecord{}, false, err
	}

	have := make(map[string]bool, len(workerCaps))
	for _, c := range workerCaps {
		have[c] = true
	}
	pick := ""
	for _, c := range cands {
		if capsSubset(c.caps, have) {
			pick = c.id
			break
		}
	}
	if pick == "" {
		_ = tx.Commit()
		return TaskRecord{}, false, nil // backpressure: nothing matches this worker
	}

	expiry := time.Now().UnixMilli() + leaseMS
	res, err := tx.ExecContext(ctx, s.rebind(`UPDATE tasks SET status=?, lease_owner=?, lease_expiry_ms=?, updated_at=? WHERE task_id=? AND status=?`),
		TaskLeased, workerID, expiry, now(), pick, TaskQueued)
	if err != nil {
		return TaskRecord{}, false, fmt.Errorf("claim update: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return TaskRecord{}, false, err
	}
	if n == 0 {
		return TaskRecord{}, false, nil // raced with another claimer; caller polls again
	}
	return s.GetTask(ctx, pick)
}

func (s *sqlStore) HeartbeatTask(ctx context.Context, taskID, workerID string, leaseMS int64) (bool, error) {
	expiry := time.Now().UnixMilli() + leaseMS
	res, err := s.exec(ctx, `UPDATE tasks SET lease_expiry_ms=?, updated_at=? WHERE task_id=? AND lease_owner=? AND status=?`,
		expiry, now(), taskID, workerID, TaskLeased)
	if err != nil {
		return false, fmt.Errorf("heartbeat: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *sqlStore) CompleteTask(ctx context.Context, taskID, workerID, status string, result []byte) (bool, error) {
	res, err := s.exec(ctx, `UPDATE tasks SET status=?, result=?, lease_owner=?, updated_at=? WHERE task_id=? AND lease_owner=? AND status=?`,
		status, text(result), "", now(), taskID, workerID, TaskLeased)
	if err != nil {
		return false, fmt.Errorf("complete task: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil // false → lease was reclaimed; this (late) result is rejected
}

func (s *sqlStore) GetTask(ctx context.Context, taskID string) (TaskRecord, bool, error) {
	q := `SELECT task_id, run_id, node_id, attempt, caps, payload, status, lease_owner, lease_expiry_ms, result, created_at, updated_at FROM tasks WHERE task_id=?`
	row := s.db.QueryRowContext(ctx, s.rebind(q), taskID)
	var t TaskRecord
	var payload, result []byte
	err := row.Scan(&t.TaskID, &t.RunID, &t.NodeID, &t.Attempt, &t.Caps, &payload, &t.Status,
		&t.LeaseOwner, &t.LeaseExpiryMs, &result, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRecord{}, false, nil
	}
	if err != nil {
		return TaskRecord{}, false, fmt.Errorf("get task: %w", err)
	}
	t.Payload = bytesOrNil(payload)
	t.Result = bytesOrNil(result)
	return t, true, nil
}

func (s *sqlStore) ReclaimExpiredTasks(ctx context.Context) (int, error) {
	res, err := s.exec(ctx, `UPDATE tasks SET status=?, lease_owner=?, updated_at=? WHERE status=? AND lease_expiry_ms < ?`,
		TaskQueued, "", now(), TaskLeased, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("reclaim tasks: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// capsSubset reports whether every required cap (comma-separated) is in have.
func capsSubset(csv string, have map[string]bool) bool {
	if csv == "" {
		return true // no required caps → any worker can run it
	}
	for _, c := range strings.Split(csv, ",") {
		c = strings.TrimSpace(c)
		if c != "" && !have[c] {
			return false
		}
	}
	return true
}

func (s *sqlStore) PurgeRuns(ctx context.Context, before string) (int, error) {
	sub := `(SELECT run_id FROM workflow_status WHERE created_at < ?)`
	for _, q := range []string{
		`DELETE FROM events WHERE run_id IN ` + sub,
		`DELETE FROM operation_outputs WHERE run_id IN ` + sub,
		`DELETE FROM human_inputs WHERE run_id IN ` + sub,
	} {
		if _, err := s.exec(ctx, q, before); err != nil {
			return 0, fmt.Errorf("purge children: %w", err)
		}
	}
	res, err := s.exec(ctx, `DELETE FROM workflow_status WHERE created_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("purge runs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *sqlStore) EnqueueOutbox(ctx context.Context, key string, payload []byte) error {
	ts := now()
	_, err := s.exec(ctx, `INSERT INTO outbox (key, payload, status, created_at, updated_at)
		VALUES (?,?,?,?,?) ON CONFLICT(key) DO NOTHING`, key, text(payload), "pending", ts, ts)
	if err != nil {
		return fmt.Errorf("enqueue outbox: %w", err)
	}
	return nil
}

func (s *sqlStore) ClaimPendingOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT key, payload, status, created_at FROM outbox WHERE status='pending' ORDER BY created_at LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.rebind(q), limit)
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	defer rows.Close()
	var out []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		var payload []byte
		if err := rows.Scan(&e.Key, &payload, &e.Status, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = bytesOrNil(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqlStore) MarkDelivered(ctx context.Context, key string) error {
	_, err := s.exec(ctx, `UPDATE outbox SET status='delivered', updated_at=? WHERE key=?`, now(), key)
	if err != nil {
		return fmt.Errorf("mark delivered: %w", err)
	}
	return nil
}

func (s *sqlStore) Close() error { return s.db.Close() }

// text stores JSON/[]byte payloads as a TEXT value (dialect-neutral).
func text(b []byte) any {
	if b == nil {
		return ""
	}
	return string(b)
}

func bytesOrNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
