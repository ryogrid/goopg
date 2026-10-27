# M0147-0044 — retire `GOOPG_MEMOIZE` (process-wide Memoize kill switch)

Status: landed 2026-10-27.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm, rebase its tests onto the PG switch). Lever count 7 → 6 (`scripts/lever-count.sh`). Inventory row:
`analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

PG gates Memoize on `enable_memoize`, per session (`get_memoize_path`, `postgres/src/backend/optimizer/path/joinpath.c`).
goopg has two Memoize arms:
- the searched arm, `getMemoizePath` in `internal/optimizer/joinpathsmemoize.go`;
- the legacy post-search NLI insertion rule, `maybeAttachMemoize` in `memoize.go`, reached from `rewriteJoinsToNLI`.

Both read a process-wide `memoizeOn` atomic, which `GOOPG_MEMOIZE=off` cleared at start and `SetMemoizeEnabled` set
from tests. take2 P2-02c had already moved the session `SET enable_memoize` onto `PlannerSettings.EnableMemoize`
(`cp.enableMemoize`), but only the searched arm read it. The legacy arm still read the global, so a session's
`SET enable_memoize = off` did not reach it. That contradicted `getMemoizePath`'s own gate-1 note ("kills both arms").

## Removed / changed

- `memoize.go`: `memoizeOn`, its `init`, `memoizeFromEnv`, `SetMemoizeEnabled`, `MemoizeEnabled`, and the `os` and
  `sync/atomic` imports. `maybeAttachMemoize(nli, enableMemoize)` now takes the statement's setting.
- `nl_index_join.go`: `rewriteJoinsToNLI` passes `ps.EnableMemoize` into `walkRewriteNLI`, which threads it through
  its recursion (10 call sites) to the insertion rule. **Behaviour change:** `SET enable_memoize = off` now also
  suppresses the legacy arm's Memoize, per statement, as PG's GUC does.
- `joinpathsmemoize.go`: `getMemoizePath` gates on `cp.enableMemoize` alone.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0044)`). `scripts/planner-flags.env` is regenerated.
- Comments:
  - `cmd/goopg/main.go`, `parallel.go`, `exists_to_any.go`, `unnest.go` and `nl_index_join.go` no longer point at
    the deleted symbols.
  - The `docs/wiki/modules/optimizer.md` env-table row is removed.

`deadcode ./cmd/...` was diffed before and after. The exported `SetMemoizeEnabled` and `MemoizeEnabled`, both
test-only, left the unreachable list; nothing was added to it.

## Tests

- `TestMemoizeAttachKillSwitch` now calls `maybeAttachMemoize(nli, false)`, the statement's `enable_memoize = off`.
  The other attach tests pass `true`.
- The `getMemoizePath` "switch off" subtest uses a `costParams` copy with `enableMemoize = false`.
- `TestEnableMemoizeIsPerStatementNotProcessGlobal` loses its check that the global stayed set; the global is gone.
- `internal/executor` `TestMemoizeExplainAndResults`: three `SetMemoizeEnabled(true)` pins are deleted. Its off arm
  now plans through `runSQLWith` with `PlannerSettings{EnableMemoize: false}`.
  - Without the cache the cost model prefers a Hash Join, so the arm also turns hash and merge joins off. It is then
    the bare NLI the test's docstring promises.
  - It asserts that the off-arm plan is still a Nested Loop, so the results comparison cannot go vacuous.

## Inert proof (AGENT.md C6)

`git grep` over `scripts`, `ci`, `Makefile`, `.githooks`, bench shell and env files, `AGENT.md`, `CLAUDE.md` and
`docs/wiki` finds no setter. Unset, the global was on, so with `enable_memoize` at its default both arms behave
exactly as before. The fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0044 entry in `.ralph/fix_plan.md`.
