# M0147-0047 — retire `GOOPG_PARTIAL_AGG_PATHS` (the size-rule split verdict)

Status: landed 2026-10-27.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 4 → 3 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

C-19g replaced goopg's partial-aggregation split gate, `splitAggregateIsProfitable` (`parallel_agg.go`), with a
priced two-candidate path tournament: `createPartialGroupingPaths`, adjudicated by `addPath`. It ports PG's
`create_partial_grouping_paths` (`postgres/src/backend/optimizer/plan/planner.c`).
- The old gate used five non-PG constants (`cXfer`, `cTrans`, `cHash`, `cMerge`, `cOut`). Its own comment called
  them "calibrated against one query".
- `GOOPG_PARTIAL_AGG_PATHS` chose between the two. It defaulted to `on` from 2026-09-07; `off` kept the size rule as
  the serial control arm.
- Two sites read it:
  - `partialAggSplitPays`, the post-pass verdict, which delegated to the size rule when off;
  - `addPartialAggSplitPath`, the upper-rel split producer, which refused when off.

## Measurement first (the M0147-0020 recipe)

Before cutting, the census ran with `GOOPG_PARTIAL_AGG_PATHS=off` at `3a8bfaa96`. The flag stamp in each capture
confirms the arm reached the servers. Evidence: `analysis/m0147/m0147-0047/`.

| corpus | OFF (size rule) | ON (default) |
|---|---|---|
| TPC-H | 10 / 22 | 22 / 22 |
| TPC-DS SF0.25 | 78 / 99 | 99 / 99 |
| TPC-DS SF1 | 74 / 99 | 99 / 99 |

The OFF arm is reachable and far from PG. The tournament is the PG-faithful arm and is kept.

## Removed / changed

- `partialaggpaths.go`: `partialAggMode` with its constants, the env read, `partialAggModeFromEnv`,
  `partialAggModeLabel`, `setPartialAggPathsModeForTest`, the exported `SetPartialAggPathsMode`, the OFF branch of
  `partialAggSplitPays`, and the `os` and `strings` imports.
- `partialaggupper.go`: the mode refusal in `addPartialAggSplitPath`.
- `parallel_agg.go`: the size rule `splitAggregateIsProfitable`, its five constants, and `parallelDivisor`, a private
  duplicate of `getParallelDivisor` (`cost_funcs.go`) whose only production caller was the size rule.
  `groupsToRowsRatio` and `aggColumnStats` stay because the tournament's blind estimate still reads them.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0047)`). `scripts/planner-flags.env` is regenerated.

`deadcode ./cmd/...` was diffed before and after. The setters left the unreachable list, and so did
`parallelDivisor`, which the size rule's deletion had briefly made unreachable. Nothing was added.

## Tests

- 31 `setPartialAggPathsModeForTest(partialAggPathsOn)` pins in `partialaggupper_test.go` and one in
  `partial_lateral_test.go` are deleted. ON was already the default.
- `TestUpperSplitFailsClosed` loses its mode column and its "knob off" case. The two PG refusals stay:
  `ParallelStatementOK` and `max_parallel_workers_per_gather = 0`.
- Deleted, because each pinned only the OFF arm or the size rule:
  - `TestUpperSplitModeRefusalWithTraceLine`;
  - `TestPartialAggModeOffIsTheSerialControlArm`;
  - `TestPartialAggModeLabelRoundTrips`;
  - the six `TestSplitGate*` tests;
  - `TestParallelDivisorMatchesUpstream`, a duplicate of `TestGetParallelDivisor`.
- `TestPartialAggModeOnPricesWhatTheSizeRuleRefused` keeps its premise through `groupsToRowsRatio`, minus the
  deleted rule's own check.
- `internal/executor` `runAggSplitPriced` no longer flips the mode.

## Inert proof (AGENT.md C6)

`git grep` over `scripts`, `ci`, `Makefile`, `.githooks`, bench shell and env files, `AGENT.md`, `CLAUDE.md` and
`docs/wiki` finds no setter. Unset, the mode resolved to `on`, which is the only arm left. The fire-set A/B and the
census confirm no plan change.

## Gates

See the M0147-0047 entry in `.ralph/fix_plan.md`.
