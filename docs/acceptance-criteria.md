# Acceptance criteria

> The default bar for "done" in this project. Kanban issues may override
> any of these via their `acceptance_criteria` frontmatter field. Run
> `/tb:verify <issue-id>` to evaluate against this doc + the issue's
> overrides.

## Defaults

1. **Tests cover the new code.** Every line of new behaviour is covered
   by unit or integration tests as appropriate.
2. **The change satisfies the linked kanban issue's stated requirements.**
3. **The change is consistent with `docs/ARCHITECTURE.md`.** No new
   modules / data flows / invariants without a doc update.
4. **All existing tests still pass; doctor is green.**
5. **No new lint or type errors introduced.**

## Per-issue overrides

Set `acceptance_criteria` in the kanban issue's frontmatter to:

- a Markdown bullet list of additional criteria,
- a relative path to a more detailed doc,
- or both (path on the first line, bullets after).

## Anti-patterns

- ❌ Closing an issue without running the acceptance pass.
- ❌ Updating the bar mid-flight to make a failing change pass.
- ❌ Replacing real tests with snapshot-only or smoke-only coverage.
