# M0146-0228 — TPC-DS SF1 Q23: aggregate input-expression cost

Status: slice 1 landed 2026-10-19. The task stays open for the cast cost (§4). Filed by the S9 pass at 8465f5e72
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

## 4. Open: the cast (slice 2)

Q23's serial arm is now 112644.10..114227.43, still below its split arm, and PG's is 119518.79. The other half of
PG's 0.005 per row is the implicit `int4_numeric` cast. goopg's `qualEvalOps` prices every cast at 0, because its
CastExpr does not say whether PG would build a free RelabelType, a cast function (procost 1) or a CoerceViaIO (2).

Porting `pg_cast.dat`'s castmethod/castfunc table, and pg_proc's procost for the cast function, would let
`cost_qual_eval` price coercions as PG does. The int4 operand of `int4 * numeric` must also be seen as coerced.
That change reaches every qual with a cast, so it is its own slice.
