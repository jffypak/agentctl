//go:build tools

// Package deps pins every module agentctl Phase 1 depends on so that
// `go mod tidy` keeps them before the cards that use them merge. No card adds
// a module after the walking skeleton (ARCHITECTURE.md §Packages).
package deps

import (
	_ "charm.land/bubbles/v2"
	_ "charm.land/bubbles/v2/textarea"
	_ "charm.land/bubbles/v2/textinput"
	_ "charm.land/bubbletea/v2"
	_ "charm.land/lipgloss/v2"
	_ "github.com/charmbracelet/x/exp/teatest/v2"
	_ "go.yaml.in/yaml/v3"
	_ "golang.org/x/term"
	_ "modernc.org/sqlite"
)
