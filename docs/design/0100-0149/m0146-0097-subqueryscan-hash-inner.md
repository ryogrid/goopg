# M0146-0097 — Subquery Scan on a hash join's hashed side

Status: done 2026-10-08 (5a17a7fd1). Parent: M0146-0092. Evidence:
`analysis/m0146/m0146-0097/`.

## Problem

`stripTrivialSubqueryScans` decides each wrapper by the tlist regime its
consumer imposes. `subqueryStripSpineBreaker` listed `Join` as a
physical-regime breaker for both children, so a subquery leaf on a hash
join's hashed side was stripped whenever its width matched. PG keeps it
unless the join reads its columns whole and in order. For example, in
`select big.v from big, (select a, sum(b) s from t group by a) x where
big.k = x.a`, PG prints `Subquery Scan on x` under the Hash.

## PG behaviour

- `create_hashjoin_plan` (createplan.c) plans the inner input with
  `CP_SMALL_TLIST` (its Hash node), so `use_physical_tlist` declines.
- It plans the outer input with `CP_SMALL_TLIST` only for a multi-batch
  join, and with flags 0 (physical) otherwise.
- A merge join plans its inputs with flags 0 unless they need sorting; its
  `materialize_inner` Material is added over an inner planned with flags 0.
  A `MaterialPath`, as used on a nested loop's inner, passes
  `CP_SMALL_TLIST`.

The PG 18.3 probes (`probe-hash-inner.*`):
- On the hashed side, a subset read, an out-of-order read and a resjunk
  group key each keep the wrapper; a full in-order read strips it.
- An ORDER BY subquery on the probe side is stripped. Its physical tlist
  matches the subplan, including the NULL constant
  `remove_unused_subquery_outputs` leaves.
- On the hashed side, a one-of-two read keeps it.

## Change

1. **Hashed child.** The strip walk gives a hash join's build child —
   `BuildLeft ? Left : Right`, EXPLAIN's `hashBuildChild` rule — the
   pathtarget regime. The outer side stays physical.
2. **Own level only.** `subqueryPlanContains` stops at a nested
   `SubqueryScan`, a `CTEScan` body and a `SetOp`'s arms.
   - It decides whether a derived body is non-simple
     (`derivedSubqueryNeedsScan`) or an unsafe append member
     (`isSafeAppendMember`).
   - PG's `is_simple_subquery` reads only the subquery's own level. TPC-DS
     Q44's `asceding` (`select * from (… rank() …) v11 where rnk < 11`) is
     simple and pulled up in PG. goopg wrapped it because the window sat one
     level down.
   - Under the physical regime that wrapper had always been stripped, so
     the bug never showed. Under the Hash it would print (Q44 SF1: 6
     Subquery Scans against PG's 4).

## Verification

- **Probes.** All five probe shapes match PG.
  `TestSubqueryScanOnHashJoinInner` checks four shapes through EXPLAIN.
  Disabling the rule fails its three keep cases.
- **TPC-DS fire sets.** Q8 and Q44 change at both scales, and executed
  results are identical.
  - Q44 SF1 now has PG's 4 Subquery Scans.
  - Q44 SF0.25 has 2 against PG's 4. v11 now sits directly under a Merge
    Join on the WindowAgg's `rnk` output. goopg infers that order from
    `rank()` and adds no Sort; PG adds one and keeps the wrapper. That is
    the 0066 recon's class D (filed as M0146-0099).
  - Q8 changes costs only.
  - First-divergence categories are unchanged at both scales.
- **Regress A/B.** subselect, with, window and union are identical. join
  adds `Subquery Scan on y` under a Hash where goopg hash-joins an ORDER BY
  subquery that PG merge-joins.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Deferred (ledgered)

- **Multi-batch outer side.** A multi-batch hash join's outer side is
  `CP_SMALL_TLIST` in PG; goopg's plan carries no batch estimate, so it
  stays physical.
- **Two kinds of Material.** goopg's strip treats every `Materialize` as
  `CP_SMALL_TLIST`. PG's merge-join `materialize_inner` Material sits over a
  flags-0 inner.
- **Hash Cond rendering.** A derived column prints bare (`a`) where PG
  qualifies it (`x.a`, or `t.a` once the wrapper is stripped). This predates
  the change.
