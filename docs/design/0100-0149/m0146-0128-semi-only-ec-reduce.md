# M0146-0128 — one clause per equivalence class when every special join is a SEMI join

Status: done 2026-10-09 (51484e842). Parent: M0146-0127.

## Problem

After M0146-0127, TPC-DS Q14 joins the unique-ified `cross_items` to
`item` first, as PG does. The `store_sales` probe above that join still
applied two clauses of one equivalence class:

- the index condition `ss_item_sk = item.i_item_sk`;
- `Join Filter: (store_sales.ss_item_sk = cross_items.ss_item_sk)`, which
  PG does not have.

Regress `subselect`'s `IN (VALUES …)` EXPLAINs gained the same redundant
key.

## PG behaviour

`generate_join_implied_equalities_normal` (equivclass.c) emits one clause
per class at a join. `distribute_qual_to_rels` (initsplan.c) makes a SEMI
join's qual an ordinary class member, because a semijoin has no nullable
side.

## Change

M0146-0022 stated its rule as "no special join can null-extend a class
member", but implemented it as `ecReduce = len(sjis) == 0`.
`onlySemiJoins` (relfromjoinlist.go) now enables the reduction when every
SpecialJoinInfo is a SEMI join.

The reduction stays safe with semijoin RHS members in a class:

- It chooses only among the join's base clauses. Those never include a
  clause that reads a semijoin-consumed RHS:
  - the semijoin's own qual is internal to that input;
  - M0146-0127's derived clauses are filtered by `clausesFor`.
- When PG's preferred member pair is not among the base clauses, it keeps
  every clause.

## Verification

- **Test.** `TestSemiOnlyProblemReducesEquivalenceClauses`.
- **TPC-DS fire set.** Q14 was executed in both arms with identical
  results. Its three probe spines drop the Join Filter: qual-placement goes
  9 → 8 at SF0.25 and 10 → 9 at SF1. No other query fires.
- **TPC-H and ea-ratchet.** TPC-H plans are byte-identical; ea-ratchet is
  unchanged at 1 finding.
- **Regress A/B.** `subselect` goes 1377 → 1367 diff lines, and the first
  VALUES-IN plan is back to its pre-0127 shape. `join` shows only its
  row-order flip.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Not covered

- **Q14's `item_pkey` probe.** It is still a Bitmap Heap Scan where PG uses
  an Index Scan. A one-row `item_pkey` lookup costs 16.27 on goopg against
  8.30 on PG, because `indexProbeCostMultiplier` (B8) doubles the page
  terms. The fix is gated on the owner's M0146-0068 option choice.
- **Other special joins (ledgered).** A problem with any LEFT, FULL or ANTI
  join still applies every clause. PG reduces per class there too, and only
  keeps outer-join-delayed clauses apart.
