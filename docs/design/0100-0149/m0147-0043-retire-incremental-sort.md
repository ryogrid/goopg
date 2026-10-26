# M0147-0043 — retire `GOOPG_INCREMENTAL_SORT` (third ORDERED arm switch)

Status: landed 2026-10-27.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm, rebase its tests onto the PG switch). Lever count 8 → 7 (`scripts/lever-count.sh`). Inventory row:
`analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

PG's `create_ordered_paths` (`postgres/src/backend/optimizer/plan/planner.c`) offers an Incremental Sort over any
input path whose pathkeys share a partial prefix with the required ordering. It does so only under
`enable_incremental_sort`. goopg's port of the "other candidates" loop is `addIncrementalSortPaths`
(`internal/optimizer/incrementalsortpaths.go`). The partial-path sibling is in `generateUsefulGatherPaths`
(`gatherpaths.go`).

Both gates read two switches: goopg's process-wide `GOOPG_INCREMENTAL_SORT` mode (default on since M0146-0006; `off`
dropped the arm) AND `cp.enableIncrementalSort`, which is PG's GUC wired through `PlannerSettings.EnableIncrementalSort`.
PG has only the GUC.

## Removed / changed

- `incrementalsortpaths.go`: `incrementalSortMode` with its constants, `incrementalSortPathsMode` (the env read),
  `incrementalSortModeFromEnv`, `incrementalSortModeLabel`, `setIncrementalSortPathsModeForTest` and the exported
  `SetIncrementalSortPathsMode` (no callers). The `os` and `strings` imports go with them.
  - `addIncrementalSortPaths` now returns early only on `!cp.enableIncrementalSort`.
  - The file header's "why gated off" and "GUC note" paragraphs are condensed into a short history: the GUC note
    described a GUC that was unconsumed at the time and has since been wired.
- `gatherpaths.go`: `incOn := s.cp.enableIncrementalSort`.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0043)`). `scripts/planner-flags.env` is regenerated.
- Comments in `path.go` and `internal/executor/operators_incremental_sort_build_test.go` no longer describe the arm as
  gated off.

`deadcode ./cmd/...` was diffed before and after. The two setters, both already unreachable, are gone; nothing was
added.

## Tests

- `TestAddIncrementalSortPathsOffIsInert` is rebased onto PG's switch: `cp.enableIncrementalSort = false` must leave
  the ORDERED rel with the seed's full Sort only.
- `withIncrementalSortMode` and its three ON pins are deleted, along with `TestIncrementalSortModeFromEnv` (env
  polarity).
- `TestFlagProvenanceDetectorSeesHelperWrappedReads` used this variable as its example of a helper-wrapped env read.
  - It now uses `GOOPG_NLI_COSTGATE`, which has the same `xFromEnv(os.Getenv(...))` shape.
  - It scans the package with `flaglabels.go` left out, through the new `plannerEnvNamesExcept`. `flaglabels.go`
    names every flag, live or retired, so it alone would satisfy the check.

## Finding (filed, not fixed)

`incrementalSortPathWithGroups` charges `disabledNodesFor(!cp.enableSort, sub)`. Its comment claims
`cost_incremental_sort` folds `enable_sort` in through `cost_sort`, but PG 18.3 does not:
- `cost_incremental_sort` asserts `enable_incremental_sort` and sets `disabled_nodes = input_disabled_nodes`
  (`costsize.c:2123-2125`).
- Only `cost_sort` adds the `enable_sort` penalty (`:2162`).

Under `enable_sort=off` goopg therefore disables an Incremental Sort that PG keeps. Every census corpus runs with
`enable_sort` on, so the census cannot see it. Filed as M0146-0243 (below item 10, S7 carve-out), with a ledger row
dated 2026-10-27.

## Inert proof (AGENT.md C6)

`git grep` over `scripts`, `ci`, `Makefile`, `.githooks`, bench shell and env files, `AGENT.md`, `CLAUDE.md` and
`docs/wiki` finds no setter. Unset, the mode resolved to on, which is the only arm left; the GUC still turns the arm
off per session. The fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0043 entry in `.ralph/fix_plan.md`.
