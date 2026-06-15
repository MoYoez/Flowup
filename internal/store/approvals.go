package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *SQLiteStore) CreateApproval(ctx context.Context, approval ApprovalRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO approvals (
			id, run_id, step_id, message, preview_json, status, reason,
			created_at, decided_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		approval.ID, approval.RunID, approval.StepID, approval.Message,
		[]byte(approval.Preview), approval.Status, approval.Reason,
		encodeTime(approval.CreatedAt), encodeTime(approval.DecidedAt),
	)
	if err != nil {
		return fmt.Errorf("create approval: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetApproval(ctx context.Context, id string) (ApprovalRecord, error) {
	return getApproval(ctx, s.db, id)
}

func (s *SQLiteStore) GetPendingApproval(ctx context.Context, runID string) (ApprovalRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, step_id, message, preview_json, status, reason,
		       created_at, decided_at
		FROM approvals WHERE run_id = ? AND status = ?
		ORDER BY created_at LIMIT 1`, runID, ApprovalPending)
	return scanApproval(row)
}

func (s *SQLiteStore) DecideApproval(
	ctx context.Context,
	id string,
	status ApprovalStatus,
	reason string,
	decidedAt time.Time,
) (ApprovalRecord, error) {
	if status != ApprovalApproved && status != ApprovalRejected {
		return ApprovalRecord{}, fmt.Errorf("invalid approval decision %q", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalRecord{}, fmt.Errorf("begin approval decision: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE approvals SET status = ?, reason = ?, decided_at = ?
		WHERE id = ? AND status = ?`,
		status, reason, encodeTime(decidedAt), id, ApprovalPending,
	)
	if err != nil {
		return ApprovalRecord{}, fmt.Errorf("decide approval: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ApprovalRecord{}, fmt.Errorf("decide approval rows affected: %w", err)
	}
	if count == 0 {
		if _, err := getApproval(ctx, tx, id); errors.Is(err, ErrNotFound) {
			return ApprovalRecord{}, ErrNotFound
		} else if err != nil {
			return ApprovalRecord{}, err
		}
		return ApprovalRecord{}, ErrConflict
	}
	approval, err := getApproval(ctx, tx, id)
	if err != nil {
		return ApprovalRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApprovalRecord{}, fmt.Errorf("commit approval decision: %w", err)
	}
	return approval, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getApproval(ctx context.Context, query queryRower, id string) (ApprovalRecord, error) {
	row := query.QueryRowContext(ctx, `
		SELECT id, run_id, step_id, message, preview_json, status, reason,
		       created_at, decided_at
		FROM approvals WHERE id = ?`, id)
	return scanApproval(row)
}

func scanApproval(row scanner) (ApprovalRecord, error) {
	var approval ApprovalRecord
	var preview []byte
	var createdAt, decidedAt int64
	if err := row.Scan(
		&approval.ID, &approval.RunID, &approval.StepID, &approval.Message,
		&preview, &approval.Status, &approval.Reason, &createdAt, &decidedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ApprovalRecord{}, ErrNotFound
		}
		return ApprovalRecord{}, fmt.Errorf("scan approval: %w", err)
	}
	approval.Preview = preview
	approval.CreatedAt = decodeTime(createdAt)
	approval.DecidedAt = decodeTime(decidedAt)
	return approval, nil
}
