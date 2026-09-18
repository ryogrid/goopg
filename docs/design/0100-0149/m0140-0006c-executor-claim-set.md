# M0140-0006c — executor claim-set for `setOp` under `Gather`

**Status:** accepted
**Milestone:** M0140 (TPC-DS parallelism), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
**Task:** `.ralph/fix_plan.md` M0140-0006c
**Kind:** impl
**Parent:** M0140-0006 (`m0140-0006-decomposition-into-a-b-c.md`)
**Movement:** none (correctness prerequisite; no plan can select a partial SetOp until M0140-0006b-2 lands)

## What landed

`gatherOp` builds each worker its own full copy of the child subtree — safe
for a base scan only because `parallel_scan.go`'s claim state hands each
worker a disjoint partition. `setOp` (`operators_setop.go:32` `newSetOp`)
streams BOTH children to completion unconditionally, and before this task had
no arm in `attachAll` at all: every worker would replay both entire UNION ALL
branches, and a Gather over a partial SetOp would return (workers+1) copies
of every row. Four pieces, all in this loop's diff:

1. **Executor claim-set** (`internal/executor/parallel_scan.go`).
   `parallelClaimSet` gains `setOpLeft`/`setOpRight` — INDEPENDENT claim
   state for the two branches, built once and eagerly by
   `newParallelClaimSet` (never lazily: `attachAll` runs from every worker
   goroutine concurrently). Independence is load-bearing, not tidy: a
   `parallelScanState` publishes its block-count boundary from whichever scan
   opens FIRST (`initOnce`), so sharing one state between two branches on
   DIFFERENT relations would silently apply one branch's boundary to the
   other. `newLeafParallelClaimSet` builds the branch state without its own
   nested setOp pair (bounded to one level — a branch can never itself be a
   partial SetOp: `searchedRelOf` does not recognise `createSetOpPaths`'s
   own output as a search root, so `addPartialSetOpPath` can never see a
   partial path on a SetOp-shaped branch) and with `pbm` left nil forever (a
   bitmap-driven branch is refused at the planner — see below — because
   `collectBitmapScans` does not descend into a `*setOp`). `unwrapToSetOp`
   walks the same wrapper kinds `attachParallelScan` sees
   (Filter/Project/instrumentedOp/Sort/Aggregate) so an instrumented or
   wrapped SetOp is found the same way a bare one is; `attachAll` dispatches
   to it before the flat single-state walk, attaching each branch through its
   own leaf claim set. Both `gatherOp` and `gatherMergeOp` consume the shared
   `attachAll`, so both get the arm with no second call site to keep in sync
   (the sibling-agreement class `parallelClaimSet` exists to prevent).
2. **Planner node-side twins** (`internal/optimizer/parallel.go`).
   `*SetOp` arms in `stampParallelScan` (stamps BOTH branches, copy-on-write),
   `drivingScan` (both branches must resolve to a driving scan — a single
   unmodelled branch refuses the whole SetOp, unlike a join which is partial
   through one side only), and `unstampParallelScan` (the inverse). The
   general recursive form is deliberately WIDER than the admission gate
   below: a shape the twins accept but the whitelist refuses stays serial
   (missed optimisation), never wrong — the safe direction.
3. **Whitelist arm** (`internal/optimizer/gatherpaths.go`).
   `partialPathDrivingKind` gains `case PathSetOp:`, narrowed to a bare
   `PathSeqScan` or unparameterised `PathIndexScan` on EACH side
   (`setOpBranchDrivingKindIsSupported`) — NOT the general recursion the
   other arms use. A join-driven branch would need its build side prebuilt
   the way `prebuildHashJoins` does for a top-level partial join, and a
   bitmap-driven branch would need `prebuildBitmap` to find it; neither
   collector (`collectShareableJoins`/`collectBitmapScans`) descends into a
   SetOp today, so both are refused (fail closed → serial). Ledger row filed,
   owned by **M0140-0006c-2**.
4. **Tests.** `TestGatherOverSetOpIdentity`
   (`parallel_setop_claimset_test.go`, new file): a real `Gather` over a
   hand-built streaming UNION ALL SetOp over two DIFFERENT-sized tables
   (260 + 90 rows — different counts so a shared-boundary leak shows as a
   wrong count, not just slowness), at 1/2/4 workers, asserting every row
   exactly once. `TestParallelClaimSetAttachesEveryKind` gains the
   setOpLeft/setOpRight arms (the reflection-driven coverage that fails on
   any unwired claim kind). Optimizer side: `TestDrivingScanAdmitsSetOp…`,
   `TestDrivingScanRefusesSetOp…` (both mirrors), stamp/unstamp both-branch
   tests, and five `partialPathDrivingKind` tests (accept two-scan-branches
   + `partialPathShapeIsGatherable`; refuse bitmap / join / parameterised /
   malformed-children).

