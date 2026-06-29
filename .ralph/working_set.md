(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 342** (full owner-side `REVOKE ALL ON
SCHEMA` → empty `nspacl` array `{}` round-trip in pg_dump). `REVOKE ALL ON SCHEMA
ownrevall_sch FROM postgres` strips the owner's implicit default schema privs
(USAGE, CREATE), leaving nspacl = `{}` (non-NULL empty array). pg_dump emits a
bare `REVOKE ALL ON SCHEMA … FROM postgres;` with NO re-GRANT. Fix is SERVER-ONLY
(catalog primitives already type-agnostic): `recordSchemaRevoke`
(internal/server/grant_ddl.go) now mirrors the table path — calls
`MaterializeOwnerACL(oid, "postgres", allSchemaPrivileges)` before the per-priv
`RevokeTablePrivilege` when the role is the owner; `REVOKE ALL` empties the
materialized owner entry → catalog records `{}` via the shared `relACLEmptied`
path (slice 341), rendered through `NamespaceACLText`. Gates:
TestRevokeAllFromSchemaOwnerEmptyArray (new) + slice-342
TestPort_PgDumpConnectionSetup (byte-identical vs pg_dump 18.3) + catalog suite
PASS; build clean. Committed + pushed.

NEXT M0119-0004 GRANT/ACL slices (pick one next loop):
- sequence owner REVOKE ALL: the last untyped owner-revoke branch. `REVOKE ALL ON
  SEQUENCE s FROM postgres` → relacl `{}`. tryRecordTableRevoke already has the
  isSequence flag + allSequencePrivileges; the owner-materialize call at line ~227
  ALREADY runs for sequences (it's in the shared table/sequence loop), so this may
  already work — verify with a fixture before assuming a code change is needed.
- owner-zero-coexisting-with-grantee: after `REVOKE ALL FROM owner` then `GRANT
  SELECT TO bob`, PG stores `{bob=r/postgres}` (owner absent = zero) and pg_dump
  emits BOTH `REVOKE ALL … FROM postgres;` AND the grantee GRANT. goopg's
  owner-default fallback renders `{postgres=...,bob=r/postgres}` and drops the
  owner REVOKE. Needs a persistent "owner explicitly zero" present-but-empty entry
  that coexists with grantees (deeper renderer change).
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one).
- database GRANT (`datacl`, only dumped under `--create`).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
