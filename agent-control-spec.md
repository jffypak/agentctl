# Agent Control — Product and Engineering Specification

**Status:** Draft v0.1  
**Date:** November 6, 2026  
**Working name:** `agentctl` (placeholder)  
**Target environment:** macOS/Linux terminals, tmux, Git worktrees; Claude Code first, other agent CLIs later  
**Primary audience:** A software engineer who delegates parallel coding tasks to CLI agents but wants to retain focus, judgment, and control.

---

## 1. Executive summary

Build a **minimal, keyboard-driven terminal UI (TUI) for supervising and directing coding agents** working in independent Git worktrees and tmux sessions. Its purpose is not to display more agent output. Its purpose is to **reduce cognitive overload and context switching**.

The product should let an engineer:

1. See what work is underway, what has been verified, and what genuinely needs human attention.
2. Start or adopt agents running in separate Git worktrees, inspect their state, and interact with them directly.
3. Assign instructions to an individual agent without navigating a collection of terminals; queue instructions safely when the agent is busy.
4. Let agents produce, check, correct, and retry bounded units of work against explicit acceptance criteria.
5. Compose tasks into dependency-aware workflows that run independent work in parallel, verify the combined result, and return only failed units for correction.
6. Extend the system through **portable skills** (e.g., PR review, local testing), with workflows deciding when skills run and policies deciding which actions require approval.
7. Make human intervention an **exception path** rather than the default operating mode.

The product is **not** initially a new coding agent, a terminal emulator, or a general-purpose agent platform. It orchestrates and observes existing CLI agents using tmux and Git, with a small controller and a custom TUI.

### Product promise

> Delegate coding work, keep doing your own work, and return to one concise view that shows what happened, what passed its checks, and what needs your judgment.

### Success criterion

After being away for 30 minutes, the user should be able to open the TUI and understand **what needs attention in under a minute**, without opening every agent's transcript. This is a design target to measure, not a guaranteed outcome.

---

## 2. Problem and guiding principles

### The problem

A developer with several parallel coding agents currently has to reconstruct each agent's context, inspect logs, remember worktree/session mappings, identify stalled or failed work, decide which output is trustworthy, and repeatedly jump between tmux panes. Agent concurrency can increase the developer's review and interruption load rather than reduce it.

### Principles

- **Attention over activity.** Surface decisions, failures, verified results, and review items; make raw logs optional.
- **Outcomes over confidence.** Separate assertions by a model from independent evidence such as command exit codes, scope checks, and test results.
- **Skills describe capabilities; workflows describe order; policies constrain authority.** Do not conflate these layers.
- **Loops within nodes, dependencies between nodes.** A bounded task can retry locally; the graph handles ordering, fan-out/fan-in, and integration.
- **Return the failed unit, not the whole batch.** Never discard independently verified work because another unit failed.
- **Make decisions explicit.** Store why a task exists, what success means, what files it may touch, and when an approval is required.
- **Default to safe, reversible behavior.** Hard-to-reverse actions require explicit authorization, regardless of agent confidence.
- **Use existing infrastructure.** tmux owns terminal processes; Git owns code/worktree history. Do not rebuild either.
- **Keep the user in control.** One-keystroke access to any agent; no opaque agent-to-agent actions beyond defined permissions.
- **Progressive disclosure.** The initial screen stays small; diffs, logs, checks, graph, and skill details are one action away.

### Non-goals for the first release

- Implementing a new LLM or coding agent.
- Replacing tmux, Git, or the user's editor.
- Providing a large, always-visible multi-pane dashboard.
- Automatic production deployment or destructive database actions.
- Automatically merging pull requests without user approval.
- Guaranteeing program correctness from passing tests or AI code reviews.
- A general distributed scheduler or remote multi-user service.
- A self-updating memory that permanently promotes every model conclusion into a rule.

---

## 3. Target user workflows

### A. Begin a task

1. Open `agentctl` from a tmux key binding, preferably a popup; full-screen mode is also available.
2. Press `n` and describe a bounded task, or adopt a known running tmux agent.
3. The controller creates/registers a task, its Git worktree and tmux session, and starts the chosen agent.
4. The task runs in the background while the developer returns to their normal editor/terminal.

