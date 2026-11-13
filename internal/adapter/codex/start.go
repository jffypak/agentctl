package codex

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/tmux"
)

// HookTimeoutSeconds is the per-hook timeout in the hook flags (the hook
// itself finishes within its < 2 s budget).
const HookTimeoutSeconds = 5

// clampedTimeoutSeconds is Codex's limit for Interrupt and SessionEnd hooks
// (Codex 0.167.2 clamps them to 3 s and warns on every launch otherwise).
const clampedTimeoutSeconds = 3

// HookTimeout is the timeout (seconds) in the hook flag of event.
func HookTimeout(event string) int {
	if event == "Interrupt" || event == "SessionEnd" {
		return clampedTimeoutSeconds
	}
	return HookTimeoutSeconds
}

// hookEvents are the Codex events agentctl subscribes to, in flag order.
var hookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
	"PreCompact", "Stop", "Interrupt", "SessionEnd",
}

// HookEvents lists the subscribed events in flag order (a copy).
func HookEvents() []string { return append([]string(nil), hookEvents...) }

// HookCommand is the shell command each hook runs. It must stay byte-stable:
// Codex keys a user's hook trust by a hash of it.
func HookCommand(agentctlPath string) string {
	return "'" + strings.ReplaceAll(agentctlPath, "'", `'\''`) + "' hook --agent codex"
}

// HookFlags renders agentctl's hooks as Codex -c overrides, one
// `-c hooks.<Event>=[{hooks=[{type="command",command="…",timeout=5}]}]` pair
// per subscribed event. Any -c also forces Codex's embedded mode, the only
// mode in which hooks run in the pane's process (see the package doc).
func HookFlags(agentctlPath string) ([]string, error) {
	switch {
	case agentctlPath == "":
		return nil, errors.New("codex: agentctl path for the hook command is unknown")
	case !filepath.IsAbs(agentctlPath):
		return nil, fmt.Errorf("codex: agentctl path %q is not absolute", agentctlPath)
	case strings.ContainsAny(agentctlPath, "\x00\n\r"):
		return nil, fmt.Errorf("codex: agentctl path %q contains a control character", agentctlPath)
	}
	cmd := tomlString(HookCommand(agentctlPath))
	out := make([]string, 0, 2*len(hookEvents))
	for _, ev := range hookEvents {
		out = append(out, "-c", fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s,timeout=%d}]}]`, ev, cmd, HookTimeout(ev)))
	}
	return out, nil
}

// tomlString quotes s as a TOML basic string. Control characters are
// rejected before this is called, so only '\' and '"' need escaping.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// LaunchArgv is prefix + hook flags + [goal]. The goal is one element (never
// shell-parsed) and is omitted when empty.
func LaunchArgv(prefix []string, agentctlPath, goal string) ([]string, error) {
	flags, err := HookFlags(agentctlPath)
	if err != nil {
		return nil, err
	}
	argv := append(append([]string(nil), prefix...), flags...)
	if goal != "" {
		argv = append(argv, goal)
	}
	return argv, nil
}

// Start creates detached session jpark-<task> in the worktree running
// `<codex cmd> <hook flags> <goal>` with AGENTCTL_TASK_ID and AGENTCTL_HOME
// exported and remain-on-exit on, in one tmux invocation (like claude.Start).
func (a *Adapter) Start(ctx context.Context, spec adapter.LaunchSpec) (adapter.Handle, error) {
	if err := validateLaunch(spec); err != nil {
		return adapter.Handle{}, err
	}
	argv, err := LaunchArgv(spec.AgentCmd, a.cfg.AgentctlPath, spec.Goal)
	if err != nil {
		return adapter.Handle{}, err
	}
	name := claude.SessionName(spec.TaskID)
	created, err := tmux.NewClient(a.cfg.Runner, tmux.Socket{Path: spec.Socket}).NewSession(ctx, tmux.NewSessionSpec{
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
			return fmt.Errorf("codex: %s path %q is not absolute", name, p)
		}
	}
	if len(spec.AgentCmd) == 0 || !filepath.IsAbs(spec.AgentCmd[0]) {
		return errors.New("codex: agent command must start with an absolute path")
	}
	return nil
}
