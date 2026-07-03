# 0119-0004 — FOREIGN SERVER GRANT (`pg_foreign_server.srvacl`) round-trip in pg_dump (DU-002 slice 427)

Status: implemented (2026-07-02)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/bin/pg_dump/pg_dump.c` (`getForeignServers`,
`dumpForeignServer`, `dumpACL`/`buildACLCommands`);
`postgres/src/backend/utils/adt/acl.c` (`acldefault`, `OBJECT_FOREIGN_SERVER`
case)

## Problem

`GRANT USAGE ON FOREIGN SERVER <name> TO <role>` was silently dropped from
every dump. Two independent gaps:

1. `tryRecordTableGrant`/`tryRecordTableRevoke` (`internal/server/grant_ddl.go`)
   classified the leading `foreign` keyword as a non-table object class and
   bailed — nothing was ever recorded in the catalog ACL store.
2. The `pg_foreign_server` virtual view (`internal/catalog/catalog.go`)
   hard-coded `srvacl` to the constant `""` (SQL NULL) for every server, so
   even if a grant had been recorded there was no projection wired to surface
   it.

pg_dump's `getForeignServers` reads `srvacl` and diffs it client-side (in
`buildACLCommands`) against `acldefault('S', srvowner)`. A foreign server's
world default is `ACL_NO_RIGHTS` (unlike FUNCTION/TYPE, PUBLIC gets nothing),
so `acldefault('S', 10)` = `{postgres=U/postgres}` (owner-only USAGE).

## Fix

Foreign servers share the OID-keyed ACL store with relations, schemas,
routines, and types (goopg mints foreign-server OIDs from the same `nextOID`
counter, so there is no collision) — this is the same object-type-agnostic
`relaclTextLockedFor` core used by slices 333/335/345.

1. **Catalog** (`internal/catalog/catalog.go`): `foreignServerACLPrivOrder`
   (`USAGE` → `'U'`, the sole foreign-server privilege) +
   `ownerForeignServerACLString = "U"` (owner-only default — no implicit
   PUBLIC entry, mirroring `ownerSchemaACLString`/`ownerTableACLString` rather
   than the dual owner+PUBLIC shape of `ownerFunctionACLString`/
   `ownerTypeACLString`) + `ForeignServerACLText(srvOID)` delegating to
   `relaclTextLockedFor`. `pg_foreign_server.VirtualRows` now projects
   `c.ForeignServerACLText(s.OID)` for `srvacl` instead of the hard-coded `""`.
   New `Catalog.ForeignServerOID(name)` interface method (the concrete
   `InMemory` method already existed from slice 377 for `pg_user_mappings`)
   lets the server-side GRANT recorder resolve a server name to its OID.
2. **Server** (`internal/server/grant_ddl.go`): `allForeignServerPrivileges =
   {"USAGE"}`; `tryRecordTableGrant`/`tryRecordTableRevoke` gain a
   `foreign` → `server` two-keyword branch (checked ahead of the
   `nonTableGrantObjects["foreign"]` bail) dispatching to new
   `recordForeignServerGrant`/`recordForeignServerRevoke` — mirrors
   `recordSchemaGrant`/`recordSchemaRevoke`: resolve each server via
   `Catalog.ForeignServerOID`, record/revoke via the existing
   `GrantTablePrivilegeWithGrantOption`/`RevokeTablePrivilege`/
   `MaterializeOwnerACL` primitives (all already object-type-agnostic).
   `GRANT … ON FOREIGN DATA WRAPPER` (`fdwacl`) is not modelled — it falls
   through to the `nonTableGrantObjects` bail unchanged.

USAGE is FOREIGN SERVER's only privilege, so a full grant's privilege set
always equals `ACL_ALL_RIGHTS_FOREIGN_SERVER` — `buildACLCommands` therefore
collapses the re-emitted grant to the `ALL` form, exactly like the
single-privilege FUNCTION/EXECUTE case (slice 345):
`GRANT ALL ON FOREIGN SERVER goopg_srv TO srv_grantee;`, not
`GRANT USAGE ON FOREIGN SERVER …`.

Dump-fidelity only — goopg does not enforce foreign-server USAGE privileges
at connect/query time. Zero blast radius on every other object class (only
adds a new `foreign server` branch ahead of the existing bail).

## Tests

- `internal/catalog/relacl_test.go`: `TestForeignServerACLText` — NULL with no
  grants; `GRANT USAGE` materializes `{postgres=U/postgres,srv_role=U/postgres}`;
  grant-option renders `U*`; `GRANT … TO PUBLIC` materializes the empty
  grantee; owner-side `REVOKE ALL` empties to `{}` (not NULL).
- `internal/testport/pgdump_connsetup_test.go`: `TestPort_PgDumpConnectionSetup`
  **DU-002 slice 427** — `CREATE ROLE srv_grantee` + `GRANT USAGE ON FOREIGN
  SERVER goopg_srv TO srv_grantee` (reuses `goopg_srv` from slice 376) asserts
  the exact `GRANT ALL ON FOREIGN SERVER goopg_srv TO srv_grantee;` line;
  byte-identical vs real pg_dump 18.3, driving the real pg_dump binary against
  a live goopg server.

## Gates

- `go build ./...` clean.
- `go test ./internal/catalog/...` (new `TestForeignServerACLText` +
  existing relacl/nspacl suites) PASS.
- `go test ./internal/server/...` PASS.
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS
  (byte-identical vs real pg_dump 18.3).
- pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, heap re-sync) / database (`datacl`,
`--create`-only) GRANT projection; extended-protocol commit-time deferral (see
M0119-0004-ACLHEAP, already tracked in the deferral ledger).
`GRANT … ON FOREIGN DATA WRAPPER` (`fdwacl`) round-trip landed separately —
see `0119-0004-foreign-data-wrapper-grant-fdwacl-pgdump.md` (DU-002 slice 428).
