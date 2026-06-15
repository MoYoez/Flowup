package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *SQLiteStore) CreateRun(ctx context.Context, run RunRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (
			id, workflow_name, workflow_version, workflow_yaml, inputs_json,
			output_json, current_step, status, error_code, error_message,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.WorkflowName, run.WorkflowVersion, run.WorkflowYAML, []byte(run.Inputs),
		[]byte(run.Output), run.CurrentStep, run.Status, run.ErrorCode, run.ErrorMessage,
		encodeTime(run.CreatedAt), encodeTime(run.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetRun(ctx context.Context, id string) (RunRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workflow_name, workflow_version, workflow_yaml, inputs_json,
		       output_json, current_step, status, error_code, error_message,
		       created_at, updated_at
		FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func (s *SQLiteStore) UpdateRun(ctx context.Context, run RunRecord) error {
	result, err := s.db.ExecContext(ctx, `
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
		return fmt.Errorf("update run: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update run rows affected: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) PutStep(ctx context.Context, step StepRecord) error {
	_, err := s.db.ExecContext(ctx, `
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
		return fmt.Errorf("put step: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetStep(ctx context.Context, runID, stepID string) (StepRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, step_id, step_index, action_name, input_json, output_json,
		       status, attempt, error_code, error_message, started_at, finished_at
		FROM steps WHERE run_id = ? AND step_id = ?`, runID, stepID)
	return scanStep(row)
}

func (s *SQLiteStore) ListSteps(ctx context.Context, runID string) ([]StepRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, step_id, step_index, action_name, input_json, output_json,
		       status, attempt, error_code, error_message, started_at, finished_at
		FROM steps WHERE run_id = ? ORDER BY step_index`, runID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer rows.Close()
	var steps []StepRecord
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	return steps, nil
}

type scanner interface {
	Scan(...any) error
}

func scanRun(row scanner) (RunRecord, error) {
	var run RunRecord
	var inputs, output []byte
	var createdAt, updatedAt int64
	if err := row.Scan(
		&run.ID, &run.WorkflowName, &run.WorkflowVersion, &run.WorkflowYAML, &inputs,
		&output, &run.CurrentStep, &run.Status, &run.ErrorCode, &run.ErrorMessage,
		&createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunRecord{}, ErrNotFound
		}
		return RunRecord{}, fmt.Errorf("scan run: %w", err)
	}
	run.Inputs = inputs
	run.Output = output
	run.CreatedAt = decodeTime(createdAt)
	run.UpdatedAt = decodeTime(updatedAt)
	return run, nil
}

func scanStep(row scanner) (StepRecord, error) {
	var step StepRecord
	var input, output []byte
	var startedAt, finishedAt int64
	if err := row.Scan(
		&step.RunID, &step.StepID, &step.StepIndex, &step.ActionName, &input, &output,
		&step.Status, &step.Attempt, &step.ErrorCode, &step.ErrorMessage,
		&startedAt, &finishedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StepRecord{}, ErrNotFound
		}
		return StepRecord{}, fmt.Errorf("scan step: %w", err)
	}
	step.Input = input
	step.Output = output
	step.StartedAt = decodeTime(startedAt)
	step.FinishedAt = decodeTime(finishedAt)
	return step, nil
}
