# M0147-0032 — retire `GOOPG_COOP_JOIN_BUILD` (cooperative join-tree build switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 19 → 18 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

`coopDrivingScan` (`internal/executor/parallel_hash_build.go`) finds the scan that a cooperative hash build's
producers partition. E-18 slice 2 widened it by one node kind: a hash join's probe side. That lets a join-tree build
side be built cooperatively, with the nested shareable joins prebuilt once in the leader.

The widening sat behind `GOOPG_COOP_JOIN_BUILD`, default ON, where `=off` disabled it. It was measured as a win on
TPC-H SF1 in parallel mode, with md5-identical results:
- Q20 1.91 → 0.65 s;
- Q21 14.63 → 13.75 s;
- no query regressed.

The comment above it still opened with "OFF by default". That was its state before the E-18 measurement, and it is
corrected here.

PG has no cooperative build of this kind. Its Parallel Hash builds one shared table from a partial inner, which is
M0140-0007 and is held. The widening is goopg-private executor plumbing with a measured win, so phase 2 keeps the
shipped ON arm and deletes the OFF arm.

## Removed / changed

- `parallel_hash_build.go`: `coopJoinBuildOn` and the `os` import. `coopDrivingScan`'s Join arm no longer checks
  the flag. The comment block now records the retirement and no longer says "OFF by default".

## Tests

`TestCoopBuildRecycledBatchesMatchSerialBuild` (`coop_build_recycle_test.go`) took its "serial build" reference by setting
`coopJoinBuildOn = false`. That variable gates only the join-tree widening, and the test's three queries build a bare
scan or a column-only Project, which are cooperative either way. So the reference was itself a cooperative build.
The test now takes the reference with `ctx.MaxParallelWorkers = 0`: no workers, therefore no cooperative build, the
same serial budget PG's `max_parallel_workers_per_gather = 0` gives. The poison and repeat loop and the
recycle/borrow assertions are unchanged.

## Inert proof (AGENT.md C6)

No gate, CI config, bench script or env file sets the variable (M0147-0030 inventory). Unset, it resolved to ON,
which is the only arm left.

## Gates

See the M0147-0032 entry in `.ralph/fix_plan.md`.
