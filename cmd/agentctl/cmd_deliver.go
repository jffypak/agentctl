package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/paths"
)

// Environment overrides for the background deliverer's timing (Go duration
// strings). Tests use them to shorten the ack window; users never need them.
const (
	envAckWindow       = "AGENTCTL_ACK_WINDOW"
	envDeliverSettle   = "AGENTCTL_DELIVER_SETTLE"
	envPasteEnterDelay = "AGENTCTL_PASTE_ENTER_DELAY"
)

// deliverLogName is the file under <home>/logs that deliver appends to.
const deliverLogName = "deliver.log"

// cmdDeliver: `agentctl deliver --session <id> --after-pid <pid>` is spawned
// detached by the hook that moved a session to ready with messages queued
// (hookingest/trigger.go). It waits for the hook process to exit,
// DeliverSettle, runs controller.Drain, then waits up to AckWindow for the
// UserPromptSubmit ack (unconfirmed otherwise). It always exits 0, prints
// nothing, and appends one line per step to <home>/logs/deliver.log.
func cmdDeliver(ctx context.Context, sys *system, args []string) int {
	logw := openDeliverLog(sys)
	defer logw.Close()
	logf := func(format string, a ...any) {
		fmt.Fprintf(logw, "%s deliver pid=%d: %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), os.Getpid(),
			controller.OneLine(fmt.Sprintf(format, a...)))
	}
	fs := newFlagSet(sys, "deliver")
	fs.SetOutput(io.Discard)
	session := fs.Int64("session", 0, "session id")
	afterPID := fs.Int("after-pid", 0, "wait for this pid to exit first")
	if err := fs.Parse(args); err != nil || *session <= 0 || fs.NArg() != 0 || *afterPID < 0 {
		logf("usage: agentctl deliver --session <id> [--after-pid <pid>] (got %q)", args)
		return exitOK
	}
	tun, err := deliverTunables(sys.getenv)
	if err != nil {
		logf("session=%d: %v", *session, err)
		return exitOK
	}
	ctl, err := controller.Open(ctx, controller.Config{Environ: sys.environ, Now: sys.now, AgentctlPath: sys.executable, Tunables: tun})
	if err != nil {
		logf("session=%d: %v", *session, err)
		return exitOK
	}
	defer ctl.Close()
	t := ctl.Tunables()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second+t.DeliverSettle+t.AckWindow+time.Minute)
	defer cancel()
	rep := ctl.DeliverAfterHook(ctx, controller.DeliverRequest{SessionID: *session, AfterPID: *afterPID, Log: logw})
	if rep.Err != nil {
		logf("session=%d: finished with error: %v", *session, rep.Err)
	}
	return exitOK
}

// openDeliverLog appends to <home>/logs/deliver.log (0600). Without a usable
// home it discards: deliver never writes to stdout or creates a home.
func openDeliverLog(sys *system) io.WriteCloser {
	p, err := paths.Resolve(sys.getenv)
	if err != nil {
		return nopCloser{io.Discard}
	}
	if fi, err := os.Stat(p.Home); err != nil || !fi.IsDir() {
		return nopCloser{io.Discard}
	}
	if err := os.MkdirAll(p.Logs, 0o700); err != nil {
		return nopCloser{io.Discard}
	}
	f, err := os.OpenFile(filepath.Join(p.Logs, deliverLogName), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nopCloser{io.Discard}
	}
	return f
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// deliverTunables reads the AGENTCTL_* timing overrides (zero = default).
func deliverTunables(getenv func(string) string) (controller.Tunables, error) {
	var t controller.Tunables
	for _, o := range []struct {
		env string
		dst *time.Duration
	}{{envAckWindow, &t.AckWindow}, {envDeliverSettle, &t.DeliverSettle}, {envPasteEnterDelay, &t.PasteEnterDelay}} {
		v := getenv(o.env)
		if v == "" {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return t, fmt.Errorf("%s=%q is not a positive duration", o.env, v)
		}
		*o.dst = d
	}
	return t, nil
}