### B. Check progress without context switching

1. Open the popup.
2. See an **Attention inbox**, **Running**, and **Verified / Ready for review** sections.
3. Select an item to read a one-screen summary: goal, state, evidence, next action, and changes.
4. Inspect full logs, Git diff, or the original agent session only when necessary.

### C. Send an agent a prompt

1. Select an agent; press `p`.
2. Enter a message.
3. If the adapter reports that the agent is ready to accept input, deliver it via a supported input channel (tmux paste when appropriate).
4. If the agent is busy or its input-readiness is unknown, **queue the instruction** and show its delivery state. Do not inject keystrokes blindly.
5. Press `Enter` to focus/attach to the actual tmux pane for an unrestricted conversation; return to the manager afterward.

### D. Finish work with evidence

1. Agent completes an implementation unit.
2. Controller runs configured checks (tests, lint, typecheck, scope checks), stores exit status and logs, and decides whether to retry.
3. If checks fail, return the exact failure evidence and allowed correction scope to that unit, up to a configured budget.
4. When checks pass, optional review skills run, integration checks execute, and the result enters a review/approval queue when policy requires it.
5. Human inspects the concise evidence bundle and diff, then approves, requests changes, or rejects.

### E. Extend with a skill

1. Add or install a compatible `SKILL.md` directory, such as `pr-review` or `local-testing`.
2. The registry discovers its metadata and makes it available via the command palette.
3. Invoke it manually on a task/worktree, or reference it from a declarative workflow.
4. The controller runs the skill with explicitly scoped permissions and records an auditable result.

---

## 4. UX specification

### 4.1 Default form factor

**Preferred:** a short-lived tmux popup over the user's current work. **Alternative:** a full-screen TUI using the same views. The application must not require a persistent sidebar or continuous monitoring.

### 4.2 Default view: attention-first

```text
 agents / my-app                                  3 running · 1 needs you
 ───────────────────────────────────────────────────────────────────────────
 INBOX (1)                           RUNNING (3)              VERIFIED (1)

 NEEDS YOU
 > auth-migration      APPROVAL   12 checks passed · migration risk: high

 RUNNING AUTONOMOUSLY
   api-tests           Checking 2/3 · last failure: redirect case
   frontend            Implementing · feat/ui
   docs                Reviewing generated docs

 READY FOR REVIEW
   auth-refactor       Verified · tests + lint passed · 6 files changed

 ───────────────────────────────────────────────────────────────────────────
 ↑↓/jk move   ↵ inspect/attach   p prompt   d diff   s skills   g graph
 / filter    n new               e evidence q close
```

States and descriptions shown above are illustrative. Avoid persistent color-heavy cards, animated activity, and constant log streams.

### 4.3 Task detail view

For the selected task, display:

- Goal and last meaningful outcome (not merely last printed line).
- Agent / CLI type; worktree path; branch; tmux session/pane identity.
- Task state and whether user action is required.
- Verification status with individual checks and their timestamps.
- Changed file count and +/- diff summary; easy access to actual diff.
- Retry count and last failure evidence, if applicable.
- Current question, proposed next action, and risk/approval status.
- Prompt composer and queued-message delivery state.
- Access to raw terminal output and original tmux session.

### 4.4 Intervention screen

An attention item should be actionable rather than a mere notification:

```text
 auth-tests                                           BLOCKED 3/3 attempts

 Failed check:   npm run test:auth
 Evidence:       expected HTTP 302, received 200
                 tests/auth/redirect.test.ts:88
 Allowed scope:  tests/auth/**

 Suggested next step: inspect redirect-handler contract.

 [p] Send instruction    [d] Diff    [l] Logs    [r] Retry (confirm)
 [x] Cancel task         [Esc] Back
```

Do not automatically treat an agent's *suggested* next step as an established root cause.

### 4.5 Minimal command set

| Key | Action |
|---|---|
| `j/k` or arrows | Move between tasks / inbox items |
| `Enter` | Inspect; from detail, attach/focus tmux pane |
| `p` | Compose instruction for selected agent |
| `d` | Open Git diff for selected worktree |
| `e` | View verification evidence |
| `l` | View recent logs or full capture |
| `s` | Search and invoke installed skills |
| `g` | View dependency graph (text-first) |
| `n` | Start a new task/worktree/agent |
| `/` | Filter tasks and skills |
| `q` / `Esc` | Close or return |

