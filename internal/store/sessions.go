package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jffypak/agentctl/internal/domain"
)

// MaxDetailLen bounds sessions.last_detail (bytes, after sanitizing).
const MaxDetailLen = 512

// Session is an agent process bound to one tmux pane by explicit identity.
type Session struct {
	ID             int64
	TaskID         string
	AgentType      string
	Origin         domain.Origin
	Identity       domain.Identity
	SessionName    string // tmux session name at capture time (informational)
	AgentSessionID string // Claude Code session_id, from hooks
	State          domain.AgentState
	Confidence     domain.Confidence
	StateSince     time.Time
	LastDetail     string
	CreatedAt      time.Time
	EndedAt        time.Time // set when the session reaches a terminal state
}

// NewSession describes a session row to insert. Events are appended in the
// same transaction; their TaskID/SessionID are filled in when zero.
type NewSession struct {
	TaskID      string
	AgentType   string
	Origin      domain.Origin
	Identity    domain.Identity
	SessionName string
	State       domain.AgentState // starting (launch), unknown (adopt), or failed (launch failed)
	Confidence  domain.Confidence
	Detail      string
	Events      []EventInput
}

// TransitionResult reports what Transition did.
type TransitionResult struct {
	From    domain.AgentState
	To      domain.AgentState
	Changed bool  // false for an allowed self-transition (no event, state_since kept)
	EventID int64 // the agent.state_changed event id when Changed
}

// transitionInput is the full form used inside the package (sessions_hook.go,
// sessions_reconcile.go): it adds a cause id and runs inside a caller's tx.
type transitionInput struct {
	SessionID  int64
	To         domain.AgentState
	Confidence domain.Confidence
	Detail     string
	Source     string
	CauseID    int64          // e.g. the hook.received event id
	Extra      map[string]any // merged into the agent.state_changed payload
}

// Sessions is the sessions repository. Hook-specific writes live in
// sessions_hook.go (m1-hook-state); reconcile writes in sessions_reconcile.go
// (m1-pane-identity).
type Sessions struct{ s *Store }

// Sessions returns the sessions repository.
func (s *Store) Sessions() *Sessions { return &Sessions{s} }

const sessionCols = `id, task_id, agent_type, origin, tmux_socket, tmux_server_pid, tmux_session_id,
	tmux_session_name, tmux_pane_id, pane_pid, agent_session_id, agent_state, state_confidence,
	state_since, last_detail, created_at, ended_at`

func scanSession(r rowScanner) (Session, error) {
	var (
		s              Session
		since, created int64
		ended          sql.NullInt64
	)
	if err := r.Scan(&s.ID, &s.TaskID, &s.AgentType, &s.Origin, &s.Identity.Socket, &s.Identity.ServerPID,
		&s.Identity.SessionID, &s.SessionName, &s.Identity.PaneID, &s.Identity.PanePID, &s.AgentSessionID,
		&s.State, &s.Confidence, &since, &s.LastDetail, &created, &ended); err != nil {
		return Session{}, err
	}
	s.StateSince, s.CreatedAt, s.EndedAt = fromMS(since), fromMS(created), fromNullMS(ended)
	return s, nil
}

