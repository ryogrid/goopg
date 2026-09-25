# M0140-0006b — the partial-Append cost producer for SETOP

**Status:** accepted
**Milestone:** M0140 (TPC-DS parallelism), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Task:** `.ralph/fix_plan.md` M0140-0006b
**Parent:** `m0140-0006-decomposition-into-a-b-c.md`
**Depends on:** M0140-0006a (`RelOptInfo.LeftBranchRel`/`RightBranchRel`)

## What landed

`addPartialSetOpPath` (`internal/optimizer/windowsetoppaths.go`), called from
`createSetOpPaths` right after the M0140-0006a branch-rel threading. It is the
streaming-UNION-ALL / `cost_append` (`postgres/src/backend/optimizer/path/
costsize.c:2250-2403`) counterpart of `addPartialHashJoinPath`
(`joinpathsparallel.go`), specialised to goopg's fixed two-child SetOp shape:

- Only `setOpStreams(setOpNode)` (Op=UNION, All=true) is Append-shaped. The
  buffered/hashed arm drains both inputs fully before emitting anything and
  has no partial-safe executor shape — refused outright.
- Gated by the SAME `gatherPathsMode` knob `addPartialHashJoinPath` reads:
  `off` produces nothing, matching the K80 precedent this task was scoped
  against.
- `setOpRel.ConsiderParallel` is set to the conjunction of both branches'
  `ConsiderParallel` (`build_join_rel`, relnode.c:842's join-rel rule, applied
  to the SetOp's two inputs instead of a join's two sides).
- Requires BOTH branches to already carry a partial path
  (`LeftBranchRel.PartialPathlist`/`RightBranchRel.PartialPathlist`, both
  non-empty) — PG's `partial_subpaths_valid` arm (allpaths.c:1538-1577).
  **The mixed partial/non-partial arm is NOT built** (allpaths.c:1592-1622,
  needs `append_nonpartial_cost`'s worker-rotation arithmetic over an
  arbitrary-length subpath list) — a branch with no partial path at all
  simply gets no partial SetOp path. Ledger row filed (see below).
