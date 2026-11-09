package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
)

// This file (m2-prompt-queue) is the messages repository. Every change of
// messages.state is a conditional UPDATE here, guarded by the expected old
// state(s) and domain.CanTransitionMessage, and appends its message.* event
// in the same BEGIN IMMEDIATE transaction (ARCHITECTURE.md §Code conventions
// 4). Message bodies never appear in event payloads.

// ErrMessageState: the message is not in a state the write applies to (it
// was cancelled, claimed, swept, or acknowledged concurrently).
var ErrMessageState = errors.New("message state does not allow this change")

// Message is a prompt queued for one session.
type Message struct {
	ID         int64
	TaskID     string
	SessionID  int64
	Body       string
	BodySHA256 string
	State      domain.MessageState
	AckMatch   domain.AckMatch
	CreatedAt  time.Time
	ClaimedAt  time.Time
	SentAt     time.Time
	AckedAt    time.Time
	Error      string
}

// NewMessage is a prompt to enqueue.
type NewMessage struct {
	TaskID    string
	SessionID int64
	Body      string
	Source    string // "cli" / "tui", recorded in message.queued
}

// Claim is a successful claim: the message (now delivering) and the
// session's state_since at the moment of the claim, which the deliverer
// re-checks before pasting.
type Claim struct {
	Message    Message
	StateSince time.Time
}

// Messages is the messages repository.
type Messages struct{ s *Store }

// Messages returns the messages repository.
func (s *Store) Messages() *Messages { return &Messages{s} }

const messageCols = `id, task_id, session_id, body, body_sha256, state, ack_match, created_at, claimed_at, sent_at, acked_at, error`

func scanMessage(r rowScanner) (Message, error) {
	var (
		m                     Message
		created               int64
		claimed, sent, acked  sql.NullInt64
		state, match, errText string
	)
	if err := r.Scan(&m.ID, &m.TaskID, &m.SessionID, &m.Body, &m.BodySHA256, &state, &match, &created,
		&claimed, &sent, &acked, &errText); err != nil {
		return Message{}, err
	}
	m.State, m.AckMatch, m.Error = domain.MessageState(state), domain.AckMatch(match), errText
	m.CreatedAt, m.ClaimedAt, m.SentAt, m.AckedAt = fromMS(created), fromNullMS(claimed), fromNullMS(sent), fromNullMS(acked)
	return m, nil
}

// BodyHash is the hex sha256 of a body, compared with UserPromptSubmit's
// prompt hash.
func BodyHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

