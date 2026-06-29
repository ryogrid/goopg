(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 346** (function-level REVOKE
`pg_proc.proacl` round-trip in pg_dump). The routine REVOKE analogue of the
table REVOKE slices (338+); the follow-up slice 345 deferred. `REVOKE EXECUTE
ON FUNCTION public.revokefn(integer) FROM PUBLIC` now materializes proacl =
`{postgres=X/postgres}` and pg_dump emits `REVOKE ALL ON FUNCTION
public.revokefn(integer) FROM PUBLIC;` (verified byte-identical vs real PG 18.3).
Server-only fix (catalog primitives from slices 340/345 reused): `tryRecordTableRevoke`
gains function/procedure/routine branches → `recordFunctionRevoke`
(MaterializeOwnerACL owner EXECUTE first, then RevokeTablePrivilege per role;
PUBLIC absent-entry no-op leaves owner-only). Tests: TestProcACLRevokeFromPublic
(catalog/relacl_test.go) + slice-346 fixture/assert in
TestPort_PgDumpConnectionSetup. Gates: catalog+server+initdb suites +
TestPort_PgDumpConnectionSetup PASS; build clean; pgbench smoke (pre-commit).

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- function/object REVOKE from a named grantee leaving surviving privs (sequence
  USAGE-style); or owner-side function REVOKE ALL → `{}` (generalized code
  already handles it — just needs a pinned slice if wanted).
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one:
  pg_attribute is HEAP-backed, needs delete-old-rows + syncTableToCatalogHeap,
  which the server GRANT short-circuit cannot reach without executor routing).
- database GRANT (`datacl`, only dumped under `--create`; the test harness runs
  pg_dump with `--no-sync` only, so untestable there as-is).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
