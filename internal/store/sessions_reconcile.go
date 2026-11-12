package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jffypak/agentctl/internal/domain"
)

// This file (m1-pane-identity) holds the identity side of the sessions
// repository: reconcile writes (pane / timeout verdicts), pane-identity
// lookups, and the atomic registration of an adopted pane.

// ErrPaneRegistered: the pane identity is already bound to a session.
var ErrPaneRegistered = errors.New("pane already registered")

// ReconcileInput is one reconcile verdict for a session.
type ReconcileInput struct {
	SessionID  int64
	From       domain.AgentState // optional guard: apply only while the session is still in From
	To         domain.AgentState
	Confidence domain.Confidence
	Detail     string
	Source     string                  // domain.SourcePane or domain.SourceTimeout
	Reason     domain.DisconnectReason // required when To is disconnected
}

// ReconcileTransition applies a reconcile verdict through the same guarded
// transition as Sessions.Transition, in one BEGIN IMMEDIATE transaction. It
// is idempotent and race-tolerant, so concurrent reconcilers (TUI tick, `ls`)
// never duplicate events:
//
//   - a session already in To, or already terminal, is left alone (no error);
//   - a session no longer in From (a hook moved it meanwhile) is left alone;
//   - entering disconnected first appends session.disconnected{reason, from},
//     which becomes the cause_id of the agent.state_changed event.
func (ss *Sessions) ReconcileTransition(ctx context.Context, in ReconcileInput) (TransitionResult, error) {
	if !in.To.Valid() || !in.Confidence.Valid() {
		return TransitionResult{}, fmt.Errorf("store: reconcile session %d: invalid state %q / confidence %q", in.SessionID, in.To, in.Confidence)
	}
	if in.To == domain.StateDisconnected && in.Reason == "" {
		return TransitionResult{}, fmt.Errorf("store: reconcile session %d: disconnect needs a reason", in.SessionID)
	}
	var res TransitionResult
	err := ss.s.withTx(ctx, func(tx *sql.Tx) error {
		cur, err := getSession(ctx, tx, in.SessionID)
		if err != nil {
			return err
		}
		res = TransitionResult{From: cur.State, To: in.To}
		if cur.State == in.To || cur.State.IsTerminal() || (in.From != "" && cur.State != in.From) {
			return nil
		}
		var cause int64
		extra := map[string]any{}
		if in.To == domain.StateDisconnected {
			extra["reason"] = string(in.Reason)
			cause, err = appendEvent(ctx, tx, ss.s.now(), EventInput{
				Type: domain.EventSessionDisconnected, TaskID: cur.TaskID, SessionID: cur.ID,
				Payload: map[string]any{"reason": string(in.Reason), "from": string(cur.State)},
			})
			if err != nil {
				return err
			}
		}
		res, err = ss.transitionTx(ctx, tx, transitionInput{
			SessionID: in.SessionID, To: in.To, Confidence: in.Confidence, Detail: in.Detail,
			Source: in.Source, CauseID: cause, Extra: extra,
		})
		return err
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

const identityWhere = `tmux_socket = ? AND tmux_server_pid = ? AND tmux_pane_id = ? AND pane_pid = ?`

// PaneOwner returns the newest session bound to exactly this pane identity
// (socket, server pid, pane id, pane pid), in any state. A reused %N on a
// restarted server has a different server pid and pane pid, so it is never
// matched to the old session. ErrNotFound if none.
func (ss *Sessions) PaneOwner(ctx context.Context, id domain.Identity) (Session, error) {
	return paneOwner(ctx, ss.s.db, id)
}

func paneOwner(ctx context.Context, q querier, id domain.Identity) (Session, error) {
	sess, err := scanSession(q.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE `+identityWhere+`
		ORDER BY id DESC LIMIT 1`, id.Socket, id.ServerPID, id.PaneID, id.PanePID))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("store: pane %s on %s: %w", id.PaneID, id.Socket, ErrNotFound)
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: pane %s on %s: %w", id.PaneID, id.Socket, err)
	}
	return sess, nil
}

// OnSocket returns every session (any state, any task state) recorded on a
// tmux socket, oldest first. Adopt uses it to hide registered panes.
func (ss *Sessions) OnSocket(ctx context.Context, socket string) ([]Session, error) {
	rows, err := ss.s.db.QueryContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE tmux_socket = ? ORDER BY id`, socket)
	if err != nil {
		return nil, fmt.Errorf("store: sessions on %s: %w", socket, err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sessions on %s: %w", socket, err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// RegisterAdopted inserts a running task and its adopted session in one
// transaction, so two concurrent `adopt` calls can never bind one pane to two
// tasks and a failure leaves no partial row. It fails with ErrPaneRegistered
// when the identity already has a session and ErrTaskExists when the name is
// taken. ns.Events are appended in the same transaction (task/session ids
// filled in when zero).
func (ss *Sessions) RegisterAdopted(ctx context.Context, task Task, ns NewSession) (Task, Session, error) {
	if err := domain.ValidateSlug(task.ID); err != nil {
		return Task{}, Session{}, err
	}
	id := ns.Identity
	if id.IsZero() || id.Socket == "" || id.ServerPID <= 0 || id.PanePID <= 0 {
		return Task{}, Session{}, fmt.Errorf("store: adopt %s: a full pane identity is required", task.ID)
	}
	if task.WorktreePath == "" || task.RepoID == 0 || ns.AgentType == "" || ns.TaskID != task.ID ||
		!registrableOrigin(ns.Origin, ns.State) || !ns.Confidence.Valid() {
		return Task{}, Session{}, fmt.Errorf("store: adopt %s: invalid task or session", task.ID)
	}
	if task.Name == "" {
		task.Name = task.ID
	}
	now := ss.s.now()
	task.State, task.CreatedAt, task.UpdatedAt = domain.TaskRunning, now, now
	sess := Session{
		TaskID: task.ID, AgentType: ns.AgentType, Origin: ns.Origin, Identity: id, SessionName: ns.SessionName,
		State: ns.State, Confidence: ns.Confidence, StateSince: now, LastDetail: cleanDetail(ns.Detail), CreatedAt: now,
	}
	err := ss.s.withTx(ctx, func(tx *sql.Tx) error {
		if owner, err := paneOwner(ctx, tx, id); err == nil {
			return fmt.Errorf("store: pane %s is registered to task %s: %w", id.PaneID, owner.TaskID, ErrPaneRegistered)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = ?`, task.ID).Scan(&n); err != nil {
			return fmt.Errorf("store: adopt %s: %w", task.ID, err)
		}
		if n > 0 {
			return fmt.Errorf("store: task %s: %w", task.ID, ErrTaskExists)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (`+taskCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			task.ID, task.RepoID, task.Name, task.Goal, task.State, task.WorktreePath, task.Branch,
			task.BaseRef, task.BaseSHA, ms(now), ms(now)); err != nil {
			return fmt.Errorf("store: adopt %s: insert task: %w", task.ID, err)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO sessions (task_id, agent_type, origin, tmux_socket, tmux_server_pid,
			tmux_session_id, tmux_session_name, tmux_pane_id, pane_pid, agent_state, state_confidence, state_since,
			last_detail, created_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			sess.TaskID, sess.AgentType, sess.Origin, id.Socket, id.ServerPID, id.SessionID, sess.SessionName,
			id.PaneID, id.PanePID, sess.State, sess.Confidence, ms(now), sess.LastDetail, ms(now))
		if err != nil {
			return fmt.Errorf("store: adopt %s: insert session: %w", task.ID, err)
		}
		if sess.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, ev := range ns.Events {
			if ev.TaskID == "" {
				ev.TaskID = task.ID
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
		return Task{}, Session{}, err
	}
	return task, sess, nil
}

// registrableOrigin is the origin/initial-state pair RegisterAdopted accepts:
// an adopted pane starts unknown (no hooks yet); a wrapped pane (`agentctl
// codex`) starts starting, like a launch, so its startup screens are checked.
func registrableOrigin(o domain.Origin, st domain.AgentState) bool {
	return o == domain.OriginAdopted && st == domain.StateUnknown ||
		o == domain.OriginWrapped && st == domain.StateStarting
}
