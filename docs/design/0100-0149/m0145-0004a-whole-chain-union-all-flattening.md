# M0145-0004a — whole-chain UNION ALL flattening under one Gather

Status: **LANDED 2026-09-22.** TPC-DS Q71 elects
`Gather → Parallel Append{web_sales, catalog_sales, store_sales}` on both
planner arms — PG 18.3's shape — and returns the same 290 rows.
Task: `.ralph/fix_plan.md` M0145-0004a. Parent: M0145-0004 (the leaf-hoist
arm; the mark-propagation repair that made Q71's candidate visible landed
in `f8349122c`). Kind: impl.

## The defect

PG's `is_simple_union_all_recurse` (prepjointree.c:1617) walks BOTH `larg`
and `rarg`, so `A UNION ALL B UNION ALL C` collapses into ONE flat
appendrel, and `add_paths_to_append_rel` files a single partial Append
covering every member. goopg keeps the binary `SetOp(A, SetOp(B,C))`
tree — the chain-flattening DEPTH divergence M0145-0004's narrowing
recorded: the inner link gets its own Gather and the outer link's inputs
are gathered separately, leaving `Append{Gather→Append{ws,cs},
Gather→store_sales}` where PG emits `Gather{Parallel Append{ws,cs,ss}}`.

The mark+hoist machinery M0145-0004 landed already produced the
whole-chain candidate: `setOpBranchTag` carries the inner link's SETOP
rel out to the outer link's scope, `addPartialSetOpPath` files the outer
partial `PathSetOp{PathSetOp{m1,m2}, m3}` on the outer SETOP rel, and
`addAppendRelPartialPaths` hoists it onto the leaf rel. What refused it
was one `default: return false`: `setOpBranchDrivingKindIsSupported`
declined a branch whose driving kind is a nested `PathSetOp` — no
branch-local executor twin existed, because `newLeafParallelClaimSet`
gave a SetOp branch's claim set no `setOpLeft`/`setOpRight` of its own.
`gatherSubpathIsRunnable` therefore rejected the hoisted candidate,
`makeGatherPath` never filed the outer Gather, and cost elected the
serial Append. The candidate was filed, accepted into the pathlist, and
unusable — the "admitted-but-unelected" state Q71 was stuck in.

## The change

Three pieces, each fail-closed, all in shared (arm-independent)
machinery — the executor twin had to exist BEFORE the planner could
admit the shape:

1. **Nested claim sets grow lazily** (`internal/executor/parallel_scan.go`).
   `parallelClaimSet.setOpBranch(right bool)` returns the per-branch
   claim set, growing `setOpLeft`/`setOpRight` under `setOpKidsOnce` on
   first use. The top-level set still builds its pair eagerly
   (`newParallelClaimSet`); leaf sets grow theirs only where `attachAll`
   actually walks — an N-link chain materialises N−1 levels of branch
   state, in O(depth) instead of the 2^depth a fixed eager bound would
   need. The `sync.Once` is what makes the growth safe: `attachAll` runs
   from every worker goroutine concurrently, and the Once's
   happens-before edge publishes both children to every caller.
   `attachAll`'s `*setOp` arm routes BOTH sides through the accessor —
   claimed-whole branches take that LEVEL's `claimedWhole` flag, partial
   branches recurse into that level's set — so a nested link's member
   scans claim independently of the outer link's. That independence is
   the property the old `default: refuse` was protecting: without
   per-level sets every participant would drain the inner members itself
   and the leader would re-emit them — N+1 copies of every row.

2. **Bitmap prebuild descends** (`bitmapPrebuildTargets` →
   `appendBitmapPrebuildTarget` recursion). A nested `*setOp` branch's
   member bitmaps publish into the leaf claim set one level down — the
   set its workers will actually attach. The flat
   `collectBitmapScans` would have found ≥2 scans on the branch and
   published nothing, leaving every member bitmap unattached.

3. **Planner admission** (`internal/optimizer/gatherpaths.go`).
   `setOpBranchDrivingKindIsSupported` gains a `PathSetOp` arm:
   `return partialPathDrivingKind(p) == PathSetOp`. The contract is the
   parent's own SetOp arm reapplied one level down — two children, each
   either claimed-whole (`ParallelSafe`, no driving kind needed) or
   itself a supported branch — rather than a restated copy. Recursion is
   bounded by chain length and still fails closed: `Sort`, `Memoize`,
   `Aggregate`, and any other wrapper without a branch-local executor
   twin still refuse.

