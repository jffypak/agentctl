package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckScriptFailsOnEachGateCondition copies the real scripts/check.sh
// (and doctor.sh) into tiny throwaway modules and proves the gate fails on
// non-empty gofmt -l, go vet, go test -race, and a go mod tidy diff, and
// passes on a clean module. It runs offline (GOPROXY=off, GOTOOLCHAIN=local).
func TestCheckScriptFailsOnEachGateCondition(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go toolchain subprocesses")
	}
	for _, tool := range []string{"bash", "go", "gofmt", "find", "xargs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	root, _ := moduleRoot(t)
	check, err := os.ReadFile(filepath.Join(root, "scripts", "check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	doctor, err := os.ReadFile(filepath.Join(root, "scripts", "doctor.sh"))
	if err != nil {
		t.Fatal(err)
	}

	base := map[string]string{
		"go.mod":    "module example.com/gate\n\ngo 1.27\n",
		"a.go":      "package gate\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n",
		"a_test.go": "package gate\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"add\")\n\t}\n}\n",
	}
	cases := []struct {
		name     string
		files    map[string]string
		wantFail string // "" = must pass
	}{
		{"clean", nil, ""},
		{"gofmt", map[string]string{"b.go": "package gate\nfunc  Sub(a,b int)int{return a-b}\n"}, "gofmt"},
		// self-assignment: caught by vet's assign analyzer, which go test's vet subset does not run
		{"vet", map[string]string{"c.go": "package gate\n\n// Self assigns x to itself.\nfunc Self() int {\n\tx := 1\n\tx = x\n\treturn x\n}\n"}, "go vet"},
		{"test", map[string]string{"d_test.go": "package gate\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"boom\") }\n"}, "go test -race"},
		{"tidy", map[string]string{
			"go.mod":     "module example.com/gate\n\ngo 1.27\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n",
			"dep/go.mod": "module example.com/dep\n\ngo 1.27\n",
			"dep/dep.go": "package dep\n",
		}, "go mod tidy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			write := func(rel, content string, mode os.FileMode) {
				p := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), mode); err != nil {
					t.Fatal(err)
				}
			}
			for rel, content := range base {
				write(rel, content, 0o644)
			}
			for rel, content := range c.files {
				write(rel, content, 0o644)
			}
			write("scripts/check.sh", string(check), 0o755)
			write("scripts/doctor.sh", string(doctor), 0o755)

			cmd := exec.Command("bash", filepath.Join(dir, "scripts", "doctor.sh"))
			cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off")
			out, err := cmd.CombinedOutput()
			if c.wantFail == "" {
				if err != nil {
					t.Fatalf("clean module failed the gate: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("gate passed although %s should fail:\n%s", c.wantFail, out)
			}
			if !strings.Contains(string(out), "FAIL: "+c.wantFail) {
				t.Fatalf("gate output does not name %q:\n%s", c.wantFail, out)
			}
			for _, other := range []string{"gofmt", "go vet", "go test -race", "go mod tidy"} {
				if other != c.wantFail && strings.Contains(string(out), "FAIL: "+other+"\n") {
					t.Fatalf("defect %q also failed %q (fixture is not isolated):\n%s", c.wantFail, other, out)
				}
			}
		})
	}
}
