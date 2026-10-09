# M0146-0058 — the ANY-derived arm plans its body without the FROM pull-up

Status: done 2026-10-06 (`70a86beb0`). WRONG RESULTS fix.

## Symptom

With `m7one(a text)` holding `x` and `y`:

```sql
SELECT count(*) FROM (VALUES ('X'), ('Y')) v(c)
WHERE c IN (SELECT upper(a) FROM m7one WHERE a <> 'x');
```

goopg returned 0; PG 18.3 returns 1. The plan was `Hash Right Semi Join
Hash Cond: (a = c)` over a bare `Seq Scan on m7one`, with no filter and no
`upper`.

## Cause

- `sublinkBodyIsSimple` refuses a function-call target, so the IN body
  took the ANY-derived arm, `pullUpAnyDerivedBody`. That arm plans `(<body>)
  AS ANY_subquery` as one semi-side leaf, which is PG's subquery RTE
  (`convert_ANY_sublink_to_join`, subselect.c).
- It planned the wrap through `planFromClause`, whose M0146-0028 pull-up
  (`expandDerivedPullups`) flattened the body: `simpleDerivedPullupBody`
  admits it.
- The leaf node was then the body's bare FROM, and the body's WHERE sat
  unread in the wrap context's pulled quals.
- With a one-column table the `len(out) != 1` check still passed, so the
  link bound the raw column.

## Fix

`pullUpAnyDerivedBody` plans the wrap with `planFromClauseItems` directly,
without `expandDerivedPullups`. The leaf is the body's whole plan, so the
WHERE and the target expression stay inside it.

## Verification

- `TestAnyDerivedBodyKeepsWhereAndTarget`: four queries with PG 18.3's
  answers (with and without WHERE, a two-column table, row identity).
  Three fail at HEAD.
- Gates: units, tpch-spotcheck, acceptance arm 24/24, sf025 (no plan
  changed), fire set (no TPC-DS query fired), ea-ratchet.
- Regress A/B over 14 files: subselect identical. join.sql's
  not-materialized-CTE full-join IN body now takes this arm (Nested Loop
  Semi Join, same rows); PG folds that query to a `Result` either way.

## Not covered (ledger 2026-10-06)

- PG pulls this body up flat (`pull_up_sublinks` with a simple body; the
  target expression joins the parent: `Hash Semi Join  Hash Cond: (c =
  upper(m7one.a))`). goopg's `sublinkBodyIsSimple` refuses function-call
  targets, so the body stays a derived leaf (plan shape, not results).
- The derived arm's EXPLAIN prints the link twice, as `Hash Cond` and as
  `Join Filter`, and names the target `upper` rather than
  `upper(m7one.a)`. This predates the fix: grouped bodies show it at HEAD.
