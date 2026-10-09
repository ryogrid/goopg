# M0146-0027 slice 5 — Parallel Hash join as a SetOp branch driver

2026-09-28. Child of M0146-0005. Target: the ledgered deferral from
M0146-0002 slice 2 (`.ralph/deferral_ledger.md` 2026-09-24 row) — PG
files `Parallel Hash Join` inside Parallel Append subpaths (Q71's three
UNION ALL legs each elect `sales ⋈ date_dim` PHJs), while goopg refused
`PathHashJoin.ParallelHash` in `setOpBranchDrivingKindIsSupported`
because "the branch claim sets carry no per-join build state".

## Finding: the executor half already existed

Slice 4's reading was confirmed by audit — every mechanism the deferral
row named was already generic:

- `collectShareableJoins` (parallel_hash_build.go) descends both
  `*setOp` branches.
- `ParallelHashJoinsIn` (optimizer/parallel.go) reaches SetOp children
  through generic `parallelChildren`.
- `attachAll`'s `*setOp` arm (slice 4) wires branch claim sets AND calls
  `attachParallelHashBuildSides(op)`, so a PHJ *inside* a branch gets
  its `hashBuildBranch` claim from the leaf claim set.
- `parallelHashBuild.attach`/`registerParallelHashBuilds` are keyed per
  optimizer join, indifferent to whether the join sits above or inside
  an Append branch.
- `stampParallelScan`/`unstampParallelScan`/`drivingScan` handle PHJ
  under SetOp generically.

The only actual gate was the explicit `p.ParallelHash -> false` arm in
`setOpBranchDrivingKindIsSupported` (gatherpaths.go).

## Change

`setOpBranchDrivingKindIsSupported`'s `PathHashJoin` arm now admits
`ParallelHash` under the same restriction the top-level
`partialPathDrivingKind` arm carries: the build child's driving kind
must be `PathSeqScan`. A bitmap-driven build would find no prebuilt
bitmap in the leaf claim set (the N-copies defect); a merge-join build
over a seqscan outer resolves to `PathSeqScan` and is admitted, matching
the top-level rule — each participant merge-joins its claimed outer
partition against the whole shared build.

Two-line guard change; no executor or registration code touched.

## Tests

- `TestSetOpBranchDrivingKindAdmitsParallelHashSeqBuild` (new):
  admission matrix — seq build admitted, merge-over-seq build admitted
  (terminal driving kind PathSeqScan), bitmap/index/merge-over-bitmap
  builds refused, plain (non-parallel) HashJoin unaffected.
- `TestAddPartialSetOpPathPicksCheapestRunnableBranchPartial` +
  `TestSetOpBranchPickSkipsUnrunnablePartial` (updated): the slice-4
  "unrunnable" fixture was a seq-build PHJ — now runnable; switched to
  a bitmap-driven build so the tests still pin skip-of-refused-branch.
- `TestAttachAllWiresHashBuildInsideSetOpBranch` (new): claim-set
  wiring for a PHJ that IS the branch driver.
- `TestGatherOverSetOpBranchParallelHashIdentity` (new): e2e serial-vs-
  parallel identity at 1/2/4 workers — no duplicate output, no
  cross-worker over-counting (the slice-4 defect class, now for the
  PHJ-in-branch direction).

## Measured (private SF0.25 clone :5590, bin/goopg staged build)

- Q71: **PG's shape elected** — `Gather -> NL -> NL -> Parallel Append
  -> per-leg Parallel Hash Join (ws/cs/ss ⋈ date_dim) -> Index Scan
  item -> Index Scan time_dim` (q71-sf025.txt vs q71-pg-sf025.txt).
  290 rows, ck=`e9f1fcd7c28a1f8f` — oracle-exact. Remaining delta vs
  PG: PG wraps each leg in `Subquery Scan on "*SELECT* N"` and orders
  the append legs differently (store, catalog, web vs web, catalog,
  store) — the Subquery-Scan arm is M0146-0026's residual.
- Q76: `Gather Merge -> Parallel Append` legs now include per-leg PHJs
  (q76-sf025.txt).
- Q14/Q55/Q6 unchanged vs slice 4; all checksums clean.
- Sweep plan channel: changed = {Q14, Q71, Q76} — the three PHJ-in-
  branch witnesses; verdicts all PASS.

## Census

`census-sf025-slice5.txt` (from the fireset candidate capture,
`tmp/tpcds-sf025/m0146-0027s5c-tpcds-sf025-*`).

First-divergence records: divergent 88 -> 88, match 11 -> 11. Q76's
record relabeled `sort-strategy` -> `aggregation-strategy` at the same
depth-1 site (the plan underneath changed; the recorded category moved,
not resolved). Q71's record keeps the `depth=4 join-order` label but
its substance moved from "single PHJ vs PG's NL-over-append" to
leg-ordering/Subquery-Scan inside now-identical NL spines.

All-depth categories (fireset baseline -> candidate, SF0.25):
join-method 46 -> 45, parallelism 54 -> 53, sort-strategy 59 -> 58,
parameterisation 34 -> 35, rendering 27 -> 28. SF1: join-method
42 -> 41, sort-strategy 53 -> 52, parallelism 58 -> 57,
D3-partialpath 25 -> 26.

## Gates

See `gates.txt` — all PASS on the staged index.
