package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DefaultEventsLimit bounds one Since page.
const DefaultEventsLimit = 1000

// EventInput is an event to append. Zero TaskID/SessionID/CauseID mean "none".
// Payload is marshalled to JSON; nil becomes {}.
type EventInput struct {
	Type      string
	TaskID    string
	SessionID int64
	CauseID   int64 // id of the triggering event (e.g. the hook.received)
	Payload   any
}

// Event is a stored, immutable event.
type Event struct {
	ID        int64
	TS        time.Time
	Type      string
	TaskID    string
	SessionID int64
	CauseID   int64
	Payload   json.RawMessage
}

// Events is the append-only events repository. Stats queries live in
// events_stats.go (m2-tui-inbox).
type Events struct{ s *Store }

// Events returns the events repository.
func (s *Store) Events() *Events { return &Events{s} }

const eventCols = `id, ts, type, task_id, session_id, cause_id, payload`

// eventPayload validates in and returns its JSON payload.
func eventPayload(in EventInput) ([]byte, error) {
	if in.Type == "" || !strings.Contains(in.Type, ".") {
		return nil, fmt.Errorf("store: invalid event type %q", in.Type)
	}
	if in.Payload == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, fmt.Errorf("store: event %s payload: %w", in.Type, err)
	}
	return b, nil
}

// appendEvent inserts one event using q (a *sql.Tx when the event must commit
// atomically with a state change).
func appendEvent(ctx context.Context, q querier, now time.Time, in EventInput) (int64, error) {
	payload, err := eventPayload(in)
	if err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx, `INSERT INTO events (ts, type, task_id, session_id, cause_id, payload) VALUES (?, ?, ?, ?, ?, ?)`,
		ms(now), in.Type, in.TaskID, nullInt(in.SessionID), nullInt(in.CauseID), string(payload))
	if err != nil {
		return 0, fmt.Errorf("store: append %s: %w", in.Type, err)
	}
	return res.LastInsertId()
}

func scanEvent(r rowScanner) (Event, error) {
	var (
		e             Event
		ts            int64
		sess, cause   sql.NullInt64
		payload, kind string
	)
	if err := r.Scan(&e.ID, &ts, &kind, &e.TaskID, &sess, &cause, &payload); err != nil {
		return Event{}, err
	}
	e.TS, e.Type, e.SessionID, e.CauseID, e.Payload = fromMS(ts), kind, sess.Int64, cause.Int64, json.RawMessage(payload)
	return e, nil
}

// Append appends one event and returns its id. Like every store write it runs
// in withTx, so it takes the write lock fairly instead of waiting on SQLite's
// busy handler as an autocommit INSERT would.
func (e *Events) Append(ctx context.Context, in EventInput) (int64, error) {
	if _, err := eventPayload(in); err != nil {
		return 0, err // reject before taking the write lock
	}
	var id int64
	err := e.s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = appendEvent(ctx, tx, e.s.now(), in)
		return err
	})
	return id, err
}

// Since returns up to limit events with id > afterID in id order (the TUI
// polls this every Poll). limit <= 0 means DefaultEventsLimit.
func (e *Events) Since(ctx context.Context, afterID int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = DefaultEventsLimit
	}
	return e.query(ctx, `SELECT `+eventCols+` FROM events WHERE id > ? ORDER BY id LIMIT ?`, afterID, limit)
}

// ForTask returns every event for a task in id order.
func (e *Events) ForTask(ctx context.Context, taskID string) ([]Event, error) {
	return e.query(ctx, `SELECT `+eventCols+` FROM events WHERE task_id = ? ORDER BY id`, taskID)
}

// LastID is the newest event id (0 when empty); pollers start from it.
func (e *Events) LastID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	if err := e.s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: last event id: %w", err)
	}
	return id.Int64, nil
}

func (e *Events) query(ctx context.Context, q string, args ...any) ([]Event, error) {
	rows, err := e.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: events: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
