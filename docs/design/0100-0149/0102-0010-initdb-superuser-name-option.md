# M0102-0010: initdb `-U`/`--username` superuser-name option

**Status**: accepted
**Milestone**: M0102-0010

## Problem

`goopg init` hard-coded the bootstrap superuser role name to `"postgres"`
(`bootstrapPostgresRole` in `internal/initdb/initdb.go`). Upstream
`initdb` lets the operator choose the bootstrap superuser with
`-U`/`--username`; the upstream TAP test
`postgres/src/bin/initdb/t/001_initdb.pl` additionally asserts that a
reserved `pg_`-prefixed name is rejected
(`command_fails([ 'initdb', '--username' => 'pg_test', … ])`, mirroring
`initdb.c:3479`: *"superuser name … is disallowed; role names cannot
begin with \"pg_\""*).

This is the first concrete, fully-verifiable gap closed under the
otherwise-vague M0102-0010 ("15 initdb test failures") line: the goopg
`init` CLI accepted no initdb options beyond `-D`.

## Change

1. **`initdb.Options.SuperuserName`** (new field). Empty → defaults to
   `"postgres"`, so every existing caller (test harness, `cluster.Init`,
   replication E2E) is byte-for-byte unchanged.
2. **`Init`** resolves the name and validates the reserved `pg_` prefix
   *before* any filesystem layout (`ensureEmptyDir`/`MkdirAll`), so a
   rejected name leaves no partial data directory — matching upstream's
   early `pg_fatal`.
3. **`bootstrapPostgresRole(dataDir, superuser)`** now takes the name.
   The OID-10 `BOOTSTRAP_SUPERUSERID` row uses it; the OS-user row
   (OID 16384) is still seeded only when `$USER` differs from the chosen
   superuser (the dedup condition switched from the literal `"postgres"`
   to `superuser`). The 16 predefined `pg_*` roles are unaffected.
4. **`cmd/goopg` `runInit`** registers `-U` and `--username` (both bound
   to one variable; empty → initdb default) and threads the value into
   `Options.SuperuserName`.

No on-disk format changed: the pg_authid heap tuple layout, OIDs, and
index bootstraps are identical; only the `rolname` NameData of the
OID-10 row varies. The byte-layout regression
(`TestBootstrapPostgresRoleHeapRowRolnameByteLayout`) still passes with
the default name.

## Why not more initdb options this loop

`-U` is self-contained (one role name, no cross-subsystem coupling) and
fully testable without the upstream TAP harness. The remaining initdb
options enumerated by `001_initdb.pl` (`--encoding`, `--locale*`,
`--locale-provider`/`--icu-locale`, `--waldir`, `--data-checksums`,
`--allow-group-access`, `--auth*`/`--pwfile`, `--sync-only`,
`--no-sync`/`--sync-method`, `--set`/`--text-search-config`) each pull in
a distinct subsystem (encoding catalogs, ICU, WAL relocation, page
checksums, group-mode permissions, auth bootstrap) and are tracked as
remaining M0102-0010 work; see the fix-plan decomposition and the
deferral ledger.

## Tests

`internal/initdb/superuser_name_test.go`:

- `TestBootstrapPostgresRoleCustomSuperuser` — OID-10 row carries the
  custom name; no duplicate OS-user row when `$USER == superuser`.
- `TestBootstrapPostgresRoleDistinctOSUser` — OS-user row (OID 16384)
  still seeded when distinct.
- `TestInitRejectsReservedSuperuserName` — `pg_`-prefixed name rejected;
  no data dir created.
- `TestInitThreadsSuperuserToPgAuthid` — end-to-end: `Options.SuperuserName`
  reaches the on-disk `global/1260` pg_authid heap.

End-to-end CLI smoke (manual): `goopg init -D … -U alice` writes `alice`
as the sole superuser; `goopg init … --username pg_bad` exits 1 with the
PG-matching message and no data dir.

## Upstream reference

- `postgres/src/bin/initdb/initdb.c:3479` (reserved-prefix `pg_fatal`).
- `postgres/src/bin/initdb/t/001_initdb.pl` (`--username pg_test` rejection).
