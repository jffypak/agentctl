package tui

import (
	"strings"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// closedKeyword is the filter word that lists closed tasks (hidden otherwise,
// ARCHITECTURE.md §Attention classification).
const closedKeyword = "closed"

// filterState is the `/` filter: editing while the user types, applied once
// they press enter (or as they type: matching is live).
type filterState struct {
	input   lineInput
	editing bool
}

// query is the applied filter text.
func (f filterState) query() string { return strings.TrimSpace(f.input.Value()) }

// active reports whether any filter is in effect or being typed.
func (f filterState) active() bool { return f.editing || f.query() != "" }

// parsedFilter is a query split into the closed switch and text terms.
type parsedFilter struct {
	closed bool     // the word "closed": list closed tasks instead of open ones
	terms  []string // every term must match name, goal or branch (case-insensitive)
}

func parseFilter(q string) parsedFilter {
	var p parsedFilter
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if w == closedKeyword {
			p.closed = true
			continue
		}
		p.terms = append(p.terms, w)
	}
	return p
}

// matches reports whether task v passes the filter.
func (p parsedFilter) matches(v controller.TaskView) bool {
	if (v.Attention.Section == domain.SectionClosed) != p.closed {
		return false
	}
	hay := strings.ToLower(v.Name + "\x00" + v.Goal + "\x00" + v.Branch)
	for _, t := range p.terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// visibleRows applies the filter to the overview rows (already in section
// order, oldest state_since first).
func visibleRows(rows []controller.TaskView, q string) []controller.TaskView {
	p := parseFilter(q)
	out := make([]controller.TaskView, 0, len(rows))
	for _, r := range rows {
		if p.matches(r) {
			out = append(out, r)
		}
	}
	return out
}
