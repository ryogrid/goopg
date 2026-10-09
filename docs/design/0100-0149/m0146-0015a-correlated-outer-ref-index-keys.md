# M0146-0015a: correlated outer references are index keys again

Status: **LANDED 2026-09-25** (`5dd5144e1`). Task: `.ralph/fix_plan.md`
M0146-0015a (Kind: impl, Parent: M0146-0015, rank inherited from banner item
2a). Recon: `analysis/m0146/m0146-0015/`. Evidence:
`analysis/m0146/m0146-0015a/`.

## Defect

After the M0145-0008 cutover a correlated SubPlan never used its outer
reference as an index key. `where d.thousand = a.thousand` inside a
subquery became a Filter over a full scan, in either operand order. The
regress `subselect` query (a nested EXISTS / NOT EXISTS over `tenk1`) went
from 207 s before the flip to more than an hour; PG runs it in 4 ms.

## Causes

1. **The correlated qual never reached the leaf.**
   `partitionConjunctsForJoinPlanning` (`local_filters.go`) kept every
   conjunct containing an `OuterColumnRef` in the join residual, above the
   search. In PG an outer Var is a `PARAM_EXEC` Param with no relids, so
   `distribute_qual_to_rels` (initsplan.c) makes `d.x = outer.y` a base
   restriction of `d`.
2. **The index producers took only literal keys.**
   `restrictionEqualityPrefix`, `restrictionRangeOnColumn`,
   `consumingIndexClauses` and `matchBitmapIndexQuals` recognised operands
   through `normalizeColumnConst` / `restrictionKeyUsable`, which require
   `isConstExpr`. PG's `match_clause_to_indexcol` accepts any operand that
   references none of the index's relation's columns and is not volatile.

## Change

- **One-relation scopes admit the correlated qual as a base restriction**
  (`conjunctLocalEligibility(c, len(spans) == 1)`).
  - `localizeExprToLeaf` leaves an `OuterColumnRef` untouched, and
    `tableForCol` ignores it.
  - `isParallelSafeExpr` keeps a leaf carrying one off partial paths, as
    PG's parallel-restricted Param does.
  - The executor re-runs a correlated SubPlan's tree per outer row
    (re-Open, Close+Open or rebuild; `subplan.go`), so a leaf Filter or index
    key re-reads the value.
  - `conjunctIsLocalEligible` keeps its contract for its other callers.
- **Multi-relation scopes keep the old placement.** A wider draft admitted
  correlated quals everywhere. That sank them under the body's join, where
  goopg's *post-planning* EXISTS→ANY pass (`exists_to_any.go`, which reads
  the correlation off the body's top qual holder) no longer saw them. TPC-DS
  Q35 lost its hashed `ANY` SubPlans and hit the 300 s timeout. PG runs
  `convert_EXISTS_to_ANY` on the query tree, before planning, so it has no
  such conflict. The remaining gap is ledgered.
- **`normalizeColumnIndexKey`** (`pathindexrestrict.go`) admits a literal, an
  `OuterColumnRef` or an `ExecParamRef`.
  - `restrictionKeyUsable` and the bitmap matcher require an outer key to
    carry the column's own type, because the btree probe encodes it uncast.
  - Equality is priced with PG's `var_eq_non_const`
    (`indexKeyEqSelectivity` → `varEqNonConstSelectivity`), not with MCVs.

## Latent defects the change exposed (fixed together)

- **Nested pull-up rebase** (`rebasePulledQual`, `jointreepullup.go`).
  - An outer reference that walked off the top of the pulled-body chain was
    decremented by one. That is right only for a top-level body; a nested
    body's grandparent reference stayed `OuterColumnRef{Level:1}` and failed
    at execution with "out of range (depth=0)".
  - It now counts hops. A nested body that reads the emitting scope declines
    the pull-up, because the search cannot yet place a join clause that
    crosses into a nested semi join's RHS: `createPlan` panicked re-basing
    it. That support is filed as M0146-0015c.
- **Unnest collectors** (`collectUnnestParams`,
  `collectUnnestParamsAndResiduals`, `unnest.go`).
  - Both walked into every join and lifted correlated conjuncts to the
    unnested join's keys, including from a semi/anti RHS. There they read
    the semi join's LEFT column at that position: goopg returned 0 / 0 / 299
    where PG returns 4 / 0 / 297.
  - `correlationOnUnliftableJoinSide` now makes both decline. It covers a
    semi/anti RHS, an outer join's nullable side, either side of a full
    join, and a correlated non-inner ON predicate.
  - With the bounded admission this is defensive; the collectors' soundness
    should not depend on where the search placed a qual.
- **One-relation bypass hand-off** (`flattenStrandedSeqScanFilters`,
  `planner.go`). The correlated qual now arrives inside the searched leaf
  Filter, so a lone searched `Filter{SeqScan}` carrying one is also handed to
  the rule-based bypass. TPC-H Q17/Q20 bodies therefore keep their index
  probe when the search elects no index, and `canUnnestSubquery`'s
  probe-cheap guard still reads them as cheap.
- **SubPlan rescan classification** (`classifySubPlan`,
  `internal/executor/subplan.go`).
  - A keyed single-producer `BitmapHeapScan` is now a common SubPlan leaf.
    It was unmodelled, so the tree was rebuilt per call.
  - It is now `rescanCloseOpen`, matching its twins `planIsIndexScanBased`
    (executor) and `innerPlanIsIndexProbeCheap` (planner).
  - It uses Close+Open rather than a bare re-Open: `openPrep` builds a fresh
    producer on every Open.

## Results

- **One-level plan:** goopg now matches PG,
  `Index Only Scan … Index Cond: (thousand = a.thousand)`.
- **Regress `subselect` query:**
  - Both SubPlans use index keys again; the query runs about 197 s
    standalone, the pre-flip level.
  - The whole `subselect` regress file completes in 11.9 s; it stays skipped
    for other divergences.
  - PG's 4 ms needs the two sublinks pulled up into joins (M0146-0015b).
- **Correlated-shape probes:** 12 shapes, all equal to PG except one
  pre-existing HEAD error, which is filed.
- **Gates:**
  - Units, `tpch-spotcheck`: PASS.
  - Acceptance arm: values identical.
  - SF0.25 sweep: 96 PASS, 0 timeouts.
  - Fire set: Q6 only, a cost-only change at both scales. The decorrelated
    `item j` body's displayed Seq Scan cost dropped from 896 to 180: its
    stripped leaf Filter no longer carries the page term. Values unchanged;
    ledgered.
  - Full `TestPort_RegressSuite`: PASS.

Movement: none on the parity instruments.

## Found here, filed separately

- **Explicit-opclass restart defect.** An index created with an explicit
  opclass (`USING btree (a int4_ops)`) returns wrong rows after a clean
  restart; it reproduces before this change. Filed as an S2 wrong-results
  task with an escalation.
- **LEFT JOIN ON outer reference.** An outer reference in a LEFT JOIN ON
  clause on the nullable side errors `column … does not exist`. It errors
  identically at HEAD. Filed.