Key bindings must be configurable. Do not silently overload a key to perform a destructive action.

---

## 5. Domain model

### 5.1 Core objects

| Object | Meaning |
|---|---|
| **Project** | Repository plus per-project configuration and policies |
| **Task** | Bounded unit of work, goal, inputs, outputs, scope, checks, dependencies, and risk |
| **Agent session** | Running CLI process plus stable tmux pane/session ID and adapter |
| **Worktree** | Isolated Git checkout associated with a task |
| **Skill** | Reusable, discoverable agent instructions and optional supporting resources/scripts |
| **Workflow** | Declarative graph of tasks/skills and dependency edges |
| **Check** | Independently executed, machine-observable acceptance condition |
| **Evidence** | Check output, exit code, diff, artifact, citation, log pointer, or reviewer finding |
| **Gate** | Rule evaluating evidence and deciding whether a transition is allowed |
| **Approval** | Explicit human decision for a consequence-bearing transition |
| **Event** | Append-only record of important state changes and actions |
| **Constraint** | Versioned, evidence-backed reusable project rule suggested by an accepted outcome |

### 5.2 Separate two state machines

**Agent state** describes process/interaction status:

`starting → idle/ready ↔ working ↔ awaiting_input → stopped/failed/unknown`

**Task state** describes the work outcome:

`planned → queued → running → checking → retrying | blocked | verified → review_required → approved | rejected → completed`

A stopped or idle agent does **not** imply a verified or completed task. Status may be `unknown` if hooks are missing or ambiguous.

### 5.3 Durable identity

Persist explicit mappings among `task_id`, `worktree_path`, `repo_id`, `branch`, `agent_type`, `tmux_session_id`, and **tmux pane ID**. Do not infer identity solely from the current directory or command name. Support controller restarts and reconnect to still-running sessions.

### 5.4 Suggested storage

Use **SQLite** for session registry, tasks, status, messages, workflows, events, checks, and approvals. Store bulky logs and artifacts as files referenced from the database. Keep project config and skills in ordinary version-controlled files. The database must not contain raw secrets.

---

## 6. Architecture

```text
                       ┌───────────────────────────────┐
                       │  TUI (Go + Bubble Tea)        │
                       │  Inbox / Tasks / Skills / Diff│
                       └───────────────┬───────────────┘
                                       │ commands + event stream
                       ┌───────────────▼───────────────┐
                       │  Local controller             │
                       │  task/session registry        │
                       │  workflow scheduler           │
                       │  verifier / retry policy      │
                       │  approvals / notifications    │
                       └──────┬────────┬────────┬──────┘
                              │        │        │
                    ┌─────────▼──┐ ┌───▼──────┐ ┌──▼────────────┐
                    │ Skill      │ │ Agent    │ │ Git/tmux      │
                    │ registry & │ │ adapters │ │ integration   │
                    │ runner     │ │          │ │              │
                    └─────────┬──┘ └───┬──────┘ └──┬────────────┘
                              │        │           │
                       ┌──────▼────────▼───────────▼───────┐
                       │ Worktrees + tmux agent processes  │
                       │ Logs / check results / artifacts │
                       └───────────────────────────────────┘
```

### 6.1 TUI

- Presentation only. Never store authoritative task state solely in the UI.
- Subscribe to controller events or poll a well-defined local API.
- May exit at any time without killing agent processes.

### 6.2 Controller

- Owns task state transitions, scheduling, prompt queue, evidence, retries, and approval decisions.
- Must persist enough data to recover after restart.
- For MVP, the controller may be a single local process invoked by the TUI; a separate daemon is optional until background scheduling and delivery need it.
- Process lifetime must not accidentally determine agent lifetime.

### 6.3 tmux integration

