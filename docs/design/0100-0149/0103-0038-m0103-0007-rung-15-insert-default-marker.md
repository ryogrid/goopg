# M0103-0007 rung 15 — INSERT … VALUES (DEFAULT, …) parser + planner support

Status: accepted (2026-05-14)
Scope: M0103-0007 (Scenario A E2E test path, rung 15)
Related: 0103-0036 (rung 13 — apply-side DEFAULT eval),
0103-0037 (rung 14 — dispatcher INSERT DEFAULT eval)

## Why

Rung 14 made the dispatcher's `INSERT INTO t (a, b) VALUES (…)` path
fill DEFAULT-expression values for columns NOT in the target list.
The complementary surface — `INSERT INTO t (a, b, c) VALUES (1, 2,
DEFAULT)` where the user *explicitly* requests DEFAULT for a column
that IS in the target list — still tripped the parser. `parseValuesRow`
called `parseExpr` directly, which has no case for the `DEFAULT`
keyword (`KwDefault` is reserved), so the parser produced
`syntax error at or near "DEFAULT"` at the row's `DEFAULT` token.

PostgreSQL's grammar accepts `DEFAULT` only inside `VALUES` rows of an
`INSERT` (and inside `UPDATE … SET col = DEFAULT`). Upstream's
rewriter (`rewriteValuesRTE`) substitutes the column's actual default
expression at rewrite time, before the executor sees the row.

Pgbench's standard workload does not use `VALUES (DEFAULT, …)`, so
this rung does not directly unblock the Scenario A E2E test.  It is
still inside M0103-0007 scope because it closes the symmetric
companion to rung 14 — every other client tool the Scenario A harness
relies on (psql, libpq) will accept `DEFAULT` in `VALUES`, and
without parser support a regression-test fixture that targets a real
pgbench-shaped subscriber-extra column with a published peer table
would have to avoid the explicit-DEFAULT shape entirely. Better to
land it as one small rung now than to leave the parser in a state
where `VALUES (DEFAULT)` raises a syntax error after rung 14
advertises that DEFAULT semantics are fully wired.

## Design

### Parser: new `DefaultMarker` AST node

Add a zero-field sentinel in `internal/parser/expr.go`:

```go
type DefaultMarker struct{ pos int }
func (e *DefaultMarker) Pos() int { return e.pos }
func (*DefaultMarker) exprNode()  {}
```

The marker carries no operands. It is only legal inside an `INSERT …
VALUES` row; reaching the planner anywhere else surfaces as a
`PlanError` (`42601`) because no other resolver knows about it.

### Parser: `parseValuesRow` accepts `KwDefault`

In `internal/parser/dml.go::parseValuesRow`, accept the `DEFAULT`
keyword as a row cell:

- before each cell, if `p.cur().Kind == TokenKeyword && p.cur().Keyword
  == KwDefault`, consume the token and emit `&DefaultMarker{pos: …}`.
- otherwise fall back to `p.parseExpr()` as today.

The change is parse-only — no analyzer / scope work, no
`exprNode()` interactions beyond satisfying the `Expr` interface.

### Planner: substitute in `planInsert`

In `internal/planner/planner.go::planInsert`, the VALUES branch
already walks each row and runs `resolveExpr` on every cell.  Insert
a pre-substitution step BEFORE `resolveExpr`:

```go
for i, e := range r {
    if _, ok := e.(*parser.DefaultMarker); ok {
        // Translate row position i -> target column.
        tgt := colIndex[i]
        col := tbl.Columns[tgt]
        if col.DefaultExpr != nil {
            e = col.DefaultExpr
        } else {
            e = &parser.NullConst{}
        }
    }
    pe, err := resolveExpr(e, ctx)
    …
}
```

`col.DefaultExpr` was added by rung 13 (catalog), populated by rung
13's `execCreateTable`.  `resolveExpr` then handles the substituted
expression exactly as if the user had written it inline. SERIAL
columns retain their existing nil DefaultExpr → NullConst → the
executor's `nextval` block (rung 14 hot path) picks them up.

