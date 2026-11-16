package controller

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// maxAutoNameTries bounds the -2, -3, … suffixes AutoAdopt tries per pane.
const maxAutoNameTries = 50

// AutoAdoptResult reports one AutoAdopt pass.
type AutoAdoptResult struct {
	Adopted []AdoptResult
	// Skipped names, one line each, the candidate panes AutoAdopt could not
	// adopt and why (for example a pane outside any git repository).
	Skipped []string
	Warning string // the candidate listing degraded (see AdoptCandidates)
	Hints   []string
}

// AutoAdopt adopts every candidate pane that ListAdoptCandidates offers by
// default (live, unregistered, running a configured agent command, on any
// repository). Each task is named after the pane's directory, with -2, -3, …
// added when that name is taken. A pane registered by someone else in the
// meantime is skipped without a note. A missing tmux server adopts nothing.
func (c *Controller) AutoAdopt(ctx context.Context) (AutoAdoptResult, error) {
	cands, err := c.ListAdoptCandidates(ctx, CandidateQuery{})
	out := AutoAdoptResult{Warning: cands.Warning, Hints: cands.Hints}
	if err != nil {
		return out, err
	}
	for _, p := range cands.Panes {
		res, err := c.autoAdoptPane(ctx, p)
		switch {
		case err == nil:
			out.Adopted = append(out.Adopted, res)
		case errors.Is(err, ErrPaneRegistered):
		default:
			out.Skipped = append(out.Skipped, "pane "+p.PaneID+": "+OneLine(err.Error()))
		}
	}
	return out, nil
}

func (c *Controller) autoAdoptPane(ctx context.Context, p AdoptCandidate) (AdoptResult, error) {
	base := SuggestTaskName(p.Cwd)
	if base == "" {
		base = "agent"
	}
	for i := 1; i <= maxAutoNameTries; i++ {
		name := numberedName(base, i)
		if _, err := c.store.Tasks().Get(ctx, name); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return AdoptResult{}, err
		}
		res, err := c.Adopt(ctx, AdoptRequest{PaneID: p.PaneID, Name: name})
		if errors.Is(err, ErrTaskExists) {
			continue // taken in the meantime
		}
		return res, err
	}
	return AdoptResult{}, userErr(ErrTaskExists, nil, "no free task name like %q (tried %d)", base, maxAutoNameTries)
}

// numberedName is base for n == 1, else base-n, cut so it stays a valid slug.
func numberedName(base string, n int) string {
	if n == 1 {
		return base
	}
	suffix := "-" + strconv.Itoa(n)
	if len(base)+len(suffix) > domain.MaxSlugLen {
		base = strings.TrimRight(base[:domain.MaxSlugLen-len(suffix)], "-")
	}
	return base + suffix
}

// SuggestTaskName turns a directory's base name into a task-name suggestion
// (lowercase, other characters as '-'), or "" when nothing usable is left.
func SuggestTaskName(dir string) string {
	base := filepath.Base(filepath.Clean(dir))
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(base) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		switch {
		case ok:
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0 && (r == '-' || r == '_' || r == '.' || unicode.IsSpace(r) || r > unicode.MaxASCII || unicode.IsPunct(r)):
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > domain.MaxSlugLen {
		s = strings.TrimRight(s[:domain.MaxSlugLen], "-")
	}
	if domain.ValidateSlug(s) != nil {
		return ""
	}
	return s
}
