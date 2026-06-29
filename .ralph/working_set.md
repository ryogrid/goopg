(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 345** (function-level GRANT
`pg_proc.proacl` round-trip in pg_dump). The routine analogue of the
table/schema/sequence GRANT slices. `GRANT EXECUTE ON FUNCTION
public.grantfn(integer) TO func_grantee` now materializes proacl =
`{=X/postgres,postgres=X/postgres,func_grantee=X/postgres}` and pg_dump emits
`GRANT ALL ON FUNCTION public.grantfn(integer) TO func_grantee;`. pg_proc is
VIRTUAL → pure projection, no heap re-sync. Fix: catalog `functionACLPrivOrder`
+ `ProcACLText` (reuses `relaclTextLockedFor`); server `recordFunctionGrant`
(function/procedure/routine branches in `tryRecordTableGrant`, seeds implicit
PUBLIC EXECUTE, paren-aware `splitFunctionList`); `pg_proc_view.go` projects
`cat.ProcACLText(r.OID)`. Tests: TestProcACLText (catalog/relacl_test.go) +
slice-345 fixture/assert in TestPort_PgDumpConnectionSetup. Gates:
catalog+server+initdb suites + TestPort_PgDumpConnectionSetup PASS; build clean;
verified vs real PG 18.3 (postgres/local_install). Committed + pushed.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- function/object REVOKE (e.g. `REVOKE EXECUTE … FROM PUBLIC`) — the no-op path
  still swallows it; mirror the table REVOKE recorder for pg_proc proacl.
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one:
  pg_attribute is HEAP-backed, needs delete-old-rows + syncTableToCatalogHeap,
  which the server GRANT short-circuit cannot reach without executor routing).
- database GRANT (`datacl`, only dumped under `--create`; the test harness runs
  pg_dump with `--no-sync` only, so untestable there as-is).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
