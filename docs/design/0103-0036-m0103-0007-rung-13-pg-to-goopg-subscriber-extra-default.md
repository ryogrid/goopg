# 0103-0036 — M0103-0007 rung 13: subscriber-extra column DEFAULT evaluation

Status: accepted
Date: 2026-05-14
Milestone: M0103-0007 (Scenario A: PG primary → goopg subscriber)
Prior rung: 0103-0035 (REPLICA IDENTITY USING INDEX)

## Problem

`M0103-0007` rung 11 (design doc `0103-0034`) closed the case where the
goopg subscriber's table carries a column the publisher does not
mention, on the *UPDATE* path: `applyUpdateByKey` now copies the value
from the matched heap row into the new tuple before the delete+insert
that backs the replicated UPDATE, so subscriber-side bookkeeping
columns are preserved across publisher mutations.

The same code path on *INSERT* was deliberately deferred: every
publisher INSERT that the subscriber received installed `NullDatum`
into every column the publisher did not describe. That contradicts
upstream PostgreSQL behaviour:

  `slot_fill_defaults()` in `src/backend/replication/logical/worker.c`
  is invoked from `apply_handle_insert_internal` and evaluates the
  declared `DEFAULT` expression for any column that the publisher's
  Relation message does not claim. The subscriber rows therefore
  arrive with the configured default, not with NULL.

A goopg subscriber whose schema, e.g., adds an audit column

```sql
CREATE TABLE public.t (
    id   int PRIMARY KEY,
    v    text,
    note text DEFAULT 'auto'
);
```

against a publisher with `(id, v)` would receive every replicated row
with `note=NULL`. A NOT NULL DEFAULT would fail the heap write outright
once the writer enforces NOT NULL; today the column silently disagrees
with the schema author's intent.

## Decision

Capture each column's `DEFAULT` expression at CREATE TABLE time and
evaluate it inline in the apply worker's INSERT path when the
publisher's Relation message did not claim that column.

### Parser

`internal/parser/ast.go::ColumnDef` gains a `DefaultExpr Expr` field
(nil when no DEFAULT was given). `internal/parser/ddl.go`'s
`parseColumnDef` previously consumed-and-discarded the DEFAULT tokens;
it now calls `p.parseExpr()` and stores the resulting AST on
`col.DefaultExpr`. Storing the AST directly avoids a token→text→AST
round-trip — the executor only ever needs to evaluate the expression,
never re-render it.

### Catalog

`internal/catalog/catalog.go::Column` gains `DefaultExpr parser.Expr`.
The catalog already imports `internal/parser` (for `View
*parser.SelectStmt`), so this introduces no new dependency.

### Executor

`internal/executor/operators_ddl.go::execCreateTable` propagates
`c.DefaultExpr` from the parser AST into the new
`catalog.Column.DefaultExpr` field at table creation time.

`internal/executor/operators_generated.go` gains
`applyDefaultsForMissing(cols, row, missing)` — a thin wrapper that
reuses the existing lightweight `evalGenExpr` AST walker. For each
position `i` where `missing[i] == true` AND `cols[i].DefaultExpr !=
nil`, evaluate the expression against `(cols, row)` and overwrite
`row[i]`. Slots with no `DefaultExpr` stay at their incoming value
(typically `NullDatum` from `decodePgoutputTupleAsRow`). Evaluation
errors (an expression `evalGenExpr` can't handle, e.g.
`DEFAULT now()`) leave the slot unchanged so a downstream NOT NULL
violation surfaces loudly rather than silently NULL-ing the row.

`internal/executor/applyworker.go::applyInsert` now retains the
`missing` mask returned by `decodePgoutputTupleAsRow` and calls
`applyDefaultsForMissing(r.local.Columns, row, missing)` before the
heap write. The rung-11 UPDATE path is untouched: it continues to
prefer the matched heap row's value via the existing read-then-write
sequence, which is the correct semantics for UPDATE (an UPDATE
should not re-evaluate DEFAULTs).

### Scope of evalGenExpr

`evalGenExpr` (operators_generated.go) handles `IntegerConst`,
`NumericConst`, `StringConst`, `NullConst`, `BooleanConst`,
`ColumnRef`, `CastExpr` (passthrough), `BinaryOp`, `UnaryOp`. That
covers the DEFAULT shapes the pgoutput-replicated test fixtures
actually use:

  - `DEFAULT 'literal'`  (StringConst)
  - `DEFAULT 0`         (IntegerConst)
  - `DEFAULT TRUE`      (BooleanConst)
  - `DEFAULT NULL`      (NullConst)
  - `DEFAULT 0 + 1`     (BinaryOp)

Function calls (`DEFAULT now()`, `DEFAULT nextval('seq')`) and more
exotic expressions evaluate to NullDatum via the catch-all fallthrough
in `evalGenExpr`; `applyDefaultsForMissing` then leaves the slot
unchanged because the error path is swallowed in the helper.
Promoting those requires the full expression executor or a richer
DEFAULT evaluator, which is out of rung-13 scope.

### Why not the regular planner?