- Discover sessions/panes using `tmux list-panes` / `list-sessions` and stable IDs.
- Obtain recent output using `tmux capture-pane`.
- Focus or attach to the selected session/pane.
- For supported interactive agents, use tmux buffers/paste and an explicit submit action **only when the agent is known to be input-ready**; otherwise queue or prompt the user to attach.
- A tmux pane is a process surface, **not a reliable structured agent protocol**. State and completions should favor agent hooks/APIs.
- Support both controller-created sessions and explicitly adopted existing ones. Adoption may have limited metadata or status confidence.
- Optional compatibility with workmux is allowed, but must not be required for core operation.

### 6.4 Git integration

- Create and list Git worktrees; associate one task/worktree when mutating code.
- Show branch, base revision, working-tree diff, changed files, and conflict status.
- Enforce allowed-file scope during verification.
- Distinguish worktree isolation from external-system isolation (ports, databases, credentials, deployment targets).
- Use a controlled integration worktree/branch to combine outputs and run integration tests; do not assume that independently passing branches work together.

### 6.5 Agent adapters

Define a small interface, conceptually:

```go
// Illustrative interface: refine during implementation.
type AgentAdapter interface {
    Start(ctx context.Context, spec AgentLaunchSpec) (AgentHandle, error)
    Observe(ctx context.Context, handle AgentHandle) (AgentObservation, error)
    Deliver(ctx context.Context, handle AgentHandle, msg Message) (DeliveryReceipt, error)
    Stop(ctx context.Context, handle AgentHandle) error
}
```

Start with **Claude Code** (including lifecycle hooks if available). Add Codex/OpenCode behind adapters later. Adapters report observations; they do **not** independently decide that a task is verified or approved.

---

## 7. Skills: generic extension system

### 7.1 Core design

Use the existing **Agent Skills** convention (`SKILL.md` with metadata and instructions) as the primary authoring format. Skills may include scripts, references, templates, or tools. Avoid a new proprietary skill-document format.

The orchestrator adds its own **execution contract** around a skill: inputs, outputs, permissions, triggers, and verification. A skills file alone is a set of instructions, **not** an independently enforced policy.

Example layout:

```text
.agents/
  skills/
    pr-review/
      SKILL.md
    local-testing/
      SKILL.md
      scripts/run-tests.sh
    security-audit/
      SKILL.md

.agentctl/
  config.yaml
  workflows/verify.yaml
  policies.yaml
```

### 7.2 Example skill

```markdown
---
name: pr-review
description: Review code changes for actionable bugs and regressions.
---

# PR review

1. Compare the task branch against the configured base revision.
2. Inspect affected callers, tests, and assumptions.
3. Report actionable findings with file/line evidence.
4. Do not modify implementation files.
5. Distinguish confirmed defects from suspected issues.
```

### 7.3 Discovery and invocation

Support three invocation paths:

1. **Manual:** select task → `s` → `pr-review`.
2. **Event-driven:** a workflow configured for `task.ready_for_review` runs tests and review skills.
3. **Planner-suggested (later):** a task planner proposes skills, which are accepted only if installed, allowed by policy, and compatible with the task.

Use metadata for search and discovery; load full skill instructions only when invoked to conserve context and tokens.

### 7.4 Runner contract

A skill execution must carry:

- Skill name and version/content hash.
- Target project, task, base revision, and worktree.
- Explicit input artifacts and allowed tool/command access.
- Execution mode: deterministic command, CLI agent, or external tool/MCP adapter.
- Timeout, retry budget, resource locks, and cancellation support.
- Structured result envelope: `status`, `summary`, `evidence[]`, `artifacts[]`, `findings[]`, `errors[]`, and provenance.

**Do not equate a model review's `status: passed` with deterministic correctness.** A review provides findings and a limited signal; deterministic checks provide machine-observed evidence.

### 7.5 Permissions and skill safety

- Skills are untrusted until reviewed/approved. Installing a skill does not grant new credentials or permissions.
- Define allowed commands, writable paths, external-network access, available MCP tools, and secret exposure per execution.
- Enforce safety at the controller/execution boundary, not just in natural-language instructions.
- A PR-review skill should be read-only by default.
- Shared resources (test DBs, services, ports, caches) need locks or isolated namespaces.
- Long-running or risky operations need timeouts, cancellation, and explicit approval.

---

## 8. Workflows and dependency graph

### 8.1 Model

