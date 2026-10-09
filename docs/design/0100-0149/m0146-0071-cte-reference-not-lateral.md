# M0146-0071 — a CTE reference never makes its join lateral

Status: done 2026-10-06 (`6f0b02894`).

## Symptom

```sql
SELECT g, (WITH c AS MATERIALIZED (SELECT g*2 AS k)
           SELECT c.k || '/' || c2.k FROM c, c AS c2)
FROM generate_series(1,3) g;
```

goopg returned `2/4, 4/8, 6/12`; PG 18.3 returns `2/2, 4/4, 6/6`. In a
multi-row body (`SELECT g*10 + x AS k FROM generate_series(1,2) x`) the
second reference produced `111, 112`: its `g` had read the first
reference's `k` (11).

## Cause

- `nodeReferencesOuter` (planner.go) decides `Join.Lateral` for the joins
  `planFromClause` builds over FROM items. Its general case is
  `planHasOuterRef(n)`, which walks the right item's plan, including a CTE
  scan's body. The body's reference to the enclosing `g` therefore counted,
  and the join over `c, c AS c2` became lateral.
- For each left row, the lateral driver (`join_lateral_stream.go`):
  - pushes the left row onto `ctx.OuterRows`;
  - gives the right side a fresh `CTERowCache`.
- The second scan therefore re-materialised the body, and the body's
  level-1 reference, which names the enclosing query, read the pushed left
  row.

## PG

- A CTE body belongs to the WITH list's query level, where no FROM sibling
  is in scope. It can reference enclosing query levels by ordinary
  correlation, but never a sibling.
- Every reference reads one shared tuplestore (`ctescan.c`, leader and
  reader). The body is evaluated once per execution of the enclosing
  subplan.

## Change

`nodeReferencesOuter` returns false for `*CTEScan`. The enclosing-level
correlation is still tracked where it belongs: `planHasEscapingOuterRef`
counts the body's references, so the sublink stays correlated and
re-executes per outer row. Within one execution, both references now share
the per-execution materialisation (`enterSublinkCTEWindow`, M0146-0050).

## Verification

- `TestCorrelatedCTEReadTwiceInSublink`: 9 shapes, every want PG 18.3's
  output:
  - MATERIALIZED and inlined CTEs;
  - a multi-row body;
  - a WHERE qual on the second reference;
  - an explicit JOIN;
  - three references;
  - an uncorrelated control.

  Six fail at HEAD.
- Derived-table and LATERAL neighbours (a plain and a LATERAL derived
  table reading the enclosing `g`, with and without a sibling) were probed
  unchanged and identical to PG.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96 with no runtime moves and plan shapes 99/99;
  - fire set: no fires;
  - ea-ratchet PASS.
- Regress A/B over 8 files (with, subselect, join, rangefuncs, select,
  aggregates, union, plpgsql): 7 byte-identical, plpgsql within its known
  flap.

## Not covered

- A LATERAL derived table that both reads its left sibling and reads a
  correlated CTE still runs under the lateral driver, so the CTE is
  re-materialised per left row. That is correct, because the subquery
  re-executes per left row in PG too, but PG reuses one tuplestore per
  CTE level.
- goopg still decides "lateral" from any outer reference in a FROM item's
  plan, rather than from the item being written LATERAL or being a FROM
  function, as PG's `LATERAL` / implicitly-lateral rules do. Only CTE
  scans are excepted here.
