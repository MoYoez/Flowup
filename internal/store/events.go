package store

import (
	"context"
	"fmt"
)

func (s *SQLiteStore) AppendEvent(ctx context.Context, event EventRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (run_id, step_id, type, data_json, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		event.RunID, event.StepID, event.Type, []byte(event.Data), encodeTime(event.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListEvents(ctx context.Context, runID string) ([]EventRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, step_id, type, data_json, created_at
		FROM events WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var events []EventRecord
	for rows.Next() {
		var event EventRecord
		var data []byte
		var createdAt int64
		if err := rows.Scan(
			&event.ID, &event.RunID, &event.StepID, &event.Type, &data, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.Data = data
		event.CreatedAt = decodeTime(createdAt)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return events, nil
}
