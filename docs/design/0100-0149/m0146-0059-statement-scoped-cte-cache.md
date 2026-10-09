# M0146-0059 — each statement gets its own CTE materialisations

Status: done 2026-10-06 (`f439c26e6`). WRONG RESULTS fix.

## Symptom

A PL/pgSQL function ran two statements, each declaring a CTE `x` at the
same position:

```sql
r1 := (WITH x AS (SELECT a, b FROM t WHERE b < 5)
       SELECT count(*)::text FROM x, x x2 WHERE x.a = x2.a);
r2 := (WITH x AS (SELECT b, count(*) c FROM t GROUP BY b)
       SELECT string_agg(x2.c::text, ',' ORDER BY x.b) FROM x, x x2 WHERE x.b = x2.b);
```

The second statement read the first one's rows: goopg returned a
500-value list where PG 18.3 returns ten counts of 100.

## Cause

- `ctx.CTERowCache` and `ctx.CTEStableCache` are keyed by CTE
  declaration (`CTEScan.DeclKey`: offset plus name).
- The wire dispatcher clears both per statement (`dispatch.go`), and the
  extended protocol builds a fresh Context per Execute.
- Routine bodies run all their statements on one Context and never
  cleared them. Two statements on one in-process Context showed the same
  replay.
- PG starts every statement with fresh CTE tuplestores.

## Fix

- `stmtCTEScopeOp` (executor.go) wraps the root operator of one
  statement execution. It swaps that statement's caches in around each
  `Open`, `Next` and `Close`, and out again afterwards, as the LATERAL
  join does per outer tuple (`bindOuter` / `unbindOuter`).
  - An enclosing statement still replaying its own CTE scans keeps its
    caches.
  - A statement whose operator outlives its caller (a FOR loop's query, a
    cursor) never leaks entries into statements that run in between.
- All eleven routine statement sites in `plpgsql_runtime.go` build
  through `buildStatementScoped`: PL/pgSQL statements, RETURN QUERY, SQL
  functions and procedures, and expression evaluation via SQL.
  `executor.Run`, the in-process path, scopes the same way.
- The per-execution window for correlated sublinks (M0146-0050) works the
  same way, inside one statement.

## Verification

- `TestCTEMaterialisationIsStatementScoped`, with PG 18.3's answers:
  - the PL/pgSQL function;
  - a FOR loop whose body redeclares the loop query's CTE name;
  - two statements on one Context.

  Three of the four fail at HEAD.
- Gates: units, tpch-spotcheck, acceptance arm 24/24, sf025 (96/96
  values, no plan changed), ea-ratchet, regress A/B over 14 files
  (with.sql identical; only known flaps elsewhere). The fire set is out
  of scope (executor only).

## Not covered

- **M0146-0072** (filed): a PL/pgSQL variable in a statement with a WITH
  clause is not substituted (`column "i" does not exist`); a record field
  in a CTE body fails `missing FROM-clause entry`. Found while writing the
  loop test.
