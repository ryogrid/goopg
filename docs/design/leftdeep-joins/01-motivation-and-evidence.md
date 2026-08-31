# 01 — Motivation and Measured Evidence

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| role | factual ground for the whole bundle; every number here has a file in `analysis/` or a commit hash |

## 1. Where three lines of work converged and stalled

Three independent efforts all hit the same wall in 2026-07/08:

**(1) MultiHashJoin (the n-ary plan node, M0038 → M0126-0011).** Introduced
because a binary hash cascade "copied ~800K rows × 17 cols" at one Q2 seam
([0038-0001](../0000-0049/0038-0001-multi-way-hash-join.md)). It worked — and it grew a
planner-side coordinate round-trip (flatten to OID-sorted table list, remap
every `ColumnRef`: `rewriteMultiWayChain` `internal/planner/bushy.go:1795`,
`buildMHJPosMap` `:2385`, plus `mhj_input_rewrite.go`, 903 lines) that became
a recurring bug generator: ~34 non-test `case *MultiHashJoin:` arms across
18 files at the 2026-08-02 grep (28/15 at the 2026-07-31 audit — the count
drifts upward), and a documented defect list (no spill; the spanning-tree key walk
silently drops cycle-closing edges; unreached tables get an arbitrary hash
key; incomplete residual walker — `analysis/cost-driven-second-try-200731/02-premise-audit.md`
§11). M0126-0011 (commit `e85e5347`) retired it as the default
(`mhjPackingEnabled = false`, `bushy.go:586`); the node and all its arms are
still in-tree.

**(2) Runtime fusion (`fusedHashJoinOp`, M0126-0006).** The "make MHJ a
runtime strategy" attempt: detect a left-deep INNER hash cascade at
`executor.Build` time and run it as one odometer operator
(`internal/executor/fused_hash_join.go`). Correctness failed on TPC-DS SF0.5
— Q14 returned 100 rows instead of 200, Q22 0 instead of 100 — and the kill
switch is permanently off
(`analysis/cost-driven-second-try-200731/evidence/stage2-fusion-verdict.txt`).
The operator stays in-tree, dead.

**(3) Cost-driven join order (M0126, `GOOPG_COST_DRIVEN_JOINORDER`).** The
acceptance run was a **NO-GO on Q9 alone**
(`evidence/acceptance-run-1.txt`, M0126-0012): every clause of the bar failed
because the cost-driven DP picks a small-dimension-first order that builds
three consecutive ~6M-row intermediate hash tables; Q9 times out at 600 s+
where the integer planner runs it in 58.83 s (default arm,
`evidence/stage3-order-ab.txt`; the pinned R0 sweep recorded Q9 at 85.46 s). Two costing follow-ups
(M0126-0013, commits `c63f8023` and `e13d6c6f`) did not change the plan shape:
*"every alternative join order also builds on large intermediates"* — within
the **bushy** search space as costed. The commit message names the two exits:
"join-enumeration improvement or fusion-operator integration". Fusion is dead
by (2). This bundle is the join-enumeration exit.

## 2. What retiring MHJ costs today (the executor gap this bundle must close)

With MHJ off and no other change, the same scans in a binary cascade regress
(TPC-H SF1, `evidence/stage0-ab.txt` — cascade/fused ratios; and
`evidence/acceptance-run-1.txt` Arm A ~609 s vs pinned R0 493.31 s, +23 %
total):

| query | cascade / MHJ-fused | acceptance Arm A vs R0 |
|---|---|---|
| Q3 | 2.92× | 8.46 s → 32.46 s |
| Q10 | 3.44× | 6.04 s → 22.81 s |
| Q18 | 2.52× | 27.58 s → 79.26 s |
| Q7 | 1.57× | 25.13 s → 41.91 s |
| Q9 | **0.72× (cascade better)** | — |
| Q2/Q11/Q20/Q21 | ~1.0× | neutral |

Two facts matter for the design:

- The regression is **not** operator-dispatch overhead (N operators vs 1) —
  refuted in `analysis/cost-driven-second-try-200731/01-motivation-and-measured-evidence.md`
  §2.5. It is the **probe-seam re-materialisation** (§3 below) plus per-row
  allocation regressions.
- Q9 got *faster* without MHJ under the integer planner — MHJ was never a
  uniform win; it was compensating for specific seam costs on specific
  shapes. Remove the seam costs and the compensator is unnecessary
  everywhere.

## 3. The seam, precisely

A binary hash cascade (left-deep or bushy) **already pipelines** in goopg:
each `joinOp.Open`
drains only its build side (`buildLazyHashTable`,
`internal/executor/operators_join_agg.go:524`) and streams its probe side
(`nextLazy` pulls one `TupleSlot` per row, `:1247`). No intermediate result
set is materialised. Structurally this is already MHJ's "N builds, one probe
pass". What costs is what happens **per emitted tuple per seam**:

- **Legacy `Build` path** (every aggregate-topped query — `buildRec` migrates
  no `Aggregate`, `internal/executor/executor.go:451-588`, so all TPC-H/DS
  star queries run it): `nextLazy` calls `r := slotRow(probeSlot)`
  (`operators_join_agg.go:1254`); when the child is another `joinOp` this is
  `VirtualSlot.Row()` (`internal/executor/slot.go:159-166`) = one pooled-row
  `Get` + width-wide zeroing + width-wide 48-byte-`Datum` copy — and the
  pooled row is never released on this path. Magnitude: ~6M probe rows × 3
  seams ≈ 18M pool round-trips and ~2×2.3 GB of `Datum` traffic on a Q9-class
  query (`02-premise-audit.md` §4.1).