A workflow is a **directed acyclic dependency graph for each planned execution**, with explicit bounded retry edges handled inside nodes. A graph node has a contract (input, output, checks); a dependency edge exists only when the downstream node requires the upstream result or a shared resource constraint forces ordering.

Supported node types (implementation-level vocabulary):

- **Splitter / planner:** proposes bounded units, dependencies, checks, and scope.
- **Worker:** uses an agent or skill to produce an artifact.
- **Code / deterministic node:** merge, parse, compare, deduplicate, execute tests, or compute diffs without an LLM.
- **Gate:** evaluates evidence and risk; may block, return one task for correction, or request human approval.

An agent isn't required for every node. Prefer code for deterministic transformations.

### 8.2 Example workflow definition

```yaml
# Proposed .agentctl/workflows/verify.yaml (custom controller schema)
name: verify-change
on:
  - task.ready_for_review
jobs:
  tests:
    skill: local-testing
    target: task.worktree
  review:
    skill: pr-review
    target: task.worktree
  approval:
    needs: [tests, review]
    gate: merge-approval
```

Tests and review can run independently (subject to resource constraints). The approval gate waits for both. The controller must avoid infinite retriggers from events produced by its own workflow.

### 8.3 Correction loops (inside a node)

Every bounded implementation node can follow:

`produce → run checks → if red, return evidence and bounded scope → correct → repeat`

- Write acceptance checks **before** or at task creation, not only after the model declares work done.
- Evaluate actual exit codes and outputs; missing checks are not a pass.
- Retry **only the failed unit**.
- Default to no more than **3 correction attempts**; make the budget configurable and escalate on exhaustion.
- Return `unit_id`, failed check, exact evidence, and allowed correction scope.
- Treat repeated failure as a signal to reassess either implementation, environment, or plan; don't assume the plan is necessarily wrong.
- Include time, token, and compute budgets to avoid runaway loops.

### 8.4 Integration gate

After independent units verify:

1. Combine changes in a controlled integration checkout.
2. Detect merge conflicts and overlapping file changes.
3. Run cross-unit/integration checks.
4. If integration fails, identify the smallest responsible unit(s) where possible; otherwise escalate with evidence.
5. Preserve successful units rather than re-running the entire batch without cause.
6. Enter human approval before merge under the initial policy.

### 8.5 Long-term learning edge

Accepted results may yield **candidate constraints** to improve future task planning, such as “API adapters must preserve keyword arguments and defaults.”

Each candidate must include scope, source task, supporting checks/evidence, proposed wording, and a human review path. Store accepted constraints in a versioned project knowledge file that the splitter reads on later runs. Avoid silent model-authored rules, unbounded accumulation, and unsupported generalization.

This learning edge is **post-MVP**.

---

## 9. Verification, risk and approvals

### 9.1 Evidence hierarchy

Prefer evidence in this order:

1. Deterministic checks (tests, lint, typecheck, diff-scope checks, security scanners, reproducible commands).
2. Integration checks and trajectory (what failed, what changed, whether the retry narrowed the issue).
3. Historical reliability of a specific workflow/skill when enough observations exist.
4. Agent-generated summaries and subjective assessments, clearly labeled as such.

A `0` exit code is evidence that a command succeeded under its conditions, not proof that the feature meets every unstated requirement.

### 9.2 Initial risk policy

| Class | Examples | Policy |
|---|---|---|
| Contained/reversible | Docs, unit tests, isolated functions | Allow automatic local iteration and verification; require approval before merge initially |
| Reversible but broad | Shared utilities, public API changes, schema additions | Extra tests and explicit review before merge |
| Hard to reverse | Production data writes/deletions, migrations with destructive steps, deployments, financial actions | Do not execute consequential operation without explicit human authorization; checks alone never waive this |

Risk is based on **impact and reversibility**, not the model's confidence score. The controller should ask approval at the boundary of consequence, not for every intermediate model step.

### 9.3 Approval record

Capture `who/when`, task/workflow, proposed action, risk category, diff/plan hash, evidence snapshot, and decision. If the material being approved changes afterward, invalidate or re-request approval as appropriate.

---

## 10. Task contract and events

### 10.1 Task example

