# M0103-0007 rung 16 — UPDATE … SET col = DEFAULT parser + planner support

Status: accepted (2026-05-14)
Scope: M0103-0007 (Scenario A E2E test path, rung 16)
Related: 0103-0038 (rung 15 — INSERT VALUES DEFAULT parser support),
0103-0037 (rung 14 — dispatcher INSERT DEFAULT eval),
0103-0036 (rung 13 — apply-side DEFAULT eval)

## Why

Rung 15 closed `INSERT … VALUES (…, DEFAULT, …)`. The symmetric
UPDATE-side surface — `UPDATE t SET col = DEFAULT WHERE …` —
still tripped the parser at the `DEFAULT` token because
`parseAssign` called `parseExpr` directly and `KwDefault` is a
reserved keyword with no expression-level production.

PostgreSQL's grammar accepts `DEFAULT` only as the complete RHS of
a `SET col = …` assignment (and as a row cell inside an `INSERT
… VALUES`). Upstream's rewriter (`rewriteTargetListUD`) substitutes
the column's actual default expression at rewrite time, before the
executor sees the assignment.

Pgbench's standard workload does not use `SET col = DEFAULT`, so
this rung does not directly unblock the Scenario A E2E test. It
is still inside M0103-0007 scope because it closes the symmetric
companion to rung 15 — every other client tool the Scenario A
harness relies on (psql, libpq) will accept `DEFAULT` on the RHS
of an UPDATE assignment, and without parser support a regression
fixture that targets a real pgbench-shaped subscriber-extra
column would have to avoid the explicit-DEFAULT shape entirely.
Better to land it as one small rung now than to leave the parser
asymmetric across the two DEFAULT-keyword surfaces while rungs
13–15 advertise DEFAULT semantics as fully wired.

## Design

### Parser: `parseAssign` accepts `KwDefault`

In `internal/parser/dml.go::parseAssign`, after consuming the
`=` operator, peek for the `DEFAULT` keyword:

```go
if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault {
    t := p.cur()
    p.advance()
    return UpdateAssign{pos: pos, Column: identText(col), Expr: &DefaultMarker{pos: t.Pos}}, nil
}
```

Falls back to `p.parseExpr()` otherwise. Mirrors the per-cell
hook rung 15 added to `parseValuesRow`. No new AST node — the
existing `DefaultMarker` sentinel is reused (zero-field; the
analyzer never observes it).

The marker is only legal as the complete RHS of a SET assignment;
reaching the planner anywhere else surfaces as a `PlanError`
(`42601`) because no other resolver knows about it.

### Planner: `rewriteUpdateDefaultMarkers`

New helper alongside rung 15's `rewriteInsertDefaultMarkers` in
`internal/planner/planner.go`:

```go
func rewriteUpdateDefaultMarkers(s *parser.UpdateStmt, cat catalog.Catalog) error {
    if len(s.Set) == 0 {
        return nil
    }
    tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
    if !ok {
        return nil // planUpdate will raise the missing-relation error
    }
    for i := range s.Set {
        if _, ok := s.Set[i].Expr.(*parser.DefaultMarker); !ok {
            continue
        }
        col, ok := cat.LookupColumn(tbl, s.Set[i].Column)
        if !ok {
            return nil // planUpdate / analyzer will raise 42703
        }
        if def := tbl.Columns[col.Ordinal].DefaultExpr; def != nil {
            s.Set[i].Expr = def
        } else {
            s.Set[i].Expr = &parser.NullConst{}
        }
    }
    return nil
}
```

`Plan()` calls it for `*parser.UpdateStmt` BEFORE `analyzer.Analyze`,
matching rung 15's INSERT prologue. The analyzer never sees the
marker; the substituted expression flows through `analyzer.Analyze`
→ `planUpdate`'s existing `resolveExpr(a.Expr, ctx)` path unchanged.

### Executor: no change

The executor's `updateOp` already evaluates each `Set[i]` expression
per row. After rewrite the marker has been substituted, so a SET
assignment with `DEFAULT` becomes equivalent to writing the
substituted constant inline.

