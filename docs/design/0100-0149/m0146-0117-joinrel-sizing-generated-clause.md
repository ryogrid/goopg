# M0146-0117 — a joinrel is sized with the generated equivalence-class clause

Status: done 2026-10-09 (ff8607922). Parent: M0146-0009.

## Problem

After M0146-0116, TPC-DS Q59 still differed from PG 18.3 in join order. Its
top join was estimated at 15 rows where PG's plan has 1, and that estimate
drove goopg's order.

Q59 joins two FROM subqueries over the CTE `wss` on
`d_week_seq1 = d_week_seq2 - 52`. The equivalence class is
`[d.d_week_seq, wss.d_week_seq, (wss_1.d_week_seq - 52)]`. goopg sized the
joinrel with the written clause `wss.d_week_seq = (wss_1.d_week_seq - 52)`.
Both sides are CTE outputs without statistics, so eqjoinsel's default of
200 distinct values gave a selectivity of 1/200.

PG sizes with the clause it generates for the pair, `d.d_week_seq =
wss.d_week_seq`. date\_dim's statistics give about 1/10436 there.

This gap was ledgered by M0146-0022 (2026-09-26, part (1)): that task
reduced the clauses a join APPLIES but kept sizing on the explicit-first
choice.

## PG behaviour

`build_join_rel` (relnode.c) creates a joinrel for the first pair that
reaches it. It calls `build_joinrel_restrictlist`, whose
`generate_join_implied_equalities` contributes one clause per equivalence
class, and `set_joinrel_size_estimates` sizes the rel with that list. Later
pairs reuse the size.

## Change

- **Sizing list.** `makeJoinRel` (joinsearchlevel.go) passes the reduced
  `buildJoinRelRestrictList` result, the list the join applies, to
  `sizeJoinRel`.
- **Dead path removed.** `joinRelSizingClauses` and the `sizingOnly` switch
  that suspended the reduction are gone.
- **Fail-closed problems unchanged.** Where the reduction declines (special
  joins, or the M0146-0022 safety conditions), the list is unreduced and
  `oneClausePerEquivClass` applies its explicit-first choice as before.

## Verification

- **Test.** `TestJoinRelIsSizedWithTheGeneratedClause` fails at HEAD, where
  `{a,b} ⋈ {c}` was sized with both `b = c` and `a = c`. PG's single
  generated clause is `a = c`.
- **TPC-DS fire set** (results identical). Q59 is a full MATCH at both
  scales, with 1 row, as PG.

  | scale | match | join-order | join-method | parameterisation |
  |---|---|---|---|---|
  | SF0.25 | 45 → 46 | 45 → 44 | 22 → 21 | 24 → 23 |
  | SF1 | 36 → 37 | 50 → 49 | 19 → 18 | 29 → 28 |

  Q37 and Q82 change cost only, with the same verdict.
- **Estimates and TPC-H.** ea-ratchet 9/9. TPC-H plans are byte-identical.
- **Regress A/B** (19 files). Neutral.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Not covered

Parts (2) and (3) of the M0146-0022 ledger row stay open:

- outer-join equivalence classes keep every clause;
- a class with a constant still yields a join clause.
