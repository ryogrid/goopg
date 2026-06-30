(idle — nothing in flight)

Loop #20 COMPLETE: M0119-0004 DU-002 slice 381 — `CREATE SERVER … TYPE 'x'
VERSION 'y'` round-trip THROUGH pg_dump.

Slice 376 made a foreign server dumpable but hard-coded srvtype/srvversion to
NULL, dropping the TYPE/VERSION clauses. pg_dump's getForeignServers selects
srvtype/srvversion directly and dumpForeignServer re-emits ` TYPE 'x' VERSION
'y'` (string literals via appendStringLiteralAH, client-side) between the server
name and ` FOREIGN DATA WRAPPER`, ahead of OPTIONS. Clause emitted only when the
column is non-empty.

Fix (4 layers, mirrors slice 378/380):
- AST (ast.go): CompatNoopStmt gains ServerType / ServerVersion string.
- parser (ddl.go): CREATE SERVER scan loop detects TYPE/VERSION (ident-or-kw,
  case-insensitive) each followed by a string literal → ns.ServerType/Version.
- catalog (catalog.go): ForeignServer.Type/Version; RegisterForeignServer sig now
  (name,fdw,srvType,srvVersion,options) (idempotent refresh only when non-empty);
  pg_foreign_server VirtualRows render s.Type/s.Version (was hard-coded "").
- executor (operators_ddl.go): server arm threads s.ServerType/ServerVersion.

Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/parser/op_compat_test.go (new TestParseCreateServerTypeVersion),
internal/catalog/catalog.go, internal/catalog/fdw_registry_test.go (tv_srv
assert + 6 caller sig fixes), internal/executor/operators_ddl.go,
internal/testport/pgdump_connsetup_test.go (goopg_srv_tv fixture + slice-381
assert), docs/design/0110-0001-pg-dump-tap-port.md (Slice 381).

Gates: TestParseCreateServerTypeVersion + TestForeignServerRegistry + full
parser/catalog suites PASS; TestPort_PgDumpConnectionSetup PASS (5.3s). go build
./... clean, go vet testport clean. pgbench smoke = pre-commit hook. No TPC-H
(metadata-only virtual-catalog change). No deferral row (TYPE/VERSION is a
complete round-trip; escaping is client-side appendStringLiteralAH).

Next loop: fresh M0119-0004 pg_dump slice. FDW/SERVER/MAPPING TYPE/VERSION/OPTIONS
now COMPLETE. Candidates: ALTER SERVER … OPTIONS/VERSION; ALTER FOREIGN DATA
WRAPPER … OPTIONS; array-metachar quoting in optionsArrayLiteral (cross-cutting
w/ reloptions); pgQuoteIdent reserved-keyword fix (cross-cutting); range types;
aggregates; operators; text-search configs; CREATE COLLATION.
