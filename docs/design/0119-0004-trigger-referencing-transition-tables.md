# 0119-0004ae — REFERENCING transition-table trigger round-trip in pg_dump (DU-002 slice 328)

Status: accepted

## Problem

A trigger declared with a `REFERENCING` clause —

```sql
CREATE TRIGGER t AFTER UPDATE ON tbl
    REFERENCING OLD TABLE AS ot NEW TABLE AS nt
    FOR EACH STATEMENT EXECUTE FUNCTION f();
```

— could not round-trip through pg_dump, and could not even be re-parsed.
pg_dump's `getTriggers` emits `pg_get_triggerdef(t.oid, false)` verbatim, and
`pg_get_triggerdef_worker` (ruleutils.c) reads `pg_trigger.tgoldtable` /
`tgnewtable` and renders

```
… ON <nsp>.<rel> REFERENCING OLD TABLE AS <ot> NEW TABLE AS <nt> FOR EACH …
```

— the `REFERENCING` clause sits between the ON-table name (after the
constraint-deferrability clause, if any) and `FOR EACH ROW`/`STATEMENT`, with
`OLD TABLE` always emitted before `NEW TABLE`. Either or both transition tables
may be present.

goopg had three gaps:

1. **Parser.** `parseCreateTriggerTail` had no `REFERENCING` branch — the
   `REFERENCING` token tripped the parser right after `ON <table>`, so the
   statement failed to parse outright.
2. **No transition-table state.** `CreateTriggerStmt` / `catalog.Trigger` had no
   fields for the OLD/NEW transition relation names.
3. **No deparse / projection.** `buildTriggerDefString` never emitted the
   `REFERENCING` clause, and `pg_trigger.tgoldtable` / `tgnewtable` were
   hard-coded empty.

## Fix (dump-fidelity only)

goopg does **not** materialise transition tables for trigger execution; this
slice reproduces the dump text only.

- **Parser** (`internal/parser/ast.go`, `ddl.go`): new
  `CreateTriggerStmt.{OldTransitionTable,NewTransitionTable}`.
  `parseCreateTriggerTail` parses an optional `REFERENCING { OLD | NEW } TABLE
  [AS] <name> [ … ]` clause after the deferrability clause and before
  `FOR EACH`. Either or both `OLD TABLE` / `NEW TABLE` clauses may appear, in
  any order, with an optional `AS` keyword; a following `OLD`/`NEW` ident (no
  separator) continues the loop. `OLD`/`NEW`/`REFERENCING` are matched as
  case-insensitive identifiers (none is a reserved keyword token); `TABLE` and
  `AS` use the existing `KwTable`/`KwAs` tokens.
- **Catalog** (`internal/catalog/catalog.go`): new
  `Trigger.{OldTransitionTable,NewTransitionTable}`. The `pg_trigger` virtual
  builder projects them into `tgoldtable` (row 17) / `tgnewtable` (row 18),
  previously hard-coded empty.
- **Executor** (`internal/executor/operators_ddl.go`): `execCreateTrigger`
  copies the two names onto the `catalog.Trigger`.
- **Deparse** (`internal/executor/expr.go`): `buildTriggerDefString` emits
  `REFERENCING OLD TABLE AS <ot> NEW TABLE AS <nt> ` (OLD first, each name via
  `pgQuoteIdent`) between the deferrability clause and `FOR EACH`, mirroring
  `pg_get_triggerdef_worker`.

## Blast radius

Nil for triggers without a `REFERENCING` clause: both transition-table fields
default empty, so the deparse and the `pg_trigger` projection are byte-identical
to slice 327 for every existing trigger. The new parser branch only fires on the
`REFERENCING` token, which previously caused a parse error. TPC-H/pgbench carry
no such triggers.

## Gates

- `TestParseCreateTriggerReferencing` (parser): NEW-only, OLD-only, both,
  reversed order, omitted `AS`, and a no-`REFERENCING` control.
- `TestBuildTriggerDefString` (executor): two new cases — `REFERENCING NEW TABLE
  AS nt` and `REFERENCING OLD TABLE AS ot NEW TABLE AS "New"` (double-quoted
  mixed-case name, OLD-before-NEW order).
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 328**: `trg_ref`
  (OLD+NEW) and `trg_refn` (NEW only) re-emit byte-identical vs real
  pg_dump 18.3.
- `internal/parser` + `internal/catalog` + `internal/executor` suites PASS;
  `go build ./...` clean; pgbench smoke via pre-commit hook.

## Still open under M0119-0004

`WHEN (condition)` triggers (`tgqual`) — the one remaining `pg_get_triggerdef`
gap — need an OLD/NEW-qualified expression deparser (`formatExprForAttrdef`
drops qualifiers, so `NEW.b` would render as bare `b`). GRANT/ACL (`relacl`) +
named-role policies (per-role OID registry + the `ARRAY(SELECT …)`/`quote_ident`
query stack goopg lacks); extended-protocol commit-time deferral.
