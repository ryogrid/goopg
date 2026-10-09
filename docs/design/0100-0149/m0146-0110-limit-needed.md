# M0146-0110 — no Limit node for OFFSET 0 / LIMIT NULL (`limit_needed`)

Status: done 2026-10-08 (c08d71f7c). Parent: M0146-0005.

## Problem

goopg built a Limit node for every LIMIT or OFFSET clause.
`select 1 offset 0` printed `Limit -> Result`, and every `(… offset 0)`
subquery fence in regress join and subselect carried a goopg-only
`Limit`.

## PG behaviour

`limit_needed` (planner.c) returns false, so no Limit node is planned,
unless one of these holds:

- LIMIT is non-constant, or a non-null constant;
- OFFSET is non-constant, or a non-null constant other than 0.

`OFFSET 0` alone and `LIMIT NULL` / `LIMIT ALL` therefore plan nothing.
The subquery fence that `OFFSET 0` builds is decided on the Query
(`limitOffset != NULL` blocks pull-up and qual pushdown), not on the plan.

## Change

- **The rule.** `limitNeeded` (tuplefraction.go) implements those rules,
  looking through casts to the folded literal (`offset 0::bigint`). It
  uses type assertions, because the Expr type-switch inventory test
  rejects a new hand-written switch.
- **The call sites.** All four SELECT-level Limit constructions in
  planner.go build the node only when `limitNeeded` holds. That includes
  R83's deferred LIMIT above DISTINCT.

## Verification

- **Probe** (PG 18.3 vs goopg). Each of these is identical to PG:
  - `select 1 offset 0` and `(select 1 offset 0) s`;
  - `offset 0`, `limit null`, `limit null offset 0`;
  - `offset 2` and `limit 3 offset 0`, which keep their Limit;
  - a fenced subquery with an outer qual;
  - `distinct … offset 0` and `offset 0::bigint`;
  - result counts.

  The only exception is `LIMIT ALL`, which goopg's grammar rejects; that
  is pre-existing and ledgered.
- **Test.** `TestExplainLimitNeeded`. Planning a Limit for any bound fails
  it.
- **Regress A/B** (22 files). Diff lines fell: join 14931 → 14911,
  subselect 1394 → 1383. Error counts are unchanged.
- **TPC-DS / TPC-H.** No plan changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS) and ea-ratchet all PASS.
