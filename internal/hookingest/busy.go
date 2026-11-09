package hookingest

import (
	"context"
	"strings"
	"time"

	"github.com/jffypak/agentctl/internal/store"
)

// SQLite can fail BEGIN IMMEDIATE with SQLITE_BUSY at once (e.g. while
// another connection recovers the WAL index) instead of waiting out
// busy_timeout. Hooks are concurrent multi-process writers, so such a fast
// failure is retried a few times within the budget; a busy error that took
// at least fastBusy is the busy timeout itself and is not retried.
const (
	fastBusy       = 100 * time.Millisecond
	maxBusyRetries = 4
	busyBackoff    = 25 * time.Millisecond
)

func retryFastBusy(ctx context.Context, op func() (store.HookResult, error)) (store.HookResult, error) {
	for attempt := 0; ; attempt++ {
		start := time.Now()
		res, err := op()
		if err == nil || !isBusy(err) || time.Since(start) >= fastBusy || attempt >= maxBusyRetries {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, err
		case <-time.After(busyBackoff * time.Duration(attempt+1)):
		}
	}
}

// isBusy recognizes SQLITE_BUSY / SQLITE_LOCKED through the store's wrapping.
func isBusy(err error) bool {
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "SQLITE_LOCKED") || strings.Contains(s, "database is locked")
}
