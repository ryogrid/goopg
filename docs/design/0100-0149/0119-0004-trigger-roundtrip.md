# CREATE TRIGGER round-trip in pg_dump (DU-002 slice 319)

- **Milestone/Spec:** M0119-0004 (pg_dump 002–010 TAP / DU-002 catalog-view parity battery)
- **Status:** accepted
- **Loop:** #41

## Problem

goopg supports user triggers in the executor (`catalog.Table.Triggers`,
`fireTriggers`) but they were **invisible to pg_dump**, so a `CREATE TRIGGER`
was silently lost on dump/restore. Three independent gaps caused this:

1. `pg_class.relhastriggers` was **hardcoded `'f'`** in the virtual pg_class
   builder (`pg_class.VirtualRows`, the one pg_dump reads — see the
   "pg_class is virtual" note). pg_dump's `getTriggers` (pg_dump.c:8523) only
   adds a table's OID to the `tbloids` array it probes when
   `tbinfo->hastriggers` is true, so a table reading back `relhastriggers='f'`
   is **never queried** against `pg_trigger`.
2. `pg_catalog.pg_trigger.VirtualRows` returned **`nil`** (always zero rows),
   reflecting an earlier "goopg has no user triggers" assumption.
3. `pg_get_triggerdef(oid, pretty)` was **registered in `pg_proc` but
   unimplemented** in the executor's function dispatch — it returned NULL, and
   pg_dump emits `pg_get_triggerdef(t.oid, false)` verbatim.

## Fix

- **`catalog.Trigger.OID`** (new field) — assigned from the catalog OID counter
  (`AllocOID()`) at `CREATE TRIGGER` time (`execCreateTrigger`,
  operators_ddl.go). A zero OID (predates tracking) stays invisible to pg_dump.
- **`pg_class.relhastriggers`** — the virtual pg_class builder
  (`pg_class.VirtualRows`, catalog.go) now projects `'t'` when
  `len(t.Triggers) > 0`.
- **`pg_trigger.VirtualRows`** — projects one row per table trigger: the
  trigger oid/tgrelid/tgname, the PG `tgtype` bitmask (ROW=1, BEFORE=2,
  INSERT=4, DELETE=8, UPDATE=16, TRUNCATE=32, INSTEAD=64; AFTER = absence of
  BEFORE/INSTEAD), `tgfoid` resolved from the routine registry, `tgenabled='O'`,
  `tgisinternal='f'`, and `tgparentid=0`. The pg_dump self-JOIN on
  `tgparentid` finds no parent (0 ≠ any oid) so the LEFT JOIN keeps the row and
  the WHERE's first disjunct (`NOT tgisinternal AND tgparentid=0`) admits it.
- **`pg_get_triggerdef`** — new case in `evalFuncCall` (expr.go) scans all
  tables for the OID and calls `buildTriggerDefString(tbl, trig)`, which mirrors
  ruleutils.c `pg_get_triggerdef_worker` exactly:
  `CREATE TRIGGER <name> {BEFORE|AFTER|INSTEAD OF} <ev>[ OR <ev>…] ON
  <schema>.<table> FOR EACH {ROW|STATEMENT} EXECUTE FUNCTION
  <schema>.<func>(<'arg'>…)`. Events emit in PG's fixed order (INSERT, DELETE,
  UPDATE, TRUNCATE) regardless of declaration order; the table and function are
  schema-qualified because pg_dump runs with `search_path=''`; trigger arguments
  are single-quote-escaped literals.

## Scope / limitations

goopg's parser captures only the basic trigger form (`CreateTriggerStmt`): no
`WHEN`, `REFERENCING` transition tables, `UPDATE OF columns`, or `CONSTRAINT`
trigger. `buildTriggerDefString` therefore emits none of those clauses, and the
virtual row leaves `tgattr`/`tgqual`/`tgoldtable`/`tgnewtable` empty. This
matches what the parser can express; richer trigger forms are a future slice
once the parser/catalog carry the extra fields. Dump-fidelity only — no
executor trigger-firing behaviour changed.

## Blast radius

- `relhastriggers='t'` only for tables that actually own a trigger; no existing
  test asserts the column, and pgbench/TPC-H carry no triggers → `'f'`
  byte-unchanged for them.
- `catalog.Trigger.OID` defaults 0 for any trigger created before this slice.
- New builtin case is reached only for `pg_get_triggerdef`.

## Oracle

Mirrors `postgres/src/backend/utils/adt/ruleutils.c`
`pg_get_triggerdef_worker` and `postgres/src/bin/pg_dump/pg_dump.c`
`getTriggers`. Compared against real pg_dump 18.3.

## Gates

- `TestBuildTriggerDefString` (4 cases: BEFORE INSERT row-level; AFTER INSERT OR
  UPDATE statement-level with reordered events; all-four-events with quoted
  args; INSTEAD OF with default func schema) PASS.
- **DU-002 slice 319** in `TestPort_PgDumpConnectionSetup`: a BEFORE INSERT OR
  UPDATE FOR EACH ROW trigger and an AFTER DELETE FOR EACH STATEMENT trigger on
  `public.trig_t` (function `public.trig_fn()`) both re-emit their exact
  `CREATE TRIGGER … ;` statement, verified vs real pg_dump 18.3 (~5 s).
- `internal/catalog` + `internal/executor` suites PASS; `go build ./...` clean;
  pgbench smoke = pre-commit hook.

## Still open under M0119-0004

pg_dump 002–010 catalog parity (further slices); richer trigger forms (WHEN /
REFERENCING / UPDATE OF / CONSTRAINT); extended-protocol commit-time deferral.
