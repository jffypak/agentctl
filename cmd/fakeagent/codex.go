package main

// Codex mode (codex-support): `fakeagent --codex [-c key=value]... [prompt]`
// stands in for Codex CLI in embedded mode, the only mode agentctl tracks.
//
//   - Hooks come from repeated `-c hooks.<Event>=[{hooks=[{type="command",
//     command="…",timeout=N}]}]` flags, exactly the shape codex.HookFlags
//     emits; any other hooks.* shape is an error (exit 2), other -c values
//     are ignored.
//   - No hook fires when the TUI opens: SessionStart(startup) fires right
//     before the first prompt's UserPromptSubmit, as real Codex does.
//   - Payloads add model and, on turn-scoped events, turn_id.
//   - #ask fires PreToolUse + PermissionRequest (Codex has no Notification).
//   - FAKEAGENT_HOOK_TRUST=1 first shows Codex's hook-trust screen and fires
//     nothing until it is answered: "2" or Enter trusts, "3" continues
//     without hooks (none ever fire), "1" (Review hooks) is ignored.

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var codexHookFlag = regexp.MustCompile(`^hooks\.([A-Za-z]+)=\[\{hooks=\[\{type="command",command="((?:[^"\\]|\\.)*)",timeout=([0-9]+)\}\]\}\]$`)

// parseCodexArgs reads the arguments after --codex.
func parseCodexArgs(args []string) (settings, string, error) {
	s := settings{Hooks: map[string][]matcherGroup{}}
	var prompt []string
	for i := 0; i < len(args); i++ {
		if args[i] != "-c" {
			prompt = append(prompt, args[i])
			continue
		}
		if i+1 >= len(args) {
			return settings{}, "", errors.New("fakeagent --codex: -c needs a value")
		}
		i++
		kv := args[i]
		if !strings.HasPrefix(kv, "hooks.") {
			continue // another config override: ignored
		}
		m := codexHookFlag.FindStringSubmatch(kv)
		if m == nil {
			return settings{}, "", fmt.Errorf("fakeagent --codex: unsupported hook flag %q", kv)
		}
		timeout, _ := strconv.Atoi(m[3])
		cmd := strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(m[2])
		s.Hooks[m[1]] = append(s.Hooks[m[1]], matcherGroup{Hooks: []hookHandler{{Type: "command", Command: cmd, Timeout: timeout}}})
	}
	if len(prompt) > 1 {
		return settings{}, "", errors.New("usage: fakeagent --codex [-c key=value]... [initial prompt]")
	}
	if len(prompt) == 1 {
		return s, prompt[0], nil
	}
	return s, "", nil
}

// hookTrustText mirrors Codex CLI 0.167.2's startup hook-trust screen.
const hookTrustText = " Hooks need review\n\n" +
	" New hooks were found. Hooks run commands on your machine.\n\n" +
	" › 1. Review hooks\n" +
	"   2. Trust all and continue\n" +
	"   3. Continue without trusting (hooks won't run)\n"

// hookTrustGate shows the hook-trust screen when configured. It returns
// proceed=false (with code) only when the user quit.
func (a *agent) hookTrustGate() (code int, proceed bool) {
	if !a.cfg.hookTrust {
		return 0, true
	}
	a.setState(stTrust)
	a.term.write(hookTrustText)
	for ev := range a.events {
		switch ev.kind {
		case evApprove:
			a.term.write("\x1b[2J\x1b[H")
			a.setState(stStarting)
			return 0, true
		case evDeny: // continue without trusting: hooks never run
			a.hooks = settings{}
			a.term.write("\x1b[2J\x1b[H")
			a.setState(stStarting)
			return 0, true
		case evQuit:
			return 0, false
		}
	}
	return 0, false
}
