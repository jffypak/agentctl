package claude

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Start ensures the hook settings file exists (EnsureSettings, settings.go),
// then creates detached session jpark-<task> in the worktree running
// `<agent cmd> --settings <file> <goal>` with AGENTCTL_TASK_ID and
// AGENTCTL_HOME exported and remain-on-exit on, all in one tmux invocation.
// The returned identity comes from that same invocation.
func (a *Adapter) Start(ctx context.Context, spec adapter.LaunchSpec) (adapter.Handle, error) {
	if err := validateLaunch(spec); err != nil {
		return adapter.Handle{}, err
	}
	settings, err := a.EnsureSettings(spec.Home)
	if err != nil {
		return adapter.Handle{}, fmt.Errorf("claude: hook settings: %w", err)
	}
	argv := LaunchArgv(spec.AgentCmd, settings, spec.Goal)
	name := SessionName(spec.TaskID)
	created, err := a.client(spec.Socket).NewSession(ctx, tmux.NewSessionSpec{
		Name: name,
		Dir:  spec.Worktree,
		Env:  []string{"AGENTCTL_TASK_ID=" + spec.TaskID, paths.EnvHome + "=" + spec.Home},
		Argv: argv,
	})
	if err != nil {
		return adapter.Handle{}, err
	}
	return adapter.Handle{
		Identity: domain.Identity{
			Socket: spec.Socket, ServerPID: created.ServerPID, SessionID: created.SessionID,
			PaneID: created.PaneID, PanePID: created.PanePID,
		},
		SessionName: name,
		Argv:        argv,
	}, nil
}

func validateLaunch(spec adapter.LaunchSpec) error {
	if err := domain.ValidateSlug(spec.TaskID); err != nil {
		return err
	}
	for name, p := range map[string]string{"worktree": spec.Worktree, "home": spec.Home, "socket": spec.Socket} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("claude: %s path %q is not absolute", name, p)
		}
	}
	if len(spec.AgentCmd) == 0 || !filepath.IsAbs(spec.AgentCmd[0]) {
		return errors.New("claude: agent command must start with an absolute path")
	}
	return nil
}
