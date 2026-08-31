# 0118-0101 — PL/pgSQL `EXECUTE … INTO STRICT` (horizons.spec enabler)

**Milestone:** M0118-0009 (isolation spec suite pass-through)
**Status:** landed (enabler — NOT a spec promotion)
**Date:** 2026-06-25 (loop #39)

## Problem

`horizons.spec`'s setup defines a helper that wraps `EXPLAIN (FORMAT json)` so
the test can assert pruning/VACUUM behaviour through a stable JSON return value:

```sql
CREATE OR REPLACE FUNCTION explain_json(p_query text)
RETURNS json LANGUAGE plpgsql AS $$
    DECLARE v_ret json;
    BEGIN
        EXECUTE p_query INTO STRICT v_ret;   -- <-- blocker
        RETURN v_ret;
    END;$$;
```

goopg's PL/pgSQL `EXECUTE` parser (`parseExecute`, design M0100-0005) accepted
`EXECUTE expr [INTO var] [USING …]` but **not** the `STRICT` modifier between
`INTO` and the target variable. The setup function therefore failed to create,
so every horizons permutation diverged at the first `explain_json(...)` call.

This is the first rung of the horizons ladder (see working_set / 0118-0100):
after this, the remaining horizons blockers are `EXPLAIN (FORMAT json)` emitting
`Heap Fetches` for index-only scans and the Effort-L MVCC pruning-horizon core —
both genuinely large, out of scope here.

## Change

Mirror the already-correct static `SELECT … INTO STRICT` path (which has carried
`Strict` end-to-end since M0118-0008 / plpgsql-toast) onto the dynamic `EXECUTE`
path:

1. **AST** (`internal/plpgsql/ast.go`): add `Strict bool` to `ExecuteStmt`.
2. **Parser** (`internal/plpgsql/parser.go`, `parseExecute`): after consuming
   `INTO`, accept an optional `STRICT` (an unreserved word surfacing as a plain
   identifier, exactly as the `SELECT … INTO STRICT` scan handles it) and set
   `stmt.Strict`.
3. **Runtime** (`internal/executor/plpgsql_runtime.go`, `*ExecuteStmt` case):
   when `Strict`, pull a second row after the first to detect a multi-row
   result, then enforce the PG-canonical row-count contract:
   - 0 rows → `ExecError{Code: "P0002", Message: "query returned no rows"}`
   - >1 row → `ExecError{Code: "P0003", Message: "query returned more than one row"}`
   These match the error codes/text the static `SELECT … INTO STRICT` branch
   already raises (and which the existing `no_data_found`/`too_many_rows`
   exception-name mapping at `plpgsql_runtime.go` keys off, so a `BEGIN …
   EXCEPTION WHEN no_data_found` handler catches them). Non-STRICT `EXECUTE …
   INTO` is unchanged: it still binds the first row and ignores extras.

The first INTO datum is copied out of the pooled slot before the second
`Next()`/`Close()`, following the same discipline as the static path.

## Tests

- `internal/plpgsql/parser_test.go::TestParseExecuteIntoStrict` — `STRICT` is
  flagged on `ExecuteStmt`; plain `INTO` stays non-strict.
- `internal/executor/plpgsql_execute_strict_test.go::TestPlpgSQLExecuteIntoStrict`
  — one row binds; zero rows → P0002; many rows → P0003; non-STRICT multi-row
  binds the first row without error.

## Validation

Re-probing `horizons.spec` after the change: the first divergence advanced from
the `explain_json` setup failure to the `EXPLAIN`-result `Heap Fetches` / pruning
values (the expected `Heap Fetches: 2`/`0` counts), confirming the setup function
now runs. Spec stays deferred — remaining blockers are the Effort-L EXPLAIN
JSON `Heap Fetches` emission + MVCC pruning-horizon core.

## Blast radius

Bounded to PL/pgSQL `EXECUTE … INTO`. STRICT was previously unparseable (hard
error), so no existing function changes behaviour; non-STRICT EXECUTE is
byte-identical. No SQL-level, planner, or storage changes.
