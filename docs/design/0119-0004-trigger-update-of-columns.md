# 0119-0004 — column-specific `UPDATE OF col1, col2` trigger round-trip in pg_dump (DU-002 slice 326)

Status: accepted

## Problem

A PostgreSQL `UPDATE` trigger may be restricted to a column list:

```sql
CREATE TRIGGER t BEFORE UPDATE OF a, b ON tbl
    FOR EACH ROW EXECUTE FUNCTION f();
```

pg_dump's `getTriggers` selects `pg_get_triggerdef(t.oid, false)` and emits the
result verbatim. `pg_get_triggerdef_worker` (ruleutils.c) appends ` OF <cols>`
immediately after the `UPDATE` event, reading the column attnums from
`pg_trigger.tgattr` and rendering each via `quote_identifier`.

Slice 319 made the basic `CREATE TRIGGER` form round-trip but explicitly skipped
`UPDATE OF`: the parser (`parseCreateTriggerTail`) consumed only the bare
`UPDATE` keyword, so an `UPDATE OF` clause tripped the event loop (the `OF`
keyword was mistaken for the start of the `ON <table>` clause and the parse
failed) — and even had it parsed, `buildTriggerDefString` had no column list to
emit and `pg_trigger.tgattr` was hard-coded empty. A column-specific trigger was
therefore unrestorable. This slice closes that gap.

goopg fires triggers but does not implement the column-list firing restriction;
this is **dump fidelity only** — the captured column list has no runtime effect
on when the trigger fires.

## Change

- **Parser** (`internal/parser/ast.go`, `ddl.go`): new
  `CreateTriggerStmt.UpdateColumns []string`. In `parseCreateTriggerTail` the
  `UPDATE` event arm now accepts an optional `OF col1, col2, …` list (the `OF`
  keyword already exists — it is consumed for `INSTEAD OF`), appending each
  identifier to `UpdateColumns`. The list is only valid after `UPDATE`; every
  other form leaves it empty.

- **Catalog** (`internal/catalog/catalog.go`): new
  `Trigger.UpdateColumns []string`. The `pg_trigger` `VirtualRows` projection now
  fills `tgattr` (column 14) via the new `triggerUpdateColAttrs(tbl, cols)`
  helper, which renders the columns' 1-based attnums as a space-separated
  int2vector (the same text form `pg_index.indkey` uses), or `""` for any
  non-column-specific trigger.

- **Executor** (`internal/executor/operators_ddl.go`): `execCreateTrigger` copies
  `s.UpdateColumns` into the `catalog.Trigger`.

- **Deparser** (`internal/executor/expr.go`): `buildTriggerDefString` emits
  ` OF c1, c2` right after the `UPDATE` event when `trig.UpdateColumns` is
  non-empty, quoting each column via `pgQuoteIdent` (PG's `quote_identifier`
  analog). The clause attaches to the `UPDATE` event even inside an OR-ed event
  list, which keeps PG's fixed `INSERT, DELETE, UPDATE, TRUNCATE` order.

## Why dump-fidelity only

goopg's `fireTriggers` runs every row-level UPDATE trigger regardless of which
columns changed; it does not consult `tgattr`. The column list exists solely so
a goopg dump faithfully reproduces a column-specific trigger and can restore it
into either goopg or real PostgreSQL.

## Blast radius

`CreateTriggerStmt.UpdateColumns` / `Trigger.UpdateColumns` default empty, so
every non-column-specific trigger deparse and every `tgattr` projection is
byte-identical to slice 319. The parser branch only fires on the `OF` token
following `UPDATE`; the bare `UPDATE` form is unchanged. TPC-H / pgbench carry no
triggers → zero blast radius.

## Oracle

- `src/backend/utils/adt/ruleutils.c` `pg_get_triggerdef_worker` (the
  `TRIGGER_FOR_UPDATE` ` OF ` block, `quote_identifier` per column).
- `src/bin/pg_dump/pg_dump.c` `getTriggers` / `dumpTrigger` (verbatim emit).
- `src/include/catalog/pg_trigger.h` (`tgattr` int2vector of column attnums).

## Tests / gates

- `internal/parser` `TestParseCreateTriggerUpdateOf` — single / multiple
  columns, combined `INSERT OR UPDATE OF …`, and bare `UPDATE` (no list)
  round-trip into `UpdateColumns`.
- `internal/executor` `TestBuildTriggerDefString` — two new cases: `BEFORE
  UPDATE OF a, b` and `AFTER INSERT OR UPDATE OF "Mixed"` (the mixed-case column
  is double-quoted; the OF clause attaches to UPDATE within the OR-ed list).
- `internal/testport` `TestPort_PgDumpConnectionSetup` **DU-002 slice 326** —
  `public.trig_t` carries `trg_uof BEFORE INSERT OR UPDATE OF a, b`; the dump
  re-emits `CREATE TRIGGER trg_uof BEFORE INSERT OR UPDATE OF a, b ON
  public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn();`, byte-identical
  vs real pg_dump 18.3.
- `parser` / `catalog` / full `executor` suites PASS; `go build ./...` clean;
  pgbench smoke = pre-commit hook.

## Limitations / still open under M0119-0004

- Richer trigger forms still unmodelled: `WHEN (condition)` (`tgqual`),
  `REFERENCING … OLD/NEW TABLE` (`tgoldtable`/`tgnewtable`), and `CONSTRAINT`
  triggers (deferral columns).
- GRANT/ACL (`relacl`) and named-role policies remain blocked on a per-role OID
  registry **and** the `ARRAY(SELECT …)` / `array_to_string` / `quote_ident`
  query stack goopg does not yet implement.
- Extended-protocol commit-time deferral.
