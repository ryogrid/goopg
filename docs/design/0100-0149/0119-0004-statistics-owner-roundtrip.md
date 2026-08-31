# 0119-0004 — Extended-statistics ownership round-trip in pg_dump (DU-002 slice 318)

## Context

Slices 314–317 made `CREATE STATISTICS` objects fully dumpable: the object
definition (`pg_get_statisticsobjdef`), `COMMENT ON STATISTICS`, expression
targets, and the per-object `SET STATISTICS n` target all round-trip through the
real `pg_dump` 18.3 binary. One archive-entry attribute remained unasserted:
**object ownership**.

pg_dump emits ownership not from the object's `createStmt` but from the TOC
archive entry. `dumpStatisticsExt` (pg_dump.c) builds the entry with
`.owner = statsextinfo->rolname`, where `rolname = getRoleName(stxowner)`. The
archiver's `_printTocEntry` (pg_backup_archiver.c) then renders
`ALTER STATISTICS <nsp>.<name> OWNER TO <role>;` because `"STATISTICS"` is in the
ALTER-able object list inside `_getObjectDescription`
(pg_backup_archiver.c:3799).

For goopg this exercises a goopg-specific surface that the earlier slices never
touched: the `pg_statistic_ext.stxowner` virtual-row cell. It is projected as the
constant `10` (the bootstrap superuser) in
`catalog.InMemory` (`catalog.go`, the `pg_statistic_ext` `VirtualRows` builder).
pg_dump's `getExtendedStatistics` selects `stxowner` and resolves it with
`getRoleName(10)`. If that cell ever regressed to NULL or a dangling OID,
`getRoleName` would fail to resolve a role and the `OWNER TO` line would silently
vanish (or pg_dump would abort) — a real, currently-unguarded risk.

## Change

Test-only slice: a regression guard in `TestPort_PgDumpConnectionSetup`
(`internal/testport/pgdump_connsetup_test.go`). After the slice 317 `SET
STATISTICS` assertions, it asserts that all four fixture statistics objects
(`statext_all`, `statext_nd`, `statext_expr`, `statext_mix`) emit
`ALTER STATISTICS public.<name> OWNER TO `. As with the existing
`ALTER TABLE public.foo OWNER TO` assertion, only the prefix is checked — the
role name is the same bootstrap superuser the table path already resolves, so
asserting the literal role name would couple the test to the initdb superuser
name without adding signal.

No production code changed: ownership already round-tripped correctly via the
existing `stxowner = 10` projection. The slice converts an untested-but-working
path into an asserted regression guard, consistent with slice 315 (COMMENT) and
slice 317 (SET STATISTICS).

## Oracle

- `postgres/src/bin/pg_dump/pg_dump.c` — `dumpStatisticsExt` (`.owner =
  statsextinfo->rolname`).
- `postgres/src/bin/pg_dump/pg_backup_archiver.c` — `_printTocEntry` /
  `_getObjectDescription` (`STATISTICS` in the ALTER-able list, line ~3799).

## Verification

- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` — PASS
  (4.3s) against real pg_dump 18.3.
- pgbench CI-parity smoke via the `.githooks/pre-commit` hook.
