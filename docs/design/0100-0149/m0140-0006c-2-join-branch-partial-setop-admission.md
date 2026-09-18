# M0140-0006c-2 — widen the partial-SetOp admission past bare scans (hash-join branch)

**Status:** accepted (partial — hash-join and nested-loop branches landed; merge/bitmap branches remain open under this same task)
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
- ~~**Nested-loop-driven branch**~~ — **landed 2026-09-19 (slice A), see below.**
- **Bitmap-driven branch**: collectors and gates now descend, but `prebuildBitmap` publishes only to the top-level claim set while a branch attaches through its own leaf (whose `pbm` is nil by construction) — needs per-branch publication, then its own identity test.

## Update 2026-09-19 — slice A (nested-loop branch) landed

The recon's recommended first slice. Exactly one production change, as the
scoping predicted: `setOpBranchDrivingKindIsSupported`
(`internal/optimizer/gatherpaths.go`) gains `case PathNestLoop`, mirroring
`partialPathDrivingKind`'s own PathNestLoop arm guard-for-guard —
`JoinInner` only, `RequiredOuter == 0`, exactly two children, the V5 outer
(`ParallelWorkers > 0 && ParallelSafe && RequiredOuter == 0`), no
`PathMemoize` inner, unparameterized whole-inner → recurse outer, or the
R95 parameterized-probe inner (`PathIndexScan` + `IndexClauses` +
non-zero `OuterRelids`/`InnerRelids` + `calcNestloopRequiredOuter == 0`).
The recursion goes through *this* test, not the general classifier, so a
merge- or bitmap-driven NL outer still refuses — nested-NL spines (Q76's
shape minus its Memoize) admit via the same recursion. One deliberate
simplification: the general arm's second identical `Jointype` re-check is
folded into the single top guard (the field is immutable within the call).
No executor change: `attachAll`'s `*setOp` arm hands each branch its own
leaf claim set and all three `attachParallel*` walks already carry the
`JoinAlgoNestedLoop` arm — the recon's "nothing to add" held exactly.

Corpus position unchanged: **Q76 still needs M0142-0005a** (its hash
branch's probe NL wraps `Memoize`→`Index Scan`, which the Memoize guard
still refuses) — slice A flips no corpus query alone, same posture the
hash slice landed in. Sweep confirms: PLAN-SHAPE 99/99 identical.

Tests (11 admission + 1 executor identity):

