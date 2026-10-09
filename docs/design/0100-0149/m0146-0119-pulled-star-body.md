# M0146-0119 — a pulled-up body whose targets are `*` is pulled up

Status: done 2026-10-09 (532e61d73). Parent: M0146-0007.

## Problem

M0146-0007i ledgered part (1): goopg's `is_simple_subquery` gate
(`simpleDerivedPullupBody`) declined any body whose target list held `*` or
`alias.*`. Regress subselect's NOT MATERIALIZED pair shows the effect:

```sql
with x as not materialized (select * from (select f1, now() as n from subselect_tbl) ss)
select * from x, x x2 where x.n = x2.n;
```

| | plan |
|---|---|
| PG 18.3 | `Result  One-Time Filter: (now() = now())` over a Nested Loop of the two scans |
| goopg | `Merge Join  Merge Cond: ((now()) = (now()))`, each reference kept as a subquery |

A single-table star body only looked pulled up: qual pushdown plus trivial
Subquery Scan removal gave the same plan. Over a nested derived table
the difference showed: `(select * from (select f1, f1 + 1 as n …) ss) a
where a.n = 3` printed `Filter: (n = 3)` where PG prints
`Filter: ((f1 + 1) = 3)`.

## PG behaviour

Parse analysis expands `*` and `alias.*` into the columns they stand for
(`transformTargetList`, `ExpandColumnRefStar` in parse\_target.c), so
`pull_up_simple_subquery` never sees a star. After the pull-up, `x.n =
x2.n` is `now() = now()`. It has no Vars and no volatile function, so it
is a pseudoconstant gating qual.

## Change

`expandPullupBodyStars` (derivedpullup.go) runs inside
`simpleDerivedPullupBody`, before the target checks. It rewrites each star
target into qualified column references, in FROM order:

- **A plain table** expands to its live columns from the catalog.
- **A derived item, or an inlinable CTE reference** (`cteAsDerivedItem`),
  expands to its output names: the item's alias list first, then each
  target's alias or plain column name. A nested star expands first,
  recursively.
- **Fail-closed cases** leave the body unpulled:
  - an unaliased or unpullable derived item;
  - an output that is neither aliased nor a plain column;
  - NATURAL or USING joins;
  - a non-inlined CTE;
  - an unknown relation;
  - a schema-qualified star.

The alias-list length check now runs on the expanded list.

## Verification

- **Test.** `TestPulledStarBodyIsPulledUp` fails at HEAD on both EXPLAIN
  shapes. It also checks results through star bodies, `s.*` beside another
  column, and an alias list over a star.
- **Regress A/B** (19 files).
  - **with:** 3014 → 2997 diff lines. A scalar subquery over a CTE that
    raised "more than one row returned" now returns PG's 5 rows; ERROR
    lines 91 → 90.
  - **subselect:** 2694 → 2686.
  - **join:** 18106 → 18130.
    - `Seq Scan on sj t2  Filter: (a = 42)` now matches PG.
    - The tenk1 `unique1 = b.f1` probe is index-driven, as PG's.
    - A LATERAL star body moves its difference from qual placement to
      join order, with the same 10 rows.
    - The rest is the known row-order flip.
  - Error counts are otherwise unchanged.
- **TPC.** The fire set reports no changed query; TPC-H plans are
  byte-identical.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet (9/9) all PASS.

## Not covered (ledgered)

- **VERBOSE output lists.** `Output:` lists of pulled bodies print bare
  source columns: `f1, f2, f3`, where PG prints `subselect_tbl.f1, now()`.
  This is a rendering difference, M0146-0042's family, which is held.
- **Duplicate output names.** Two star-expanded items with a same-named
  column produce duplicate output names, which `resolvePulledDerived`
  declines. PG pulls them up.
- **Unnamed expression outputs** of a nested derived item decline. PG's
  FigureColname would name them.