### Executor: no change

The executor never sees `DefaultMarker` — the planner has already
substituted. The rung 14 `applyDefaultsForMissing` path stays
authoritative for columns *not* in the explicit column list.

`INSERT INTO t (a, b, c) VALUES (1, 2, DEFAULT)` becomes equivalent
to `INSERT INTO t (a, b, c) VALUES (1, 2, 'auto')` at plan time
(when `c text DEFAULT 'auto'`); `INSERT INTO t (a, b, c) VALUES (1,
2, DEFAULT)` against a column with no DEFAULT becomes `… VALUES (1,
2, NULL)`.

### Why substitute at plan time, not at execute time

Two alternatives were considered:

1. **Per-row mask threaded into `insertOp.Next`.** Each VALUES row
   would carry a `[]bool` "default-requested" flag; insertOp would
   merge it with `insertMissing` and call `applyDefaultsForMissing`
   per row.  Rejected: the merge would have to happen INSIDE the
   per-row loop (per-row variation), losing rung 14's hoisted-mask
   property; and the analyzer would still need a special case for
   `DefaultMarker` so the type check doesn't trip.
2. **Inline at executor row-construction.** `valuesOp` could detect
   `DefaultMarker` while evaluating cells.  Rejected: same
   per-row-cost concern, and it pushes catalog-table knowledge into
   `valuesOp` (currently catalog-free).

Plan-time substitution mirrors upstream's `rewriteValuesRTE` and is
the least invasive change.  The substituted expression flows through
the existing `resolveExpr` → executor pipeline unchanged.

## Tests (pins)

1. **Parser** (`internal/parser/dml_test.go`):
   `TestParseInsertValuesAcceptsDefaultKeyword` — parses
   `INSERT INTO t (a, b) VALUES (1, DEFAULT)`, asserts the AST has
   two cells where `rows[0][1]` is a `*DefaultMarker`. Negative
   companion: `TestParseInsertValuesRejectsBareDefault` — `INSERT
   INTO t VALUES (DEFAULT + 1)` raises a syntax error at the `+`
   token (DefaultMarker is not a regular expression).  *(The second
   negative test confirms `DEFAULT` is only accepted as a complete
   cell, not as a sub-expression — matches PG.)*

2. **Planner** (`internal/planner/planner_test.go`):
   `TestPlanInsertValuesDefaultSubstitutesColumnDefault` — registers
   table `t (id int, note text DEFAULT 'auto')`, plans `INSERT INTO
   t (id, note) VALUES (1, DEFAULT)`, asserts the planner's Values
   row has the resolved DEFAULT expression (not the marker).
   Companion: `TestPlanInsertValuesDefaultColumnWithoutDefaultGivesNull`
   — column has no DEFAULT, DEFAULT becomes NullDatum.

3. **End-to-end** (no new file): rung 13/14's existing live tests
   confirm DEFAULT evaluation works once the value reaches
   `applyDefaultsForMissing` or the column's resolved expression.

## Out of scope

- `UPDATE … SET col = DEFAULT` (a separate parse path; doesn't ship
  with rung 15).
- `INSERT INTO t DEFAULT VALUES` (the "all defaults" form).
- DEFAULT in `MERGE` `WHEN MATCHED THEN UPDATE SET …` clauses.

## Verification

```
go test -count=1 -timeout 60s -run "TestParseInsertValues" ./internal/parser/
go test -count=1 -timeout 60s -run "TestPlanInsertValuesDefault" ./internal/planner/
go test -count=1 -timeout 60s -run "TestInsertFillsMissingColumnDefault|TestInsertDoesNotOverrideExplicitColumnDefault" ./internal/executor/
go test -count=1 -timeout 180s -run "TestPort_PgoutputInteropPGToGoopg" ./internal/testport/
go test -race -count=1 -timeout 300s ./internal/parser/ ./internal/planner/ ./internal/executor/
```

All gates green at commit time.
