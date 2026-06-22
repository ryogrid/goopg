# 0118-0050 — PL/pgSQL `SELECT … INTO` statement form (M0118-0008 enabler)

**Status:** accepted
**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency — `plpgsql-toast`)
**Kind:** Enabler — NOT a spec promotion.

## Problem

The `plpgsql-toast` isolation spec drives PL/pgSQL procedures whose variable
assignments exercise the different code paths PostgreSQL uses to detoast values
that must survive a `COMMIT`. The first such path (`assign1`) is the canonical
`SELECT … INTO` form:

```sql
do $$
  declare x text;
  begin
    select test1.b into x from test1;   -- bind first row's column to x
    delete from test1;
    commit;
    perform pg_advisory_lock(1);
    raise notice 'length(x) = %', length(x);
  end;
$$;
```

goopg captured the entire `select test1.b into x from test1` verbatim as an
embedded SQL statement (`plpgsql.SQLStmt`) and handed it to the SQL planner,
which interprets `SELECT … INTO <name>` as SQL's *CREATE-TABLE-AS* spelling —
so `x` was treated as a **new table name**, not a PL/pgSQL variable. The step
errored (or created a stray relation) instead of binding the query result to
the declared variable.

The prior enabler (`0118-0049`) lifted the spec's first blocker (`COMMIT` in a
`DO` block); this enabler lifts the next one (`SELECT … INTO`).

## Design

PL/pgSQL reinterprets a **top-level** `INTO` clause inside a `SELECT` as
variable assignment — you cannot write a SQL `SELECT INTO <table>` inside a
PL/pgSQL body (you would use `CREATE TABLE … AS` explicitly). So treating every
top-level `INTO` in a body-level `SELECT` as a target list is correct PG
behavior.

### Parser (`internal/plpgsql/parser.go`, `internal/plpgsql/ast.go`)

`parseSQLStmt` now special-cases a leading `SELECT`. While scanning to the
terminating `;`, it watches for a `depth == 0` `INTO` keyword. On finding one it:

1. records the byte offset of the `INTO` token,
2. consumes an optional `STRICT` modifier,
3. collects the comma-separated target list (each target may be a dotted name,
   e.g. `r.b`, for a record field), stopping at the first token that is not an
   identifier / `.` / `,` (typically `FROM`),
4. records the byte offset where the target list ends.

When the `;` is reached, the query text is reconstructed as
`src[start : intoStart] + " " + src[targetsEnd : end]` — i.e. the original
SELECT with the `INTO …` clause excised — and a new
`*SelectIntoStmt{SQL, Targets, Strict}` node is returned. A `SELECT` with no
top-level `INTO` still returns a verbatim `*SQLStmt` exactly as before.

New AST node:

```go
type SelectIntoStmt struct {
    pos     int
    SQL     string   // SELECT with the INTO clause stripped
    Targets []string // target variable name(s)
    Strict  bool     // STRICT modifier
}
```

### Runtime (`internal/executor/plpgsql_runtime.go`)

A new `case *plpgsql.SelectIntoStmt` plans/builds/opens the stripped query,
captures the **first** result row (copied out of the pooled slot before
`Close()`), and binds it to the target(s) via the new `bindSelectIntoRow`
helper, mirroring the existing `FOR … IN SELECT` loop binding conventions:

- **single target, single column** → assign directly to the scalar variable;
- **single target, multiple columns** → treat as a record, binding each column
  to a `_<target>_<colname>` sub-field (auto-registered if absent), the same
  naming `ForSelectStmt` already uses for record/row loop variables;
- **multiple targets** → bind result columns positionally to the scalar
  variables.

A missing column (or no matching row) yields `NULL`, matching PG: an unmatched
`SELECT INTO` sets its target(s) to NULL. `STRICT` is parsed and enforced — zero
rows raises `P0002` (no_data_found), more than one raises `P0003`
(too_many_rows).

The output schema is captured from `op.Schema()` **before** the first `Next()`
so the no-row case can still bind NULL to the correct number of columns.

## Scope / non-goals

This enabler covers the `SELECT … INTO` **statement form** and scalar /
multi-scalar / best-effort record binding. The full `plpgsql-toast` promotion
remains deferred (Effort-L) on the genuinely hard pieces the spec measures:

- **detoasting across `COMMIT`** — values held in PL/pgSQL variables must be
  freed of external TOAST pointers before a commit (else a concurrent `VACUUM`
  reclaims the chunks and `length()` later fails); goopg does not yet force
  detoast at the assignment boundary;
- **subquery-valued assignment** (`assign2`: `x := (select … )`) — PL/pgSQL
  scalar expressions do not yet accept a subquery (`subqueries are not
  supported in PL/pgSQL expressions`);
- **expanded record types** (`assign3`/`assign4`: `r record` / `r test2`,
  `r.b := …`, `length(r::text)`) and the `FOR r IN SELECT` detoast paths
  (`assign5`/`assign6`);
- the runner's `<waiting ...>` concurrency-marker timing around the
  advisory-lock / VACUUM interplay.

A probe confirmed the spec's first divergence advances from the `SELECT INTO`
mis-parse to these later gaps: `assign1` now executes and emits
`length(x) = 6000`.

## Oracle

Upstream behavior mirrored: `src/pl/pgsql/src/pl_gram.y` (`read_into_target`,
the `K_INTO` handling that splits the INTO clause off a SELECT) and
`pl_exec.c` `exec_stmt_execsql` / `exec_move_row` (binding the first row to
scalar vs row/record targets, NULL on no-row, `STRICT` row-count checks).
Spec/expected: `postgres/src/test/isolation/{specs,expected}/plpgsql-toast.*`.

## Tests / gates

- `internal/plpgsql`: `TestParseSelectInto` (scalar, record `*`, multi-target +
  STRICT, query reconstruction), `TestParseSelectNoIntoIsEmbeddedSQL`
  (regression: plain SELECT still `*SQLStmt`).
- `internal/executor`: `TestPlpgSQLSelectInto` (scalar bind, positional
  multi-target bind, no-row → NULL) against a storage-backed fixture.
- Regression: full `internal/executor` package PASS; `internal/plpgsql` PASS;
  `TestPlpgSQLDoCommitChainDurability`/`…RollbackChain`/
  `…CommitInExplicitBlockRejected` + `TestPort_IsolationSubxidOverflow` PASS
  (no DO-path regression).
- `go vet ./internal/plpgsql/ ./internal/executor/` clean.
- pgbench smoke = pre-commit hook (no executor hot-path change).
