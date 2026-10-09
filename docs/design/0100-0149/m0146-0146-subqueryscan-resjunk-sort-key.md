# M0146-0146 — a Subquery Scan under a Sort whose key is an expression is not trivial

Status: done 2026-10-10 (3e2ddfe91). Parent: M0146-0014b.

## Problem

TPC-DS Q36, Q70 and Q86 wrap a GROUP BY ROLLUP + `rank()` body in
`select * from (…) as sub` and order by
`lochierarchy desc, case when lochierarchy = 0 then i_category end,
rank_within_parent`.

- **PG** prints `Sort -> Subquery Scan on sub -> WindowAgg`.
- **goopg** printed `Sort -> WindowAgg`.

That is the first divergence of all six records at both scales
(`[scan-type] under Sort`; M0146-0014b routing).

## PG behaviour

- **The sort key lands on the scan.** `make_sort_input_target`
  (postgres/src/backend/optimizer/plan/planner.c:6441) builds the sort
  input target from the final target list plus the ORDER BY keys. A key
  that is not already a target is a resjunk entry. Sort does not project,
  so the target list is applied below it, at the scan (here the
  `SubqueryScan`, the only leaf).
- **That makes the scan non-trivial.** `trivial_subqueryscan`
  (postgres/src/backend/optimizer/plan/setrefs.c:1476) removes the node
  only when its target list is the subplan's output position for position.
  The resjunk `CASE` entry makes it longer, so the node stays.

## Change

`stripTrivialSubqueryScans` (subqueryscan\_strip.go) keeps the wrapper when
its parent is a Sort or Incremental Sort and any key is not a plain column
reference (`sortKeysCompute`).

- **Why the old test missed it.** goopg's Sort evaluates expression keys
  itself, so the consumption test only saw the full, in-order column read.
- **Its twin.** This is the Sort twin of M0146-0005av's rule for a parent
  Project that computes a target.

## Verification

- **Test.** `TestSubqueryScanKeepsUnderAComputedSortKey` covers an
  expression key and an expression among column keys (both keep), and a
  column key (strips). The two keep cases fail on HEAD.
- **Probes against PG 18.3** (generate\_series bodies). The same three
  shapes make the same Subquery Scan decision on both engines. Only
  EXPLAIN qualification text and one pre-existing group-then-sort strategy
  differ.
- **TPC-DS fire set.** Only Q36, Q70 and Q86 fire.
  - Q36 and Q86 → MATCH at both scales: SF0.25 matches 53 → 55, SF1
    39 → 41.
  - Q70's first divergence moves to its `s_state IN (…)` semi join, where
    PG uses a Hash Join Left Semi and goopg a Nested Loop.
- **TPC-H.** Plans are byte-identical; the acceptance arm matches on
  values.
- **Regress A/B** (32 cases): no change.
- **Gates.** Units, TPC-H spotcheck, SF0.25 sweep (PASS=99) and
  ea-ratchet (1) all PASS.

## Not covered (ledgered)

- **A Project of bare columns between the leaf and the Sort.** The rule
  reads only the leaf's direct parent, so a goopg narrowing Project in
  between would hide an expression key. None of the corpus shapes has one.
- **Q70's semi join.** It goes to the next parity-closure sweep.