`UPDATE t SET note = DEFAULT WHERE id = 1` becomes equivalent to
`UPDATE t SET note = 'auto' WHERE id = 1` at plan time (when
`note text DEFAULT 'auto'`). `UPDATE t SET bare = DEFAULT WHERE
id = 1` against a column with no DEFAULT becomes `… SET bare =
NULL WHERE id = 1`.

### Why substitute at plan time, not at execute time

Two alternatives were considered:

1. **Per-assignment marker threaded into `updateOp`.** `updateOp`
   would detect `*DefaultMarker` while evaluating each `Set[i]`
   and substitute on the fly. Rejected: pushes catalog-table
   knowledge into the executor (currently catalog-free for SET
   evaluation), and the analyzer would still need a special case
   so the type check doesn't trip on the un-typed marker.
2. **Replace at analyzer time.** The analyzer's UPDATE walker
   could substitute. Rejected: the analyzer is purposefully
   pre-planner — keeping rewrites in the planner mirrors PG's
   pipeline (parser → rewriter → planner) and keeps both
   DEFAULT-marker rewrites colocated.

Plan-time substitution mirrors upstream's `rewriteTargetListUD`
and is the least invasive change. The substituted expression
flows through the existing `resolveExpr` → executor pipeline
unchanged. Symmetric with rung 15.

## Tests (pins)

1. **Parser** (`internal/parser/dml_test.go`):
   - `TestParseUpdateSetDefaultKeyword` — parses `UPDATE t SET v
     = DEFAULT WHERE id = 1`, asserts the AST has one SET pair
     where `Set[0].Expr` is a `*DefaultMarker`.
   - `TestParseUpdateSetDefaultMultiAssign` — comma-separated
     SET list with DEFAULT on a subset of assignments and a
     plain expression in the middle; asserts the marker lands
     only on the DEFAULT positions.
   - `TestParseUpdateSetRejectsBareDefaultInExpression` —
     `UPDATE t SET v = DEFAULT + 1` raises a syntax error. The
     keyword is accepted only as a complete RHS, not as a
     sub-expression. Matches upstream PG.

2. **Planner** (`internal/planner/planner_test.go`):
   - `TestPlanUpdateSetDefaultSubstitutesColumnDefault` —
     registers table `t (id int, note text DEFAULT 'auto')`,
     plans `UPDATE t SET note = DEFAULT WHERE id = 1`, asserts
     `Update.Set[1]` is the resolved DEFAULT expression (not
     the marker), `Update.Set[0]` is nil (UPDATE preserves the
     existing value of columns not named in SET).
   - `TestPlanUpdateSetDefaultColumnWithoutDefaultGivesNull` —
     column has no DEFAULT, DEFAULT becomes NullConst.

3. **End-to-end** (no new file): rung 13/14's existing live
   tests confirm DEFAULT-derived values flow end-to-end once
   the value reaches the resolved expression layer.

## Out of scope

- `UPDATE … SET (col1, col2) = (DEFAULT, 1)` row-syntax SET
  assignment (separate parse path; out of scope).
- DEFAULT in `MERGE … WHEN MATCHED THEN UPDATE SET …` clauses
  (separate parse path through `parseMerge`; not exercised by
  Scenario A).
- Richer DEFAULT evaluator (function calls, sequences) — still
  the rung-14-noted deferred item.

## Verification

```
go test -count=1 -timeout 60s -run "TestParseUpdateSetDefault|TestParseUpdateSetRejects" ./internal/parser/
go test -count=1 -timeout 60s -run "TestPlanUpdateSetDefault" ./internal/planner/
go test -count=1 -timeout 180s -run "TestPort_PgoutputInteropPGToGoopg" ./internal/testport/
go test -race -count=1 -timeout 300s ./internal/parser/ ./internal/planner/ ./internal/analyzer/ ./internal/executor/ ./internal/catalog/
```

All gates green at commit time.