```yaml
id: auth-tests
name: Add authentication integration tests
agent: claude
repo: my-app
worktree: ../worktrees/auth-tests
base_ref: main

depends_on: []
allowed_paths:
  - 'tests/auth/**'

checks:
  - id: auth-tests
    command: npm run test:auth
    timeout_seconds: 180
  - id: lint
    command: npm run lint
    timeout_seconds: 180

retry:
  max_attempts: 3
risk: contained
approval: before_merge
```

The schema is illustrative and belongs to `agentctl`, not to the Agent Skills standard.

### 10.2 Event examples

```json
{"type":"agent.started","task_id":"auth-tests","pane_id":"%12"}
{"type":"agent.needs_input","task_id":"auth-tests","question":"Which test DB?"}
{"type":"task.check_failed","task_id":"auth-tests","check_id":"auth-tests","attempt":1}
{"type":"task.verified","task_id":"auth-tests","evidence_id":"ev_104"}
{"type":"approval.requested","task_id":"auth-tests","action":"merge","risk":"contained"}
```

Timestamps, event IDs, causal references, and persistence are required in the actual event schema. Events should support UI refresh, audit/debugging, retries, and crash recovery.

---

## 11. MVP scope, sequence, and acceptance criteria

### Phase 1 — Useful agent manager (recommended first deliverable)

**Must have**

- Go + Bubble Tea keyboard-first TUI, launched full-screen or through tmux popup.
- Scan/register tmux sessions and explicit mappings to Git worktrees.
- Launch a Claude Code agent in a newly created worktree, or adopt an existing known pane.
- List tasks by attention status and show branch/worktree/session.
- Capture recent output; attach/focus full tmux terminal.
- Compose and safely deliver or queue prompts with visible delivery state.
- Show Git diff summary and full diff on demand.
- Persist session/task registry; reconnect after TUI restart.
- Clear `unknown` state when robust status is unavailable.

**Acceptance criteria**

- Start two agents in separate worktrees and switch between them from the TUI.
- Close the TUI; agents continue running; reopen and recover mappings.
- Select either agent, view recent output and diff, and return without searching tmux sessions manually.
- A message queued while an agent is busy is not injected into its active terminal unexpectedly.
- A stale/missing pane is detected and shown as disconnected rather than misattributed to another agent.

### Phase 2 — Verified tasks and attention inbox

**Must have**

- Task contracts with goals, allowed files, commands, retry budgets, and risk.
- Controller-executed checks and structured evidence.
- Correction loop with exact failure feedback, max attempts, and cancellation.
- Distinct task/agent states; meaningful inbox of blocked, failed, and review-ready items.
- Manual skill discovery/invocation for `local-testing` and `pr-review`.
- Default human approval before merge.

**Acceptance criteria**

- On a reproducible failing test, the controller captures nonzero exit and relevant output and requests a bounded correction.
- A task that reaches its retry limit stops and produces one actionable inbox item.
- A task cannot become `verified` if required checks did not execute or its changed files exceed allowed scope.
- PR review output is labeled as model findings, not a deterministic pass.
- No deployment, destructive database command, or merge occurs merely because an agent says it succeeded.

### Phase 3 — Skill workflows and graph

**Must have**

- Declarative workflow with dependency edges and parallel-ready execution.
- Event-triggered skills, deduplicated by task/revision/workflow identity.
- Integration checkout and cross-unit checks.
- Return only failed units for corrections when attribution is possible.
- Resource locks, cancellation, retry budgets, execution provenance, and workflow recovery.
- Text-first graph view with task states and blockers.

**Acceptance criteria**

- Independent tests and review start concurrently when the workflow permits.
- A downstream gate waits for all required upstream results.
- Failure in one worker does not automatically cause verified, unaffected siblings to rerun.
- Repeated events or controller restarts do not duplicate consequential executions.

### Phase 4 — Expanded platform (deferred)

- More agent adapters (Codex, OpenCode, others).
- Planner/splitter that suggests task decomposition and graph dependencies.
- Human-approved reusable constraints and versioned project knowledge.
- Remote sessions, multi-project support, advanced analytics and notifications.
- More expressive risk policies and safe low-risk auto-merge experiments (opt-in only).

