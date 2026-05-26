# 0097-0024c — Reject `COPY (SELECT … INTO …)` with the PG-compatible message

**Status:** accepted
**Milestone:** M0097-0024 (COPY / sequence / identity regress test porting — `copyselect`)

## Problem

`copyselect` exercises the deprecated `SELECT … INTO …` form inside a COPY
query:

```sql
copy (select t into temp test3 from test1 where id=3) to stdout;
```

PostgreSQL's grammar *accepts* `SELECT INTO` inside `COPY (...)` (it parses to
a `CreateTableAsStmt` utility node), then `DoCopy` rejects it at execution time
(`src/backend/commands/copyto.c`):

```c
if (query->utilityStmt != NULL &&
    IsA(query->utilityStmt, CreateTableAsStmt))
    ereport(ERROR,
            (errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
             errmsg("COPY (SELECT INTO) is not supported")));
```

The expected `.out` line is exactly:

```
ERROR:  COPY (SELECT INTO) is not supported
```

(no `LINE`/caret — it is a feature-not-supported error, not a syntax error).

goopg has no `SELECT … INTO …` support at all. `parseSelect` parses the target
list `select t`, then stops at the **reserved** `INTO` keyword (it cannot be a
column alias and is not a clause `parseSelect` recognises). Control returned to
`parseCopy`, whose `if !p.acceptSymbol(")")` check then fired against the
dangling `into` token and produced a stray `expected ')'` syntax error — not
PG's message.

## Fix

Mirror PostgreSQL's "grammar accepts, command rejects" split across goopg's
parser/planner boundary. Purely additive; no `SELECT INTO` execution support is
introduced (the form is only ever rejected).

1. **Parser** (`internal/parser/copy.go`, `parseCopy`): after parsing the inner
   query, if it is a `*SelectStmt` and the cursor sits on the reserved `INTO`
   keyword, set the new `CopyStmt.SelectInto` flag and call
   `skipInnerQueryRemainder` to consume the unparsed `INTO <target> FROM …`
   tail up to — but not including — the matching `)` (parenthesis-depth tracked
   so nested subqueries/calls are skipped over). The caller's existing `)`
   check then closes the statement normally. The tail's exact shape is
   irrelevant because the statement is rejected; we only need a clean parse.

2. **AST** (`internal/parser/ast.go`): `CopyStmt` gains `SelectInto bool`.

3. **Planner** (`internal/planner/copy.go`, `planCopy`): at the top of the
   `s.Query != nil` branch, if `s.SelectInto`, return
   `*PlanError{Code: "0A000", Message: "COPY (SELECT INTO) is not supported"}`
   before any catalog work. `0A000` is `ERRCODE_FEATURE_NOT_SUPPORTED`, matching
   upstream; psql renders only the message, so the SQLSTATE is invisible in the
   regress `.out` but kept correct for clients.

The planner error reaches the wire via the same `dispatchCopyViaExecutor`
(`internal/server/copy.go`) path proven by the view-rejection work
([[0097-0009b-copy-from-view-rejection]]): a `planCopy` `PlanError`'s
`Code`/`Message` are rendered as `ERROR:  <message>`.

## Tests

- `TestParseCopySelectIntoFlagged` (`internal/parser/copy_test.go`): the
  SELECT-INTO form parses with `SelectInto` set; a plain `COPY (SELECT …)` is
  **not** flagged.
- `TestPlanCopySelectIntoRejected` (`internal/planner/copy_test.go`): planning
  yields `0A000 "COPY (SELECT INTO) is not supported"`.
- Verified live on port 5599: the exact `ERROR:  COPY (SELECT INTO) is not
  supported` line is produced, and `copy (select t from test1 where id=1) to
  stdout` still streams its row.

## Remaining `copyselect` gaps (independent, next COPY wins)

- `copy (select * from test1) from stdin` — PG: `syntax error at or near
  "from"` (grammar allows only `TO` for the query form). goopg currently leaks
  its own `COPY (query) is only valid with TO` message at byte 0; needs a
  syntax error positioned at the `FROM` token instead.
- `copy (select * from test1) (t,id) to stdout` — PG: `syntax error at or near
  "("` (no column list on the query form).
- psql multi-command `\;`/`\.` COPY handling.
