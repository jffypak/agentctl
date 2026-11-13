package tmux

import (
	"context"
	"reflect"
	"testing"
)

func TestReturnKeyFrom(t *testing.T) {
	for _, tc := range []struct{ name, out, want string }{
		{"default", "bind-key    -T prefix       (                    switch-client -p\n" +
			"bind-key    -T prefix       L                    switch-client -l\n", "L"},
		{"repeat flag", "bind-key -r -T prefix L resize-pane -R 10\nbind-key -T prefix b switch-client -l\n", "b"},
		{"first wins", "bind-key -T prefix b switch-client -l\nbind-key -T prefix B switch-client -l\n", "b"},
		{"other flags on switch-client", "bind-key -T prefix L switch-client -l -Z\n", ""},
		{"none", "bind-key -T prefix L resize-pane -R 10\n", ""},
		{"empty", "", ""},
	} {
		if got := ReturnKeyFrom(tc.out); got != tc.want {
			t.Errorf("%s: ReturnKeyFrom = %q, want %q", tc.name, got, tc.want)
		}
	}
}

type keysRunner struct {
	reqs []Request
	out  string
}

func (r *keysRunner) Run(_ context.Context, req Request) (Result, error) {
	r.reqs = append(r.reqs, req)
	return Result{Stdout: []byte(r.out)}, nil
}

func TestClientReturnKey(t *testing.T) {
	rec := &keysRunner{out: "bind-key -T prefix b switch-client -l\n"}
	c := NewClient(rec, Socket{Path: "/tmp/sock"})
	key, err := c.ReturnKey(context.Background())
	if err != nil || key != "b" {
		t.Fatalf("ReturnKey = %q, %v", key, err)
	}
	if len(rec.reqs) != 1 || !reflect.DeepEqual(rec.reqs[0].Commands, [][]string{{"list-keys", "-T", "prefix"}}) {
		t.Fatalf("requests = %+v", rec.reqs)
	}
}
