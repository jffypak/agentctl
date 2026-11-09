package main

import (
	"fmt"
	"strconv"
	"time"
)

// envGoalDelay is the gap between the SessionStart hook returning and the
// initial prompt (the goal) being submitted. Real Claude Code leaves ~230 ms
// there (m3-goal-start-race); tests widen it to land a send inside it.
const envGoalDelay = "FAKEAGENT_GOAL_DELAY_MS"

func parseGoalDelay(getenv func(string) string) (time.Duration, error) {
	v := getenv(envGoalDelay)
	if v == "" {
		return 0, nil
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms < 0 {
		return 0, fmt.Errorf("%s=%q is not a non-negative integer", envGoalDelay, v)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// awaitGoalSubmit is the SessionStart -> goal submit gap. The agent stays
// starting: it is not at its prompt, so input arriving now is logged as
// received while starting.
func (a *agent) awaitGoalSubmit() {
	if a.cfg.goalDelay > 0 {
		time.Sleep(a.cfg.goalDelay)
	}
}
