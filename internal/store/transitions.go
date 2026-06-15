package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *SQLiteStore) PauseForApproval(
	ctx context.Context,
	approval ApprovalRecord,
	step StepRecord,
	run RunRecord,
	event EventRecord,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin approval pause: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO approvals (
			id, run_id, step_id, message, preview_json, status, reason,
			created_at, decided_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		approval.ID, approval.RunID, approval.StepID, approval.Message,
		[]byte(approval.Preview), approval.Status, approval.Reason,
		encodeTime(approval.CreatedAt), encodeTime(approval.DecidedAt),
	); err != nil {
		return fmt.Errorf("insert approval pause: %w", err)
	}
	if err := putStepTx(ctx, tx, step); err != nil {
		return err
	}
	if err := updateRunTx(ctx, tx, run); err != nil {
		return err
	}
	if err := appendEventTx(ctx, tx, event); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit approval pause: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DecideApprovalAndUpdate(
	ctx context.Context,
	id string,
	status ApprovalStatus,
	reason string,
	decidedAt time.Time,
	step StepRecord,
	run RunRecord,
	event EventRecord,
) (ApprovalRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalRecord{}, fmt.Errorf("begin approval transition: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE approvals SET status = ?, reason = ?, decided_at = ?
		WHERE id = ? AND status = ?`,
		status, reason, encodeTime(decidedAt), id, ApprovalPending,
	)
	if err != nil {
		return ApprovalRecord{}, fmt.Errorf("decide approval transition: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ApprovalRecord{}, fmt.Errorf("approval transition rows affected: %w", err)
	}
	if count == 0 {
		if _, err := getApproval(ctx, tx, id); err == ErrNotFound {
			return ApprovalRecord{}, ErrNotFound
		} else if err != nil {
			return ApprovalRecord{}, err
		}
		return ApprovalRecord{}, ErrConflict
	}
	if err := putStepTx(ctx, tx, step); err != nil {
		return ApprovalRecord{}, err
	}
	if err := updateRunTx(ctx, tx, run); err != nil {
		return ApprovalRecord{}, err
	}
	if err := appendEventTx(ctx, tx, event); err != nil {
		return ApprovalRecord{}, err
	}
	approval, err := getApproval(ctx, tx, id)
	if err != nil {
		return ApprovalRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApprovalRecord{}, fmt.Errorf("commit approval transition: %w", err)
	}
	return approval, nil
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func putStepTx(ctx context.Context, executor sqlExecutor, step StepRecord) error {
	_, err := executor.ExecContext(ctx, `
		INSERT INTO steps (
			run_id, step_id, step_index, action_name, input_json, output_json,
			status, attempt, error_code, error_message, started_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, step_id) DO UPDATE SET
			step_index = excluded.step_index,
			action_name = excluded.action_name,
			input_json = excluded.input_json,
			output_json = excluded.output_json,
			status = excluded.status,
			attempt = excluded.attempt,
			error_code = excluded.error_code,
			error_message = excluded.error_message,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at`,
		step.RunID, step.StepID, step.StepIndex, step.ActionName, []byte(step.Input),
		[]byte(step.Output), step.Status, step.Attempt, step.ErrorCode, step.ErrorMessage,
		encodeTime(step.StartedAt), encodeTime(step.FinishedAt),
	)
	if err != nil {
		return fmt.Errorf("put transition step: %w", err)
	}
	return nil
}

func updateRunTx(ctx context.Context, executor sqlExecutor, run RunRecord) error {
	result, err := executor.ExecContext(ctx, `
		UPDATE runs SET
			workflow_name = ?, workflow_version = ?, workflow_yaml = ?, inputs_json = ?,
			output_json = ?, current_step = ?, status = ?, error_code = ?,
			error_message = ?, created_at = ?, updated_at = ?
		WHERE id = ?`,
		run.WorkflowName, run.WorkflowVersion, run.WorkflowYAML, []byte(run.Inputs),
		[]byte(run.Output), run.CurrentStep, run.Status, run.ErrorCode, run.ErrorMessage,
		encodeTime(run.CreatedAt), encodeTime(run.UpdatedAt), run.ID,
	)
	if err != nil {
		return fmt.Errorf("update transition run: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("transition run rows affected: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func appendEventTx(ctx context.Context, executor sqlExecutor, event EventRecord) error {
	_, err := executor.ExecContext(ctx, `
		INSERT INTO events (run_id, step_id, type, data_json, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		event.RunID, event.StepID, event.Type, []byte(event.Data), encodeTime(event.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("append transition event: %w", err)
	}
	return nil
}
