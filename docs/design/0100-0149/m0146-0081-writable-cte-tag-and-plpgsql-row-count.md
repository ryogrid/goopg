# M0146-0081 — writable-CTE command tags and the PL/pgSQL processed count

Status: done 2026-10-07 (`caed28ea6`).

## Symptom

```sql
WITH d AS (DELETE FROM t WHERE a = 6 RETURNING a) INSERT INTO t SELECT a + 100 FROM d;
-- goopg: SELECT 0        PG 18.3: INSERT 0 1
```

A WITH-led UPDATE or DELETE whose WITH holds a data-modifying CTE did the
same. The rows were written correctly. PL/pgSQL reads the same count and
was wrong more widely:

- FOUND after `INSERT`/`UPDATE`/`DELETE` without RETURNING was always
  false, plain or WITH-led.
- SELECT … INTO did not set FOUND.
- `GET DIAGNOSTICS n = ROW_COUNT` was a parse error (`unsupported statement
  (got get)`). In regress merge, a correct FOUND sends `merge_func` into
  exactly that statement.

## PG

- The command tag comes from the top-level Query's command type, and its
  count from `es_processed`. Only the top-level ModifyTable advances that
  count (`canSetTag`); the data-modifying CTEs do not.
- PL/pgSQL keeps that count in `estate->eval_processed`
  (`SPI_processed`). FOUND is `processed != 0`. Different statements set
  the two differently:
  - `exec_stmt_execsql` (SQL statements, with or without INTO) sets both;
  - `exec_run_select` (PERFORM) sets both;
  - `exec_stmt_dynexecute` (EXECUTE) sets only `eval_processed`;
  - RETURN QUERY and FETCH set `eval_processed`.
- `exec_stmt_getdiag` reads `eval_processed` for ROW_COUNT, `fn_oid` for
  PG_ROUTINE_OID, and the handled error (`cur_error`) for the stacked
  items.
  - GET STACKED outside a handler raises 0Z002.
  - pl_gram.y rejects items used in the wrong area: `diagnostics item %s
    is not allowed in GET CURRENT|STACKED DIAGNOSTICS`.

## Change

- **Tag.** `commandTagFor` handles `*optimizer.CTEDMLPrefix` by taking
  its Body's tag.
  - `cteDMLPrefixOp.RowsAffected` forwards to the body operator.
  - `stmtCTEScopeOp.RowsAffected` forwards to its root. The embedded
    `Operator` interface does not promote it, so before this a plain DML
    root wrapped by `buildStatementScoped` reported no count either.
  - Both protocols share `commandTagFor`.
- **Embedded SQL.** For a DML-rooted plan, `execPLpgSQLEmbeddedSQL`
  counts `RowsAffected`. `planIsDMLStatement` looks through
  `CTEDMLPrefix`. FOUND and the new frame `rowCount` follow.
- **`rowCount`.** It is set by:
  - SQL statements, PERFORM, and SELECT … INTO (which now also sets FOUND);
  - dynamic EXECUTE, which leaves FOUND alone. Without INTO it now runs to
    completion and counts every row, where it used to stop after the first.
- **GET DIAGNOSTICS.** `plpgsql.GetDiagStmt` and `parseGetDiag` follow
  PG's grammar and its area checks. The runtime arm assigns each item
  through `plpgsqlAssignCoerce`.
  - Implemented: ROW_COUNT, PG_ROUTINE_OID, RETURNED_SQLSTATE,
    MESSAGE_TEXT, PG_EXCEPTION_DETAIL, PG_EXCEPTION_HINT.
  - The exception handler stores the caught error in `frame.caughtErr`
    for the duration of the handler.
  - PG_CONTEXT, PG_EXCEPTION_CONTEXT and the object-name items
    (COLUMN_NAME, CONSTRAINT_NAME, PG_DATATYPE_NAME, TABLE_NAME,
    SCHEMA_NAME) raise 0A000. An empty string would be a wrong value: a
    unique violation carries constraint/table/schema names in PG.

## Verification

- `TestWritableCTEStatementCountsTopLevelRows` covers:
  - `RowsAffected` for four WITH-led shapes;
  - FOUND after plain and WITH-led DML;
  - GET DIAGNOSTICS across UPDATE, PERFORM, SELECT INTO, EXECUTE (DML and
    SELECT), WITH-led INSERT and GET STACKED;
  - four parse and runtime errors.

  Every want is PG 18.3's, and the test fails at HEAD.
- `TestCommandTagForWritableCTEUsesTopLevelTag` covers the tag arm.
- Server probes match PG for tags (simple and extended protocol), FOUND
  and GET DIAGNOSTICS.
- Regress A/B over 6 files, fresh clusters:
  - plpgsql 4189 → 4102, merge 1674 → 1587.
  - Some new output reaches existing gaps that earlier errors hid: RETURN
    QUERY EXECUTE, nested DECLARE blocks, the `f1` overload state.
  - with, triggers, returning and insert_conflict are identical. pg_regress
    runs psql quietly, so it does not show tags.
- Gates: units, tpch-spotcheck, arm 24/24, sf025 96/96 (plan shapes
  99/99), ea-ratchet PASS.

## Not covered

Ledgered:

- PG_CONTEXT / PG_EXCEPTION_CONTEXT need a PL/pgSQL error-context stack.
- The object-name stacked items need object-name fields on ExecError.
- RETURN QUERY and FETCH do not set ROW_COUNT.
- In PG, an expression evaluated through `exec_run_select` (a non-simple
  expression) also updates `eval_processed`; goopg's expressions do not.
- Dynamic EXECUTE ignores a SQL string that fails to parse, instead of
  raising the syntax error.

Filed:

- `WITH … MERGE` is a syntax error: M0146-0085.
- `text || bool` uses bool's output function (`xt`), where PG's
  `anytextcat` casts (`xtrue`): M0146-0086.
