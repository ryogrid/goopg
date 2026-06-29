(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 341** (full owner-side `REVOKE ALL` →
empty `relacl` array `{}` round-trip in pg_dump). `REVOKE ALL ON TABLE
ownrevall_t FROM postgres` strips every owner default privilege, leaving
relacl = `{}` (non-NULL empty array, distinct from NULL); pg_dump emits a bare
`REVOKE ALL … FROM postgres;` with NO re-GRANT. Fix is catalog-only (server
recording unchanged — REVOKE ALL expands to every priv through the slice-340
owner path): new `relACLEmptied map[uint32]bool` set on the final removal of the
owner's own aclitem; `relaclTextLockedFor` returns `"{}"` for empty byRole when
the flag is set; `GrantTablePrivilege`/`DropTableACL` clear it; `MaterializeOwnerACL`
early-returns when set. Gates: TestRevokeAllFromOwnerEmptyArray (new) +
slice-341 TestPort_PgDumpConnectionSetup (byte-identical vs pg_dump 18.3) PASS;
catalog+server PASS; build clean. Committed + pushed.

NEXT M0119-0004 GRANT/ACL slices (pick one next loop):
- owner-zero-coexisting-with-grantee: after `REVOKE ALL FROM owner` then `GRANT
  SELECT TO bob`, PG stores `{bob=r/postgres}` (owner absent = zero) and pg_dump
  emits BOTH `REVOKE ALL … FROM postgres;` AND the grantee GRANT. goopg's
  owner-default fallback renders `{postgres=arwdDxtm/postgres,bob=r/postgres}` and
  drops the owner REVOKE. Needs a persistent "owner explicitly zero" present-but-
  empty entry that coexists with grantees (deeper renderer change).
- schema/sequence owner REVOKE ALL (relACLEmptied is type-agnostic; wire the
  recordSchemaRevoke owner path + the sequence branch).
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one).
- database GRANT (`datacl`, only dumped under `--create`).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