4. **The "Parallel Append" label** (`SetOp.ParallelAware` in
   `internal/optimizer/plan.go`). PG prints the prefix through the
   generic `parallel_aware` rule (explain.c:1630) — the same rule the
   `*SeqScan` and `*Join` arms already honour. `createSetOpPlan`
   stamps the node from `p.ParallelAware`, which only a partial
   `PathSetOp` carries; `setOpNodeName` renders "Parallel Append" for
   it; `unstampParallelScan` clears it when a plan loses its Gather
   post-cache — a "Parallel Append" with no Gather above it would claim
   a parallelism the executor will not run. The flag is render state
   only: claim wiring keys off `LeftNonPartial`/`RightNonPartial` and
   the claim sets, not the flag.

## What did NOT need changing

- The mark, member-scope forcing, `ConsiderParallel` inheritance, and
  hoist (M0145-0004) were already whole-chain-correct: the outer SETOP
  rel's partial IS the flat candidate, because the fold is left-deep —
  the inner link's partial is the outer's left branch, and the executor
  flattens the nesting at claim-attach time.
- `drivingScans`, `unwrapToSetOp`, and the hash-build prebuild
  collectors already recursed nested `*setOp`/`PathSetOp` shapes.
- EXPLAIN's member enumeration already flattened nested streaming UNION
  ALL (`setOpAppendBranches`), so the rendered plan shows one `Parallel
  Append` line with all three members — the node stays binary; only the
  label and the claim tree are new.

## Tests

- `TestWholeChainPartialPathIsGatherable` — a nested `PathSetOp` branch
  and a deeper (3-link) chain are gatherable; a nested branch containing
  a `Sort` still refuses; a non-parallel-safe claimed-whole branch still
  refuses.
- `TestUnionAllChainLeafFilesGatherablePartial` — end-to-end at the PATH
  level on the jointree arm: the three-member leaf's SETOP rel carries a
  partial `PathSetOp` whose child is another `PathSetOp`, and
  `gatherSubpathIsRunnable` admits it. Election is deliberately NOT
  pinned: on this fixture's identical members the member scopes elect
  their own Gathers, which compresses the whole-chain margin below
  `add_path`'s cost fuzz (observed 819125 vs 820750, ~0.2%), so the
  serial outer link wins legitimately. The corpus witness elects it on
  real statistics — see below — and pinning a fixture shape that needs
  manufactured margins would test the fixture, not the admission.
- `TestGatherOverNestedSetOpIdentity`,
  `TestGatherOverNestedSetOpClaimedWholeIdentity` — per-worker row
  identity under one Gather at workers 1/2/4: exactly 390 rows
  (260+90+40), no duplicates, all three members represented; the
  claimed-whole flag is consumed at the correct nesting level. This is
  the carried-risk pin the task text demanded (the
  `TestGatherOverJoinProbeSetOpIdentity` family precedent).
- The claim-set field anti-drift test excludes `setOpKidsOnce`
  (bookkeeping, not a claim kind).

## Lane verification (Q71)

Private SF0.25 clones on `:5591` (jointree arm) and `:5592` (legacy arm —
the admission machinery is shared, so the shape change is not
knob-gated): both emit

```
Gather
  -> Parallel Append
       -> Subquery Scan -> Parallel Hash Join   (web_sales)
       -> Subquery Scan -> Parallel Hash Join   (catalog_sales)
       -> Subquery Scan -> Parallel Hash Join   (store_sales)
```

matching PG 18.3's shape, and both return 290 rows, matching the PG
reference. The pre-change shape was `Append{Gather→Append{2},
Gather→1}`.

## Movement

TPC-DS SF0.25 `parallelism` records and the sweep/floor numbers are
filled from the gate captures at land time (see the commit message and
`.ralph/fix_plan.md` entry).

## Deferrals

- **Legacy-arm bare-member fixture divergence**: a WHERE-less
  single-table member never reaches the legacy search seam
  (`planner.go` gates on `outerLink || appendrelMember || jointree`), so
  a bare `A ∪ B ∪ C` leaf stays serial `Append` on the legacy arm — the
  same seam posture M0145-0004 recorded. Members that ARE searched
  (joins, real corpus queries) flatten on both arms; the shape pin is
  therefore jointree-side only, with the legacy arm's election verified
  on the live corpus.
- Member-level Gather suppression when a whole-chain candidate exists:
  PG gives members no rels of their own, so the question does not arise
  there; goopg's member scopes elect per-member Gathers on cost, which
  is correct in isolation and only compresses the whole-chain margin.
  Whether a hoisted whole-chain candidate should suppress member-level
  Gather offers is a costing question for M0145-0005's member-rel work,
  not an admission defect.
