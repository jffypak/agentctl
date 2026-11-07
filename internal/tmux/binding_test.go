package tmux

import (
	"strings"
	"testing"
)

func TestPopupBindingPlainPath(t *testing.T) {
	got, err := PopupBinding("/opt/agentctl/bin/agentctl")
	if err != nil {
		t.Fatal(err)
	}
	want := `bind-key a display-popup -E -w 80% -h 75% -d '#{pane_current_path}' '/opt/agentctl/bin/agentctl'`
	if got != want {
		t.Fatalf("binding =\n%s\nwant\n%s", got, want)
	}
}

func TestPopupBindingQuotesSpacesForTheShell(t *testing.T) {
	got, err := PopupBinding("/Users/Jo Smith/go/bin/agentctl")
	if err != nil {
		t.Fatal(err)
	}
	// tmux strips the single quotes; the popup's sh -c then sees one double-quoted word.
	if !strings.HasSuffix(got, ` '"/Users/Jo Smith/go/bin/agentctl"'`) {
		t.Fatalf("binding = %s", got)
	}
}

func TestPopupBindingRejectsUnquotablePaths(t *testing.T) {
	for _, p := range []string{"", "agentctl", "bin/agentctl", "/a'b/agentctl", "/a\"b/agentctl", "/a$b/agentctl",
		"/a`b`/agentctl", "/a#b/agentctl", "/a\\b/agentctl", "/a\nb/agentctl", "/a!b/agentctl"} {
		if got, err := PopupBinding(p); err == nil {
			t.Errorf("PopupBinding(%q) = %q, want an error", p, got)
		}
	}
}
