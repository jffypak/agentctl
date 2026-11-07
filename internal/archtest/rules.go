package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// Rules returns every ARCHITECTURE.md §Code conventions 6 rule.
func Rules() []Rule {
	return []Rule{
		ruleDomainImports(),
		ruleTUIImports(),
		ruleControllerHookingest(),
		ruleExecOwnership(),
		rulePasteSendKeys(),
		ruleKillCommands(),
		ruleGitxNonDestructive(),
	}
}

// DomainAllowlist is the set of pure stdlib packages internal/domain may import.
var DomainAllowlist = map[string]bool{
	"bytes": true, "cmp": true, "errors": true, "fmt": true, "maps": true, "math": true,
	"regexp": true, "slices": true, "sort": true, "strconv": true, "strings": true,
	"time": true, "unicode": true, "unicode/utf8": true,
}

func ruleDomainImports() Rule {
	return Rule{
		ID:  "domain-imports",
		Doc: "internal/domain imports only an allowlist of pure stdlib packages (no I/O, no module packages)",
		Check: func(m *Module) []Violation {
			var out []Violation
			for _, f := range m.Files {
				if !f.in("internal/domain") || f.IsTest {
					continue
				}
				for _, imp := range f.imports() {
					if !DomainAllowlist[imp.path] {
						out = append(out, Violation{"domain-imports", f.pos(imp.pos), "domain imports " + strconv.Quote(imp.path) + " (not in the pure allowlist)"})
					}
				}
			}
			return out
		},
	}
}

func ruleTUIImports() Rule {
	return Rule{
		ID:  "tui-imports",
		Doc: "internal/tui does not import store, tmux, gitx, hookingest, database/sql, or sqlite",
		Check: func(m *Module) []Violation {
			banned := []string{
				m.Path + "/internal/store", m.Path + "/internal/tmux", m.Path + "/internal/gitx",
				m.Path + "/internal/hookingest", "database/sql", "modernc.org/sqlite",
			}
			var out []Violation
			for _, f := range m.Files {
				if !f.in("internal/tui") || f.IsTest {
					continue
				}
				for _, imp := range f.imports() {
					for _, b := range banned {
						if hasPathPrefix(imp.path, b) {
							out = append(out, Violation{"tui-imports", f.pos(imp.pos), "tui imports " + strconv.Quote(imp.path) + " (presentation only: act through controller)"})
						}
					}
				}
			}
			return out
		},
	}
}

func ruleControllerHookingest() Rule {
	return Rule{
		ID:  "controller-hookingest",
		Doc: "internal/controller and internal/hookingest do not import each other",
		Check: func(m *Module) []Violation {
			ctl, hook := m.Path+"/internal/controller", m.Path+"/internal/hookingest"
			var out []Violation
			for _, f := range m.Files {
				var banned string
				switch {
				case f.in("internal/controller"):
					banned = hook
				case f.in("internal/hookingest"):
					banned = ctl
				default:
					continue
				}
				for _, imp := range f.imports() {
					if hasPathPrefix(imp.path, banned) {
						out = append(out, Violation{"controller-hookingest", f.pos(imp.pos), f.Dir + " imports " + strconv.Quote(imp.path)})
					}
				}
			}
			return out
		},
	}
}

// ruleExecOwnership: exec.Command("tmux"...) only in internal/tmux;
// exec.Command("git"...) only in internal/gitx and internal/testenv (all files).
func ruleExecOwnership() Rule {
	return Rule{
		ID:  "exec-ownership",
		Doc: `exec.Command("tmux") only in internal/tmux; exec.Command("git") only in internal/gitx and internal/testenv`,
		Check: func(m *Module) []Violation {
			var out []Violation
			for _, f := range m.Files {
				execName := f.importName("os/exec")
				if execName == "" {
					continue
				}
				ast.Inspect(f.Syntax, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if x, ok := sel.X.(*ast.Ident); !ok || x.Name != execName {
						return true
					}
					idx := -1
					switch sel.Sel.Name {
					case "Command":
						idx = 0
					case "CommandContext":
						idx = 1
					}
					if idx < 0 || len(call.Args) <= idx {
						return true
					}
					lit, ok := call.Args[idx].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					name, _ := strconv.Unquote(lit.Value)
					switch filepath.Base(name) {
					case "tmux":
						if !f.in("internal/tmux") {
							out = append(out, Violation{"exec-ownership", f.pos(call.Pos()), "exec of tmux outside internal/tmux (use tmux.Runner with an explicit socket)"})
						}
					case "git":
						if !f.in("internal/gitx") && !f.in("internal/testenv") {
							out = append(out, Violation{"exec-ownership", f.pos(call.Pos()), "exec of git outside internal/gitx and internal/testenv"})
						}
					}
					return true
				})
			}
			return out
		},
	}
}

