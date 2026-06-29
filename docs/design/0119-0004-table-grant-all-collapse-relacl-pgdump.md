# DU-002 slice 351 — table `GRANT ALL` collapse (relacl) round-trip in pg_dump

Status: landed. Part of M0110-0001 / M0119-0004 (DU-002 pg_dump round-trip
slices). Test-only — NO engine change.

## What

A table-level `GRANT ALL` to a grantee must round-trip through pg_dump as the
single `ALL` keyword, not an explicit eight-way privilege list:

```sql
CREATE TABLE public.grantall_t(id int);
CREATE ROLE grantall_role;
GRANT ALL ON TABLE public.grantall_t TO grantall_role;
```

PostgreSQL 18.3 materializes `pg_class.relacl` as:

```
{postgres=arwdDxtm/postgres,grantall_role=arwdDxtm/postgres}
```

and `pg_dump --no-sync` emits exactly:

```sql
GRANT ALL ON TABLE public.grantall_t TO grantall_role;
```

(captured from real PG 18.3 in `./postgres/local_install`).

## Why it already works

This is the table analogue of the function `GRANT ALL` collapse (slice 345,
`proacl` → `GRANT ALL ON FUNCTION`) and the sequence collapse (slice 333). No
engine change was required:

- `tryRecordTableGrant` → `parseGrantPrivileges` expands `ALL` to
  `allTablePrivileges` (the eight table privileges
  SELECT/INSERT/UPDATE/DELETE/TRUNCATE/REFERENCES/TRIGGER/MAINTAIN) and records
  each via `GrantTablePrivilegeWithGrantOption`.
- `relaclTextLockedFor` → `renderACLLetters` projects that full set in
  `tableACLPrivOrder` to `"arwdDxtm"`, matching `acldefault('r', 10)`'s
  all-rights string.
- pg_dump's client-side `buildACLCommands` (`src/bin/pg_dump/dumputils.c`) diffs
  the grantee's privilege set against `ACL_ALL_RIGHTS_RELATION`; an exact match
  renders the `ALL` keyword.

The slice therefore adds only a fixture + assertion to
`TestPort_PgDumpConnectionSetup`, completing GRANT ALL coverage for the
most-used object class and guarding against a regression that would drop a
privilege bit (which would make pg_dump emit an explicit survivor list instead
of `ALL`). A negative assertion guards the `GRANT INSERT, SELECT … ON TABLE
public.grantall_t` explicit-list form.

## Tests

- `internal/testport/pgdump_connsetup_test.go` —
  `TestPort_PgDumpConnectionSetup` (slice 351 fixture + positive/negative
  assertions). PASS.

## Gates

- `go build ./...` clean.
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
- pgbench CI-parity smoke (pre-commit hook).
