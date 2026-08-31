# 0119-0004 — FOREIGN DATA WRAPPER GRANT (`pg_foreign_data_wrapper.fdwacl`) round-trip in pg_dump (DU-002 slice 428)

Status: implemented (2026-07-02)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/bin/pg_dump/pg_dump.c` (`getForeignDataWrappers`,
`dumpForeignDataWrapper`, `dumpACL`/`buildACLCommands`);
`postgres/src/backend/utils/adt/acl.c` (`acldefault`, `OBJECT_FDW` case)

## Problem

`GRANT USAGE ON FOREIGN DATA WRAPPER <name> TO <role>` was silently dropped
from every dump — the exact same shape of gap as the just-landed FOREIGN
SERVER GRANT slice (427), one object class over:

1. `tryRecordTableGrant`/`tryRecordTableRevoke` (`internal/server/grant_ddl.go`)
   classified the leading `foreign` keyword as a non-table object class and
   bailed before ever inspecting whether the following words were `server` or
   `data wrapper` — nothing was ever recorded in the catalog ACL store.
2. The `pg_foreign_data_wrapper` virtual view (`internal/catalog/catalog.go`)
   hard-coded `fdwacl` to the constant `""` (SQL NULL) for every FDW, so even
   if a grant had been recorded there was no projection wired to surface it.

pg_dump's `getForeignDataWrappers` reads `fdwacl` and diffs it client-side (in
`buildACLCommands`) against `acldefault('F', fdwowner)`. Per
`postgres/src/backend/utils/adt/acl.c`'s `acldefault` switch, `OBJECT_FDW` sits
right above `OBJECT_FOREIGN_SERVER` with an identical shape: world default
`ACL_NO_RIGHTS`, owner default `ACL_ALL_RIGHTS_FDW` (== `ACL_USAGE`, the FDW's
sole privilege, per `postgres/src/include/utils/acl.h`). So
`acldefault('F', 10)` = `{postgres=U/postgres}` (owner-only USAGE), byte-for-byte
the same as the foreign-server case.

## Fix

FDWs share the OID-keyed ACL store with relations, schemas, routines, types,
and foreign servers (goopg mints FDW OIDs from the same `nextOID` counter, so
there is no collision) — the same object-type-agnostic `relaclTextLockedFor`
core used by slices 333/335/345/427.

1. **Catalog** (`internal/catalog/catalog.go`): `foreignDataWrapperACLPrivOrder`
   (`USAGE` → `'U'`, the sole FDW privilege) + `ownerForeignDataWrapperACLString
   = "U"` (owner-only default — no implicit PUBLIC entry, mirroring
   `ownerForeignServerACLString`) + `ForeignDataWrapperACLText(fdwOID)`
   delegating to `relaclTextLockedFor`. `pg_foreign_data_wrapper.VirtualRows`
   now projects `c.ForeignDataWrapperACLText(f.OID)` for `fdwacl` instead of
   the hard-coded `""`. New `Catalog.ForeignDataWrapperOID(name)` interface
   method (the concrete `InMemory` method already existed from slice 375 for
   `pg_foreign_server.srvfdw` resolution) lets the server-side GRANT recorder
   resolve an FDW name to its OID.
2. **Server** (`internal/server/grant_ddl.go`): `allForeignDataWrapperPrivileges
   = {"USAGE"}`; `tryRecordTableGrant`/`tryRecordTableRevoke`'s existing
   `foreign` branch gains a second `data` → `wrapper` two-keyword sub-branch
   (alongside the slice-427 `server` sub-branch, both checked ahead of the
   `nonTableGrantObjects["foreign"]` bail) dispatching to new
   `recordForeignDataWrapperGrant`/`recordForeignDataWrapperRevoke` — mirrors
   `recordForeignServerGrant`/`recordForeignServerRevoke`: resolve each FDW via
   `Catalog.ForeignDataWrapperOID`, record/revoke via the existing
   `GrantTablePrivilegeWithGrantOption`/`RevokeTablePrivilege`/
   `MaterializeOwnerACL` primitives (all already object-type-agnostic).

USAGE is FOREIGN DATA WRAPPER's only privilege, so a full grant's privilege
set always equals `ACL_ALL_RIGHTS_FDW` — `buildACLCommands` therefore
collapses the re-emitted grant to the `ALL` form, exactly like the
single-privilege FUNCTION/EXECUTE (slice 345) and FOREIGN SERVER (slice 427)
cases: `GRANT ALL ON FOREIGN DATA WRAPPER goopg_fdw TO fdw_grantee;`, not
`GRANT USAGE ON FOREIGN DATA WRAPPER …`.

Dump-fidelity only — goopg does not enforce FDW USAGE privileges at
connect/query time. Zero blast radius on every other object class (only adds
a new `data wrapper` sub-branch alongside the existing `server` sub-branch
under `foreign`).

## Tests

- `internal/catalog/relacl_test.go`: `TestForeignDataWrapperACLText` — NULL
  with no grants; `GRANT USAGE` materializes
  `{postgres=U/postgres,fdw_role=U/postgres}`; grant-option renders `U*`;
  `GRANT … TO PUBLIC` materializes the empty grantee; owner-side `REVOKE ALL`
  empties to `{}` (not NULL).
- `internal/testport/pgdump_connsetup_test.go`: `TestPort_PgDumpConnectionSetup`
  **DU-002 slice 428** — `CREATE ROLE fdw_grantee` + `GRANT USAGE ON FOREIGN
  DATA WRAPPER goopg_fdw TO fdw_grantee` (reuses `goopg_fdw` from slice 375)
  asserts the exact `GRANT ALL ON FOREIGN DATA WRAPPER goopg_fdw TO
  fdw_grantee;` line; byte-identical vs real pg_dump 18.3, driving the real
  pg_dump binary against a live goopg server.

## Gates

- `go build ./...` clean.
- `go test ./internal/catalog/...` (new `TestForeignDataWrapperACLText` +
  existing relacl/nspacl suites) PASS.
- `go test ./internal/server/...` PASS.
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS
  (byte-identical vs real pg_dump 18.3).
- pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, heap re-sync) / database (`datacl`,
`--create`-only) GRANT projection; extended-protocol commit-time deferral (see
M0119-0004-ACLHEAP, already tracked in the deferral ledger). No open scope
remains for foreign-server- or FDW-level GRANT round-trip — both are now
fully modelled.
