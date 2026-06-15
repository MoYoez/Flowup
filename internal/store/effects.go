package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (s *SQLiteStore) BeginEffect(ctx context.Context, effect EffectRecord) (EffectRecord, bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO effects (
			key, run_id, step_id, action_name, status, output_json,
			created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO NOTHING`,
		effect.Key, effect.RunID, effect.StepID, effect.ActionName, effect.Status,
		[]byte(effect.Output), encodeTime(effect.CreatedAt), encodeTime(effect.CompletedAt),
	)
	if err != nil {
		return EffectRecord{}, false, fmt.Errorf("begin effect: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return EffectRecord{}, false, fmt.Errorf("begin effect rows affected: %w", err)
	}
	current, err := s.GetEffect(ctx, effect.Key)
	if err != nil {
		return EffectRecord{}, false, err
	}
	return current, count == 1, nil
}

func (s *SQLiteStore) GetEffect(ctx context.Context, key string) (EffectRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT key, run_id, step_id, action_name, status, output_json,
		       created_at, completed_at
		FROM effects WHERE key = ?`, key)
	var effect EffectRecord
	var output []byte
	var createdAt, completedAt int64
	if err := row.Scan(
		&effect.Key, &effect.RunID, &effect.StepID, &effect.ActionName,
		&effect.Status, &output, &createdAt, &completedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EffectRecord{}, ErrNotFound
		}
		return EffectRecord{}, fmt.Errorf("scan effect: %w", err)
	}
	effect.Output = output
	effect.CreatedAt = decodeTime(createdAt)
	effect.CompletedAt = decodeTime(completedAt)
	return effect, nil
}

func (s *SQLiteStore) CompleteEffect(
	ctx context.Context,
	key string,
	output json.RawMessage,
	completedAt time.Time,
) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE effects SET status = ?, output_json = ?, completed_at = ?
		WHERE key = ? AND status = ?`,
		EffectCompleted, []byte(output), encodeTime(completedAt), key, EffectStarted,
	)
	if err != nil {
		return fmt.Errorf("complete effect: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete effect rows affected: %w", err)
	}
	if count == 0 {
		return ErrConflict
	}
	return nil
}
