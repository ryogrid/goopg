# M0146-0106 — a NestLoop index-probe param deparses through the loop's outer plan

Status: done 2026-10-08 (0dec89dd0). Parent: M0146-0042.

## Problem

TPC-DS Q71 joins `item` and `time_dim` to a UNION ALL of three sales
subqueries through parameterised index probes. At SF0.25 its only
difference from PG 18.3 was two Index Cond lines:

```
goopg:  Index Cond: (i_item_sk = sold_item_sk)
PG:     Index Cond: (i_item_sk = "*SELECT* 3".sold_item_sk)
```

The probe's key is an `OuterColumnRef` into the loop's outer row. The
OuterColumnRef arm of `formatExprQual` names it two ways:

- `resolveLabelInAncestor` finds a relation of the outer plan that
  exposes the name;
- `resolveLabelInAncestorSrc` does the same, narrowed by the key's
  binding id.

A UNION ALL subquery's column is exposed by no base relation, and its
binding id names none. So the key printed bare, or, when the
statement-wide fallback found another level's relation under that id,
with the wrong prefix (`m106u1.a` in the fixture).

## PG behaviour

`get_parameter` (ruleutils.c) deparses a NestLoop param against the
NestLoop's outer plan with the relation prefix forced. An outer Var over
an Append goes through `set_deparse_plan`, which makes the Append's first
child its outer plan. That reaches the first member's kept Subquery Scan
(`"*SELECT* 3"` in Q71, whose parallel Append puts store\_sales first),
or the member's own scan when setrefs stripped the wrapper.

A non-Var referent, such as an aggregate's output, prints wrapped in
parentheses: regress join's `(thousand = (sum(i4b.f1)))`.

## Change

`formatIndexCondKey` calls `nestLoopParamThroughOuter` for an
`OuterColumnRef` key inside a parameterised inner (`reg.paramInner`). The
helper does the following:

- It applies only when both label lookups fail, so every key they already
  name renders as before.
- It requires the key's index to hold the key's own column in the outer
  input's row.
- It chases that column through the outer input with the Sort Key's
  `resolveKeySource`: through joins, through a UNION's first arm, and to a
  kept wrapper's alias or to the relation or aggregate that computes it.
- It wraps a non-Var result in parentheses.

The chase is limited to Index Cond keys. A first attempt in the general
OuterColumnRef arm also reached a lateral subquery's Filter. Its Level-1
reference does not index the current loop's outer row, and regress
partition\_prune printed the self-comparison `t2.a = t2.b` for
`t2.a = t1.b`.

## Verification

- **Probe** (PG 18.3 and goopg, `enable_hashjoin = enable_mergejoin =
  off`). Both forms are identical to PG:
  - UNION ALL members with quals keep their wrappers, so the probe prints
    `Index Cond: (k = "*SELECT* 1".a)`;
  - a qual-less UNION ALL strips them, so it prints `(k = m106u1.a)`.
- **Test.** `TestNestLoopParamOverUnionAllMember`. Without the change it
  prints `k = m106u1.a`.
- **TPC-DS fire set** (results identical). Q5, Q14, Q54 and Q71 change.
  - Q71 is a full MATCH at SF0.25 (43 → 44).
  - qual-placement 11 → 10 at both scales.
  - Q54's two probes and Q5's SF1 catalog\_page probe print PG's text.
  - Q14's second statement prints PG's `store_sales_1.ss_sold_date_sk`.
    Its item probes name the column goopg's own join order reads, where
    PG's join order differs.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (16 files). join's `(thousand = (sum(i4b.f1)))` now
  matches PG's expected line. The rest is identical apart from diff
  realignment and rowsecurity's pointer text.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS; only Q5, Q14, Q54 and Q71 change shape) and ea-ratchet all
  PASS.

## Not covered

- **A compound key.** An Index Cond key that is an expression over outer
  columns keeps today's text, as in regress join's
  `tenthous = ((f1 + f1) + 999)`.
- **Hash and merge join keys** over UNION ALL member wrappers (TPC-DS Q5's
  `web_returns.wr_returned_date_sk`). These stay under the existing
  M0146-0042 ledger row on join keys over member wrappers.
