# M0147-0017 — retire `GOOPG_DISABLE_NLI` (the M0054-0006 nested-loop-index rollback switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 1. Lever count 24 → 23
(`scripts/lever-count.sh`).

## What was retired

M0054-0006 added goopg's nested-loop-index (NLI) rewrite: a post-search pass, `rewriteJoinsToNLI`, that turns an
eligible `*Join` into a `*NestedLoopIndexJoin` probing a B-tree index. It came with a rollback switch:
`GOOPG_DISABLE_NLI=1`, read in `cmd/goopg/main.go`, called `optimizer.SetNLIEnabled(false)` on a process-wide atomic.
take2 P2-02c moved the real control onto the per-statement `PlannerSettings.EnableNestLoopIndex`, which the session
GUC `goopg.enable_nestloop_index` feeds (`internal/postmaster/dispatch.go`). The atomic survived only as the seed
`DefaultPlannerSettings` read, and the env switch was its only production setter.

PG has no such switch. Its parameterised index nested loop is an ordinary join path built by `add_paths_to_joinrel`
→ `match_unsorted_outer` (`postgres/src/backend/optimizer/path/joinpath.c`).

## Removed

- `cmd/goopg/main.go`: the `GOOPG_DISABLE_NLI` read and the now-unused `optimizer` import. The comment that called
  `SetNLIEnabled` "the test hook and the env default" now records the retirement.
- `internal/optimizer/nl_index_join.go`: `nliEnabled`, its `init` store, `SetNLIEnabled` and `NLIEnabled`.
  `rewriteJoinsToNLI`'s doc comment now names `PlannerSettings.EnableNestLoopIndex` as its kill-switch.
- `internal/optimizer/plannersettings.go`: `DefaultPlannerSettings` seeds `EnableNestLoopIndex: true` directly.
- Comments in `planner.go`, `unnest.go` and `internal/utils/misc/guc.go` that cited the setter.
- `docs/wiki/modules/cmd-goopg.md`: the env-table row and the flow-chart node.

The task text allowed keeping the setter when tests use it and no PG-equivalent switch exists. A per-statement route
already exists, so both test users were rebased and the setter was deleted.

## Tests

- **`TestNLIRuleRespectsKillSwitch`** (`internal/optimizer`) now plans with
  `PlanWithSettings(…, ps)` where `ps.EnableNestLoopIndex = false`. That is the same input a session SET produces.
- **`TestNLIResultParityVsHashJoin` / `TestNLIResultParityCompositeKey`** (`internal/testutil/tpch`) were vacuous.
  - Why: their "NLI off" arm called `optimizer.SetNLIEnabled(false)` in the test process. `cluster.New` runs the
    server as a separate process, so the toggle never reached it. And on their 5-6-row fixtures the default plan is
    a Hash Join anyway, so the "NLI on" arm was not NLI either.
  - Now: the arms compare the default plan with a nested loop forced by PG's own switches
    (`enable_hashjoin`, `enable_mergejoin`, `enable_seqscan` off). The switches are passed through the connection's
    startup `options` by a new `cluster.QueryWithOptions`, because `Query` opens a fresh connection per call.
  - `requireArmsPlanDifferently` asserts that the two EXPLAINs differ, so a switch that stops reaching the server
    fails loudly instead of passing.

## Finding (recorded, not fixed)

With hash join, merge join and seq scans off, adding `goopg.enable_nestloop_index=off` still did **not** change the
fixture's plan: the search built the nested loop itself, and `rewriteJoinsToNLI` did not fire. Whether that GUC
and the legacy post-pass still move any TPC-H/TPC-DS plan is unmeasured. M0147-0030's phase-2 inventory now carries
it, with a census under `PGOPTIONS='-c goopg.enable_nestloop_index=off'` as the first step (ledger row 2026-10-26).

## Inert proof (AGENT.md C6)

- The env read fired only for `1` / `true`. No script, CI config, bench config, git hook, Makefile, `AGENT.md` or
  `CLAUDE.md` sets the variable (`git grep`). Unset, the seed stayed `true`, and `DefaultPlannerSettings` now
  hard-codes that same value.
- The setter's other callers were tests. One of them (the cluster tests) never affected the server anyway.

## Gates

See the M0147-0017 entry in `.ralph/fix_plan.md`.
