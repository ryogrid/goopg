# 0119-0004af — WHEN-condition trigger round-trip in pg_dump (DU-002 slice 329)

Status: accepted

## Problem

A trigger declared with a `WHEN (condition)` qualification —

```sql
CREATE TRIGGER t BEFORE UPDATE ON tbl
    FOR EACH ROW WHEN (NEW.b <> OLD.b)
    EXECUTE FUNCTION f();
```

— could not round-trip through pg_dump: the condition was silently dropped.
pg_dump's `getTriggers` emits `pg_get_triggerdef(t.oid, false)` verbatim, and
`pg_get_triggerdef_worker` (ruleutils.c) reads `pg_trigger.tgqual` and renders

```
… FOR EACH ROW WHEN (<condition>) EXECUTE FUNCTION …
```

— the `WHEN` clause sits between `FOR EACH ROW`/`STATEMENT` and `EXECUTE
FUNCTION`. To deparse the condition, PG builds two minimal range-table entries
aliased `old` and `new` over the trigger's relation and calls `get_rule_expr`
with `varprefix = true`, so every column reference renders with a lowercased
`old.` / `new.` qualifier. Because `pg_get_triggerdef` uses `prettyFlags = 0`,
`get_rule_expr` fully parenthesizes the boolean `OpExpr`, and the `WHEN (…)`
wrapper adds the outer pair — a comparison renders as `WHEN ((new.b <> old.b))`.

goopg had three gaps:

1. **Parser.** `parseCreateTriggerTail` *recognised* `WHEN` but threw the body
   away — it consumed the parenthesised tokens with a paren-balance loop and
   stored nothing, so the condition was lost before it ever reached the catalog.
2. **No `WhenExpr` state.** `CreateTriggerStmt` / `catalog.Trigger` had no field
   for the parsed condition.
3. **No deparse.** `buildTriggerDefString` never emitted a `WHEN` clause.

## Fix (dump-fidelity only)

goopg does **not** yet evaluate the `WHEN` condition at trigger-firing time; this
slice reproduces the dump text only.

- **Parser** (`internal/parser/ast.go`, `ddl.go`): new
  `CreateTriggerStmt.WhenExpr parser.Expr`. `parseCreateTriggerTail` now parses
  `WHEN '(' a_expr ')'` (PG grammar) into `WhenExpr` via the standard expression
  parser instead of discarding the tokens. The unquoted `NEW`/`OLD` qualifier is
  already lowercased onto each `*ColumnRef` by the lexer (`identText`), so
  `NEW.b` parses to `ColumnRef{Table:"new", Column:"b"}` — matching the alias
  names PG's deparser uses.
- **Catalog** (`internal/catalog/catalog.go`): new `Trigger.WhenExpr parser.Expr`,
  carried for dump fidelity. (The `pg_trigger.tgqual` projection stays empty: PG
  stores a serialized node tree there, which goopg does not synthesize, and
  pg_dump never reads `tgqual` directly — it drives entirely off
  `pg_get_triggerdef`, which goopg implements from the `catalog.Trigger` struct.)
- **Executor** (`internal/executor/operators_ddl.go`): `execCreateTrigger` copies
  `s.WhenExpr` onto the `catalog.Trigger`.
- **Deparse** (`internal/executor/expr.go`): `buildTriggerDefString` emits
  `WHEN (<condition>) ` between `FOR EACH` and `EXECUTE FUNCTION`, rendering the
  condition with the existing executor-side deparser `defaultExprToSQL`.

### Why `defaultExprToSQL` (not `formatExprForAttrdef`)

The catalog-side `formatExprForAttrdef` *drops* a `ColumnRef`'s qualifier (it
deparses column defaults / CHECK predicates, which are scoped to one relation and
so are always unqualified), so it would render `NEW.b` as a bare `b`. The
executor twin `defaultExprToSQL` already preserves the qualifier
(`v.Table + "." + v.Column`) and already fully parenthesizes binary `OpExpr`s
(`(left op right)`, DU-002 slice 298) — exactly PG's `prettyFlags = 0` behaviour.
Since the parser lowercases the unquoted `OLD`/`NEW` qualifier, the rendered
qualifier is already `new.`/`old.`, so reusing `defaultExprToSQL` reproduces PG's
`get_rule_expr(varprefix=true)` output for the common comparison forms with no new
deparser. A *quoted* qualifier or column needing `quote_identifier` is a known
edge this slice does not chase (trigger `WHEN` conditions in practice reference
the bare `NEW`/`OLD` keyword and simple column names).

## Blast radius

Nil for triggers without a `WHEN` clause: `WhenExpr` defaults nil, so the deparse
is byte-identical to slice 328 for every existing trigger. The parser change only
affects the `WHEN` branch, which previously discarded its body (it never produced
a parse error, but it never preserved the condition either). TPC-H/pgbench carry
no such triggers.

## Gates

- `TestParseCreateTriggerWhen` (parser): `WHEN (NEW.b <> OLD.b)` parses to a
  `*BinaryOp` over two `*ColumnRef`s with lowercased `new`/`old` qualifiers, and a
  trigger with no `WHEN` leaves `WhenExpr` nil.
- `TestBuildTriggerDefString` (executor): two new cases — `WHEN ((new.b <>
  old.b))` (NEW vs OLD, double-parenthesized OpExpr) and `WHEN (new.active)` (a
  bare NEW column, single paren from the wrapper only).
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 329**: `trg_when`
  (`WHEN ((new.b <> old.b))`) and `trg_whna` (`WHEN ((new.a > 0))`) re-emit
  byte-identical vs real pg_dump 18.3.
- `internal/parser` + `internal/catalog` + `internal/executor` suites PASS;
  `go build ./...` clean; pgbench smoke via pre-commit hook.

## Still open under M0119-0004

The `pg_get_triggerdef` getter battery is now complete (timing, OR-ed events,
`UPDATE OF` columns, `CREATE CONSTRAINT TRIGGER` + deferrability, `REFERENCING`
transition tables, and `WHEN`). Remaining DU-002 surfaces: actually *evaluating*
the `WHEN` condition at trigger-firing time (runtime, not dump); GRANT/ACL
(`relacl`) + named-role policies (per-role OID registry + the
`ARRAY(SELECT …)`/`quote_ident` query stack goopg lacks); extended-protocol
commit-time deferral.
