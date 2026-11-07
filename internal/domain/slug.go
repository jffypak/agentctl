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
// names become a branch (agentctl/<name>), a directory, and a tmux session
// name (agentctl-<name>), so '.', ':', '/', whitespace and a leading '-' are
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
