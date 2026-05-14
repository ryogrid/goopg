# 0103-0040 — M0103-0007 rung 17: `INSERT … DEFAULT VALUES` (all-defaults form)

Status: accepted (2026-05-14)

## Context

Rungs 14–16 closed the per-cell `DEFAULT` substitution path:

- Rung 14 (dispatcher): the executor's `insertOp` fills missing columns
  from `Column.DefaultExpr` before SERIAL / triggers / CHECK / FK /
  generated.
- Rung 15 (parser+planner): `INSERT INTO t (a,b) VALUES (1, DEFAULT)`
  parses the bare `DEFAULT` keyword as a `*parser.DefaultMarker` and the
  planner substitutes the column's catalog `DefaultExpr` before the
  analyzer runs.
- Rung 16 (parser+planner): the same substitution path on the RHS of
  `UPDATE SET col = DEFAULT`.

The remaining shape in standard SQL is the **all-defaults** form:

```sql
INSERT INTO t DEFAULT VALUES;
```

— a single row whose every column receives the table's declared
DEFAULT (or NULL where no DEFAULT exists). Before this rung the parser
rejected it as a syntax error.

## Why land it now

`pg_dump` emits this shape for tables whose columns all carry DEFAULTs
when no explicit values exist (rare but legitimate). pgbench's `-i`
init script does not emit it, but library-style test harnesses
(specifically the deferred D-005 client-tools scripts in
`docs/test-port/postgres-oracle-port-status.csv`) do; surfacing the
parser branch keeps the DEFAULT story uniform and unblocks any future
fixture that uses the form.

## Design

### Parser

`InsertStmt` gains a single field:

```go
DefaultValues bool
```

`parseInsert` (`internal/parser/dml.go`) handles three mutually
exclusive bodies after the optional column list:

1. `SELECT …` — `s.Select` populated (existing).
2. `DEFAULT VALUES` — `s.DefaultValues = true`, `s.Rows` stays nil (new).
3. `VALUES (…)` — `s.Rows` populated (existing).

The `DEFAULT VALUES` arm is implemented as a peek for `KwDefault`,
consume, then `expectKeyword(KwValues)`. Any other token after
`DEFAULT` raises the standard `expect 'VALUES'` syntax error — pinned
by `TestParseInsertDefaultValuesRejectsExtraValues`.

### Planner

`rewriteInsertDefaultMarkers` (`internal/planner/planner.go`) — the
same pre-`analyzer.Analyze` pass that substitutes per-cell DEFAULT
markers — learns one extra step. When `s.DefaultValues` is true:

1. Compute `colIndex` exactly as the existing branch does: skip
   `GeneratedAlways` columns when the user omitted a column list (and
   they always do for `DEFAULT VALUES`).
2. Synthesize `s.Rows = [[DefaultMarker, …]]` sized to `len(colIndex)`.
3. Clear `s.DefaultValues` so the analyzer (which doesn't know about
   the flag) sees an ordinary one-row VALUES INSERT.
4. Fall through to the existing per-cell substitution loop.

After the rewrite the analyzer, `planInsert`, and the executor see a
shape **byte-identical** to what the user would have produced with an
explicit `VALUES (DEFAULT, DEFAULT, …)` list. No new code path
downstream.

### Missing-table / missing-column

When `cat.LookupTable` fails, `rewriteInsertDefaultMarkers` returns nil
without modifying `s`. The analyzer's `lookupTable` then raises the
correct `42P01: relation … does not exist`. `s.DefaultValues` is still
true at that point but the analyzer never reaches the `len(s.Rows) == 0`
guard because `lookupTable` short-circuits.

## Tests

- `internal/parser/dml_test.go`:
  - `TestParseInsertDefaultValues` — pins `DefaultValues=true`,
    `Rows=nil`, `Select=nil`, `Columns=nil`.
  - `TestParseInsertDefaultValuesWithReturning` — `RETURNING` after
    `DEFAULT VALUES` parses normally.
  - `TestParseInsertDefaultValuesRejectsExtraValues` — `DEFAULT (1)`
    raises a syntax error.

- `internal/planner/planner_test.go`:
  - `TestPlanInsertDefaultValuesExpandsToColumnDefaults` — three
    columns, the first two with `DefaultExpr`, the third without; the
    resulting Insert's `Values` row has `(IntegerConst{7},
    StringConst{"auto"}, NullConst{})`.
  - `TestPlanInsertDefaultValuesSkipsGeneratedColumns` — a generated
    column is excluded from the expansion; `ColumnIndex=[0]` and the
    expansion row has arity 1.

## Verification

- `go test -count=1 -timeout 60s -run "TestParseInsertDefaultValues|TestPlanInsertDefaultValues" ./internal/parser/ ./internal/planner/`
  → PASS.
- Broader sweep recorded at commit time covering parser/planner/
  analyzer/executor/catalog/server/wal.

## Out of scope

Sequence-driven SERIAL expansion when `DefaultExpr` is nil but the
column is SERIAL: that flow continues via `insertOp.Next`'s SERIAL
`nextval` block (rung 14 hot path), which fires only when
`row[i].IsNull()`. The planner emits `NullConst` for SERIAL cells
under `DEFAULT VALUES` (no `DefaultExpr`), so SERIAL allocation still
works without rung-17-specific changes.

Function-call / volatile DEFAULT evaluation (e.g. `now()`,
`nextval('seq')`) remains the rung-14 deferred surface; no fixture
in M0103-0007 currently requires it.