PG citations: the partial-Append shape priced by M0140-0006b follows
`cost_append` (`postgres/src/backend/optimizer/path/costsize.c:2250-2403`)
and `partial_subpaths_valid` (`allpaths.c:1538-1577`); the mixed
partial/non-partial arm NOT built follows `append_nonpartial_cost`
(`allpaths.c:1592-1622`, already ledgered under M0140-0006b). Executor side:
PG's parallel Append partitions subplan execution across workers; goopg's
equivalent is the per-branch leaf claim sets here (each worker's branch scan
reads a disjoint block/leaf set, the union over workers is the branch
exactly once).

## Why landing the whitelist before M0140-0006b-2 is safe

This loop lands the whitelist opening ahead of the reachability wiring
(M0140-0006b-2, still open) — the order the 0006b design doc blesses
("even with gate 2 opened first … gate 1 still refuses until the wiring
lands"). Verified, not assumed, by auditing every production caller that
can offer a partial path to `makeGatherPath`/`makeGatherMergePath`:

- `generateUsefulGatherPaths` has exactly three production call sites
  (`gatherpaths.go:558` over base rels, `joinsearchlevel.go:346` over a
  join-search level, `geqo.go:418` over the GEQO joinrel) — all read a
  `*searchCtx`'s own joinrels, none reachable from any upper-rel producer
  (`createSetOpPaths` runs from `planner.go`'s SetOp fold, outside any
  `*searchCtx`). No partial `PathSetOp` exists in any base/join rel's
  `PartialPathlist` either (`addPartialSetOpPath` only seeds the upper
  SETOP rel), so the new arm has no reachable input — dead code until
  0006b-2 lands, and when it does, the executor half is already here.
- The "whitelist last" constraint from the 0006b doc is satisfied
  atomically: executor claim-set, node twins, and whitelist open in ONE
  commit, so the whitelist never opens without the claim-set behind it.

## Non-vacuity

`TestGatherOverSetOpIdentity` with only the `attachAll` `*setOp` dispatch
disabled (one-line `&& false` mutation, reverted after): workers=1 → 700
rows, workers=2 → 700, workers=4 → 1750, want 350 — the exact
(workers+1)x N-copies defect the task exists to prevent. With the fix:
350/350 at all three worker counts, every row exactly once.

## Acceptance

`go build ./...` clean; `go vet` clean (both packages — including a fix to
the adopted in-flight test, which referenced a non-existent
`intDatumForTest`; replaced with the existing `NewIntDatum`, same
`datumTestString` rendering the rest of the file already used);
`go test ./internal/optimizer/... ./internal/executor/...` fully green;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34);
`scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0, PLAN-SHAPE queries=99 same=99 changed=0;
`scripts/tpch-acceptance-arm.sh` PGSHAPED=1 HEAD-baseline (stash
round-trip, private port 5583, `GOOPG_ANALYZE_SEED=20260905` pinned both
arms) vs staged tree: VERDICT PASS, 24/24 labels MATCH;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` no FAIL
(includes `internal/parser`, whose pre-existing AST-drift failure prior
loops reported is green at this HEAD). All three gate stamps share one
`code_tree` matching the staged index (`dirty_code: false`).

D2 report: no parity capture run in this loop (PARITY: N/A — see commit
message); shape-delta is the sweep's PLAN-SHAPE line (99/99 identical) plus
the arm's 24/24 MATCH; stats epoch pinned via `GOOPG_ANALYZE_SEED=20260905`
on both arms; planning route unchanged (PG-shaped search default);
wall-time: no plan changed, so no per-query timing owed.

## Provenance note

This loop adopted an uncommitted in-flight diff (6 modified files plus one
untracked test file, ~10 minutes old — the previous loop was cut off without
recording it; `working_set.md` said "In-flight: none") rather than re-deriving: executor
claim-set, twins, whitelist, and all optimizer tests were complete and
coherent; the only gap was the untracked identity-test file referencing a
helper that does not exist (fixed as above, then mutation-verified). No
design content was re-derived — the parent decomposition doc and the 0006b
doc's ordering note already scope this task exactly.

## Deferred / follow-ups

- **M0140-0006c-2** (filed in `.ralph/fix_plan.md`, Parent: M0140-0006c):
  join/bitmap-driven SetOp branches — teach `collectShareableJoins` /
  `collectBitmapScans` (and `prebuildHashJoins` / `prebuildBitmap`) to
  descend into a `*setOp`'s children, then widen
  `setOpBranchDrivingKindIsSupported` past bare scans. Ledger row filed
  (`.ralph/deferral_ledger.md`, 2026-09-18, `M0140-0006c`).
- M0140-0006b-2 (reachability wiring) is unaffected and remains the step
  that can first SELECT a partial SetOp plan; with this task landed, its
  arrival needs no further executor work for scan-driven branches.
