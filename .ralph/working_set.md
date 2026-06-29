(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 347** (owner-side function REVOKE
`pg_proc.proacl` round-trip in pg_dump). The counterpart of slice 346: a
function's acldefault grants EXECUTE to BOTH owner and PUBLIC, so the single-role
revokes diverge — `REVOKE EXECUTE ON FUNCTION f FROM postgres` → `{=X/postgres}`
(PUBLIC survives), `… FROM PUBLIC` → `{postgres=X/postgres}` (slice 346), both →
`{}` (verified vs real PG 18.3). goopg previously emptied to `{}`/NULL on the
owner revoke (re-granting owner, dropping PUBLIC) because the recorder never
materialized PUBLIC and the renderer re-added the absent owner.

Fix: new catalog flag `relACLOwnerRevoked` (broader than `relACLEmptied`: owner's
implicit default explicitly revoked, even if grantees survive) set in
`RevokeTablePrivilege`, read in `relaclTextLockedFor` (suppress owner +
`{}` early-return) / `MaterializeOwnerACL` (no resurrect) / `GrantTablePrivilege`
(owner-GRANT clears) / `DropTableACL`. `recordFunctionRevoke` (grant_ddl.go) now
seeds owner+PUBLIC implicit EXECUTE only while proacl is NULL (via new
interface-exposed `Catalog.ProcACLText`). Also fixes a latent table bug
(REVOKE ALL FROM owner with a surviving grantee). Tests: `TestProcACLRevokeFromOwner`
(catalog) + slice-347 fixture/assert in `TestPort_PgDumpConnectionSetup`.
Gates: catalog+server+initdb+testport connsetup PASS; build clean; pgbench smoke
(pre-commit). Design 0119-0004-owner-side-function-revoke-proacl-pgdump.md.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one:
  pg_attribute is HEAP-backed, needs delete-old-rows + syncTableToCatalogHeap,
  which the server GRANT short-circuit cannot reach without executor routing).
- database GRANT (`datacl`, only dumped under `--create`; the harness runs
  pg_dump with `--no-sync` only, so untestable there as-is).
- `WITH GRANT OPTION` on functions (proacl `*` flag, currently dropped).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
