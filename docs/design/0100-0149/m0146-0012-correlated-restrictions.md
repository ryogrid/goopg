# M0146-0012 — correlated restrictions as base-rel index quals

Status: in progress. The prerequisite slice landed 2026-10-05
(`c8369e384`), and so did impl slice 1 (`c21a02c93`). Still open: retiring
`flattenStrandedSeqScanFilters` and the restoring rule, and multi-relation
EXISTS bodies.
Parent: none (banner item 3, M0146 file order). Follows M0146-0015a, which
made a one-relation scope's correlated qual a base restriction.

## PG mechanism

- An outer-level Var in a sublink becomes a `PARAM_EXEC` Param with no
  relids. `distribute_qual_to_rels` therefore files `col = $n` as a base
  restriction of `col`'s relation, however many relations the body joins.
- `match_clause_to_indexcol` accepts the Param side as a pseudo-constant
  (`is_pseudo_constant_for_index`,
  `./postgres/src/backend/optimizer/path/indxpath.c`), so the restriction
  drives an index or bitmap probe.
- `create_bitmap_scan_plan` (`createplan.c`) keeps the probe's clauses as
  `bitmapqualorig`, printed as `Recheck Cond:`. It removes them from
  `qpqual`, so they never appear as `Filter:`.
- On rescan, `chgParam` marks every node above the Param's use for rebuild.
  goopg's twin is `classifySubPlan`'s Close+Open / Build arms (prerequisite
  slice: `analysis/m0146/m0146-0012/prerequisite.md`).

## Prerequisite slice (recon, 2026-10-05)

No per-operator cache crosses an outer binding: joins Close+Open, and
unmodelled nodes rebuild. The one stale context cache is `CTERowCache`
(M0146-0050, S2). A correlated CTE body is therefore out of scope until
M0146-0050 lands.

## Slice 1 — scalar sublink bodies (landed 2026-10-05, `c21a02c93`)

- `planSubqueryExpr` hands the body `PlannerSettings.scalarSublinkBody`.
  `planSelectWithSettings` reads the flag once and clears it, then marks
  the body's `resolveContext`. The seam's
  `partitionConjunctsForJoinPlanningScoped` admits correlated conjuncts as
  leaf restrictions in that multi-relation scope.
- EXISTS bodies stay excluded. goopg's post-planning EXISTS→ANY pass reads
  the correlation off the body's top quals; TPC-DS Q35 lost its hashed ANY
  when those quals sank.
- The bitmap restriction arm (`matchBitmapIndexQuals`) records a same-type
  outer-param key's conjunct as `local`. `createBitmapHeapScanPlan` makes
  it the recheck list and drops it from the reinstated Filter, the
  index-scan arm's existing rule. A literal key keeps its Filter, because
  the arm lacks `restrictionKeyUsable`'s cast checks.
- `BitmapHeapScan` now carries `searchedTree`, since a bare bitmap scan can
  be a one-relation search root.
- `dedupeUnnestParams` keeps one GROUP BY and hash key per correlation pair.
  A repeat that is a distinct pointer is kept as an alias for the clone.

**Result:**

- TPC-H Q2 keeps PG's SubPlan with a `partsupp_part_fkidx` probe,
  1.34 s → 0.20 s. Q17 and Q20 print `Recheck Cond:` as PG does.
- TPC-DS Q32 and Q92 SubPlans match PG's (265 → 18 ms, 36 → 21 ms).
- Fire set: aggregation-strategy 11 → 9 (SF0.25) and 19 → 17 (SF1); no
  category regresses.
- ea-ratchet: 1 fixed.

Evidence: `analysis/m0146/m0146-0012/impl-slice1.md`.

## Open

- Retire `flattenStrandedSeqScanFilters` and the restoring rule.
- Multi-relation EXISTS bodies: teach the EXISTS→ANY pass to read
  correlation from a leaf restriction, then admit the conjuncts there too.
- Rendering in goopg's own decorrelated scalar shape (no PG twin): the
  group key and the inner Hash Cond lose their qualifier once the
  correlated conjunct sits at a leaf.