// rulePasteSendKeys: the only path that writes into a pane (invariant 3).
// The paste-buffer / send-keys vocabulary may be spelled only in
// tmux/buffer.go (the builders), testenv, e2e and tests; the builders
// (identifiers named PasteBuffer* / SendKeys*) may be used only from
// tmux/buffer.go, claude/deliver.go, testenv, e2e and tests.
func rulePasteSendKeys() Rule {
	return Rule{
		ID:  "paste-sendkeys",
		Doc: "paste-buffer/send-keys builders used only from tmux/buffer.go, claude/deliver.go, testenv, e2e, _test.go",
		Check: func(m *Module) []Violation {
			var out []Violation
			for _, f := range m.Files {
				if f.IsTest || f.in("internal/testenv") || f.in("internal/e2e") {
					continue
				}
				builders := f.Rel == "internal/tmux/buffer.go"
				users := builders || f.Rel == "internal/adapter/claude/deliver.go"
				if !builders {
					f.stringLits(func(s string, pos token.Pos) {
						for _, tok := range tokens(s) {
							if tok == "paste-buffer" || tok == "pasteb" || tok == "send-keys" {
								out = append(out, Violation{"paste-sendkeys", f.pos(pos), strconv.Quote(tok) + " spelled outside tmux/buffer.go"})
							}
						}
					})
				}
				if !users {
					ast.Inspect(f.Syntax, func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok && isPasteIdent(id.Name) {
							out = append(out, Violation{"paste-sendkeys", f.pos(id.Pos()), id.Name + " used outside tmux/buffer.go and claude/deliver.go"})
						}
						return true
					})
				}
			}
			return out
		},
	}
}

func isPasteIdent(name string) bool {
	n := strings.ToLower(strings.ReplaceAll(name, "_", ""))
	return strings.Contains(n, "pastebuffer") || strings.Contains(n, "sendkeys")
}

// ruleKillCommands: kill-session / kill-pane (and kill-window) only in
// tmux/kill.go and claude/stop.go; kill-server only in testenv. All files.
func ruleKillCommands() Rule {
	return Rule{
		ID:  "kill-commands",
		Doc: "kill-session/kill-pane only in tmux/kill.go and claude/stop.go; kill-server only in testenv",
		Check: func(m *Module) []Violation {
			var out []Violation
			for _, f := range m.Files {
				killOK := f.Rel == "internal/tmux/kill.go" || f.Rel == "internal/adapter/claude/stop.go"
				serverOK := f.in("internal/testenv")
				f.stringLits(func(s string, pos token.Pos) {
					for _, tok := range tokens(s) {
						switch tok {
						case "kill-session", "kill-pane", "killp", "kill-window", "killw":
							if !killOK {
								out = append(out, Violation{"kill-commands", f.pos(pos), strconv.Quote(tok) + " outside tmux/kill.go and claude/stop.go"})
							}
						case "kill-server":
							if !serverOK {
								out = append(out, Violation{"kill-commands", f.pos(pos), `"kill-server" outside internal/testenv`})
							}
						}
					}
				})
			}
			return out
		},
	}
}

// GitxForbiddenTokens is the destructive git vocabulary internal/gitx may
// never spell (reset --hard, worktree remove/prune, branch -D/-d/--delete,
// --force, clean -f, push, rm).
var GitxForbiddenTokens = map[string]bool{
	"--hard": true, "remove": true, "prune": true, "-D": true, "-d": true, "--delete": true,
	"-f": true, "clean": true, "push": true, "rm": true,
}

func ruleGitxNonDestructive() Rule {
	return Rule{
		ID:  "gitx-nondestructive",
		Doc: "internal/gitx never contains reset --hard, worktree remove, branch -D, --force, clean -f, or push",
		Check: func(m *Module) []Violation {
			var out []Violation
			for _, f := range m.Files {
				if !f.in("internal/gitx") {
					continue
				}
				f.stringLits(func(s string, pos token.Pos) {
					for _, tok := range tokens(s) {
						if GitxForbiddenTokens[tok] || strings.HasPrefix(tok, "--force") {
							out = append(out, Violation{"gitx-nondestructive", f.pos(pos), "destructive git vocabulary " + strconv.Quote(tok)})
						}
					}
				})
			}
			return out
		},
	}
}