- `TestPartialPathDrivingKindAcceptsSetOpWithNestLoopBranch` — whole-inner
  NL branch via `nlClassifyFixture`/`nlClassifyPath` (R60's filed shape).
- `TestPartialPathDrivingKindAcceptsSetOpWithNestedNestLoopBranch` —
  NL-of-NL spine recursion.
- `TestPartialPathDrivingKindAcceptsSetOpWithNestLoopIndexProbeBranch` —
  R95 probe inner via `latClassifyFixture`/`latClassifyPath`/`latParamInner`.
- `TestPartialPathDrivingKindRefusesSetOpWithBadNestLoopBranch` — 11
  refusals: left-join, parameterized NL, one-child, zero-worker/unsafe/
  parameterized outer, Memoize inner, parameterized seq inner, clauseless
  probe, bitmap/merge outer (the narrowed recursion), unpartitioned and
  unsatisfiable-req probes.
- `TestGatherOverSetOpNestLoopBranchIdentity` — SetOp over a forced-NL
  branch (cross join `pq_setop_a.id < 3` × `pq_setop_c` = 120 rows) + a
  bare `pq_setop_b` scan; serial-vs-parallel multiset identity at 1/2/4
  workers; `planTreeHasNestedLoopUnderGather` vacuity guard.
- `TestPartialPathDrivingKindRefusesSetOpWithBadHashJoinBranch` lost its
  `nestloop-branch` case — it is now a *valid* admission (Jointype's zero
  value is `parser.JoinInner`), moved to the acceptance test.

Mutation-verified: neutering the arm flips exactly the three acceptance
tests to `PathPrebuilt` (serial), refusals stay green.

Gates: `go build ./...` clean; `go test ./internal/optimizer/...`
(2.7s) and `./internal/executor/` (13.4s) green; `tpch-spotcheck.sh`
PASS real run (Q12=2/Q13=34, ~10.5s — first non-SKIPPED run since the
`:65433` recovery); SF0.25 sweep PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0, PLAN-SHAPE 99/99 identical; units precommit all `ok`.

## Remaining-branch scoping — recon 2026-09-19 (analysis only, no production change)

The three open branches above are now scoped end-to-end against the PG
oracle and the corpus. The headline: **only the nested-loop slice has a
corpus witness** (Q76), and it shares a prerequisite with
M0142-0005a; the merge and bitmap slices have zero `Parallel Append`
branch heads of their kind in `bench/tpcds/plans-pg/` and are
completeness-only.

### PG contract (oracle-verified, not inferred)

- **Planner** (`postgres/src/backend/optimizer/path/allpaths.c:1320-1586`,
  `add_paths_to_append_rel`): the pure-partial arm requires EVERY child
  to own a partial path (`partial_subpaths_valid`, :1398-1406 →
  :1539-1586); `parallel_workers` = max over subpaths, then at least
  `pg_leftmost_one_pos32(nchildren)+1` under `enable_parallel_append`
  (:1544-1569). `addPartialSetOpPath` (`windowsetoppaths.go:525`) files
  exactly this arm — it requires both `PartialPathlist`s non-empty. The
  **mixed arm** (`pa_partial_subpaths`/`pa_nonpartial_subpaths`,
  :1408-1452 — per-child pick of the cheaper of partial vs
  parallel-safe total) is deliberately not built; see "Q5" below.
- **Executor** (`postgres/src/backend/executor/nodeAppend.c:68-80,
  704-832`, `ParallelAppendState`/`choose_next_subplan_for_worker`):
  non-partial subplans are claimed exclusively (`pa_finished[i]` set on
  selection); partial subplans stay claimable until some worker runs
  one to completion, so workers spread across in-flight partial plans
  and cooperate inside each. goopg's analogue is the per-branch leaf
  claim sets (`parallelClaimSet.setOpLeft`/`setOpRight`,
  `parallel_scan.go:506-552`).
- **Join-kind guards** (`postgres/src/backend/optimizer/path/joinpath.c`):
  `try_partial_nestloop_path` :950-1021 — outer unparameterized, inner
  may be parameterized but `ppi_req_outer ⊆` the outer's relids
  (`top_parent_relids` when set) and `PATH_PARAM_BY_PARENT` must pass
  `path_is_reparameterizable_by_child`, `lateral_relids` empty;
  `try_partial_mergejoin_path` :1145-1214 — inner must have NO
  `required_outer` (private whole-inner per worker), presorted-key
  checks may drop `outersortkeys`/`innersortkeys`;
  `try_partial_hashjoin_path` :1299-1338 — inner unparameterized
  (private copy per worker) unless `parallel_hash` (shared build).

### Corpus inventory (`bench/tpcds/plans-pg/`, every `Parallel Append` branch head)

| query | branch heads | what it needs |
|---|---|---|
| Q2 | `Parallel Seq Scan` ×2 | bare-scan arm — already landed (not a non-scan claim) |
| Q5 | bare scans, plus `Subquery Scan`→`Hash Right Join` **non-partial** branch | the mixed arm — out of scope (filed M0140-0006c-3) |
| Q14 | `Parallel Hash Join` over bare probes | hash arm — already landed |
| Q71 | `Subquery Scan`→`Parallel Hash Join` ×3 | hash arm + subquery-transparency check (below) |
| Q75 | no `Parallel Append` at all (`Merge Append`/`Gather Merge` spines) | not a SetOp-branch claim |
| Q76 | `Parallel Hash Join` (probe = `Nested Loop` over `Memoize`→`Index Scan`) + `Nested Loop` ×2 (bare index-probe inner) | NL arm + M0142-0005a |

The task's movement claim therefore reduces to: **Q76 is the sole query
gated on a remaining branch kind**; Q14/Q71 ride the landed hash arm;
Q2 never needed this task; Q5/Q75 are out of scope. Zero `Merge Join`
or `Bitmap Heap Scan` heads exist under any `Parallel Append`.

### Slice specs (each executor walk verified to already cover the branch)

Every sibling walk already descends a `*SetOp` branch and the three
attach walks already handle merge/NL under any subtree. The only
missing machinery is the admission arms themselves, plus
`prebuildBitmap`'s per-branch publication for the bitmap slice.

- **A. Nested-loop branch** — corpus-relevant (Q76):
  - Arm: mirror `partialPathDrivingKind`'s `PathNestLoop` guards
    (`gatherpaths.go:514-580`): `JoinInner` only; `RequiredOuter==0`;
    `len==2`; outer V5 (`ParallelWorkers>0 && ParallelSafe &&
    RequiredOuter==0`); inner not `PathMemoize`; unparameterized inner
    → whole-inner → recurse outer; parameterized inner → the R95 probe
    test (`PathIndexScan` + `IndexClauses` + `OuterRelids`/`InnerRelids`
    non-zero + `calcNestloopRequiredOuter == 0`). NL-of-NL spines then
    work via the same recursion — exactly Q76's nested-NL shape.
  - Executor: `attachParallelScan`/`attachParallelBitmapScan`/
    `attachParallelIndexScan` each carry the `JoinAlgoNestedLoop` arm
    (literal left + `ordinaryInnerNestedLoopPartial`/
    `lateralProbeJoinPartial` + `HasBitmapScan(inner)` refusal —
    `parallel_scan.go:196-206`, `:300-308`, `:451-458`). Nothing to add.
  - Dependency: Q76's hash branch has an NL over `Memoize`→`Index Scan`
    under its probe; the arm's `in.Kind == PathMemoize → refuse` holds
    until M0142-0005a admits Memoize-wrapped probes. **Q76 needs A +
    0005a together** — A alone flips no corpus query but is the
    prerequisite both need.
  - Identity test: `TestGatherOverSetOpIdentity` shape — SetOp over a
    bare-scan branch + forced-NL branch (index-probe inner per
    `parallel_nl_join_test.go`'s fixtures), serial-vs-parallel at
    1/2/4 workers; mutation witnesses: neutered NL arm → serial,
    neutered leaf attach → N-copies.
- **B. Merge branch** — completeness-only (zero corpus witnesses):
  - Arm: mirror the `PathMergeJoin` arm (`gatherpaths.go:497-513`):
    `RequiredOuter==0`, `len==2`, recurse `Children[0]` (outer).
  - Executor: `attachParallelScan` has the explicit `JoinAlgoMerge` arm
    (`parallel_scan.go:244-247`); `attachParallelIndexScan`/
    `attachParallelBitmapScan` reach the outer via
    `probeSideIsLeft(BuildLeft=false)`→left — correct but coincidental.
    Give them an explicit merge arm or a comment pinning the
    coincidence (sibling-agreement discipline).
  - Collector caveat — the task's open question, resolved:
    `collectShareableJoins` has no merge arm (`parallel_hash_build.go:291`
    refuses non-hash joins outright), so a hash under a merge inner is
    never collected; `HasShareableHashJoin` seeing it through
    `parallelChildren` (both join sides) only fires a prebuild gate
    that finds nothing — workers private-build the inner hash,
    correct-but-unshared, exactly the E-20 top-level deferral. Nothing
    new owed.
  - Identity test: same shape, forced-merge branch.
- **C. Bitmap branch** — completeness-only + real machinery:
  - Arm: `PathBitmapHeapScan → true` — dormant until a producer files
    a partial bitmap path (none does today; the top-level arm is
    dormant for the same reason).
  - Machinery: `prebuildBitmap` publishes only `cs.pbm`
    (`parallel_scan.go:614+`); a branch bitmap must publish to
    `cs.setOpLeft.pbm`/`setOpRight.pbm` — pick the leaf by
    `HasBitmapScan` on the PLAN's `SetOp.Left`/`Right`; relax the
    `len(bmOps)>1 → nil` refusal into per-branch grouping (a
    both-bitmap SetOp collects two); `newLeafParallelClaimSet` gains
    `pbm` (the field exists, nil today, `:547-552`).
    `attachParallelBitmapScan` already reaches the leaf — `leaf.pbm`
    nil → serial today, claims once published.
  - Identity test: needs a partial-bitmap producer first — flag that
    dependency honestly; without one the arm is untestable end-to-end.

### Sibling-agreement audit

Admission (`setOpBranchDrivingKindIsSupported`) ↔ eligibility/label
(`drivingScan` `parallel.go:743`, `stampParallelScan` `:645`,
`unstampParallelScan` `:1574` — `*SetOp` arms landed, general form) ↔
executor attach (`attachAll` `parallel_scan.go:596-604` leaf dispatch;
`attachParallel*` join arms above) ↔ collectors (`collectShareableJoins`
`parallel_hash_build.go:310`, `collectBitmapScans`
`operators_gather.go:164` — `*setOp` arms landed) ↔ plan gates
(`parallelChildren` `:1391` `*SetOp` arm → `HasShareableHashJoin`,
`HasBitmapScan`) ↔ `StripGather` (two-child block, `parallel.go:1473`)
↔ `prebuildBitmap` — per-branch publication is THE missing walk for
slice C.

Open verification item: Q71's `Subquery Scan` wrapper — goopg has no
`PathSubqueryScan` kind, so a `*SELECT* N` branch's
`PartialPathlist[0]` should be the join path itself; confirm at impl
time that no wrapper kind surfaces that `partialPathDrivingKind`'s
`default` would refuse.

Recommended slice order: **A (NL) → B (merge) → C (bitmap)** — A is
the only corpus-backed slice and its guards are the top-level arm's
verbatim; B is a one-arm admission plus the coincidence-alignment flag;
C needs the only genuinely new machinery and has no producer to drive
it today.

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
