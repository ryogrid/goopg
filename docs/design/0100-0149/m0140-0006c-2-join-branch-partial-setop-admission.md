# M0140-0006c-2 — widen the partial-SetOp admission past bare scans (hash-join branch)

**Status:** accepted (partial — hash-join branch landed; merge/nested-loop/bitmap branches remain open under this same task)
**Milestone:** M0140 (TPC-DS parallelism), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
**Task:** `.ralph/fix_plan.md` M0140-0006c-2
**Kind:** impl
**Parent:** M0140-0006c (`m0140-0006c-executor-claim-set.md`)
**Movement:** none (newly-admitted shape wins no corpus plan yet at current costs; sweep PLAN-SHAPE 99/99 identical)
**PG oracle:** `postgres/src/backend/optimizer/path/allpaths.c:1538-1577` (`partial_subpaths_valid` — a partial Append subpath may itself be a partial join), `postgres/src/backend/optimizer/path/costsize.c:2250-2403` (`cost_append`)

## What landed (hash-join branch only)

Four pieces, all in this loop's diff:

1. **Executor operator-tree collectors descend into `*setOp`** — `collectShareableJoins` (`internal/executor/parallel_hash_build.go`) and `collectBitmapScans` (`internal/executor/operators_gather.go`) each gain a `*setOp` arm descending BOTH `left` and `right`. Unlike the join arm's probe-side-only rule, a SetOp has no build side to exclude: it streams both branches to completion, so a hash join driving either branch needs its build side leader-prebuilt exactly like a top-level partial join.
2. **Plan-side prebuild gates descend into `*SetOp`** — `parallelChildren` (`internal/optimizer/parallel.go`) gains the `*SetOp` arm (both branches), which is what `HasShareableHashJoin` and `HasBitmapScan` recurse through. Audited every other consumer: `findPartialSubtree`/`rebuildWithGather` still refuse (`len(kids) != 1`, and `terminatesPartial` fires first anyway); `subtreeHasUnsafeNode`/`subtreeHasGather` only grow more conservative (safe direction); `considerparallel.go`'s walk refuses `*SetOp` in its own switch before reaching the loop (unchanged); `StripGather` gains the twin two-child `*SetOp` arm (sibling-agreement — a Gather below a branch strips copy-on-write like the Join case).
3. **Admission widened to hash-join-driven branches** — `setOpBranchDrivingKindIsSupported` (`internal/optimizer/gatherpaths.go`) gains `case PathHashJoin`: `RequiredOuter == 0`, exactly two children, recurse into `Children[0]` (the probe side by this package's child convention — the same side the `PathHashJoin` arm, `stampParallelScan`, and the executor attach walk all descend). The build side needs no admission of its own: the leader drains it once via the now-descending prebuild. The jointype trust is the producer's (`addPartialHashJoinPath` files only partial-capable jointypes), re-checked at runtime by `hashJoinIsPartialCapable` on the node twin. Merge, nested-loop, and bitmap branches stay refused (fail closed → serial), each with the reason in the comment.
4. **Tests.** Optimizer: hash-branch and nested-hash-probe admission (`TestPartialPathDrivingKindAcceptsSetOpWithHashJoinBranch`, `...NestedHashJoinBranch`), the fail-closed edges (`...RefusesSetOpWithBadHashJoinBranch`: parameterised/malformed/bitmap-probe hash, merge, nestloop), prebuild-gate descent (`TestHasShareableHashJoinDescendsSetOp`, `TestHasBitmapScanDescendsSetOp`), `TestStripGatherStripsUnderSetOp`. Executor: `TestGatherOverSetOpHashJoinBranchIdentity` (UNION ALL with a forced-hash join branch — 40 deterministic join rows — over a 90-row bare scan, serial-vs-parallel identity at 1/2/4 workers, plus the build-once sharing witness below), `TestCollectShareableJoinsDescendsSetOp`, `TestCollectBitmapScansDescendsSetOp`.

## Why the collector is build-once sharing, not row identity (found by mutation, not assumed)

The first cut of the identity test claimed two opposite silent failure modes (N-copies vs dropped matches). Neutering the new `collectShareableJoins` arm leaves the test GREEN: with no shared build published, each worker falls back to a full private build of the (unclaimed, unstamped) build side — correct rows, N+1x the build work and memory. The dropped-matches hazard (`TestC19f…` line 231) needs a claim on the BUILD side, which no walk in this shape places. So the collector's gate is a second witness in the same test: `lookupSharedHashBuild(ctx, branchJoin) != nil` after `Open` (retracted at `Close`, so asserted between). Neutered collector → no publication → the assert fires (verified); neutered `attachAll` SetOp dispatch → 390/650 rows for 130 (verified) — each witness fires on exactly one mutation.

## Still open under this task (not deferred — the owning task stays `[ ]`)

- **Merge-driven branch**: needs its own admission arm plus proof no walk must collect through a merge outer (no walk does today — a hash below a merge outer is un-prebuilt even at top level, E-20's territory, not this task's).
- **Nested-loop-driven branch**: needs the `PathNestLoop` arm's extra guards re-audited for branch paths (outer `ParallelWorkers`, Memoize, lateral subset).
- **Bitmap-driven branch**: collectors and gates now descend, but `prebuildBitmap` publishes only to the top-level claim set while a branch attaches through its own leaf (whose `pbm` is nil by construction) — needs per-branch publication, then its own identity test.

## Acceptance

- `go build ./...`, `go vet` clean (both packages).
- `go test ./internal/optimizer/... ./internal/executor/...` fully green.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34 canonical).
- `scripts/tpcds-sf025-regression.sh sweep`: PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, PLAN-SHAPE queries=99 same=99 changed=0.
- `scripts/tpch-acceptance-arm.sh` (executor change): A/B HEAD-baseline vs staged, VERDICT PASS 24/24 MATCH.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: no FAIL.
- Parity floor, transitive (no fresh capture — `PARITY: N/A`, same disposition as 0006b-2): TPC-DS goopg plans byte-identical before/after (sweep 99/99); TPC-H contains 0/22 set operations so the widened admission cannot fire there — `match=8` (P0-E7) and `match >= 2` (S2b-7) hold.
- `make ea-ratchet`: N/A — no estimate/selectivity path touched.

D2 report: `CATEGORIES:`/`CATEGORIES-EXCL-MATCH:` unchanged by construction (shape-delta zero per the sweep); stats epoch pinned by the sweep's own procedure; planning route unchanged (PG-shaped search default); wall time: no plan changed, so no per-query timing owed.