// Create inserts a session and its events atomically.
func (ss *Sessions) Create(ctx context.Context, ns NewSession) (Session, error) {
	switch ns.State {
	case domain.StateStarting, domain.StateUnknown, domain.StateFailed:
	default:
		return Session{}, fmt.Errorf("store: create session for %s: invalid initial state %q", ns.TaskID, ns.State)
	}
	if !ns.Origin.Valid() || !ns.Confidence.Valid() || ns.AgentType == "" {
		return Session{}, fmt.Errorf("store: create session for %s: origin, confidence and agent type are required", ns.TaskID)
	}
	now := ss.s.now()
	sess := Session{
		TaskID: ns.TaskID, AgentType: ns.AgentType, Origin: ns.Origin, Identity: ns.Identity,
		SessionName: ns.SessionName, State: ns.State, Confidence: ns.Confidence,
		StateSince: now, LastDetail: cleanDetail(ns.Detail), CreatedAt: now,
	}
	if ns.State.IsTerminal() {
		sess.EndedAt = now
	}
	err := ss.s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO sessions (task_id, agent_type, origin, tmux_socket, tmux_server_pid,
			tmux_session_id, tmux_session_name, tmux_pane_id, pane_pid, agent_state, state_confidence, state_since,
			last_detail, created_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sess.TaskID, sess.AgentType, sess.Origin, sess.Identity.Socket, sess.Identity.ServerPID,
			sess.Identity.SessionID, sess.SessionName, sess.Identity.PaneID, sess.Identity.PanePID,
			sess.State, sess.Confidence, ms(now), sess.LastDetail, ms(now), nullMS(sess.EndedAt))
		if err != nil {
			return fmt.Errorf("store: create session for %s: %w", ns.TaskID, err)
		}
		if sess.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, ev := range ns.Events {
			if ev.TaskID == "" {
				ev.TaskID = sess.TaskID
			}
			if ev.SessionID == 0 {
				ev.SessionID = sess.ID
			}
			if _, err := appendEvent(ctx, tx, now, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

// Get returns one session. ErrNotFound if absent.
func (ss *Sessions) Get(ctx context.Context, id int64) (Session, error) {
	return getSession(ctx, ss.s.db, id)
}

func getSession(ctx context.Context, q querier, id int64) (Session, error) {
	sess, err := scanSession(q.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("store: session %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: session %d: %w", id, err)
	}
	return sess, nil
}

// LatestForTask returns the newest session of a task. ErrNotFound if none.
func (ss *Sessions) LatestForTask(ctx context.Context, taskID string) (Session, error) {
	sess, err := scanSession(ss.s.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE task_id = ? ORDER BY id DESC LIMIT 1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("store: session for task %s: %w", taskID, ErrNotFound)
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: session for task %s: %w", taskID, err)
	}
	return sess, nil
}

// ListActive returns non-terminal sessions of running tasks, oldest first:
// the set reconcile probes and hook identity matching searches.
func (ss *Sessions) ListActive(ctx context.Context) ([]Session, error) {
	rows, err := ss.s.db.QueryContext(ctx, `SELECT `+prefixed("s.", sessionCols)+` FROM sessions s
		JOIN tasks t ON t.id = s.task_id
		WHERE t.state = 'running' AND s.agent_state NOT IN ('stopped', 'failed', 'disconnected')
		ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("store: active sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: active sessions: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Transition is the only way agent_state changes. Inside one BEGIN IMMEDIATE
// transaction it reads the current state, checks domain.CanTransition, does a
// conditional UPDATE guarded by the old state, and appends
// agent.state_changed{from,to,source,confidence,detail}. A disallowed move
// returns ErrTransitionNotAllowed and changes nothing. An allowed self move
// refreshes confidence and detail but keeps state_since and appends nothing.
// detail is sanitized and truncated to MaxDetailLen.
func (ss *Sessions) Transition(ctx context.Context, id int64, to domain.AgentState, conf domain.Confidence, detail, source string) (TransitionResult, error) {
	var res TransitionResult
	err := ss.s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		res, err = ss.transitionTx(ctx, tx, transitionInput{
			SessionID: id, To: to, Confidence: conf, Detail: detail, Source: source,
		})
		return err
	})
	return res, err
}

func (ss *Sessions) transitionTx(ctx context.Context, tx *sql.Tx, in transitionInput) (TransitionResult, error) {
	if !in.To.Valid() || !in.Confidence.Valid() {
		return TransitionResult{}, fmt.Errorf("store: transition session %d: invalid state %q / confidence %q", in.SessionID, in.To, in.Confidence)
	}
	cur, err := getSession(ctx, tx, in.SessionID)
	if err != nil {
		return TransitionResult{}, err
	}
	res := TransitionResult{From: cur.State, To: in.To}
	if !domain.CanTransition(cur.State, in.To) {
		return res, fmt.Errorf("store: session %d %s -> %s: %w", in.SessionID, cur.State, in.To, ErrTransitionNotAllowed)
	}
	detail := cleanDetail(in.Detail)
	now := ss.s.now()
	if cur.State == in.To {
		if cur.State.IsTerminal() {
			return res, nil
		}
		if detail == "" {
			detail = cur.LastDetail
		}
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET state_confidence = ?, last_detail = ? WHERE id = ? AND agent_state = ?`,
			in.Confidence, detail, in.SessionID, cur.State)
		if err != nil {
			return res, fmt.Errorf("store: transition session %d: %w", in.SessionID, err)
		}
		return res, nil
	}
	ended := any(nil)
	if in.To.IsTerminal() {
		ended = ms(now)
	}
	r, err := tx.ExecContext(ctx, `UPDATE sessions SET agent_state = ?, state_confidence = ?, state_since = ?,
		last_detail = ?, ended_at = COALESCE(ended_at, ?) WHERE id = ? AND agent_state = ?`,
		in.To, in.Confidence, ms(now), detail, ended, in.SessionID, cur.State)
	if err != nil {
		return res, fmt.Errorf("store: transition session %d: %w", in.SessionID, err)
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return res, fmt.Errorf("store: transition session %d: state changed concurrently (%d rows, %v)", in.SessionID, n, err)
	}
	payload := map[string]any{}
	for k, v := range in.Extra {
		payload[k] = v
	}
	payload["from"], payload["to"], payload["source"], payload["confidence"] = string(cur.State), string(in.To), in.Source, string(in.Confidence)
	if detail != "" {
		payload["detail"] = detail
	}
	res.EventID, err = appendEvent(ctx, tx, now, EventInput{
		Type: domain.EventAgentStateChanged, TaskID: cur.TaskID, SessionID: cur.ID, CauseID: in.CauseID, Payload: payload,
	})
	if err != nil {
		return res, err
	}
	res.Changed = true
	return res, nil
}

// cleanDetail sanitizes terminal control sequences and bounds the length on a
// rune boundary.
func cleanDetail(s string) string {
	s = domain.SanitizeTerminalText(s)
	if len(s) <= MaxDetailLen {
		return s
	}
	cut := MaxDetailLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(alias, cols string) string {
	out := make([]byte, 0, len(cols)+64)
	start := true
	for i := 0; i < len(cols); i++ {
		c := cols[i]
		if start && c != ' ' && c != '\n' && c != '\t' {
			out = append(out, alias...)
			start = false
		}
		out = append(out, c)
		if c == ',' {
			start = true
		}
	}
	return string(out)
}
