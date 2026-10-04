# M0146-0012 — correlated restrictions as base-rel index quals

Status: in progress. The prerequisite slice landed 2026-10-05
(`c8369e384`), and so did impl slice 1 (`c21a02c93`) and slice 2
(`1db4edacc`, `flattenStrandedSeqScanFilters` deleted). Still open: the
restoring rule's correlated half (after M0146-0062) and multi-relation
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

## Slice 2 — delete `flattenStrandedSeqScanFilters` (landed 2026-10-05, `1db4edacc`)

- The flatten (M0145-0027) merged a one-relation scope's searched leaf and
  its stranded correlated or sublink conjuncts into one unsearched
  `Filter{SeqScan}`, so the rule-based producers could build TPC-H Q20's
  probe. Since M0146-0015a the search builds that probe itself.
- With the flatten and the rule's correlated half both off, all 22 TPC-H
  plans are unchanged. TPC-DS SF0.25 changes only Q6 and Q41, in cost:
  Q41's correlated item scan goes 180 → 3987 (PG 4029) and its outer scan
  180 → 1512. Q41 runs 18.2 s → 10–12 s.
- The rule's correlated half stays. goopg types an integer literal `int8`
  (PG `int4`, `make_const`), so an outer key from a VALUES or derived
  literal column is `int8` against an `int4` index. `restrictionKeyUsable`
  refuses that uncast probe, and the rule builds it (regress `join`,
  `unique2 = v.x`). The same typing makes `2147483647 + 1` return a value
  where PG errors: filed S2 as M0146-0062.

Evidence: `analysis/m0146/m0146-0012/impl-slice2.md`.

## Open

- Retire the rule's correlated half once M0146-0062 lands. The rule is
  `planIndexScanFromWhere` under `planIsBareSeqScanTree` in
  `planSelectWithSettings`; its uncorrelated half is M0146-0060's.
- Multi-relation EXISTS bodies: teach the EXISTS→ANY pass to read
  correlation from a leaf restriction, then admit the conjuncts there too.
- Rendering in goopg's own decorrelated scalar shape (no PG twin): the
  group key and the inner Hash Cond lose their qualifier once the
  correlated conjunct sits at a leaf.
