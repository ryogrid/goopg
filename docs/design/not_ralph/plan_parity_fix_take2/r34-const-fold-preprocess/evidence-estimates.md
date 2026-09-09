# R34 evidence — the estimate ladder

All goopg figures from the fresh PG-faithful clone (R31b), pinned seed,
re-ANALYZEd. PG 18.3 from the live oracles.

## TPC-DS `date_dim.d_date` (actual matching rows: 15)

| predicate form | goopg | PG |
|---|---|---|
| `between '2000-08-19' AND '2000-09-02'` | 13 | 14 |
| `between DATE '2000-08-19' AND DATE '2000-09-02'` | 13 | 14 |
| `between cast(..) AND '2000-09-02'` | 12233 | 14 |
| `between '2000-08-19' AND cast(..)` | 12121 | 14 |
| `between cast(..) AND cast(..)` | 8116 | 14 |
| `between cast(..) AND (cast(..) + INTERVAL '14 days')` | 8116 | 14 |

## TPC-H `lineitem.l_shipdate`

| predicate form | goopg | PG |
|---|---|---|
| `<= '1998-09-02'` (bare) | 5,916,028 | 5,918,116 |
| `<= cast('1998-09-02' as date)` | 2,000,418 | — |
| `<= date '1998-12-01' - interval '90 days'` (Q1) | 2,000,418 | — |

`2,000,418 = 6,001,215 / 3` exactly: the DEFAULT 0.3333 range
selectivity. The estimator is not falling back gracefully, it is not
engaging at all.

## Why the literal forms work and the cast form does not

`selectivity.go:717 isConstExpr` admits `*IntegerConst`, `*StringConst`,
`*NumericConst`, `*BooleanConst`, `*TypedStringLit` — and **not**
`*CastExpr`. `formatExprConstant` (:734) renders `TypedStringLit.Value`
verbatim, byte-matching the string ANALYZE stamped into the histogram.

So the fold target is exact: `CastExpr` over a string literal must
become a **`TypedStringLit`**, which the table above shows estimates
identically to a bare literal (13). No estimator change is required.

## Consequence for the interval arm (design §4 part 2)

A folded `date + interval` yields a TIMESTAMP whose canonical rendering
is `'2000-09-02 00:00:00'`, while `d_date`'s histogram was stamped from
a DATE, rendering `'2000-09-02'`. `formatExprConstant` matches
BYTE-EQUAL, so the timestamp spelling would not match the histogram
entries even though the value is right. Part 2 therefore needs a
rendering/comparison decision that part 1 does not — which is why the
design stages them and does not bundle the hook.
