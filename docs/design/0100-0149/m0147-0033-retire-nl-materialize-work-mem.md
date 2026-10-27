# M0147-0033 — retire `GOOPG_NL_MATERIALIZE_WORK_MEM` (unbounded nested-loop inner cache)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 18 → 17 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

A nested loop's inner Materialize (`materializeOp`, `internal/executor/operators_material.go`) is bounded by
work_mem and spills past it. PG's Materialize tuplestore does the same (`nodeMaterial.c` with `tuplestore_begin_heap`
at `work_mem`). The bound became the default in M0146-0010c, once the planner priced a spilled rescan
(`costMaterial` + `materialRescanCost`, PG's `cost_rescan`).

`GOOPG_NL_MATERIALIZE_WORK_MEM=0` kept the old unbounded cache for an A/B. Phase 2 keeps the PG-shaped ON arm and
deletes the OFF arm.

## Removed / changed

- `join_nl_stream.go`: `nlInnerWorkMemEnabled`. Both `if !nlInnerWorkMemEnabled { setUnbounded() }` branches are
  removed: the plan-elected Materialize arm and the compat attach-time cache arm. The Q54 history the first branch
  carried is kept as a comment. The `os` import stays, because `nlBareReexec` still reads its env.
- `operators_material.go`: `(*materializeOp).setUnbounded`, whose only callers were those two branches. It is
  unexported, so the compiler did not flag it.

`deadcode ./cmd/...` was diffed before and after: no change. `setUnbounded` was reachable until its callers were
deleted, and is removed with them.

## Tests

- `withNLInnerWorkMem`, a helper that pinned the bound ON for three spill tests, is deleted along with its three
  calls. There is no OFF arm left to guard against.
- `TestNestedLoopInnerCacheSpillsByDefault` loses its `GOOPG_NL_MATERIALIZE_WORK_MEM=0` skip and the test file's
  `os` import.

## Inert proof (AGENT.md C6)

No gate, CI config, bench script or env file sets the variable (M0147-0030 inventory). Unset, it resolved to the
bounded arm, which is the only arm left. The executor change is values-checked by the sf025 sweep and the acceptance
arm.

## Gates

See the M0147-0033 entry in `.ralph/fix_plan.md`.
