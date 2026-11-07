package testenv

import (
	"testing"
	"time"
)

// DefaultWait bounds WaitFor when the caller passes 0.
const DefaultWait = 5 * time.Second

// pollInterval is how often WaitFor re-checks. Polling a condition is a
// deterministic wait; it is never used as a fixed sleep.
const pollInterval = 10 * time.Millisecond

// WaitFor polls cond until it returns true or timeout elapses, then fails the
// test with what plus the last detail cond reported.
func WaitFor(t testing.TB, timeout time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	if !Eventually(timeout, cond) {
		_, detail := cond()
		t.Fatalf("timed out after %v waiting for %s (last: %s)", orDefault(timeout), what, detail)
	}
}

// Eventually polls cond until it returns true (true) or timeout elapses (false).
func Eventually(timeout time.Duration, cond func() (bool, string)) bool {
	deadline := time.Now().Add(orDefault(timeout))
	for {
		if ok, _ := cond(); ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

func orDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultWait
	}
	return d
}
