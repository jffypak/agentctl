package hookingest

import "github.com/jffypak/agentctl/internal/taskctx"

// taskContext returns the task's context text for a Claude SessionStart
// (internal/taskctx). It fails open: an error is logged and the session
// starts without the context.
func (r *runner) taskContext(o Outcome, home string) string {
	text, err := taskctx.SessionText(home, o.TaskID)
	if err != nil {
		r.log.write(o, err)
		return ""
	}
	return text
}
