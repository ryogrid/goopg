# M0146-0135 — a parameterised hash join keeps the class's ppi clause

Status: done 2026-10-09 (a6704d1d8). Parent: M0146-0014a.

## Problem

TPC-DS Q95 at SF1 semi-joins `ws1` to the subquery `web_returns ⋈ ws_wh`.
The RHS is a hash join: the probe side is `CTE Scan on ws_wh ws_wh_1`, and
the hashed side is an index probe of `web_returns` keyed by
`ws1.ws_order_number`. The join is parameterised by `ws1`, and the
semijoin's nested loop binds it.

PG prints `Join Filter: (ws1.ws_order_number = ws_wh_1.ws_order_number)`
on that hash join. goopg (the M0146-0049d3 parameterised hash join) had
no such filter. The parity diff showed `qual-placement`.

## PG behaviour

`get_joinrel_parampathinfo` (relnode.c) collects the join clauses a
parameterised joinrel must apply. It starts from
`generate_join_implied_equalities`, which yields one clause per class
linking the required-outer rels to the joinrel, then:

- **Movable into an input.** A clause movable into an input path is
  dropped (`join_clause_is_movable_into`).
- **Unparameterised inputs accept nothing.** An unparameterised input's
  `*_and_req` is NULL, so no clause is ever movable into it.
- **Regeneration.** A clause dropped as movable into the parameterised
  RHS has its class regenerated against the LHS
  (`generate_join_implied_equalities_for_ecs`). The result is kept unless
  it is movable into a parameterised LHS.

For Q95 the class is `{ws1.ws_order_number, web_returns.wr_order_number,
ws_wh_1.ws_order_number}`. Whichever member the generator picks, the
joinrel ends up with `ws1 = ws_wh_1`:
- If it picks `ws_wh_1`, the clause is kept directly.
- If it picks `web_returns`, the clause is dropped as movable into the
  probe and regenerated against the CTE side.

## Change

- **Derivation.** `paramJoinFilterClauses` (paramjoin.go) derives the
  clause when two conditions hold:
  - the hashed side's probe enforces a class against the required outer
    (a `probeEnforcedClauses` member with an ecID);
  - the probe side is unparameterised and holds a member of that class
    (the first operand on that side among the class's clauses, in clause
    order).

  The clause is `required-outer member = probe-side member`, as PG prints
  it.
- **Path and cost.** The clauses ride on the path as `Path.ParamFilter`.
  They join the qual cost charged per hash match (`final_cost_hashjoin`'s
  `qp_qual`).
- **Lowering.**
  - `createHashJoinPlan` needs the binding nested loop's outer layout,
    which it does not have. So it records the join on the loop's
    `paramSink` with a `bind` callback.
  - `createNestLoopParamJoinPlan` calls the callback after binding the
    probes. Each clause joins the join predicate, so EXPLAIN shows it as
    a Join Filter.
  - The required-outer operand becomes a level-1 OuterColumnRef, the same
    binding the probe keys use. The probe-side operand is translated into
    the join's own row.

## Verification

- **Test.** `TestParamJoinFilterClausesRegenerateAgainstTheOuterInput`
  covers the Q95 shape, plus the parameterised-outer and no-member
  declines.
- **TPC-DS fire set.**
  - Q95 at SF1 goes from `[scan-type, qual-placement]` to `[scan-type]`,
    with PG's Join Filter.
  - Q95's results are identical at both scales (the fire set executed it).
  - At SF0.25 Q95 changes only in cost.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (32 cases): only `stats_ext`'s nondeterministic listing
  order changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm (values), fire set,
  SF0.25 sweep (96/96) and ea-ratchet (1, unchanged) all PASS.

## Not covered (ledgered)

- **Parameterised probe side.** PG's clause then depends on which member
  `generate_join_implied_equalities_normal` picks, which follows the
  class's member order and operator score. goopg declines this case;
  `restrictInfoList.ecMembers` carries the order needed to resume it.
- **Row estimate.** The ppi clause is not added to the parameterised
  join's row estimate (`parameterizedJoinrelSize`). goopg shows 230 rows
  where PG shows 245; the difference also includes SF1 relpages drift.
- **Remaining scan-type.** Q95's last category is PG's `Index Only Scan
  using web_returns_pkey` against goopg's `Index Scan`.
