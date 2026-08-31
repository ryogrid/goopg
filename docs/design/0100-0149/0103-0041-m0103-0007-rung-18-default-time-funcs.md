# 0103-0041 — M0103-0007 rung 18: zero-arg time functions in DEFAULT expressions

Status: accepted (2026-05-14)

## Context

Rungs 13–17 closed every parser/planner surface for the DEFAULT keyword:

- Rung 13 (apply worker): subscriber-extra columns whose `Column.DefaultExpr`
  is set get filled by `applyDefaultsForMissing` when the publisher's
  pgoutput Relation message omits the column.
- Rung 14 (dispatcher): local INSERT path runs the same helper before
  SERIAL / triggers / CHECK / FK / generated.
- Rung 15 (parser+planner): `INSERT INTO t (a,b) VALUES (1, DEFAULT)`
  parses `DEFAULT` as `*parser.DefaultMarker`; the planner substitutes the
  column's catalog `DefaultExpr` before the analyzer runs.
- Rung 16 (parser+planner): `UPDATE t SET col = DEFAULT` — same marker
  substitution on the assignment RHS.
- Rung 17 (parser+planner): `INSERT INTO t DEFAULT VALUES` expands to a
  one-row VALUES of `DEFAULT` markers, then runs through the rung-15
  substitution loop.

Every rung delegated evaluation of the catalog's `DefaultExpr` to a single
helper, `evalGenExpr` (in `internal/executor/operators_generated.go`). The
helper was deliberately scoped to the small subset of expressions the
GENERATED-ALWAYS path needed when it was first introduced (M0096-0008):
literals, NULL, bool, column refs, `CAST`, binary arithmetic, unary
negation. Anything outside that grammar — most notably function calls — fell
through to `return NullDatum, nil`, so a column with `DEFAULT now()` or
`DEFAULT current_timestamp` silently stored NULL.

The rung-17 close-out flagged this gap as the next deferred item:

> Next rungs (deferred within M0103-0007): … richer DEFAULT evaluator
> (function calls, sequences) when a fixture surfaces a need.

This rung lands the smallest fixture-surfacing subset: the zero-arg time
functions. They cover the overwhelming majority of real-world DEFAULTs
(`created_at`, `updated_at`, audit columns) and they have a stable, total
evaluator (no plumbed state, no error cases worth surfacing).

## Why land it now

Three reasons:

1. **Replication parity.** A logical-replication fixture where the
   subscriber adds an audit column `created_at timestamptz DEFAULT now()`
   is the canonical extra-column shape. Rung 13 handles the SQL surface but
   leaves the slot NULL because the evaluator can't see past `*FuncCall`.
2. **Dispatcher parity.** Rung 14 has the identical gap on the local path:
   `INSERT INTO t (id, label) VALUES (1, 'x')` against a table with
   `created_at timestamptz DEFAULT now()` lands NULL today.
3. **Bounded scope.** `now()` / `current_timestamp` / `current_date` /
   `transaction_timestamp` / `statement_timestamp` are total niladic
   functions evaluated against wall-clock time. The implementation is one
   `switch` case with no plumbing, and the fixtures are testable with
   bounded-skew assertions.

Sequences (`nextval(...)`) are deliberately deferred to a separate rung:
they require sequence registry plumbing (per-session caching, transactional
semantics on rollback) and have a meaningfully different test shape.

## Design

### Single edit point: `evalGenExpr`

`internal/executor/operators_generated.go::evalGenExpr` gains one new case:

```go
case *parser.FuncCall:
    return evalGenFuncCall(x)
```

`evalGenFuncCall` is a new helper local to this file:

```go
func evalGenFuncCall(x *parser.FuncCall) (Datum, error) {
    if len(x.Args) != 0 || x.Star {
        return NullDatum, nil
    }
    name := strings.ToLower(x.Name.Name)
    if x.Name.Schema == "pg_catalog" || x.Name.Schema == "" {
        // OK
    } else {
        return NullDatum, nil
    }
    now := time.Now().UTC()
    switch name {
    case "now", "current_timestamp",
        "transaction_timestamp", "statement_timestamp":
        return NewTimeDatum(now), nil
    case "current_date":
        return NewTimeDatum(time.Date(now.Year(), now.Month(), now.Day(),
            0, 0, 0, 0, time.UTC)), nil
    }
    return NullDatum, nil
}
```

Rationale for each choice:

- **`time.Now().UTC()` instead of plumbed `ctx.Now`.** `evalGenExpr` runs
  in two callers (apply worker + dispatcher `insertOp`) and changing the
  signature would touch every caller plus the GENERATED-ALWAYS path
  (M0096-0008). The wall-clock skew between calls within a single
  multi-row INSERT is bounded by microseconds on commodity hardware,
  which is well below the precision any real fixture asserts on
  (CREATE TABLE-time DEFAULT timestamps are universally bucketed by
  the second or coarser in practice). Upstream PG fires
  `statement_timestamp` once per statement; this is a documented
  acceptable divergence for the DEFAULT-eval slow path.
