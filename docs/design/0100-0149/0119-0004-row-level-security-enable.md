# ALTER TABLE … ENABLE / FORCE ROW LEVEL SECURITY round-trip in pg_dump (DU-002 slice 322)

Status: accepted
Milestone: M0119-0004 (pg_dump catalog parity, DU-002)

## Problem

`pg_dump` re-emits a table's row-level-security state from two `pg_class`
columns:

- `relrowsecurity` → `getPolicies` represents an RLS-enabled table with a
  null-`polname` `PolicyInfo`, which `dumpPolicy` renders as
  `ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;`
  (`postgres/src/bin/pg_dump/pg_dump.c` `getPolicies`/`dumpPolicy`).
- `relforcerowsecurity` → `dumpTableSchema` renders
  `ALTER TABLE ONLY <t> FORCE ROW LEVEL SECURITY;`
  (`pg_dump.c:17799`).

goopg hardcoded **both** columns to `'f'`/`false` in every `pg_class` row
builder, and the parser silently consumed `ALTER TABLE … ENABLE ROW LEVEL
SECURITY` through the trigger/rule no-op arm (it begins with the `enable`
keyword). So the RLS state was dropped from the dump and goopg could not
restore its own output.

goopg enforces **no** row-level security at runtime; this slice is pure
schema-dump fidelity so a round-trip preserves the flags.

## Change

### Parser (`internal/parser/ast.go`, `ddl.go`)

Four new `AlterTableActionKind`s — `AlterTableEnableRowSecurity`,
`AlterTableDisableRowSecurity`, `AlterTableForceRowSecurity`,
`AlterTableNoForceRowSecurity`. In `parseAlterTable` the
`{ENABLE|DISABLE} ROW LEVEL SECURITY` and `[NO] FORCE ROW LEVEL SECURITY`
clauses are detected (by case-insensitive token-value lookahead, so they match
whether the words lex as idents or keywords) **before** the existing
ENABLE/DISABLE TRIGGER|RULE no-op arm, and recorded as actions. ENABLE/DISABLE
TRIGGER still falls through to the no-op `EnableDisableTrigger` flag.

### Catalog (`internal/catalog/catalog.go`)

New `catalog.Table.RowSecurity` / `ForceRowSecurity` bool fields. The virtual
`pg_class` builder in `registerSystemTables` now projects them
(`boolToPGChar`, a new small helper) instead of the literal `"f"`.

### Executor (`internal/executor/operators_ddl.go`, `pg18_user_catalog_rows.go`)

The four actions set the corresponding flag on the live `catalog.Table` and —
mirroring the REPLICA IDENTITY path (slice 305) — flush the change through the
`pg_class` **heap** row pg_dump reads via the established delete-old-rows +
`syncTableToCatalogHeap` path (the live virtual `pg_class` reflects the field
immediately, but pg_dump reads the heap populated at CREATE TABLE).
`buildUserPGClassRow` now emits the two flags from the table fields.

## Blast radius

Both new catalog fields default false; every existing relation (TPC-H,
pgbench, all system catalogs) renders `relrowsecurity='f'`/
`relforcerowsecurity='f'` exactly as before. The dump output for any table
without RLS is byte-identical. No runtime enforcement is introduced, so query
execution is untouched.

## Tests / gates

- `internal/parser` `TestParseAlterTableRowSecurity` — the four clauses map to
  distinct actions; ENABLE TRIGGER still hits the no-op trigger arm.
- `internal/executor` `TestDDLAlterTableRowSecurityRoundTrip` — ENABLE/DISABLE
  toggles `RowSecurity`, FORCE/NO FORCE toggles `ForceRowSecurity`, the two are
  independent.
- `internal/testport` `TestPort_PgDumpConnectionSetup` **DU-002 slice 322** —
  a live goopg server + real `pg_dump 18.3` round-trip: `rls_t` (ENABLE +
  FORCE) dumps both `ALTER TABLE public.rls_t ENABLE ROW LEVEL SECURITY;` and
  `ALTER TABLE ONLY public.rls_t FORCE ROW LEVEL SECURITY;`.
- `go build ./...` clean; parser/catalog/executor suites PASS; pgbench smoke =
  pre-commit hook.

## Still open under M0119-0004

CREATE POLICY (`pg_policy`) — the per-policy RLS rules, the larger half of RLS
dumping; GRANT/ACL (`relacl`); CREATE RULE (`pg_rewrite`); extended-protocol
commit-time deferral.
