# M0146-0228 — TPC-DS SF1 Q23: aggregate input-expression cost

Status: slices 1 (aggregate input-expression cost) and 2 (numeric-family coercion cost) landed 2026-10-19. The task stays open for the Parallel Hash Join under Q23's partial aggregate (§5). Filed by the S9 pass at 8465f5e72
(`analysis/m0146/m0146-0014/routing-20261019d/`). Evidence: `analysis/m0146/m0146-0228/`.

## 1. Record

TPC-DS SF1 Q23, aggregation-strategy, SHAPE-DIFF at depth 2 (under CTE: PG Finalize HashAggregate | goopg
HashAggregate). It persists under PG statistics.

- PG partially aggregates the `best_ss_customer` CTE: Finalize HashAggregate → Gather(4) → Partial HashAggregate →
  Parallel Hash Join.
- goopg aggregates it serially, over a serial Hash Join.

## 2. Diagnosis

The CTE body was run alone (`cte-body-probe.sql`) on both engines: the exact :65438 replica under the tmp/pgdbg
build, and an SF1 clone with PG statistics. Both engines share the same Hash Join input, 3876.33..92020.04, 2749875
rows.

| arm | PG | goopg (before) |
|---|---|---|
| serial HashAggregate | 119518.79..121018.79 | 105769.42..107019.42 |
| Partial + Gather + Finalize | 115562.46..117062.46 | 113437.61..114687.61 |

goopg's serial aggregation step costs 13749 (2749875 × 0.005) against PG's 27498.75 (× 0.01). PG's `cost_agg`
AGG_HASHED charges per input row:

- `cpu_operator_cost` per grouping column: 0.0025;
- the transition function (`numeric_avg_accum`): 0.0025;
- `get_agg_clause_costs`' input-expression term, `cost_qual_eval_node` over the aggregate arguments (prepagg.c):
  `int4_numeric(ss_quantity)` plus `numeric_mul`, 0.005.

goopg's `aggClauseCosts` left out the third term ("Not ported: the arguments' and FILTER's evaluation cost"). With
the per-row cost halved, the serial arm undercut PG's split arm.

## 3. Slice 1: the input-expression term

`aggInputEvalOps` adds `qualEvalOps` over each transition state's arguments and FILTER to the per-input-row cost.

- It is charged in the simple and partial (initial) split modes, never in the combine step: PG's "these costs are
  incurred only by the initial aggregate node".
- A shared transition state (find_compatible_trans) is charged once.

Measured (census `analysis/m0146/m0146-0228/census/`, m28):

- No verdict moves: SF0.25 92/99, SF1 82/99, TPC-H 22/22.
- 17 plans change cost at each TPC-DS scale, and they converge on PG. For example (root cost, SF0.25):
  - Q2: 34582.22 → 37632.59 (PG 37623.39)
  - Q59: 50324.03 → 62355.80 (PG 62384.58)
  - Q43: 18920.98 → 18939.46 (PG 18940.08)
  - Q66: 20440.09 → 20444.05 (PG 20449.33)
  - Q99: 14038.45 → 14061.89 (PG 14060.85)
- Q4 overshoots: 248240.31 → 260169.39 against PG's 254479.11.
- The fire set ran 34 queries; all 34 results are byte-identical between baseline and candidate.

Pin: `TestAggClauseCostsChargesInputExpressions`, mutation-checked.

## 4. Slice 2: numeric-family coercion cost

`cost_qual_eval` prices a coercion by the function that performs it. Every pg_cast entry inside the numeric family
(int2, int4, int8, float4, float8, numeric) is a function cast (castmethod 'f') whose function has procost 1:
`int4_numeric`, `i4tod`, `numeric_float8`, `int48`, and so on. goopg's `qualEvalOps` priced every cast at 0.

`qualevalcost.go` now prices two things:

- **Implicit coercion of a binary operator's operand.**
  - Integer against numeric: the integer side is coerced (`int4_numeric`).
  - Integer against float: the integer side is coerced (`i4tod`).
  - Numeric against float: the numeric side is coerced (`numeric_float8`, the implicit direction).
  - Integer against integer and float against float coerce nothing: cross-type operators such as `int48mul` and
    `float84mul` exist.
- **A CastExpr between two numeric-family types.**

Either way, a coerced operand that folds to a constant at plan time costs nothing: `eval_const_expressions`
pre-coerces it. `isPlanTimeConst` is a `walkExprRefs` classifier (no Var, Param, function call or sublink); an
unenumerated node reads as not constant. Other casts keep the free reading (ledger).

On the exact replica, the CTE body's serial HashAggregate is 119518.79..121018.79. goopg now prices it at
119518.79..121102.12: the startup matches to the cent, and the total differs only in the per-group final term.
Q23's `best_ss_customer` now plans PG's Finalize HashAggregate → Gather → Partial HashAggregate.

Measured (census `analysis/m0146/m0146-0228/census2/`, m28b):

- No verdict moves: SF0.25 92/99, SF1 82/99, TPC-H 22/22.
- Q23's first divergence moves at SF1, both shipped and under PG statistics: from depth 2 (aggregation-strategy)
  to depth 5 (parallelism, §5).
- Six TPC-DS plans per scale and twelve TPC-H plans change cost, mostly toward PG. TPC-H Q1 goes 178517 → 200684
  against PG's 200862, and Q3, Q6, Q9 and Q14 also get closer. TPC-H Q12 moves away by 72.
- Fire set: 12 fired results, all byte-identical.

Pin: `TestQualEvalPricesNumericCoercions`, mutation-checked.

## 5. Open: the Parallel Hash Join under the partial aggregate

Under PG statistics, Q23's partial aggregate now reads its input from a non-parallel-aware Hash Join: a Parallel Seq
Scan on store_sales probes a private Hash of an Index Only Scan on customer (3876.33..64750.26). PG builds a
Parallel Hash Join with a Parallel Index Only Scan on customer_pkey and a Parallel Hash (2563.84..63437.77). Next:
trace the partial paths of (store_sales_2, customer_1) and price PG's parallel-hash arm.
