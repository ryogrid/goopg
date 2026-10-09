# M0146-0027 slice 4 — SetOp branch pick + PHJ-probe claim wiring (Q14/Q71/Q76)

Impl slice. Fixes two compounding defects that kept a `Gather` over a
`Parallel Append` from being elected — or from being correct once elected.

## Diagnosis

**Q6** (the other recon target) is routed, not fixed: goopg decorrelates the
correlated `avg()` scalar subquery into `HashJoin(i_category)+HashAgg` while
PG keeps a `SubPlan` and probes `item` above the `Gather Merge`. Keeping the
SubPlan is owned by **M0145-0008y** (`[!]`, blocked on **M0146-0012** —
correlated restrictions as base-rel index quals). No code change here.

**Q71** had a two-layer defect:

1. *Producer* (`internal/optimizer/windowsetoppaths.go`): `setOpBranchPick`
   blindly embedded `branch.PartialPathlist[0]` — for Q71's legs that is a
   `ParallelHash` partial, which `setOpBranchDrivingKindIsSupported` refuses
   (no per-branch PHJ build state, ledger row for M0140-0006c-3). The
   produced `PathSetOp` could therefore never be gathered: dead-weight
   partial. Upstream needs no filter because every entry in
   `partial_pathlist` is runnable (`linitial(child->partial_pathlist)`,
   `postgres/src/backend/optimizer/path/allpaths.c:1544`); goopg's
   branch-partial pool contains executor-refused shapes, so the pick must
   skip them. Fix: `cheapestRunnableSetOpBranchPartial` selects the cheapest
   partial satisfying `setOpBranchDrivingKindIsSupported`.

2. *Executor* (`internal/executor/parallel_scan.go`): `attachAll`'s `*setOp`
   arm returns early after wiring `setOpLeft`/`setOpRight` branch claims. When
   `unwrapToSetOp` reached the setOp *through a Parallel Hash join's probe
   side* (the newly elected `Gather -> Parallel Hash -> Parallel Append`),
   the join's own partial build side never got its `hashBuildBranch` claim
   set — every participant scanned the whole build relation into the shared
   table. Observed: the isolated `(web ∪ catalog ∪ store) ⋈ item` repro
   returned 2403 = 3 × 801 (PG's value); Q71's checksum flipped to
   `c59974eb81acf046` with 290 correct rows. Fix: after branch wiring, run
   `attachParallelHashBuildSides(op)` so ParallelHash joins on the path to
   the setOp keep their per-join build claims.

## Result

- Q71: `Gather -> NL -> PHJ(Parallel Append[3 legs], item) -> Index Scan
  time_dim`; 290 rows, ck `e9f1fcd7c28a1f8f` = oracle.
- Q14/Q76: `Append -> per-leg Gather(PHJ)` collapsed to `Gather ->
  Parallel Append -> per-leg HashJoin` — the PG shape class.
- Q55: gained `Finalize GroupAggregate -> Gather Merge -> Partial
  GroupAggregate` over the union feed.
- Census (SF0.25, capture `tmp/m0146-0027s5-capture/`): divergent 88 -> 88,
  parallelism 55 -> 54 (fireset baseline-vs-candidate), D3-partialpath
  28 -> 26 vs slice-3 census; Q37/Q55/Q71 records moved deeper. See
  `census-sf025-slice4.txt`; per-query plans `q{6,14,37,55,71,76}{,-pg}-sf025.txt`.
- Residual Q71 record is now `join-order` (`PG NL Inner | goopg NL Inner`
  depth 4 under Gather): PG probes `item` by index inside the NL chain and
  runs per-leg PHJs (needs the ledgered per-branch PHJ build state);
  goopg hash-joins `item` once. Cost-space difference, not reachability.

## Tests

- `TestAddPartialSetOpPathPicksCheapestRunnableBranchPartial`,
  `TestSetOpBranchPickSkipsUnrunnablePartial` (optimizer).
- `TestAttachAllWiresHashBuildAboveProbeSetOp` (claim wiring unit).
- `TestGatherOverParallelHashProbeSetOpIdentity` (e2e, 1/2/4 workers —
  40-row join; pre-fix returns 80/120/200).

## Gates

See `gates.txt`. All PASS on the staged index.
