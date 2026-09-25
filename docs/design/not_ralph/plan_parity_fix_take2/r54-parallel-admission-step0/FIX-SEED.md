# R54 fix round — size upper `seed.Rows` from the search joinrel (scope, 2026-09-10)

*Scope for the R54 fix round (REPORT-step2.md §4): Q5's filed split
loses by 0.32 on a ~1.2-row upper seed from `EstimateRows(child)`
while the join search prices the same relation at 1834/7335 rows;
Q9 wins by 2055.17 on the same three terms flipped. The fix sizes the
upper seed from the search joinrel's rows. No cost function changes,
no constant moves, no new producers — one assignment, fail-closed.*

## 0. Wiring (measured, not assumed)

- Single production caller: `planSelectWithSettings`
  (`planner.go:1724-1749`) → `buildAggregateStage` (:1727) →
  `createGroupingPaths(upper, agg.node, …)` (:1749). In scope: the
  finished `Node` tree only — no `searchCtx`, no joinrel list. The
  search runs before (`relfromjoinlist.go:742`), its object is gone.
- Bridge that survives: `searchedTree.searchRel` (`searchedtree.go:111`),
  stamped on the published root at `createplanroot.go:188`. The
  existing accessor `searchedRelOf` (`searchedtree.go:169`) walks
  `boundaryWalkChildren` — which descends through Aggregate/WindowAgg/
  Distinct/Filter/Limit — and that contract ("kinds between statement
  root and spliced subtree") is BROADER than this consumer needs: an
  agg-over-agg (or Filter/Limit/Distinct between agg and search root)
  would seed the outer agg with the wrong scope's rows, and
  `sr.Rows > 0` cannot catch wrong-scope. So the fix adds a restricted
  accessor, `searchedJoinInputRelOf` (new in `searchedtree.go`):
  same loop, but returns nil at Aggregate/WindowAgg/Distinct/
  DistinctOn/Filter/Limit/SetOp/multi-child joins — descending only
  through row-preserving pass-throughs (Project/Sort/Gather/
  GatherMerge/Memoize/OrdinalityWrap/LockRows). It fires exactly when
  search rows == agg input rows; anything else keeps the legacy seed
  (fail-closed direction). Unit pins: descent-through-Project finds,
  each stop kind returns nil, depth-cap intact.
- `createGroupingPaths` builds one `seed` (`groupingpaths.go:75-79`:
  `Rows = EstimateRows(child)`, `Cost = legacyDisplayCostOf(child)`)
  and hands the SAME pointer to `addGroupingPaths` (:81, serial arms
  read `seed.Rows`/`seed.Cost` at :310-311) and
  `addPartialAggSplitPath` (:88, split reads `seed.Rows` at
  `partialaggupper.go:282`, `parallelSeedCost(seed.Cost, d)` at :314).
  Mutating `seed.Rows` between :79 and :81 flows to both families.
  Blast radius: this GROUP_AGG contest's prices + winner choice only —
  `seed` is function-local, filed only on the grouped rel; the
  copy-back carries spec, not cost.

## 1. The change (one assignment, fail-closed)

In `createGroupingPaths`, between seed construction (:79) and
`addGroupingPaths` (:81):

```go
if sr := searchedJoinInputRelOf(child); sr != nil && sr.Rows > 0 {
    seed.Rows = sr.Rows
}
```

Legacy estimate stays when the child is not a row-preserved searched
tree (`sr == nil`: unsearched input, or a stop kind — inner agg,
filter, limit, distinct — between agg and search root) or carries no
rows — the fix never fires outside the measured shape. `seed.Cost`
untouched (see §2).

## 2. Rows-only, not Rows+Cost (deliberate, priced)

REPORT-step2 §3's margin is all row-driven terms: Gather tuple
(0.1×rows), `costAgg` trans/combine per input row, `perWorkerRows` →
`partialGroups` → `crossedRows`. The input PRICE cancels across the
live contest (both split and gathered-no-split build on the same
pseed). Replacing `seed.Cost` too would move the absolute serial-vs-
parallel levels (the 118k/144k gaps in REPORT-step2 §2) — wider,
unpriced, and a separate scope decision for a later round. Rows-only
is the minimal change that moves exactly the priced terms.

Deliberately excluded: `grouped.Rows` sizing (`groupingpaths.go:128`,
a second legacy read — group COUNT, not input rows, separate
question); `idxSeed` (:386-390, index-ordered sorted arm); workers,
agg algorithm, vetoes (STEP2 §5 seams stay shut).

## 3. Predicted effects (falsifiable by the §4 measurement)

- Q5: `inputRows` 1.2 → search top-rel rows (search prices 1834
  partial / 7335 serial; record which value `sr.Rows` actually is at
  measurement — both flip the verdict). `perWorkerRows` → ~458
  (1834 case), `partialGroups` → min(ndistinct(n_name)≈25, 458) = 25,
  `crossedRows` 4 → ~100: Gather-tuple delta 0.1×(100−1834) ≈ −173,
  Finalize-vs-serial saves ~−9, Partial pre-agg costs ~+3 → split wins
  by ~180 (7335 case: ~−750). The margin survives because Q5's group
  count is small — this does NOT generalize to high-cardinality
  groupings, where crossedRows growth eats the same delta it creates.
  Mixed sourcing, stated: `partialGroups` becomes search-derived
  while `finalGroups` (`grouped.Rows`) stays legacy, so ~25 partial
  groups merge into ~1-2 final groups — flatters the split slightly,
  accepted under the §2 exclusion.
- Q9: seed 40404 → search rows (~75k family, groups ~175,
  crossed ~700 vs 75k input); margin grows, split still wins (2055
  absorbs it — re-measured, not assumed).
- Serial arms move up with inputRows in both queries (still out by
  5-6 figures — the divisor gap is structural, C-19g by design).
- Q1: the one-relation protocol stamps unconditionally, so Q1's child
  is likely searched — prediction is identical plans IFF legacy seed
  ≈ search rows there; derive both numbers at measurement, and a Q1
  move traceable to the same estimator gap is an expected
  consequence, not a breach. Q84: no agg-over-searched shape in the
  corpus — expect identical; any Q84 move IS a breach.

## 4. Measurement protocol (fix-round gates)

Same capped clone/GUCs/top-mode as Step-2, Q5+Q9+Q1 (+Q84 ds05):
instrumented-vs-fixed EXPLAIN per query (the fix must MOVE Q5 to
Finalize→Gather→Partial and KEEP Q9's split), run-stable ×2, plus the
§2 DPPATH harvest re-run (new margins on the lines). Code gates:
optimizer + estimateaudit suites green, no `-count=1`, `go vet`
clean. TODO_ALL protocol arms: `make plan-gate` (with explicit
`PLAN_DB`/`PLAN_USER`/`PATH` — the silent-skip memory),
`estimate-audit -plan-only`, `pg-plan-parity-diff`. Planner-change
gates per repo rules: `tpch-spotcheck.sh` and the TPC-DS SF0.5 sweep
if runnable (SKIPPED with reason if the cluster state forbids —
Step-1 precedent; substitute a wider top-mode EXPLAIN diff, all
TPC-H + a TPC-DS subset, since every grouped query with a searched
child re-prices). EXPLAIN-diff set adds **Q3/Q10** — the C-19g guard
shapes the gathered arm exists for; their contest moves with the
seed too. No test pins Q5 upper
totals (§5 of wiring map); test helpers mirror the legacy seed
(`groupingpaths_test.go:123`, `partialaggupper_test.go:37,96,130`) —
add a search-rel override arm to at least one fixture so the new
branch is covered (existence-before-verdict: legacy-nil path pinned
too).

## 5. Exit criteria

Q5 Finalize→Gather→Partial wins; Q9 split still wins; Q1 plans
byte-identical to Step-2 top, or any Q1 move is traced to the same
estimator gap with both numbers derived (§3); Q84 plans
byte-identical; suites green; margins re-tabled in
REPORT-fix. Anything else (Q9 flip, Q84 move, serial win) fails the
round back to design.
