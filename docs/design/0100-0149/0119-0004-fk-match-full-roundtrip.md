# 0119-0004l — `MATCH FULL` FOREIGN KEY round-trip in pg_dump (DU-002 slice 309)

**Milestone:** M0119-0004 (pg_dump 002–010 catalog-view parity battery; source M0110-0001)
**Status:** accepted / implemented
**Oracle:** PostgreSQL 18.3 (`./postgres/local_install`), `pg_dump --no-sync`

## Problem

A foreign key's *match type* governs how composite keys with NULL components are
treated. PG supports three (`pg_constraint.confmatchtype`):

| confmatchtype | SQL clause     | NULL semantics                                            |
|---------------|----------------|-----------------------------------------------------------|
| `s` (default) | (none / `MATCH SIMPLE`) | the row passes the FK check if *any* key column is NULL |
| `f`           | `MATCH FULL`   | the row passes only if *all* key columns are NULL, else all must be non-NULL and match |
| `p`           | `MATCH PARTIAL`| not implemented by PG (errors at constraint creation)     |

`MATCH FULL` is a real, restorable property: a restored FK that was `MATCH FULL`
on the source must remain `MATCH FULL`, or mixed-NULL composite keys that the
source rejected would silently be accepted.

PG carries the match type through `pg_get_constraintdef_worker` (ruleutils.c),
which emits the clause **between** the `REFERENCES <reltable>(<refcols>)` list
and the `ON UPDATE`/`ON DELETE` clauses:

```c
/* Add match type */
switch (conForm->confmatchtype) {
    case FKCONSTR_MATCH_FULL:    string = " MATCH FULL";    break;
    case FKCONSTR_MATCH_PARTIAL: string = " MATCH PARTIAL"; break;
    case FKCONSTR_MATCH_SIMPLE:  string = "";               break;
}
appendStringInfoString(&buf, string);
/* then ON UPDATE, then ON DELETE */
```

pg_dump's `getConstraints` renders each FK via `pg_get_constraintdef(oid)` and
emits `ALTER TABLE ONLY … ADD CONSTRAINT <name> <condef>;`, so the clause rides
along automatically — *if* the server reproduces it.

goopg did **not** surface the match type at all: `MATCH` was never part of the
FK grammar (so the clause would have parse-errored had a dump fed it back), the
catalog had no field for it, the `pg_constraint` builder hard-coded
`confmatchtype='s'`, and `buildForeignKeyDefString` never emitted the clause.
A `MATCH FULL` FK therefore silently degraded to `MATCH SIMPLE` on restore.

## Fix

Thread a single `MatchFull bool` end-to-end, mirroring the existing
`Deferrable` / `NotValid` plumbing. Five sites:

1. **Parser grammar** (`internal/parser/ddl.go`): new `parseFKMatchType` helper
   consumes an optional `MATCH FULL | PARTIAL | SIMPLE` clause between the
   referenced-column list and the `ON DELETE/UPDATE` clauses (PG `gram.y`
   `key_match` position). Wired into **all three** FK parse forms — inline
   column `REFERENCES`, table-level `FOREIGN KEY`, and `ALTER TABLE ADD …
   FOREIGN KEY` — so the grammar stays in lockstep. `MATCH SIMPLE`/`PARTIAL` are
   accepted but yield `false` (only `FULL` round-trips, matching PG, which never
   stores `p`).
2. **AST** (`internal/parser/ast.go`): `ColumnDef.FKMatchFull`,
   `TableForeignKeyDef.MatchFull`, `AlterTableAction.MatchFull`.
3. **Catalog** (`internal/catalog/catalog.go`): `ForeignKey.MatchFull`.
4. **Executor** (`internal/executor/operators_ddl.go`): all three
   `catalog.ForeignKey{…}` build sites set `MatchFull` from the parsed value.
5. **`pg_constraint` builder** (`internal/catalog/catalog.go`): project
   `confmatchtype = 'f'` when `fk.MatchFull`, else `'s'`.
6. **Deparse** (`internal/executor/expr.go`, `buildForeignKeyDefString`): append
   ` MATCH FULL` immediately after the `REFERENCES …(…)` list and **before** the
   `ON UPDATE`/`ON DELETE` clauses, matching the upstream byte order.

## Blast radius

A `MATCH SIMPLE` FK (`MatchFull=false`, the overwhelming majority) is
byte-unchanged in both `pg_constraint.confmatchtype` (`'s'`, as before) and the
def string. The new grammar only *adds* an accepted clause; no previously valid
statement changes meaning. No WAL/dump-format change. goopg does not yet
*enforce* FK matching (FKs are recorded but not checked, per
`0003-0004-hammerdb-tpch-integration.md`), so this is dump-fidelity only — the
restored FK carries the correct match type for the day enforcement lands.

## Verification

* New **DU-002 slice 309** in `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`): a composite
  `ALTER TABLE public.mf_child ADD CONSTRAINT mf_child_fk FOREIGN KEY (a, b)
  REFERENCES public.mf_ref (a, b) MATCH FULL`; the dump now emits the line
  **with** ` MATCH FULL;`, asserted as a substring of pg_dump's stdout.
* New unit test `TestForeignKeyMatchFullRoundTrip`
  (`internal/executor/operators_fk_constraintdef_test.go`): asserts
  `catalog.ForeignKey.MatchFull`, `confmatchtype` (`'f'` vs `'s'`), and the
  def-string clause for both a MATCH FULL and a MATCH SIMPLE control.
* `go test ./internal/executor/ ./internal/parser/ ./internal/catalog/` PASS;
  `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
* `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

* pg_dump 002–010 catalog-view parity battery (further slices).
* extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement).