func getMessage(ctx context.Context, q querier, id int64) (Message, error) {
	m, err := scanMessage(q.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, fmt.Errorf("store: message %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Message{}, fmt.Errorf("store: message %d: %w", id, err)
	}
	return m, nil
}

// Get returns one message. ErrNotFound if absent.
func (mr *Messages) Get(ctx context.Context, id int64) (Message, error) {
	return getMessage(ctx, mr.s.db, id)
}

// Enqueue validates the body (domain.ValidateMessageBody) and inserts it as
// queued with message.queued{message_id, bytes, body_sha256, source} in one
// transaction. The session must belong to the task.
func (mr *Messages) Enqueue(ctx context.Context, in NewMessage) (Message, error) {
	if err := domain.ValidateMessageBody(in.Body); err != nil {
		return Message{}, err
	}
	m := Message{TaskID: in.TaskID, SessionID: in.SessionID, Body: in.Body, BodySHA256: BodyHash(in.Body), State: domain.MsgQueued}
	err := mr.s.withTx(ctx, func(tx *sql.Tx) error {
		sess, err := getSession(ctx, tx, in.SessionID)
		if err != nil {
			return err
		}
		if sess.TaskID != in.TaskID {
			return fmt.Errorf("store: enqueue: session %d belongs to task %s, not %s", sess.ID, sess.TaskID, in.TaskID)
		}
		now := mr.s.now()
		m.CreatedAt = fromMS(ms(now))
		res, err := tx.ExecContext(ctx, `INSERT INTO messages (task_id, session_id, body, body_sha256, state, created_at)
			VALUES (?, ?, ?, ?, 'queued', ?)`, m.TaskID, m.SessionID, m.Body, m.BodySHA256, ms(now))
		if err != nil {
			return fmt.Errorf("store: enqueue for %s: %w", in.TaskID, err)
		}
		if m.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		payload := map[string]any{"message_id": m.ID, "bytes": len(m.Body), "body_sha256": m.BodySHA256}
		if in.Source != "" {
			payload["source"] = in.Source
		}
		_, err = appendEvent(ctx, tx, now, EventInput{Type: domain.EventMessageQueued, TaskID: m.TaskID, SessionID: m.SessionID, Payload: payload})
		return err
	})
	if err != nil {
		return Message{}, err
	}
	return m, nil
}

// ForTask returns a task's newest messages (up to limit, <= 0 means 50),
// oldest first.
func (mr *Messages) ForTask(ctx context.Context, taskID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	return mr.list(ctx, `SELECT `+messageCols+` FROM (SELECT * FROM messages WHERE task_id = ? ORDER BY id DESC LIMIT ?) ORDER BY id`, taskID, limit)
}

// ForSession returns every message of a session, oldest first.
func (mr *Messages) ForSession(ctx context.Context, sessionID int64) ([]Message, error) {
	return mr.list(ctx, `SELECT `+messageCols+` FROM messages WHERE session_id = ? ORDER BY id`, sessionID)
}

func (mr *Messages) list(ctx context.Context, q string, args ...any) ([]Message, error) {
	rows, err := mr.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: messages: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// QueuedSessions returns the sessions with at least one queued message
// (the TUI tick drains each of them).
func (mr *Messages) QueuedSessions(ctx context.Context) ([]int64, error) {
	rows, err := mr.s.db.QueryContext(ctx, `SELECT DISTINCT session_id FROM messages WHERE state = 'queued' ORDER BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("store: queued sessions: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// HasQueued reports whether a session has a queued message.
func (mr *Messages) HasQueued(ctx context.Context, sessionID int64) (bool, error) {
	var n int
	err := mr.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id = ? AND state = 'queued'`, sessionID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: queued messages of session %d: %w", sessionID, err)
	}
	return n > 0, nil
}

// claimable is Delivery rule 1's condition on a message row of session ?1: it is
// the oldest queued message, the session is ready with hook confidence, no
// message of the session is delivering, and none was sent during the current
// ready period (sent_at >= state_since): one delivery per ready transition,
// however many drainers race. Every parameter is the session id.
const claimable = `id = (SELECT id FROM messages WHERE session_id = ?1 AND state = 'queued' ORDER BY id LIMIT 1)
	  AND state = 'queued'
	  AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = ?1 AND s.agent_state = 'ready' AND s.state_confidence = 'hook')
	  AND NOT EXISTS (SELECT 1 FROM messages o WHERE o.session_id = ?1
	        AND (o.state = 'delivering' OR o.sent_at >= (SELECT state_since FROM sessions WHERE id = ?1)))`

// claimSQL claims with one conditional UPDATE (run inside BEGIN IMMEDIATE).
const claimSQL = `UPDATE messages SET state = 'delivering', claimed_at = ?2 WHERE ` + claimable + ` RETURNING id`

// Claimable reports, with a read only, whether Claim could claim a message
// of the session now. Drain asks it first so a drain that cannot deliver
// never takes the write lock (it competes with hooks, which have a 1.5 s
// budget). Claim still decides atomically.
func (mr *Messages) Claimable(ctx context.Context, sessionID int64) (bool, error) {
	var ok bool
	err := mr.s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE `+claimable+`)`, sessionID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: claimable for session %d: %w", sessionID, err)
	}
	return ok, nil
}

// IsBusy reports whether err is SQLITE_BUSY: the write lock was not free
// within the Store's busy timeout. Nothing was written.
func IsBusy(err error) bool { return isBusy(err) }

// Claim atomically claims the session's next message (see claimSQL) and
// appends message.claimed{message_id} with causeID. ok is false when nothing
// was claimable.
func (mr *Messages) Claim(ctx context.Context, sessionID, causeID int64) (Claim, bool, error) {
	var (
		c  Claim
		ok bool
	)
	err := mr.s.withTx(ctx, func(tx *sql.Tx) error {
		now := mr.s.now()
		var id int64
		err := tx.QueryRowContext(ctx, claimSQL, sessionID, ms(now)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: claim for session %d: %w", sessionID, err)
		}
		sess, err := getSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if c.Message, err = getMessage(ctx, tx, id); err != nil {
			return err
		}
		c.StateSince, ok = sess.StateSince, true
		_, err = appendEvent(ctx, tx, now, EventInput{Type: domain.EventMessageClaimed, TaskID: c.Message.TaskID,
			SessionID: sessionID, CauseID: causeID, Payload: map[string]any{"message_id": id}})
		return err
	})
	if err != nil {
		return Claim{}, false, err
	}
	return c, ok, nil
}

// change is one guarded message state change.
type change struct {
	id      int64
	from    []domain.MessageState
	to      domain.MessageState
	set     string // extra "col = ?, " assignments
	args    []any
	event   string // "" = no event
	cause   int64
	payload map[string]any
}

// apply runs c inside tx: the UPDATE matches only while the message is in
// one of c.from, so a concurrent change makes it a no-op (changed false).
func (mr *Messages) apply(ctx context.Context, tx *sql.Tx, c change) (Message, bool, error) {
	cur, err := getMessage(ctx, tx, c.id)
	if err != nil {
		return Message{}, false, err
	}
	allowed := false
	for _, f := range c.from {
		allowed = allowed || (cur.State == f && domain.CanTransitionMessage(f, c.to))
	}
	if !allowed {
		return cur, false, nil
	}
	args := append(append(append([]any(nil), c.args...), c.to), c.id, cur.State)
	r, err := tx.ExecContext(ctx, `UPDATE messages SET `+c.set+`state = ? WHERE id = ? AND state = ?`, args...)
	if err != nil {
		return cur, false, fmt.Errorf("store: message %d %s -> %s: %w", c.id, cur.State, c.to, err)
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return cur, false, err
	}
	if c.event != "" {
		p := map[string]any{"message_id": c.id, "from": string(cur.State)}
		for k, v := range c.payload {
			p[k] = v
		}
		if _, err := appendEvent(ctx, tx, mr.s.now(), EventInput{Type: c.event, TaskID: cur.TaskID, SessionID: cur.SessionID, CauseID: c.cause, Payload: p}); err != nil {
			return cur, false, err
		}
	}
	next, err := getMessage(ctx, tx, c.id)
	return next, err == nil, err
}

// one runs a single change in its own transaction.
func (mr *Messages) one(ctx context.Context, c change) (Message, bool, error) {
	var (
		m  Message
		ok bool
	)
	err := mr.s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		m, ok, err = mr.apply(ctx, tx, c)
		return err
	})
	return m, ok, err
}

// Revert returns a claimed message to the queue (delivering -> queued) when
// the re-check before pasting fails; nothing was pasted. No event: the
// message is simply queued again. ErrMessageState if it is not delivering.
func (mr *Messages) Revert(ctx context.Context, id int64) error {
	_, ok, err := mr.one(ctx, change{id: id, from: []domain.MessageState{domain.MsgDelivering}, to: domain.MsgQueued})
	if err == nil && !ok {
		err = fmt.Errorf("store: revert message %d: %w", id, ErrMessageState)
	}
	return err
}

// MarkSent records a completed paste (delivering -> sent) and appends
// message.sent. ErrMessageState if the message is no longer delivering
// (e.g. swept to unconfirmed): it must then never be retried.
func (mr *Messages) MarkSent(ctx context.Context, id int64, at time.Time, causeID int64) (Message, error) {
	m, ok, err := mr.one(ctx, change{id: id, from: []domain.MessageState{domain.MsgDelivering}, to: domain.MsgSent,
		set: "sent_at = ?, ", args: []any{ms(at)}, event: domain.EventMessageSent, cause: causeID})
	if err == nil && !ok {
		err = fmt.Errorf("store: mark message %d sent: it is %s: %w", id, m.State, ErrMessageState)
	}
	return m, err
}

// Acknowledge marks a sent (or unconfirmed: a late hash match) message
// acknowledged and appends message.acknowledged{ack_match}. ok is false when
// it was not in such a state.
func (mr *Messages) Acknowledge(ctx context.Context, id int64, match domain.AckMatch, causeID int64) (bool, error) {
	if !match.Valid() {
		return false, fmt.Errorf("store: acknowledge message %d: invalid ack match %q", id, match)
	}
	var ok bool
	err := mr.s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		ok, err = mr.ackTx(ctx, tx, id, match, causeID)
		return err
	})
	return ok, err
}

func (mr *Messages) ackTx(ctx context.Context, tx *sql.Tx, id int64, match domain.AckMatch, causeID int64) (bool, error) {
	from := []domain.MessageState{domain.MsgSent}
	if match == domain.AckHash {
		from = append(from, domain.MsgUnconfirmed)
	}
	_, ok, err := mr.apply(ctx, tx, change{id: id, from: from, to: domain.MsgAcknowledged,
		set: "ack_match = ?, acked_at = ?, ", args: []any{string(match), ms(mr.s.now())},
		event: domain.EventMessageAcknowledged, cause: causeID, payload: map[string]any{"ack_match": string(match)}})
	return ok, err
}

// AckFromHook applies one UserPromptSubmit to a session's pasted messages
// (domain.MatchAck: hash first, else timing within window) in a single
// transaction and returns the decision (MessageID 0: nothing matched).
func (mr *Messages) AckFromHook(ctx context.Context, sessionID int64, promptSHA string, at time.Time, window time.Duration, causeID int64) (domain.AckDecision, error) {
	var d domain.AckDecision
	err := mr.s.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+messageCols+` FROM messages
			WHERE session_id = ? AND state IN ('sent', 'unconfirmed') ORDER BY id`, sessionID)
		if err != nil {
			return fmt.Errorf("store: ack candidates of session %d: %w", sessionID, err)
		}
		var cands []domain.AckCandidate
		for rows.Next() {
			m, err := scanMessage(rows)
			if err != nil {
				rows.Close()
				return err
			}
			cands = append(cands, domain.AckCandidate{ID: m.ID, State: m.State, BodySHA256: m.BodySHA256, ClaimedAt: m.ClaimedAt, SentAt: m.SentAt})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		d = domain.MatchAck(domain.AckInput{PromptSHA256: promptSHA, At: at, Window: window, Candidates: cands})
		if d.MessageID == 0 {
			return nil
		}
		ok, err := mr.ackTx(ctx, tx, d.MessageID, d.Match, causeID)
		if err == nil && !ok {
			d = domain.AckDecision{}
		}
		return err
	})
	if err != nil {
		return domain.AckDecision{}, err
	}
	return d, nil
}

// MarkUnconfirmed marks a delivering or sent message unconfirmed (no ack in
// time, or delivery interrupted) and appends message.unconfirmed{reason}. It
// is never re-queued (invariant 4). ok is false when it was not in such a state.
func (mr *Messages) MarkUnconfirmed(ctx context.Context, id int64, reason string, causeID int64) (bool, error) {
	_, ok, err := mr.one(ctx, change{id: id, from: []domain.MessageState{domain.MsgDelivering, domain.MsgSent}, to: domain.MsgUnconfirmed,
		set: "error = ?, ", args: []any{cleanDetail(reason)}, event: domain.EventMessageUnconfirmed, cause: causeID,
		payload: map[string]any{"reason": cleanDetail(reason)}})
	return ok, err
}

// Cancel cancels a queued message (only queued: one being delivered or
// already pasted cannot be taken back) and appends message.cancelled.
// ErrNotFound / ErrMessageState otherwise.
func (mr *Messages) Cancel(ctx context.Context, id int64) (Message, error) {
	m, ok, err := mr.one(ctx, change{id: id, from: []domain.MessageState{domain.MsgQueued}, to: domain.MsgCancelled,
		event: domain.EventMessageCancelled})
	if err == nil && !ok {
		err = fmt.Errorf("store: cancel message %d: it is %s, only queued messages can be cancelled: %w", id, m.State, ErrMessageState)
	}
	return m, err
}

// SweepStale marks unconfirmed every message of the session (0: every
// session) that has been delivering, or sent without an ack, for longer than
// window: a deliverer that crashed or never saw an ack (invariant 4). Each
// gets message.unconfirmed with cause_id = its own claimed / sent event.
func (mr *Messages) SweepStale(ctx context.Context, sessionID int64, window time.Duration) ([]int64, error) {
	// read first: the write lock is taken only when there is something to
	// sweep, so routine drains never queue behind (or ahead of) hook writers
	var stale bool
	if err := mr.s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE (?1 = 0 OR session_id = ?1)
		AND ((state = 'delivering' AND claimed_at <= ?2) OR (state = 'sent' AND sent_at <= ?2)))`,
		sessionID, ms(mr.s.now().Add(-window))).Scan(&stale); err != nil {
		return nil, fmt.Errorf("store: sweep: %w", err)
	}
	if !stale {
		return nil, nil
	}
	var swept []int64
	err := mr.s.withTx(ctx, func(tx *sql.Tx) error {
		cutoff := ms(mr.s.now().Add(-window))
		rows, err := tx.QueryContext(ctx, `SELECT id, state FROM messages
			WHERE (? = 0 OR session_id = ?)
			  AND ((state = 'delivering' AND claimed_at <= ?) OR (state = 'sent' AND sent_at <= ?))
			ORDER BY id`, sessionID, sessionID, cutoff, cutoff)
		if err != nil {
			return fmt.Errorf("store: sweep: %w", err)
		}
		type stale struct {
			id    int64
			state string
		}
		var found []stale
		for rows.Next() {
			var s stale
			if err := rows.Scan(&s.id, &s.state); err != nil {
				rows.Close()
				return err
			}
			found = append(found, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, s := range found {
			reason, evType := "no UserPromptSubmit within "+window.String()+" of the paste", domain.EventMessageSent
			if s.state == string(domain.MsgDelivering) {
				reason, evType = "delivery interrupted (claimed, never marked sent)", domain.EventMessageClaimed
			}
			cause, err := messageEventID(ctx, tx, evType, s.id)
			if err != nil {
				return err
			}
			_, ok, err := mr.apply(ctx, tx, change{id: s.id, from: []domain.MessageState{domain.MsgDelivering, domain.MsgSent},
				to: domain.MsgUnconfirmed, set: "error = ?, ", args: []any{reason}, event: domain.EventMessageUnconfirmed,
				cause: cause, payload: map[string]any{"reason": reason}})
			if err != nil {
				return err
			}
			if ok {
				swept = append(swept, s.id)
			}
		}
		return nil
	})
	return swept, err
}

// MessageEventID is the newest event of type typ about message id (0 if
// none), used as the cause_id of a later event in the same chain.
func (mr *Messages) MessageEventID(ctx context.Context, typ string, id int64) (int64, error) {
	return messageEventID(ctx, mr.s.db, typ, id)
}

func messageEventID(ctx context.Context, q querier, typ string, id int64) (int64, error) {
	var ev int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM events WHERE type = ? AND json_extract(payload, '$.message_id') = ?`,
		typ, id).Scan(&ev)
	if err != nil {
		return 0, fmt.Errorf("store: %s event of message %d: %w", typ, id, err)
	}
	return ev, nil
}

// ReadyCause is the event that made the session ready most recently: the
// hook.received cause of its newest agent.state_changed{to: ready} (or that
// event itself when it has no cause). 0 when the session never became ready.
// Drain uses it as the cause_id of message.claimed / message.sent.
func (mr *Messages) ReadyCause(ctx context.Context, sessionID int64) (int64, error) {
	var id int64
	err := mr.s.db.QueryRowContext(ctx, `SELECT COALESCE(cause_id, id) FROM events
		WHERE session_id = ? AND type = ? AND json_extract(payload, '$.to') = 'ready'
		ORDER BY id DESC LIMIT 1`, sessionID, domain.EventAgentStateChanged).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: ready cause of session %d: %w", sessionID, err)
	}
	return id, nil
}

// Submit is one recorded UserPromptSubmit hook.
type Submit struct {
	EventID      int64
	TS           time.Time
	PromptSHA256 string
}

// SubmitsSince returns the session's UserPromptSubmit hook.received events
// with id > afterID, oldest first. The deliverer acks from them when the
// submit's own hook raced the paste's MarkSent.
func (mr *Messages) SubmitsSince(ctx context.Context, sessionID, afterID int64) ([]Submit, error) {
	rows, err := mr.s.db.QueryContext(ctx, `SELECT id, ts, COALESCE(json_extract(payload, '$.prompt_sha256'), '') FROM events
		WHERE session_id = ? AND id > ? AND type = ? AND json_extract(payload, '$.event') = 'UserPromptSubmit'
		ORDER BY id`, sessionID, afterID, domain.EventHookReceived)
	if err != nil {
		return nil, fmt.Errorf("store: submits of session %d: %w", sessionID, err)
	}
	defer rows.Close()
	var out []Submit
	for rows.Next() {
		var (
			s  Submit
			ts int64
		)
		if err := rows.Scan(&s.EventID, &ts, &s.PromptSHA256); err != nil {
			return nil, err
		}
		s.TS = fromMS(ts)
		out = append(out, s)
	}
	return out, rows.Err()
}