### Rough implementation sizing

These are **indicative hands-on engineering estimates with AI assistance**, not commitments:

| Phase | Approximate effort |
|---|---|
| Phase 1 | 1–2 days for a useful prototype; reliability may take longer |
| Phase 2 | 2–4 additional days |
| Phase 3 | 3–5 additional days |
| Phase 4 | Scope-dependent |

The major uncertainty is correctness of status detection, safe prompt delivery, process recovery, and integration behavior across real projects. Build and measure these before optimizing the design.

---

## 12. Engineering quality and test strategy

Test the system at three levels:

1. **Unit:** task state transitions, graph readiness, policy evaluation, scope validation, retries, message queue semantics, event deduplication.
2. **Integration:** real temporary Git repos/worktrees, disposable tmux server/session, fake agent CLI, actual command exit statuses and output capture.
3. **End-to-end:** Claude Code in a nonproduction sandbox, start/stop/reconnect, blocked agent, successful verification, failed verification, approval required, and recovery after controller restart.

Additional invariants:

- No queued message may be delivered twice because of a restart without an idempotency check or user-visible recovery state.
- No command may run with permissions broader than its policy allows.
- No task gets marked verified unless all required checks completed successfully against the intended revision.
- Approval is bound to a specific material change / artifact revision.
- A failed or unknown integration state must never be rendered as success.
- Redact secrets from logs and evidence where possible; keep sensitive local data local.
- Treat cloned repos, skill packages, tool output, and model output as untrusted input.
- Require explicit cleanup/retention behavior before deleting worktrees or stopping agents.

Track performance with: time to understand inbox, manual terminal switches per task, unnecessary interruptions, proportion of tasks requiring human intervention, retries per verified task, false 'done' statuses, duplicated work, and time spent reviewing diffs. Prioritize reduced user attention **without** compromising defect detection.

---

## 13. Decisions and open questions

### Decisions made for this draft

- **UI:** Minimal keyboard-driven TUI; preferred invocation is a tmux popup; full-screen supported.
- **Core stack:** Go + Bubble Tea, tmux, Git worktrees, SQLite for durable local state.
- **Agent priority:** Claude Code first through an adapter; other CLIs later.
- **Extensibility:** Agent Skills-style `SKILL.md` packages; independent workflow and policy definitions.
- **Default safety:** Human approval before merge; explicit authorization for high-consequence actions.
- **Build order:** Session manager → verification loop/inbox → workflow graph → learning edge.
- **Dependency on workmux:** Optional integration/reference, not mandatory.

### Questions to resolve through prototyping rather than prolonged design

1. Which Claude Code hooks are sufficiently reliable for `working`, `awaiting_input`, and `ready` across real sessions?
2. Should the controller be a local daemon from day one, or first be a CLI with persistent processes and event callbacks?
3. What delivery acknowledgment can the first adapter actually guarantee for queued messages?
4. How should interactive terminal focus work across nested tmux and popup contexts?
5. What sandbox and command permission mechanism can be enforced rather than merely requested in prompts?
6. How should the system map multiple agents sharing a repo, task, or tmux session?

These do not block a Phase 1 prototype. Implement narrow interfaces so they can change with evidence.

---

## 14. Suggested coding-agent brief

> Build **Phase 1** of the `agentctl` specification in this document. Use Go, Bubble Tea, tmux, and Git. Start with a small modular codebase, a disposable tmux/Git integration test harness, and an explicit task↔worktree↔pane registry. Prioritize keyboard navigation, reliable reconnection, safe prompt queueing, and Git diff inspection. Do not build the workflow graph, autonomous planner, or learning system yet. Implement a minimal agent adapter interface but only one backend initially. Treat process state as unknown when it cannot be inferred reliably. Show a runnable demo with two agents in separate worktrees and tests for failure cases. Keep the UI compact and attention-first.

---

## 15. Definition of success

The product is successful when it lets the user **do their own engineering work while agents operate in parallel**, and then inspect **one trustworthy, compact account** of what is running, what was checked, what failed, and which decisions require human judgment.

The central question is not **“How many agents can I run?”** It is **“How much correctly bounded, verifiable work can happen without asking me to supervise every step?”**
