package main

// FAKEAGENT_TRUST_DIALOG=1 (m3-folder-trust-detect): before SessionStart,
// print a folder-trust dialog worded like Claude Code 2.x's and fire no hook
// until it is answered, as Claude Code holds back settings hooks on an
// untrusted folder. "1" or Enter accepts (the highlighted default), "2" or
// "n" declines and exits 1. Tests answer it themselves with tmux send-keys;
// agentctl never does.

// stTrust is fakeagent's state while the dialog is up (state_at_receipt).
const stTrust = "trust"

// trustDeclinedExit is the exit code after "No, exit".
const trustDeclinedExit = 1

// trustDialogText mirrors Claude Code 2.1.x's dialog (2026-11-09 demo).
func trustDialogText(cwd string) string {
	return " Accessing workspace:\n\n " + cwd + "\n\n" +
		" Quick safety check: Is this a project you created or one you trust? (Like your own code,\n" +
		" a well-known open source project, or work from your team). If not, take a moment to\n" +
		" review what's in this folder first.\n\n" +
		" ❯ 1. Yes, I trust this folder\n" +
		"   2. No, exit\n\n" +
		" Enter to confirm · Esc to cancel\n"
}

// trustGate shows the dialog when configured and waits for the answer.
// proceed is false when the agent must exit with code.
func (a *agent) trustGate() (code int, proceed bool) {
	if !a.cfg.trustDialog {
		return 0, true
	}
	a.setState(stTrust)
	a.term.write(trustDialogText(a.cfg.cwd))
	for ev := range a.events {
		switch ev.kind {
		case evApprove:
			a.term.write("\x1b[2J\x1b[H") // the dialog goes away, as in Claude Code
			a.setState(stStarting)
			return 0, true
		case evDeny, evQuit:
			return trustDeclinedExit, false
		}
	}
	return trustDeclinedExit, false
}

// trustKey handles one input token while the dialog is up. Every key is
// logged with state "trust"; pastes are logged and dropped (no text box).
func (a *agent) trustKey(tok token) {
	switch tok.kind {
	case tokText:
		for _, r := range tok.text {
			a.inputLog.write(inputEntry(string(r), stTrust, "key"))
			if a.cfg.hookTrust && !a.cfg.trustDialog { // Codex hook-trust screen (codex.go)
				switch r {
				case '2':
					a.events <- event{kind: evApprove, state: stTrust}
				case '3':
					a.events <- event{kind: evDeny, state: stTrust}
				}
				continue
			}
			switch r {
			case '1', 'y', 'Y':
				a.events <- event{kind: evApprove, state: stTrust}
			case '2', 'n', 'N':
				a.events <- event{kind: evDeny, state: stTrust}
			}
		}
	case tokPaste:
		a.inputLog.write(inputEntry(tok.text, stTrust, "paste"))
	case tokEnter:
		a.inputLog.write(inputEntry("", stTrust, "key"))
		a.events <- event{kind: evApprove, state: stTrust}
	case tokInterrupt, tokEOF:
		a.events <- event{kind: evQuit, state: stTrust}
	}
}
