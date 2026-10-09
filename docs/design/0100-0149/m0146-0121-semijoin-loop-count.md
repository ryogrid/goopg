# M0146-0121 — get_loop_count clamps a semijoin RHS to its unique-ified rows

Status: done 2026-10-09 (05d9b86c1). Parent: M0146-0009.

## Problem

TPC-DS Q23 (both scale factors) probes `catalog_sales_pkey` and
`web_sales_pkey` from a unique-ified `frequent_ss_items` CTE in a nested
loop. PG 18.3 instead hash-joins the sales tables (under a Gather +
Parallel Hash Join with `date_dim`) against the hashed CTE.

The parameterised index path on `catalog_sales` was amortised over
`loop_count = 4564`, the CTE's raw rows. PG amortises it over about 200,
the CTE's distinct `item_sk` values. With the larger loop count the
index-probe cost per execution dropped far enough for the nested loop to
win.

## PG behaviour

`get_loop_count` (indxpath.c) takes, for every outer base rel of a
parameterised path, its row count after `adjust_rowcount_for_semijoins`.
When the probed rel is in a `JOIN_SEMI`'s `syn_lefthand` and the outer rel
in its `syn_righthand`, the outer rel can supply the parameter only after
unique-ification. The row count is then clamped to
`estimate_num_groups(semi_rhs_exprs, approximate_joinrel_size(syn_righthand))`.

## Change

`internal/optimizer/pathparamindex.go`:

- `loopCountFor(cur, req)` now takes the probed rel. Each outer rel's rows
  pass through `adjustRowcountForSemijoins` before the minimum is taken.
- `adjustRowcountForSemijoins` walks `joinInfoList` for semijoins with the
  probed rel on the LHS and the outer rel on the RHS.
- `semiRhsUniqueRows` resolves `SemiRhsExprs` against the RHS leaf, the
  same way `createPulledBaseUniquePath` resolves them, and calls
  `estimateNumGroups`. An RHS already distinct on its output
  (`SemiRhsDistinct`) contributes its own rows.

All three callers pass the probed rel: the plain and skip parameterised
index paths (`pathparamindex.go`) and the parameterised bitmap path
(`pathbitmap.go`).

## Verification

- **Test.** `TestLoopCountClampsSemijoinRHSToUniqueRows` fails at HEAD. It
  also pins the no-semijoin and reverse-direction cases.
- **TPC-DS fire set** (Q23 executed in both arms, results identical).
  - SF0.25: Q23 goes from five divergence categories to `scan-type` only.
    The totals move join-order 42 → 41, join-method 20 → 19,
    parameterisation 22 → 21 and parallelism 24 → 23.
  - SF1: Q23's sales branches take PG's Gather → Parallel Hash Join with
    the hashed `frequent_ss_items`. One more `aggregation-strategy` label
    is reported, from the CTE subtree. That subtree is byte-identical
    between the arms; the label surfaces because alignment now gets
    further.
- **Estimates.** ea-ratchet PASS, 9 → 7 findings: both Q23 Nested Loop keys
  are FIXED. The baseline is not re-pinned (AGENT.md G4).
- **TPC-H.** Plans byte-identical.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Not covered (ledgered)

A multi-relation semijoin RHS is left unadjusted. PG sizes it with
`approximate_joinrel_size` and estimates the groups over that joinrel;
goopg has no RHS leaf to resolve the expressions against.
