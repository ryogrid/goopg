# M0147-0045 — retire `GOOPG_PARALLEL` (process-wide parallel kill switch)

Status: landed 2026-10-27.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm, rebase its tests onto the PG switch). Lever count 6 → 5 (`scripts/lever-count.sh`). Inventory row:
`analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

PG turns parallel query off per session with `max_parallel_workers_per_gather = 0`. With it at zero, `standard_planner`
never sets `parallelModeOK` and no partial path is built. goopg carried that GUC through two routes:
`PlannerSettings.MaxParallelWorkersPerGather` (`cp.maxParallelWorkersPerGather`) in the search, and
`ParallelSettings.MaxWorkersPerGather` in the post-pass.

It also carried a process-wide `parallelOn` atomic: `GOOPG_PARALLEL=off` at start, `SetParallelEnabled` from tests.
Four gates ANDed the atomic with the GUC:
- `parallelModeOK` (`considerparallel.go`);
- `addPartialDistinctPaths` (`distinctpaths.go`);
- the partial-aggregation upper arm (`partialaggupper.go`);
- the post-pass entry (`maybeAddGatherInner`, `parallel.go`), which stripped every Gather when the atomic was off.

The post-pass already strips on `MaxWorkersPerGather <= 0`. Every effect of the atomic was therefore already
available through PG's own switch.

## Removed / changed

- `parallel.go`: `parallelOn`, its `init`, `parallelFromEnv`, `SetParallelEnabled`, `ParallelEnabled`, the strip
  branch at the post-pass entry, and the `os` and `sync/atomic` imports.
- `considerparallel.go`, `distinctpaths.go` and `partialaggupper.go` gate on the worker GUC alone.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0045)`). `scripts/planner-flags.env` is regenerated.
- Comments in `gatherpaths.go` and `partialaggpaths.go` that pointed at `SetParallelEnabled` are updated, and so are
  the `docs/wiki/modules/optimizer.md` rows and prose.

`deadcode ./cmd/...` was diffed before and after. The exported `SetParallelEnabled`, which was test-only, left the
unreachable list; nothing was added to it.

## Tests

- `withParallelOn` (`considerparallel_test.go`) is deleted. Its 40 callers in 10 files were unwrapped mechanically:
  the closure was dropped and its body dedented by one tab, with multi-line raw strings left untouched. ON was
  already the default these tests ran under. Seven more direct `parallelOn.Store(true)` pins are deleted.
- `TestKillSwitchSuppressesGather` and `TestKillSwitchSuppressesGatherMerge` are rebased onto
  `ParallelSettings.MaxWorkersPerGather = 0`. Each first asserts that the fixture does gather with workers available,
  so the refusal cannot pass vacuously.
- The `addPartialDistinctPaths` "parallel off" subtest is deleted. Its sibling "workers disabled" already pins the
  same refusal through `MaxParallelWorkersPerGather = 0`.
- `internal/executor` `runForced` (`TestParallelSerialIdentity` and its siblings) plans the serial arm through
  `runSQLWith` with `MaxParallelWorkersPerGather = 0`. In-process executor tests never run the post-pass, so the
  switch had acted only through `parallelModeOK`, and the GUC route is exact.

## Inert proof (AGENT.md C6)

`git grep` over `scripts`, `ci`, `Makefile`, `.githooks`, bench shell and env files, `AGENT.md`, `CLAUDE.md` and
`docs/wiki` finds no setter. Unset, the atomic was on, so every gate reduces to its GUC half. The fire-set A/B and the
census confirm no plan change.

## Gates

See the M0147-0045 entry in `.ralph/fix_plan.md`.