- **`mergedKeySlot`** (`operators_join_agg.go:986-1014`) allocates five
  objects per probe row *and* per build row — a regression introduced by the
  otherwise-correct Stage-0b slot-key fix (`d197365c`). Shape-invariant per
  `Open`; trivially hoistable.
- **Build does two passes and two maps**: `drainRowsBounded` copies every
  build row into a staging `[]Row` (spilling past budget), then
  `buildLazyHashTable` re-iterates that staging op (allocating a
  `*MaterializedSlot` per row, `spill.go:435-442`) to insert into the map —
  and `lazyHashInsertDatum` (`:1016-1033`) populates **both** the
  `map[string][]Row` and the `map[int64][]Row` during build, dropping one at
  finalize. Peak build memory ≈ 2×.
- **Single-column keys**: `planner.Join` carries one `LeftKey`/`RightKey`
  (`internal/planner/plan.go:833-834`); `splitEqualityForHash` keeps the
  first equality conjunct and demotes the rest to a per-match interpreted
  residual (`joinPredicateMatchSlot`, `:1060`). Besides the per-match cost,
  this is the **degeneracy trap**: a constant-pinned key column puts the
  whole build side in one bucket and the join goes quadratic
  (`reselectDegenerateHashKeys`, `internal/planner/inner_join_qual_pushdown.go:676`,
  is the band-aid; TPC-DS Q78 was the incident).
- **No hash-table spill**: the staging list can spill, but every row still
  lands in the in-memory map. A large build under `GOMEMLIMIT` with
  `GOGC=off` is the measured hang/OOM class (Q21 OOM at 244 s in
  `stage2-fusion-verdict.txt` Arm A).

pprof corroboration: `(*VirtualSlot).Row` + `cloneRowOwned` at 12–26 %
cumulative wherever joins dominate
(`analysis/tpch-round5-bottleneck-profiles-20260724.md`); historically
`concatRows` at 56 GB alloc on Q9 and 7,980 GB on Q20 before the lazy-slot
work (`analysis/tpch-pprof-bottleneck-survey.md:133`, `:176`).

## 4. Why the join *order* fails under cost (the planner gap)

`evidence/order-attribution-summary.md` (M0126-0009) attributes every
cost-driven regression to **class (a) cardinality**: ndistinct-based join
selectivity without FK awareness compounds as O(errorⁿ). Q9's chain estimate
runs 1,250 → 37M → 1.5e11 → 1.1e15 → 5.9e15 against an actual final count of
175 (`evidence/order-attribution-Q9.txt`). On top of that, `acceptance-run-1.txt` re-classes the residual as
**class (c)**: even with correct row counts the DP does not price building
consecutive 6M-row hash tables (fixed in part by `e13d6c6f`'s PG-faithful
`inner_pages × seq_page_cost` term). The old integer planner masked all of
this: its MHJ packing swallowed the mis-ordered middle of the tree into one
n-ary node whose estimate was equally wrong (1.8e14) but whose execution
didn't care.

Consequence for this bundle: the search-space change alone does **not** fix
Q9 — a PG-shaped enumerator (left-deep + bushy,
[03](03-join-search-pg-dp.md) §4) with the same cardinality model would
still pick dimension-first orders. [04](04-cost-and-cardinality.md)
(FK-aware selectivity + honest build cost) is a co-requisite, not an
optional refinement. Conversely the cost fixes alone don't suffice either —
M0126-0013 proved goopg's subset-bitmask bushy space as costed has no good
order to find. Shape + cost move together.

## 5. Why "PG-shaped DP" and not another repair pass

The live pipeline chooses join order under one method's cost and then
rewrites methods afterwards (`rewriteJoinsToNLI`,
`internal/planner/nl_index_join.go:78`; MHJ packing when it was on;
`rewriteScanInputsWithSingleTablePredicates`). Doc
[cost-model/12](../cost-model/12-pg-style-join-path-enumeration.md) already
recorded where that leads: the first C4 attempt costed binary hash order only
and regressed Q9 27 s → 250 s+ because the executed plan wasn't the costed
plan. PG's architecture makes that class of bug inexpressible: every method
of every join pair is a costed `Path` inside the search
(`add_paths_to_joinrel`, `postgres/src/backend/optimizer/path/joinpath.c:124`),
and dominated paths die immediately (`add_path`,
`postgres/src/backend/optimizer/util/pathnode.c`). goopg already has the
substrate (`internal/planner/path.go`: `RelOptInfo` :104, `addPath` :284,
`setCheapest` :319, fuzz factor 1.01 :142) — dormant, test-only. This bundle
puts it in the live path.

## 6. What must be recovered, numerically

The bundle succeeds only if all of these hold simultaneously
(bars formalised in [09](09-verification-and-acceptance.md)):

1. **Q3 / Q10 / Q18 / Q7** return to ≤ 1.2× of their R0 (integer+MHJ,
   493.31 s total) times — these are pure executor-seam recoveries
   ([05](05-executor-pipeline-rework.md)).
2. **Q9** completes ≤ 2× R0's 85.46 s (≤ 170.9 s) under the new DP — order
   + cost fix ([03](03-join-search-pg-dp.md) +
   [04](04-cost-and-cardinality.md)); the integer default arm's 58.83 s
   (`stage3-order-ab.txt`) is the aspirational target beyond the bar.
3. **Q21** stops OOMing at SF1 without MHJ — hybrid-hash spill
   ([06](06-hash-spill-and-memory.md)).
4. **TPC-DS SF0.5**: zero row-count and zero checksum deltas across the 99
   queries (57 content-verified, 42 count-only) — the correctness floor that
   killed fusion.
5. No plan anywhere contains `MultiHashJoin` or triggers `fusedHashJoinOp`;
   both are deleted at the end state ([08](08-migration-and-removal.md)).
