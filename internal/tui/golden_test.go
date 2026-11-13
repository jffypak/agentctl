package tui

import (
	"context"
	"fmt"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/jffypak/agentctl/internal/testenv"
)

// TestGolden pins every key screen at 80x24 and 120x40 with the fixed clock
// and the fixed palette (256-color indices, dark background; one NO_COLOR
// variant). Update with: go test ./internal/tui -run TestGolden -update
func TestGolden(t *testing.T) {
	t.Parallel()
	screens := []struct {
		name  string
		build func(t *testing.T, w, h int) Model
	}{
		{"empty", func(t *testing.T, w, h int) Model {
			return newFixture(t).model(t, w, h, Options{})
		}},
		{"mixed", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seed(t, everyState)
			return f.model(t, w, h, Options{})
		}},
		{"detail", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seed(t, everyState)
			return press(t, f.model(t, w, h, Options{}), "j", "j", "j", "enter") // auth-tests
		}},
		{"detail_unknown", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seed(t, everyState)
			return press(t, f.model(t, w, h, Options{}), "enter") // legacy-pane: unknown + queued
		}},
		{"error", func(t *testing.T, w, h int) Model {
			f := newFixtureEnv(t, func(e testenv.Env) testenv.Env {
				return e.With("PATH="+testenv.BinDir(t, "git"), `AGENTCTL_AGENT_CMD=["/bin/sh"]`)
			}, false)
			f.seed(t, everyState)
			m := f.model(t, w, h, Options{})
			return run(t, m, preflightCmd(m.sh))
		}},
		{"quota", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seed(t, everyState)
			f.seedQuota(t)
			m := f.model(t, w, h, Options{})
			return run(t, m, quotaCmd(m.sh))
		}},
		{"quota_empty", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seedQuota(t)
			m := f.model(t, w, h, Options{})
			return run(t, m, quotaCmd(m.sh))
		}},
		{"command_center", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seedCommandCenter(t)
			return press(t, f.model(t, w, h, Options{}), "j", "j", "j", "j", "j", "j", "j") // docs-site
		}},
		{"detail_text", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seedCommandCenter(t)
			return press(t, f.model(t, w, h, Options{}), "j", "j", "j", "j", "j", "j", "j", "enter") // docs-site
		}},
		{"tab_unread", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seedCommandCenter(t)
			return press(t, f.model(t, w, h, Options{}), "tab", "tab") // All -> Needs you -> Unread
		}},
		{"filter", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seed(t, everyState)
			return press(t, f.model(t, w, h, Options{}), "/", "d", "o", "c", "s")
		}},
	}
	for _, s := range screens {
		for _, size := range [][2]int{{80, 24}, {120, 40}} {
			t.Run(fmt.Sprintf("%s_%dx%d", s.name, size[0], size[1]), func(t *testing.T) {
				t.Parallel()
				m := s.build(t, size[0], size[1])
				teatest.RequireEqualOutput(t, []byte(m.View().Content))
			})
		}
	}
	t.Run("mixed_nocolor_80x24", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.seed(t, everyState)
		teatest.RequireEqualOutput(t, []byte(f.model(t, 80, 24, Options{NoColor: true}).View().Content))
	})
	t.Run("unopenable_80x24", func(t *testing.T) {
		t.Parallel()
		m := New(context.Background(), Options{OpenErr: fmt.Errorf("cannot open registry /home/u/.local/state/agentctl/agentctl.db: unable to open database file")})
		teatest.RequireEqualOutput(t, []byte(m.View().Content))
	})
}
