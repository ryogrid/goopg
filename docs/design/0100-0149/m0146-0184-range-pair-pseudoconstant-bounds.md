# M0146-0184 — a range whose bounds are InitPlan outputs is one range

Status: closed 2026-10-14.

## Problem

TPC-DS Q54 filters date_dim with `d_month_seq >= (SELECT DISTINCT
d_month_seq + 1 …) AND d_month_seq <= (SELECT DISTINCT d_month_seq + 3 …)`.
Both bounds are InitPlans. At SF0.25 the estimates were:

- goopg: 0.111 of date_dim, which put `{store_sales, date_dim}` at 76335
  rows;
- PG 18.3: 0.005, which puts the same join at 3441 rows.

On a bare probe (`x >= (SELECT 1200) AND x <= (SELECT 1203)`) goopg
estimated 8117 rows and PG 365. The over-estimate cost Q54 PG's plan shape:
goopg's Gather over the Parallel Hash Join lost to a serial hash join, and
PG's nested loop over the one-row unique-ified `my_customers` never formed
(found by the M0146-0005dp verification).

## Diagnosis

`clauselist_selectivity` (clausesel.c) collects range bounds into pairs
when the clause is an operator over one relation whose other operand is
pseudo-constant (`is_pseudo_constant_clause_relids`): a Const, a Param, or
an InitPlan output. `scalarineqsel` cannot estimate a non-Const bound, so
each side is DEFAULT_INEQ_SEL. The pair then hits the punt rule — either
bound at DEFAULT_INEQ_SEL gives DEFAULT_RANGE_INEQ_SEL, 0.005.

goopg ports that pairing in `conjunctionSelectivity` (rangequery.go),
including the punt rule. But `rangeBoundOf` found a bound only through
`normalizeColumnConstRange`, which needs a literal constant. An InitPlan
bound was not a bound, so the two clauses multiplied as separate factors:
(1/3)² ≈ 0.111.

## Change

- `rangeBoundOf` falls back to `normalizeColumnPseudoConstRange`. That
  accepts a column on one side and a pseudo-constant on the other.
- `isRangePseudoConstant` defines pseudo-constant as: literals, bind
  parameters, uncorrelated scalar sublinks PG plans as InitPlans
  (`SublinkIsInitPlan`), and casts and arithmetic over those.
  - It is a `walkExprRefs` Visit callback, registered in the
    expression-switch inventory.
  - It fails closed: a function call or any other node keeps the old
    separate-factor estimate, because volatility is not checked here.
- Literal bounds keep their histogram estimates, as before.

## Evidence

Files are in `analysis/m0146/m0146-0184/`.

- **Fixture:** goopg now prints PG 18.3's exact costs and rows for all
  three forms:
  - `x >= (SELECT 100) AND x <= (SELECT 200)` → rows=100;
  - `x > (SELECT 100) + 1 AND x < (SELECT 200)` → rows=100;
  - `x >= (SELECT 100)` alone → rows=6667.
- **Q54 at SF0.25:** the plan now has PG's shape. The store_sales ⋈ date_dim
  Gather is 3052..19260, rows 3428 (PG 3071..19283, rows 3441). The only
  divergence left is the appendrel member's `Filter:` vs `Recheck Cond:`
  line, which the classifier counts as qual-placement.
- **Fire set at SF0.25:** match stays at 76; Q54 goes from six categories to
  one.

  | category | before | after |
  |---|---|---|
  | join-order | 18 | 17 |
  | parameterisation | 10 | 9 |
  | aggregation-strategy | 5 | 4 |
  | sort-strategy | 11 | 10 |
  | parallelism | 12 | 11 |
  | qual-placement | 4 | 5 |

- **Fire set at SF1:** Q54 now has PG's nested loop over the unique-ified
  `my_customers` and the Gather over the Parallel Hash Join. It still
  differs in the order of the store / customer_address joins, Gather Merge
  vs Sort over Gather, and the Recheck Cond line. The categories move:
  aggregation-strategy 11 → 10, scan-type 20 → 21, qual-placement 7 → 8.
- **SF0.25 sweep:** 99 of 99 pass; Q54 is the only plan change.
- **TPC-H:** the acceptance arm is value-identical.
- **Test:** `TestExplainRangeWithInitPlanBounds` checks PG's rows for the
  three forms. Both band forms fail with the fallback disabled.

## Not ported (filed / ledgered)

- **M0146-0186.** A single-relation scan gets a Gather whose startup has no
  `parallel_setup_cost` (`Gather (cost=0.02..2508.05)` over a Parallel Seq
  Scan), where PG keeps the serial Seq Scan. This was visible before the
  change too.
- **M0146-0187.** An appendrel member's parameterised Bitmap Heap Scan
  prints its probe clause as `Filter:` where PG prints `Recheck Cond:`. It
  is now Q54's last SF0.25 divergence.
- Pseudo-constant function calls (stable functions, `now()`) still do not
  pair, because no volatility check is made here.
