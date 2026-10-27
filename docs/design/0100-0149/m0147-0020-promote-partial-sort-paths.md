# M0147-0020 — promote `GOOPG_PARTIAL_SORT_PATHS` (the C-19e partial-sort tournament)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 0 (promote-or-delete).
Lever count 23 → 22 (`scripts/lever-count.sh`).

## The lever

C-19e ([design](../planner-c19e-partial-sort/DESIGN.md)) decides, in the parallel post-pass
(`findPartialSubtree`, `internal/optimizer/parallel.go`), whether a Sort over a partial subtree runs on the worker
side (`Gather Merge -> Sort -> partial`) or the leader side (`Sort -> Gather -> partial`). There are two arms:

- **ON** — a two-candidate path tournament priced by PG's `cost_sort`, `cost_gather` and `cost_gather_merge`
  (`costSortRun`, `gatherCost`, `gatherMergeCost`), adjudicated by `addPath` / `setCheapest`. This is PG logic.
- **OFF** (the shipped default) — `sortPartialRootPays`, a goopg type switch over the driving scan.

Under R3 the ON arm is promoted, not discarded. The task's first step was to check whether the post-pass is still
reached at all, now that most Gathers come from the search.

## Measurement

The C6 trace used `GOOPG_PGSHAPED_DP_TRACE=1`, where `traceUpperGate` writes an `upper gate=sort` record at every
verdict. It ran on the full TPC-DS corpora (artefacts under `tmp/m0147-0020-trace/`, not committed):

| corpus | OFF: verdicts | ON: verdicts | ON vs OFF goopg plans | ON PLAN-PARITY |
|---|---|---|---|---|
| SF0.25 | 3 × `sort-rule` | worker 1, serial 1, declined 1 | byte-identical (0 diff lines) | 99 / 99 |
| SF1 | 2 × `sort-rule` | worker 1, serial 1 | byte-identical (0 diff lines) | 99 / 99 |

The post-pass is reached, so the task's branch was "flip ON, then delete the OFF arm". The flip changes no TPC-DS
plan. The TPC-H census lane runs serial (the estimate-audit arm pins `-serial=true`), so the post-pass cannot fire
there. TPC-H parallel values are covered by the acceptance arm (24 MATCH, see the fix_plan entry).

## Removed / changed

- `internal/optimizer/partialsortpaths.go`: `partialSortMode` and its constants, `partialSortPathsMode` and its env
  read, `partialSortModeFromEnv`, `partialSortModeLabel`, `setPartialSortPathsModeForTest`,
  `SetPartialSortPathsMode`, and the mode-off branch of `partialSortRootPays`. Also two imports.
- `sortPartialRootPays` (`parallel.go`) is **kept**. It still answers when no tournament can be run (no worker
  budget, or no usable input estimate: the `declined` verdict). Its doc comment and the call-site comment now say so.
- `flaglabels.go`: `GOOPG_PARTIAL_SORT_PATHS` moves to `flagProvenanceRetired` (`retired(M0147-0020)`), and
  `scripts/planner-flags.env` is regenerated.
- `incrementalsortpaths.go` comments no longer cite the retired flag or setter.

`deadcode ./cmd/...` was diffed before and after. Only the two deleted setters left its list, and nothing new
appeared.

## Tests

- Deleted: `TestPartialSortModeFromEnvIsFailClosed` (the resolver is gone) and
  `TestPartialSortDefaultDelegatesToRetiredRule` (its inertness property described the OFF default).
- `TestPartialSortVerdictIsCostDriven` and `TestPartialSortOnKeepsMergeKeysInvariant` drop their mode pins; they
  now test the shipped default.
- `TestPartialSortTournamentPricesBothArms` and the direct `sortPartialRootPays` tests are unchanged.

## Residual vs PG (ledger row 2026-10-26)

PG makes this choice while generating paths: `generate_useful_gather_paths`
(`postgres/src/backend/optimizer/path/allpaths.c`) and `create_ordered_paths` (`planner.c`) `add_path` the Gather
Merge and the Sort-over-Gather candidates on the real rels, under the session's cost GUCs. goopg still decides in a
post-pass, using `DefaultPlannerSettings().costParams()`, so a session `SET` of a cost GUC never reaches this
verdict (the gap `costParams.workMem`'s comment records, ledger M0127-P5.7-a). The promotion moves the arm onto PG's
cost functions. It does not move the decision into the search.