- **`len(x.Args) != 0 || x.Star` short-circuit.** Zero-arg only. Calls
  with args (e.g. `current_time(3)`, `to_timestamp(...)`) fall through
  to `NullDatum`. Sequences are explicitly out of scope and have args,
  so they get the same NULL-leave behavior — no regression.
- **pg_catalog. and unqualified accepted; other schemas rejected.**
  Mirrors `evalFuncCall`'s prefix stripping (`internal/executor/expr.go`).
  An imaginary `myschema.now()` should not silently grab the wall clock.
- **`current_user` / `session_user` / `user` left unhandled.** They
  resolve to the current role string, which the catalog DEFAULT path
  doesn't expose cleanly. No fixture demands them yet.

### Order of operations unchanged

Inside `insertOp.Next` (rung 14) and `applyworker.applyInsert` (rung 13),
the call sequence stays:

```
source-fill → applyDefaultsForMissing → SERIAL nextval → BEFORE INSERT
triggers → CHECK → FK → computeGeneratedColumns → heap write + indexes
```

`applyDefaultsForMissing` calls `evalGenExpr` only for slots flagged
`missing[i]=true`, so explicit values still win.

### What does NOT change

- Parser: no AST changes. The parser already emits `*parser.FuncCall`
  for `CURRENT_TIMESTAMP` (no-paren, via `isNoParenFuncName` in
  `internal/parser/select.go`) and for `now()` (paren'd niladic call).
  Existing test `TestParseInsertCurrentTimestamp` in `internal/parser/`
  pins the parser shape.
- Planner: `rewriteInsertDefaultMarkers` and `rewriteUpdateDefaultMarkers`
  substitute the catalog `DefaultExpr` verbatim — a `*parser.FuncCall`
  node flows through unchanged.
- Analyzer / executor's `planner.FuncCall` path: untouched. This rung
  only adds a parser-AST evaluator for the catalog DEFAULT slow path.
- `computeGeneratedColumns`: still uses `evalGenExpr`, so STORED generated
  columns also gain `now()` / `current_timestamp` support for free.
  Generated columns must be deterministic in upstream PG, but a STORED
  expression evaluated at INSERT time will only fire once per row, which
  matches the no-rebuild guarantee. No test pin is added for this side
  effect — it's a free upgrade, not a load-bearing claim.

## Tests

Two pin tests in `internal/executor/storage_test.go`:

### `TestInsertFillsMissingColumnDefaultCurrentTimestamp`

Builds a table `(id int NOT NULL, created_at timestamptz DEFAULT
current_timestamp)` with `DefaultExpr` set to a `*parser.FuncCall` whose
`Name.Name == "current_timestamp"`. Runs INSERT with `ColumnIndex=[0]`,
captures wall-clock immediately before and after the `Build`+`Open`+`Next`
sequence, then asserts the persisted `created_at` slot is non-NULL,
`Kind==KindTime`, and its `TimeValue()` falls in `[before, after]`. The
bounded-skew window guards both correctness (the slot didn't get the
clock-time of `init()` or some fixed sentinel) and order (the helper ran
before the heap write, not after).

### `TestInsertFillsMissingColumnDefaultCurrentDate`

Same shape with `DEFAULT current_date`. Asserts the persisted slot is
KindTime, hours/minutes/seconds are all zero (date-truncated), and the
year/month/day equal `time.Now().UTC()`'s. The midnight-truncation pin
catches an accidental fallthrough to the `current_timestamp` arm.

Both tests use the existing `newStorageFixture` helper, mirroring the
rung-14 pin test `TestInsertFillsMissingColumnDefault`.

A negative-pin test is **not** added for `DEFAULT some_other_func()` —
the rung-13/14 helpers already leave unevaluable expressions alone
(silent passthrough) and that contract is pinned by
`TestApplyDefaultsForMissingFillsSlots` from rung 13.

## Verification

```
go test -count=1 -timeout 60s \
  -run "TestInsertFillsMissingColumnDefaultCurrentTimestamp|TestInsertFillsMissingColumnDefaultCurrentDate" \
  ./internal/executor/
```

Plus the rung-13/14 regression sweep:

```
go test -count=1 -timeout 60s \
  -run "TestApplyDefaultsForMissing|TestInsertFillsMissingColumnDefault|TestInsertDoesNotOverrideExplicitColumnDefault" \
  ./internal/executor/
```

Plus the broader sweep on the touched packages (parser/planner/analyzer/
executor/catalog) for the no-regression guarantee.

## Out of scope

- Sequence DEFAULTs (`nextval('seq_name')`) — separate rung; needs the
  sequence registry plumbed through the catalog `DefaultExpr` eval path.
- Function DEFAULTs with arguments — `to_timestamp(...)`,
  `make_date(...)`. Not surfaced by any current fixture.
- Role / session DEFAULTs (`current_user`, `session_user`, `user`,
  `current_schema`) — no plumbed session in the DEFAULT-eval slow path.
- DEFAULT-expression evaluation parity with the regular evaluator: a
  separate, larger refactor that would route the catalog `DefaultExpr`
  through `analyzer.Analyze` + the regular `evalExpr`. Tracked as a
  future rung; out of scope here.
