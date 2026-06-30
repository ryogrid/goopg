(idle — nothing in flight)

Loop #15 COMPLETE: M0119-0004 DU-002 slice 376 — `CREATE SERVER <name> FOREIGN
DATA WRAPPER <fdw>` now round-trips through pg_dump (PRODUCTION fix).

Natural follow-on to slice 375 (FDW registry). goopg parsed CREATE SERVER into a
CompatNoopStmt (server name + FDW association already extracted by the parser)
but only registered a bare compat object; pg_foreign_server.VirtualRows was
hard-wired to 0 rows, so a created server vanished from the dump. Real pg_dump's
getForeignServers reads all pg_foreign_server rows; dumpForeignServer recovers
the wrapper name via `SELECT fdwname FROM pg_foreign_data_wrapper WHERE oid =
srvfdw` (single-row subquery) and emits `CREATE SERVER <n> FOREIGN DATA WRAPPER
<fdw>;` + `ALTER SERVER <n> OWNER TO postgres;`.

Fix (mirrors slice-375 FDW pattern):
- catalog: ForeignServer{Name,OID,Owner,FdwName} registry + RegisterForeignServer
  /DropForeignServer/ListForeignServers + ForeignDataWrapperOID helper;
  pg_foreign_server.VirtualRows resolves srvfdw to the FDW's stable OID,
  srvtype/srvversion/srvacl/srvoptions = NULL, owner = 10.
- executor: `server` CompatNoopStmt arm ALSO calls RegisterForeignServer (compat
  + fdw-server association kept for CASCADE); DROP SERVER + FDW-cascade call
  DropForeignServer.

Files: internal/catalog/catalog.go (field+VirtualRows+struct+methods),
internal/catalog/fdw_registry_test.go (TestForeignServerRegistry, appended),
internal/executor/operators_ddl.go (3 wiring sites),
internal/testport/pgdump_connsetup_test.go (goopg_srv fixture + asserts),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 376), ledger.

Gates: TestForeignServerRegistry + TestForeignDataWrapperRegistry +
TestPort_PgDumpConnectionSetup PASS; catalog/parser suites PASS;
go build ./... + gofmt(my lines) clean. pgbench smoke = pre-commit hook.
No TPC-H (metadata-only virtual-catalog change; servers absent from TPC-H).

Deferred (ledger): TYPE/VERSION/OPTIONS discarded by parser; servers in-memory
only; CREATE USER MAPPING still not dumped; non-registered-FDW reference → srvfdw=0.

Next loop: pick a fresh M0119-0004 pg_dump slice via empirical probe. Candidates
NOT yet done: CREATE USER MAPPING (dumpUserMappings, follow-on to this slice),
range types (CREATE TYPE AS RANGE — large), aggregates, operators, text-search
configs, CREATE COLLATION (parser doesn't accept).
