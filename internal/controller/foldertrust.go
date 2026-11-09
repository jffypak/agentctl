package controller

import (
	"context"
	"strings"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// folderTrustScanLines bounds what folder-trust detection reads: the last N
// non-blank lines of the visible screen (no scrollback). Claude's dialog is
// about a dozen lines and is the only thing on a fresh agent's screen.
const folderTrustScanLines = 40

// awaitingFirstHook reports whether a session is in the window where
// Claude's folder-trust dialog can block it: launched (starting) and no hook
// recorded yet (confidence none). Any hook, SessionStart included, moves the
// session out of this window, so capture stops there.
func awaitingFirstHook(sess store.Session) bool {
	return sess.State == domain.StateStarting && sess.Confidence == domain.ConfidenceNone
}

// showsFolderTrust captures the bottom of the visible screen through the
// identity-checked capture path (liveSession, then a server-side identity
// re-check) and matches Claude's folder-trust wording on the sanitized
// text. A capture that fails for any reason is not a match: the pane and
// timeout verdicts still cover a pane that died or was reused.
func (c *Controller) showsFolderTrust(ctx context.Context, sess store.Session) bool {
	raw, ps := c.capture(ctx, &sess, 0)
	if !ps.Available || raw == "" {
		return false
	}
	return domain.ShowsFolderTrustDialog(strings.Join(recentLines(raw, folderTrustScanLines), "\n"))
}

// markFolderTrust records the dialog: unknown / none, detail
// domain.FolderTrustDetail, guarded on starting so a hook that arrived
// meanwhile wins. Unknown is the launched-but-silent route into NEEDS YOU,
// is never deliverable (sends queue), and accepts every hook mapping out of
// it, exactly like the StartingTimeout verdict. agentctl never answers the
// dialog (product call: never auto-accept folder trust).
func (c *Controller) markFolderTrust(ctx context.Context, sess store.Session) error {
	_, err := c.store.Sessions().ReconcileTransition(ctx, store.ReconcileInput{
		SessionID: sess.ID, From: domain.StateStarting, To: domain.StateUnknown, Confidence: domain.ConfidenceNone,
		Detail: domain.FolderTrustDetail, Source: domain.SourcePaneText,
	})
	return err
}