- Worker count: `Max(leftPartial.ParallelWorkers, rightPartial.ParallelWorkers)`,
  then bumped to at least `pg_leftmost_one_pos32(2)+1 == 2` — PG's
  `enable_parallel_append` bump (allpaths.c:1560-1566) applied
  unconditionally, since goopg has no such GUC to gate behind (matches
  `costSetOp`'s existing "single candidate, no serial-vs-parallel-aware
  choice" posture) — then capped at `maxParallelWorkersPerGather`. The `2`
  literal is fixed rather than computed from `len(children)` because a goopg
  SetOp always has exactly two children, unlike PG's N-way Append.
- Startup: `min(left.Startup, right.Startup)` (costsize.c:2350-2352, both
  children eligible because `first_partial_path == 0` here).
- Rows/total: each child's per-worker row count is rescaled from its OWN
  parallel divisor to the Append's chosen worker count and summed
  (costsize.c:2372-2381), total cost sums both children's totals undivided,
  plus the small per-tuple overhead `cpu_tuple_cost * APPEND_CPU_COST_MULTIPLIER
  * rows` (costsize.c:2400-2403, `APPEND_CPU_COST_MULTIPLIER = 0.5`).

14 new unit tests in `windowsetoppaths_test.go` pin: the golden two-different-
divisors cost case against the formula computed independently in-test; the
two-child worker floor; the `maxParallelWorkersPerGather` cap; refusal for
every non-streaming set-op form; refusal under `GOOPG_GATHER_PATHS=off`;
refusal when either branch does not consider parallel; refusal when either
branch has no partial path (while `ConsiderParallel` still gets set — a rel
can consider parallel with zero partial paths, same as a base rel); and an
end-to-end `createSetOpPaths` acceptance pin that the emitted node and the
serial tournament stay byte-identical even with two cheap partial paths
seeded.

## Verifying "does `generateUsefulGatherPaths` read this for free?"

M0140-0006's decomposition doc left this as this task's own acceptance item:
*"Verify `generateUsefulGatherPaths` (`considerparallel.go`) reads the new
`PartialPathlist` for free... expected per the original recon, unverified."*

**The answer is no**, and the gap is bigger than SetOp-specific. Every one of
`generateUsefulGatherPaths`'s three call sites —
`gatherpaths.go:518` (`addBaseRelGatherPaths`, over `s.joinrels[1]`),
`joinsearchlevel.go:346` (over a join-search level's `rel`), and
`geqo.go:418` (the GEQO arm's `joinrel`) — reads a **`*searchCtx`'s own**
`joinrels`/`joinrel`. `createSetOpPaths` (`windowsetoppaths.go`) is called
from the SetOp fold in `planner.go` (`applySetOp`'s closure over
`planSegment`), entirely outside any `*searchCtx`: there is no `s` in scope
to call `generateUsefulGatherPaths` with, and none of the WINDOW/ORDERED/
GROUP_AGG upper-rel producers call it either — **no upper rel in the whole
Phase-4 pipeline has EVER been wired for parallel Gather consideration**,
not just SETOP. This is a materially larger, structurally separate gap than
the original M0140-0004 recon assumed, and it is NOT this task's bound (see
`m0140-0006-decomposition-into-a-b-c.md`'s own explicit escape hatch: "if it
turns out not to be free, 0006b's own recon should say so and re-split").
Filed as **M0140-0006b-2** (`.ralph/fix_plan.md`).

## Why this cannot move a plan today (two independent reasons, not one)

The task's own ordering constraint ("must not be exercised by any gate that
could select it, until M0140-0006c lands") is satisfied by construction, not
by a new flag, for **two independent reasons**:

1. **Reachability (M0140-0006b-2, filed above).** Nothing calls
   `generateUsefulGatherPaths` for the SETOP rel (or any upper rel), so
   `PartialPathlist` has no reader. The field this task writes is inert the
   same way `SearchCandidates`/`SearchCandidateKeys` were inert until
   M0141-S2b-2c gave them a reader.
2. **Even if wired, the executor whitelist would refuse it (M0140-0006c's own
   scope).** `partialPathDrivingKind` (`gatherpaths.go`) is a fail-closed
   whitelist of Path kinds `attachAll`'s per-worker claim-set models
   (`PathSeqScan`/`PathIndexScan`/`PathBitmapHeapScan`/`PathHashJoin`/
   `PathMergeJoin`/`PathNestLoop`); it has **no `case PathSetOp:` arm**, so
   `gatherSubpathIsRunnable` returns `false` for any partial `PathSetOp` and
   `makeGatherPath`/`makeGatherMergePath` both return `nil` — the exact
   mechanism that protects every OTHER not-yet-executor-safe partial-path
   family in this package. Opening that case is explicitly M0140-0006c's job
   (the executor claim-set fix), not this task's — adding it here, ahead of
   the claim-set fix, is exactly the wrong-answer sequencing the parent
   decomposition doc warns about.

Both gates independently hold `gatherPathsMode`'s existing production default
(`all`, on since M0140-0003) safe for this change: even with reachability
wired first, gate 2 still refuses; even with gate 2 opened first (which
0006c will do), gate 1 still refuses until the wiring in M0140-0006b-2 lands.
A change can therefore land 0006b, 0006b-2, and 0006c in any order without
ever exposing the wrong-answer duplication risk mid-sequence, as long as
0006c's own whitelist opening is the LAST of the three to land — recorded
here so a future loop doesn't need to re-derive it.

## Acceptance

`go build ./...` clean; `go vet ./internal/optimizer/...` clean; `go test
./internal/optimizer/...` and `./internal/executor/...` full green (14 new
tests); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34) against the staged
tree; `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0, PLAN-SHAPE queries=99 same=99 changed=0 —
both gates confirm the producer is inert, exactly as designed.
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full green.
`scripts/tpch-acceptance-arm.sh` `PGSHAPED=1` HEAD-baseline (`git stash`/
build/`stash pop` round-trip of the two touched files, private port 5583)
vs this staged tree: **VERDICT PASS, 24/24 labels MATCH**.

## Deferred / follow-ups

- **M0140-0006b-2** (filed in `.ralph/fix_plan.md`): wire
  `generateUsefulGatherPaths` (or an upper-rel-shaped equivalent) into the
  Phase-4 upper-rel pipeline generally — needed before ANY upper rel's
  `PartialPathlist` (not just SETOP's) can ever be read. Needs a `*searchCtx`
  (or the subset of it `generateUsefulGatherPaths` actually reads:
  `parallelModeOK`, `cp`, `trace`) reachable from `planner.go`'s upper-rel
  call sites, which today only carry a `PlannerSettings`/`upperRels` — sizing
  and shape are this follow-up's own scope, not pre-judged here.
- **Mixed partial/non-partial SetOp Append** (PG's `append_nonpartial_cost`
  arm, allpaths.c:1592-1622): not built. A branch with no partial path gets
  no partial SetOp path at all rather than a worker-rotation-priced mixed
  Append. Ledger row filed (`.ralph/deferral_ledger.md`, 2026-09-18,
  `M0140-0006b`).
- M0140-0006c (executor claim-set) is unaffected by this task and remains
  the correctness gate before either the whitelist or the reachability gap
  can be closed.
