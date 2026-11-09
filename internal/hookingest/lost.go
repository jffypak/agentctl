package hookingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
)

// Lost-hook safety (ARCHITECTURE.md §Lost-hook safety). A hook that cannot
// record its event (SQLITE_BUSY past its budget, the budget itself, a broken
// registry) may have lost a UserPromptSubmit: the session would keep reading
// ready while the agent works, and a delivery would paste into it. So the
// hook writes a marker without the DB:
//
//	<home>/hooks/lost/<session-N | task-<id> | pane-N>   (atomic, 0600)
//
// controller.Drain refuses a session whose marker is newer than its
// state_since ("state stale — attach to deliver"), reconcile downgrades it
// to unknown / none, and the next hook recorded for it clears the marker.

// LostDirName is the marker directory under <home>/hooks.
const LostDirName = "lost"

// lostMarker is the marker content (ts_ms is what the controller compares).
type lostMarker struct {
	TSMillis int64  `json:"ts_ms"`
	Event    string `json:"event,omitempty"`
	TaskID   string `json:"task,omitempty"`
	Session  int64  `json:"session,omitempty"`
	Pane     string `json:"pane,omitempty"`
	Error    string `json:"error,omitempty"`
}

// lostKey is the most specific marker name for a hook: its resolved
// session, else its claimed task, else its pane ("" when none is usable).
func lostKey(sessionID int64, env adapter.HookEnv) string {
	if sessionID > 0 {
		return domain.LostHookSessionKey(sessionID)
	}
	if k := domain.LostHookTaskKey(env.TaskID); k != "" {
		return k
	}
	return domain.LostHookPaneKey(env.TMUXPane)
}

// writeLostMarker records that this hook's event was not recorded. It never
// creates the agentctl home (an unregistered machine stays untouched).
func writeLostMarker(p paths.Paths, now time.Time, o Outcome, env adapter.HookEnv, cause error) error {
	key := lostKey(o.SessionID, env)
	if key == "" || p.Home == "" {
		return nil
	}
	if fi, err := os.Stat(p.Home); err != nil || !fi.IsDir() {
		return nil
	}
	dir := filepath.Join(p.Hooks, LostDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("lost-hook marker: %w", err)
	}
	m := lostMarker{TSMillis: now.UnixMilli(), Event: clean(o.Event), TaskID: clean(env.TaskID), Session: o.SessionID, Pane: clean(env.TMUXPane)}
	if cause != nil {
		m.Error = clean(cause.Error())
		if len(m.Error) > store.MaxDetailLen {
			m.Error = m.Error[:store.MaxDetailLen]
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+key+".*.tmp")
	if err != nil {
		return fmt.Errorf("lost-hook marker: %w", err)
	}
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("lost-hook marker: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, key)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("lost-hook marker: %w", err)
	}
	return nil
}

// clearLostMarkers removes every marker that can refer to sess (after a hook
// for it was recorded with a state).
func clearLostMarkers(p paths.Paths, sess store.Session) error {
	if p.Hooks == "" {
		return nil
	}
	var errs []error
	for _, key := range domain.LostHookKeys(sess.ID, sess.TaskID, sess.Identity.PaneID) {
		if err := os.Remove(filepath.Join(p.Hooks, LostDirName, key)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// lostState is what the timeout path needs to write a marker after the
// pipeline goroutine was abandoned.
type lostState struct {
	env      adapter.HookEnv
	paths    paths.Paths
	recorded bool // the event reached the registry (or was deliberately not ours)
}

func (r *runner) setLostContext(env adapter.HookEnv, p paths.Paths) {
	r.log.mu.Lock()
	r.lost.env, r.lost.paths = env, p
	r.log.mu.Unlock()
}

// failLost is fail for a hook whose event was not recorded: it also writes
// the lost-hook marker.
func (r *runner) failLost(o Outcome, err error) Outcome {
	r.log.mu.Lock()
	env, p := r.lost.env, r.lost.paths
	r.log.mu.Unlock()
	if werr := writeLostMarker(p, r.cfg.Now(), o, env, err); werr != nil {
		r.log.write(o, werr)
	}
	return r.fail(o, err)
}

// recorded notes that the event is in the registry and, when it carried a
// state, clears the session's markers: its state is evidence again.
func (r *runner) recorded(o Outcome, p paths.Paths, obs adapter.HookObservation, res store.HookResult) {
	r.log.mu.Lock()
	r.lost.recorded = true
	r.log.mu.Unlock()
	if !res.Applied || obs.State == "" {
		return
	}
	if err := clearLostMarkers(p, res.Session); err != nil {
		r.log.write(o, err)
	}
}

// lostOnTimeout writes the marker when the budget ran out before the event
// was recorded (for a parsed event of a pane that was not an orphan).
func (r *runner) lostOnTimeout(o Outcome) {
	r.log.mu.Lock()
	st := r.lost
	r.log.mu.Unlock()
	if st.recorded || o.Event == "" || o.Orphan != "" {
		return
	}
	if st.env.TaskID == "" && st.env.TMUXPane == "" {
		return // not an agentctl pane
	}
	if err := writeLostMarker(st.paths, r.cfg.Now(), o, st.env, o.Err); err != nil {
		r.log.write(o, err)
	}
}
