(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 343** (full owner-side `REVOKE ALL ON
SEQUENCE` → empty `relacl` array `{}` round-trip in pg_dump). The sequence
analogue of slice 341 (table) / 342 (schema). `REVOKE ALL ON SEQUENCE
ownrevall_seq FROM postgres` strips the owner's implicit default sequence privs
(USAGE, SELECT, UPDATE), leaving relacl = `{}` (non-NULL empty array). pg_dump
emits a bare `REVOKE ALL ON SEQUENCE … FROM postgres;` with NO re-GRANT (verified
byte-identical to real pg_dump 18.3 via local_install). This slice needed NO new
production code: `recordTableRevoke` (internal/server/grant_ddl.go) already passes
`allSequencePrivileges` to `MaterializeOwnerACL` for an owner-side ON SEQUENCE
revoke, and the empty-array (`relACLEmptied`) state + its `relaclTextLockedSeq`
rendering are object-type-agnostic. Pinned as regression guards:
TestRevokeAllFromSequenceOwnerEmptyArray (catalog/relacl_test.go) +
slice-343 fixture/assert in TestPort_PgDumpConnectionSetup. Gates: catalog suite +
TestPort_PgDumpConnectionSetup PASS; build clean. Committed + pushed.

NEXT M0119-0004 GRANT/ACL slices (pick one next loop):
- owner-zero-coexisting-with-grantee: after `REVOKE ALL FROM owner` then `GRANT
  SELECT TO bob`, PG stores `{bob=r/postgres}` (owner absent = zero) and pg_dump
  emits BOTH `REVOKE ALL … FROM postgres;` AND the grantee GRANT. goopg's
  owner-default fallback renders `{postgres=...,bob=r/postgres}` and drops the
  owner REVOKE. Needs a persistent "owner explicitly zero" present-but-empty entry
  that coexists with grantees (deeper renderer change). This is the next real
  code change (the three REVOKE-ALL-empty-array slices 341/342/343 are now done).
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one).
- database GRANT (`datacl`, only dumped under `--create`).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
