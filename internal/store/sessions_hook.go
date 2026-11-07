package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jffypak/agentctl/internal/domain"
)

// HookInput is one Claude Code hook observation to record against a session
// that hookingest has already resolved and identity-checked.
type HookInput struct {
	SessionID      int64
	Event          string            // hook_event_name
	AgentSessionID string            // Claude session_id; "" keeps the stored value
	State          domain.AgentState // "" = record only, no state change
	Detail         string            // sanitized and bounded like every last_detail
	Payload        map[string]any    // extra hook.received fields (never prompt text)
}

// HookResult reports what RecordHook did.
type HookResult struct {
	Session    Session          // the session after the write
	ReceivedID int64            // id of the hook.received event (the cause of any state change)
	Transition TransitionResult // zero when State == "" or the move was refused
	Applied    bool             // the state write happened (a change or an allowed self move)
	Refused    bool             // domain.CanTransition said no (e.g. sticky stopped); nothing changed
}

// RecordHook appends hook.received{event, ...Payload} and, when in.State is
// set, transitions the session to it with confidence hook, source hook and
// cause_id = the hook.received id — all in one BEGIN IMMEDIATE transaction, so
// the hook process takes the write lock once. It also records the Claude
// session_id in agent_session_id (this file is its only writer). A refused
// transition (terminal states are sticky) still records the hook and is not
// an error.
func (ss *Sessions) RecordHook(ctx context.Context, in HookInput) (HookResult, error) {
	if in.Event == "" {
		return HookResult{}, errors.New("store: record hook: empty event name")
	}
	var res HookResult
	err := ss.s.withTx(ctx, func(tx *sql.Tx) error {
		cur, err := getSession(ctx, tx, in.SessionID)
		if err != nil {
			return err
		}
		now := ss.s.now()
		payload := make(map[string]any, len(in.Payload)+1)
		for k, v := range in.Payload {
			payload[k] = v
		}
		payload["event"] = in.Event
		res.ReceivedID, err = appendEvent(ctx, tx, now, EventInput{
			Type: domain.EventHookReceived, TaskID: cur.TaskID, SessionID: cur.ID, Payload: payload,
		})
		if err != nil {
			return err
		}
		if in.AgentSessionID != "" && in.AgentSessionID != cur.AgentSessionID && !cur.State.IsTerminal() {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET agent_session_id = ? WHERE id = ?`, in.AgentSessionID, cur.ID); err != nil {
				return fmt.Errorf("store: session %d agent_session_id: %w", cur.ID, err)
			}
		}
		if in.State != "" {
			tr, err := ss.transitionTx(ctx, tx, transitionInput{
				SessionID: cur.ID, To: in.State, Confidence: domain.ConfidenceHook, Detail: in.Detail,
				Source: domain.SourceHook, CauseID: res.ReceivedID, Extra: map[string]any{"hook_event": in.Event},
			})
			switch {
			case errors.Is(err, ErrTransitionNotAllowed):
				res.Refused = true
			case err != nil:
				return err
			default:
				res.Transition, res.Applied = tr, !cur.State.IsTerminal()
			}
		}
		res.Session, err = getSession(ctx, tx, cur.ID)
		return err
	})
	if err != nil {
		return HookResult{}, err
	}
	return res, nil
}

// ActiveByPane returns the non-terminal sessions of running tasks bound to a
// tmux pane id, oldest first. Hook ingestion uses it to match a hook from an
// adopted pane (no AGENTCTL_TASK_ID) by identity; the caller still compares
// socket and server pid, because %N is reused across tmux servers.
func (ss *Sessions) ActiveByPane(ctx context.Context, paneID string) ([]Session, error) {
	rows, err := ss.s.db.QueryContext(ctx, `SELECT `+prefixed("s.", sessionCols)+` FROM sessions s
		JOIN tasks t ON t.id = s.task_id
		WHERE s.tmux_pane_id = ? AND t.state = 'running'
		  AND s.agent_state NOT IN ('stopped', 'failed', 'disconnected')
		ORDER BY s.id`, paneID)
	if err != nil {
		return nil, fmt.Errorf("store: sessions on pane %s: %w", paneID, err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sessions on pane %s: %w", paneID, err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}
