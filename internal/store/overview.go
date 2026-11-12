package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jffypak/agentctl/internal/domain"
)

// OverviewFilter scopes the Overview read model.
type OverviewFilter struct {
	RepoID        int64 // 0 = every repo
	IncludeClosed bool
}

// OverviewRow is one task with its repo, newest session (nil while only the
// reservation exists), and message counts by state.
type OverviewRow struct {
	Task     Task
	Repo     Repo
	Session  *Session
	Messages domain.MessageSummary
}

// Overview returns every matching task in one consistent read (a single
// statement, so one snapshot), ordered by task creation. Attention
// classification and sorting happen in the controller.
func (s *Store) Overview(ctx context.Context, f OverviewFilter) ([]OverviewRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("t.", taskCols)+`,
			r.id, r.root_path, r.name, r.created_at,
			s.id, s.task_id, s.agent_type, s.origin, s.tmux_socket, s.tmux_server_pid, s.tmux_session_id,
			s.tmux_session_name, s.tmux_pane_id, s.pane_pid, s.agent_session_id, s.agent_state, s.state_confidence,
			s.state_since, s.last_detail, s.created_at, s.ended_at, s.first_hook_at,
			COALESCE(mc.queued, 0), COALESCE(mc.delivering, 0), COALESCE(mc.sent, 0), COALESCE(mc.acknowledged, 0),
			COALESCE(mc.unconfirmed, 0), COALESCE(mc.failed, 0), COALESCE(mc.cancelled, 0)
		FROM tasks t
		JOIN repos r ON r.id = t.repo_id
		LEFT JOIN sessions s ON s.id = (SELECT MAX(id) FROM sessions WHERE task_id = t.id)
		LEFT JOIN (SELECT task_id,
				SUM(state = 'queued') AS queued, SUM(state = 'delivering') AS delivering,
				SUM(state = 'sent') AS sent, SUM(state = 'acknowledged') AS acknowledged,
				SUM(state = 'unconfirmed') AS unconfirmed, SUM(state = 'failed') AS failed,
				SUM(state = 'cancelled') AS cancelled
			FROM messages GROUP BY task_id) mc ON mc.task_id = t.id
		WHERE (? = 0 OR t.repo_id = ?) AND (? OR t.state = 'running')
		ORDER BY t.created_at, t.id`, f.RepoID, f.RepoID, f.IncludeClosed)
	if err != nil {
		return nil, fmt.Errorf("store: overview: %w", err)
	}
	defer rows.Close()
	var out []OverviewRow
	for rows.Next() {
		row, err := scanOverviewRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: overview: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanOverviewRow(rows *sql.Rows) (OverviewRow, error) {
	var (
		row                                         OverviewRow
		tCreated, tUpdated, rCreated                int64
		tClosed, sEnded, sFirstHook                 sql.NullInt64
		sID, sServerPID, sPanePID, sSince, sCreated sql.NullInt64
		sTask, sType, sOrigin, sSocket, sSessID     sql.NullString
		sSessName, sPane, sAgentSess, sState, sConf sql.NullString
		sDetail                                     sql.NullString
		m                                           domain.MessageSummary
	)
	t := &row.Task
	if err := rows.Scan(&t.ID, &t.RepoID, &t.Name, &t.Goal, &t.State, &t.WorktreePath, &t.Branch,
		&t.BaseRef, &t.BaseSHA, &tCreated, &tUpdated, &tClosed,
		&row.Repo.ID, &row.Repo.RootPath, &row.Repo.Name, &rCreated,
		&sID, &sTask, &sType, &sOrigin, &sSocket, &sServerPID, &sSessID,
		&sSessName, &sPane, &sPanePID, &sAgentSess, &sState, &sConf,
		&sSince, &sDetail, &sCreated, &sEnded, &sFirstHook,
		&m.Queued, &m.Delivering, &m.Sent, &m.Acknowledged, &m.Unconfirmed, &m.Failed, &m.Cancelled); err != nil {
		return OverviewRow{}, err
	}
	t.CreatedAt, t.UpdatedAt, t.ClosedAt = fromMS(tCreated), fromMS(tUpdated), fromNullMS(tClosed)
	row.Repo.CreatedAt = fromMS(rCreated)
	row.Messages = m
	if sID.Valid {
		row.Session = &Session{
			ID: sID.Int64, TaskID: sTask.String, AgentType: sType.String, Origin: domain.Origin(sOrigin.String),
			Identity: domain.Identity{
				Socket: sSocket.String, ServerPID: int(sServerPID.Int64), SessionID: sSessID.String,
				PaneID: sPane.String, PanePID: int(sPanePID.Int64),
			},
			SessionName: sSessName.String, AgentSessionID: sAgentSess.String,
			State: domain.AgentState(sState.String), Confidence: domain.Confidence(sConf.String),
			StateSince: fromMS(sSince.Int64), LastDetail: sDetail.String,
			CreatedAt: fromMS(sCreated.Int64), EndedAt: fromNullMS(sEnded), FirstHookAt: fromNullMS(sFirstHook),
		}
	}
	return row, nil
}