Routing DEFAULT evaluation through `planExpr` + `evalExprSlot` would
require a planner context and a fully-materialised `SchemaColumn`
binding for every column. The apply worker has neither — it operates
on `catalog.Column` slices and raw `Row` values, mirroring the
already-established `computeGeneratedColumns` path for GENERATED
ALWAYS columns. Reusing `evalGenExpr` keeps the two paths uniform.

## Alternatives considered

1. **Store DEFAULT as raw SQL text (mirror `GeneratedExpr string`).**
   Rejected: round-tripping the token stream through `strings.Join`
   loses string-literal quoting (`'a'` becomes the bare ident `a`),
   so the simplest common DEFAULT — `DEFAULT 'literal'` — would
   silently break. Storing the AST directly avoids the rendering
   problem entirely.

2. **Evaluate DEFAULTs via a fresh planner run.** Rejected: the apply
   worker doesn't carry a planner context, and constructing one per
   row is heavy. The expression set DEFAULT actually uses in practice
   is the same set `evalGenExpr` already covers.

3. **Push DEFAULT evaluation up into the regular dispatcher INSERT
   path too.** Out of scope. Today the regular INSERT path leaves
   omitted columns at NULL (no DEFAULT applied); fixing that is its
   own thread that doesn't gate logical replication parity. The apply
   worker is the load-bearing case for now because it's the only path
   that *cannot* know about every subscriber-side column at SQL
   construction time.

## Tests

Three pins land alongside the change.

1. **Parser pin —**
   `internal/parser/ddl_test.go::TestParseCreateTableDefaultExpr`.
   Four CREATE TABLE statements (`DEFAULT 'unknown'`, `DEFAULT 0`,
   `DEFAULT TRUE`, `DEFAULT NULL`); asserts that
   `col.DefaultExpr` is non-nil and that its concrete type matches
   the expected AST node (`*parser.StringConst`,
   `*parser.IntegerConst`, etc.). Pins the parser surface; without
   the rung-13 fix the field is never populated.

2. **Executor unit pin —**
   `internal/executor/applyworker_test.go::TestApplyDefaultsForMissingFillsSlots`.
   A `Row` with `(int, text, NullDatum, NullDatum)`, columns where
   the third has `DefaultExpr = &parser.StringConst{Value: "kept"}`
   and the fourth has no DEFAULT, and `missing = (false, false, true,
   true)`. Asserts that:
     - row[0] and row[1] are not touched (missing=false)
     - row[2] becomes `"kept"`
     - row[3] stays `NullDatum` (DefaultExpr nil)
   Plus `TestApplyDefaultsForMissingIgnoresFalseMask` — a column WITH
   a DefaultExpr but `missing[i]=false` must not be overwritten;
   guards against a regression where the helper applies DEFAULT
   indiscriminately.

3. **Live E2E pin —**
   `internal/testport/pgoutput_interop_test.go::TestPort_PgoutputInteropPGToGoopgSubscriberExtraDefault`.
   Full PG-publisher + goopg-subscriber harness. Publisher has
   `(id int PK, v text)`, subscriber adds two extra columns —
   `note text DEFAULT 'auto'` AND `bare text` (no DEFAULT). Two
   INSERTs replicate. Assertions via fresh `database/sql` sessions:
     - `id=1 AND v='hello' AND note='auto'` returns 1
     - `id=2 AND v='world' AND note='auto'` returns 1
     - `bare IS NULL` returns 2
     - `count(*) = 2`, no spurious extras
   Negative pin (`bare IS NULL` returns 2) catches a regression where
   `applyDefaultsForMissing` started blanket-filling every missing
   slot instead of only those with `DefaultExpr` set.

## Verification

```
$ go test -count=1 -timeout 60s -run TestParseCreateTableDefaultExpr ./internal/parser/
ok  	github.com/goopg/goopg/internal/parser
$ go test -count=1 -timeout 60s -run "TestApplyDefaultsForMissing" ./internal/executor/
ok  	github.com/goopg/goopg/internal/executor
$ go test -count=1 -timeout 180s -run TestPort_PgoutputInteropPGToGoopgSubscriberExtraDefault ./internal/testport/
ok  	github.com/goopg/goopg/internal/testport  2.05s
$ go test -count=1 -timeout 240s -run "TestPort_PgoutputInterop" ./internal/testport/
ok  	github.com/goopg/goopg/internal/testport  23.68s
$ go test -count=1 -timeout 180s ./internal/parser/ ./internal/catalog/ ./internal/executor/
ok  	github.com/goopg/goopg/internal/parser    0.01s
ok  	github.com/goopg/goopg/internal/catalog   0.01s
ok  	github.com/goopg/goopg/internal/executor  1.14s
```

All 12 `TestPort_PgoutputInteropPGToGoopg*` tests run together cleanly
in ~24 s; rungs 1–12 stay green alongside the new rung-13 pin.

## Next rungs (deferred within M0103-0007)

- pgbench against PG publisher with `pgbench_history` polling
- proto_version=2 streaming subxacts (apply-worker subxact tracking)
- kill -9 + libpq multi-host reconnect plumbing on the client side
- DEFAULT-expression evaluation in the regular dispatcher INSERT path
  (not just the apply worker — orthogonal to logical replication
  parity but called out by the parser/catalog work landed here)
- Richer DEFAULT evaluator (function calls, sequences) — only when a
  fixture surfaces a need
