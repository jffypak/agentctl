package domain

import (
	"errors"
	"fmt"
)

// MaxSlugLen is the longest allowed task name.
const MaxSlugLen = 40

// ErrInvalidSlug is wrapped by every ValidateSlug failure.
var ErrInvalidSlug = errors.New("invalid task name")

// ValidateSlug enforces task names of the form [a-z0-9][a-z0-9-]{0,39}. Task
// names become a branch (jpark/<name>), a directory, and a tmux session
// name (jpark-<name>), so '.', ':', '/', whitespace and a leading '-' are
// all rejected. Errors are one line and say how to fix the name.
func ValidateSlug(s string) error {
	const rule = "use 1-40 lowercase letters, digits or '-', starting with a letter or digit"
	switch {
	case s == "":
		return fmt.Errorf("%w: name is empty (%s)", ErrInvalidSlug, rule)
	case len(s) > MaxSlugLen:
		return fmt.Errorf("%w: %q is %d characters (%s)", ErrInvalidSlug, s, len(s), rule)
	case s[0] == '-':
		return fmt.Errorf("%w: %q starts with '-' (%s)", ErrInvalidSlug, s, rule)
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return fmt.Errorf("%w: %q contains %q (%s)", ErrInvalidSlug, s, c, rule)
	}
	return nil
}

// SlugFrom derives a task name from free text (a directory name): lower-case,
// every run of characters outside [a-z0-9] becomes one '-', leading and
// trailing '-' are dropped, and the result is cut to MaxSlugLen (then trailing
// '-' dropped again). It returns "" when nothing usable is left; any other
// result passes ValidateSlug.
func SlugFrom(s string) string {
	var b []byte
	dash := false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A'
			fallthrough
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && len(b) > 0 {
				b = append(b, '-')
			}
			dash = false
			b = append(b, byte(r))
		default:
			dash = true
		}
	}
	if len(b) > MaxSlugLen {
		b = b[:MaxSlugLen]
	}
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	return string(b)
}
