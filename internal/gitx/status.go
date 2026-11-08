package gitx

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
)

// StatusEntry is one record of `git status --porcelain=v2 -z`.
type StatusEntry struct {
	Kind     byte   // '1' changed, '2' renamed/copied, 'u' unmerged, '?' untracked, '!' ignored
	X, Y     byte   // index / worktree status ('.' = unmodified); zero for '?' and '!'
	Path     string // verbatim (no quoting under -z); untracked directories end in '/'
	OrigPath string // rename/copy source ('2' records only)
}

// Status is a parsed porcelain-v2 status.
type Status struct {
	Entries []StatusEntry
}

// StatusArgs lists every untracked file individually (not just its
// directory). `git status` normally refreshes and rewrites the index as a
// side effect; GIT_OPTIONAL_LOCKS=0 (set by Git.env for every command)
// disables exactly that, so this stays read-only.
func StatusArgs() []string {
	return []string{"status", "--porcelain=v2", "-z", "--untracked-files=all"}
}

// Status runs `git status --porcelain=v2 -z` in dir.
func (g Git) Status(ctx context.Context, dir string) (Status, error) {
	out, err := g.run(ctx, dir, StatusArgs()...)
	if err != nil {
		return Status{}, err
	}
	return ParseStatusZ(out)
}

// Conflicts lists unmerged paths, sorted.
func (s Status) Conflicts() []string { return s.paths('u') }

// Untracked lists untracked paths, sorted. Nested repositories appear as
// "dir/".
func (s Status) Untracked() []string { return s.paths('?') }

func (s Status) paths(kind byte) []string {
	var out []string
	for _, e := range s.Entries {
		if e.Kind == kind {
			out = append(out, e.Path)
		}
	}
	sort.Strings(out)
	return out
}

// statusFields is the number of space-separated fields before the path in
// each record kind (the path itself may contain spaces, hence SplitN).
var statusFields = map[byte]int{'1': 8, '2': 9, 'u': 10}

// ParseStatusZ parses `status --porcelain=v2 -z`: NUL-terminated records,
// where a '2' (rename/copy) record is followed by its original path as a
// separate NUL-terminated field. '#' header lines are skipped.
func ParseStatusZ(out []byte) (Status, error) {
	toks, err := splitNUL(out)
	if err != nil {
		return Status{}, fmt.Errorf("gitx: status: %w", err)
	}
	var st Status
	for i := 0; i < len(toks); i++ {
		rec := toks[i]
		if rec == "" {
			return Status{}, fmt.Errorf("gitx: status: empty record")
		}
		kind := rec[0]
		switch kind {
		case '#':
			continue
		case '?', '!':
			if len(rec) < 3 || rec[1] != ' ' {
				return Status{}, fmt.Errorf("gitx: status: malformed record %q", rec)
			}
			st.Entries = append(st.Entries, StatusEntry{Kind: kind, Path: rec[2:]})
			continue
		}
		n, ok := statusFields[kind]
		if !ok {
			return Status{}, fmt.Errorf("gitx: status: unknown record kind in %q", rec)
		}
		f := strings.SplitN(rec, " ", n+1)
		if len(f) != n+1 || f[n] == "" || len(f[1]) != 2 {
			return Status{}, fmt.Errorf("gitx: status: malformed record %q", rec)
		}
		e := StatusEntry{Kind: kind, X: f[1][0], Y: f[1][1], Path: f[n]}
		if kind == '2' {
			if i+1 >= len(toks) || toks[i+1] == "" {
				return Status{}, fmt.Errorf("gitx: status: rename record %q has no original path", rec)
			}
			e.OrigPath = toks[i+1]
			i++
		}
		st.Entries = append(st.Entries, e)
	}
	return st, nil
}

// splitNUL splits NUL-terminated output into its fields. Output that does
// not end in NUL is truncated and rejected.
func splitNUL(out []byte) ([]string, error) {
	if len(out) == 0 {
		return nil, nil
	}
	if out[len(out)-1] != 0 {
		return nil, fmt.Errorf("unterminated output %q", out)
	}
	parts := bytes.Split(out[:len(out)-1], []byte{0})
	toks := make([]string, len(parts))
	for i, p := range parts {
		toks[i] = string(p)
	}
	return toks, nil
}
