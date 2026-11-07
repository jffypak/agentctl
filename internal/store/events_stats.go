package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// UIRunStat is one TUI run (a ui.opened event) and the time of its first
// ui.action, the leading indicator `agentctl stats` reports.
type UIRunStat struct {
	RunID         string
	OpenedAt      time.Time
	FirstActionAt time.Time // zero when HasAction is false
	HasAction     bool
}

// UIRuns returns every ui.opened run with a ui_run_id, oldest first, paired
// with the earliest later ui.action carrying the same ui_run_id. Payloads that
// are not JSON objects are skipped rather than failing the whole report.
func (e *Events) UIRuns(ctx context.Context) ([]UIRunStat, error) {
	rows, err := e.s.db.QueryContext(ctx, `
		SELECT o.run, o.ts, (
			SELECT MIN(a.ts) FROM events a
			WHERE a.type = 'ui.action' AND a.id > o.id
			  AND CASE WHEN json_valid(a.payload) THEN json_extract(a.payload, '$.ui_run_id') END = o.run)
		FROM (
			SELECT id, ts, CASE WHEN json_valid(payload) THEN json_extract(payload, '$.ui_run_id') END AS run
			FROM events
			WHERE type = 'ui.opened'
		) o
		WHERE o.run IS NOT NULL AND o.run != ''
		ORDER BY o.id`)
	if err != nil {
		return nil, fmt.Errorf("store: ui runs: %w", err)
	}
	defer rows.Close()
	var out []UIRunStat
	for rows.Next() {
		var (
			r      UIRunStat
			opened int64
			first  sql.NullInt64
		)
		if err := rows.Scan(&r.RunID, &opened, &first); err != nil {
			return nil, fmt.Errorf("store: ui runs: %w", err)
		}
		r.OpenedAt = fromMS(opened)
		if first.Valid {
			r.FirstActionAt, r.HasAction = fromMS(first.Int64), true
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountUIActions counts ui.action events whose payload action is action.
func (e *Events) CountUIActions(ctx context.Context, action string) (int, error) {
	var n int
	err := e.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events
		WHERE type = 'ui.action' AND CASE WHEN json_valid(payload) THEN json_extract(payload, '$.action') END = ?`, action).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count ui.action %s: %w", action, err)
	}
	return n, nil
}

// CountType counts events of one type.
func (e *Events) CountType(ctx context.Context, typ string) (int, error) {
	var n int
	if err := e.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE type = ?`, typ).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count %s: %w", typ, err)
	}
	return n, nil
}
