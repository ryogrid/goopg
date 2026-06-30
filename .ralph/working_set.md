(idle — nothing in flight)

Loop #16 COMPLETE: M0119-0004 DU-002 slice 377 — `CREATE USER MAPPING FOR <user>
SERVER <srv>` round-trip THROUGH pg_dump + **exit-0 pipeline REPAIR**.

CRITICAL find: slice 376 (CREATE SERVER) silently regressed the whole
TestPort_PgDumpConnectionSetup — a dumpable foreign server makes pg_dump's
dumpForeignServer ALWAYS call dumpUserMappings, which queries `pg_user_mappings`
(a view goopg lacked) → pg_dump aborted (exit=1, empty dump). The test runs its
positive asserts ONLY inside `if res.ExitCode==0`, so every slice's round-trip
assertion (incl. 375/376) was being SKIPPED. Confirmed via -v: the "remaining
DU-002 catalog-parity gap exit=1 … relation pg_user_mappings does not exist" log.
This slice restores exit 0 AND round-trips the mapping.

Fix (mirrors slice 375/376 registry pattern):
- catalog: `pg_user_mappings` virtual relation (umid,srvid,srvname,umuser,usename,
  umoptions) over UserMapping{OID,UmUser,SrvName} registry + RegisterUserMapping/
  DropUserMapping/ListUserMappings + `ForeignServerOID` helper. srvid←server OID,
  umuser←RoleOID, PUBLIC→'public'/0, umoptions NULL→no OPTIONS clause.
- parser: CREATE/DROP USER MAPPING arms caught BEFORE generic `user` role/compat
  stubs (plain CREATE USER still errors → server-layer role DDL). scanUserMappingForServer.
- executor: execCompatNoop `user mapping`→RegisterUserMapping; DropCompatStmt arm.

Files: internal/catalog/catalog.go (field+view+struct+5 methods),
internal/catalog/fdw_registry_test.go (TestUserMappingRegistry),
internal/parser/ddl.go (CREATE+DROP arms + scanUserMappingForServer),
internal/parser/op_compat_test.go (TestParseCreateUserMapping),
internal/executor/operators_ddl.go (2 arms),
internal/testport/pgdump_connsetup_test.go (um_role/goopg_srv fixture + asserts),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 377), fix_plan, ledger.

Gates: TestUserMappingRegistry+TestForeignServer/FDWRegistry + TestParseCreateUserMapping
+ full parser/catalog/executor suites PASS; TestPort_PgDumpConnectionSetup now reaches
exit 0 (asserts re-armed). go build ./... + gofmt(my lines) clean. pgbench smoke = pre-commit.
No TPC-H (metadata-only virtual-catalog change).

Deferred (ledger): mapping OPTIONS discarded; in-memory only; user-spec kind not
distinguished (non-public non-registered → umuser=0); pg_user_mapping heap (1418) not populated.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: SERVER/MAPPING/FDW OPTIONS
rendering (text[] options array); range types (CREATE TYPE AS RANGE); aggregates;
operators; text-search configs; CREATE COLLATION (parser doesn't accept).
