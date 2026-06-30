(idle — nothing in flight)

Loop #17 COMPLETE: M0119-0004 DU-002 slice 378 — `CREATE SERVER … OPTIONS
(name 'value', …)` round-trip THROUGH pg_dump.

Slice 376 dumped a server but discarded OPTIONS (srvoptions always NULL). This
slice round-trips them. pg_dump's getForeignServers expands srvoptions server-side
via `array_to_string(ARRAY(SELECT quote_ident(option_name)||' '||quote_literal(
option_value) FROM pg_options_to_table(srvoptions) ORDER BY option_name),
E',\n    '))` → dumpForeignServer emits ` OPTIONS (\n    %s\n)`. Options sort by
name (dbname before host). goopg's own pg_options_to_table SRF expands the
text[] literal `{name=value,…}` surfaced by pg_foreign_server.VirtualRows.

Fix (3 layers):
- parser (ddl.go): new scanFDWOptionsList consumes `OPTIONS ( name 'value', … )`
  → `name=value` elements; CREATE SERVER arm stores them in new CompatNoopStmt.Options.
- catalog (catalog.go): ForeignServer.Options []string; RegisterForeignServer
  signature now (name,fdw,options) (idempotent re-register refreshes only when
  non-empty); srvoptions cell renders text[] literal via new optionsArrayLiteral
  ("{"+join(",")+"}" — mirrors reloptions renderer).
- executor (operators_ddl.go): execCompatNoop server arm threads s.Options.

Files: internal/parser/ddl.go (CREATE SERVER arm + scanFDWOptionsList),
internal/parser/ast.go (CompatNoopStmt.Options), internal/parser/op_compat_test.go
(TestParseCreateServerOptions), internal/catalog/catalog.go (struct+register+
VirtualRows+optionsArrayLiteral), internal/catalog/fdw_registry_test.go (opt_srv +
3 caller sig fixes), internal/executor/operators_ddl.go (1 call site),
internal/testport/pgdump_connsetup_test.go (goopg_srv_opt fixture + assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 378), fix_plan, ledger.

Gates: TestParseCreateServerOptions + TestForeignServerRegistry +
TestUserMappingRegistry + full parser/catalog suites PASS; TestPort_PgDumpConnection
Setup PASS (byte-identical vs pg_dump 18.3) + NEGATIVE CONTROL confirmed assertion
live. go build ./... clean; gofmt of my lines clean (pre-existing 1.25/1.26 noise
left untouched). pgbench smoke = pre-commit hook. No TPC-H (metadata-only virtual-
catalog change).

Deferred (ledger): FDW/MAPPING OPTIONS still discarded; option values with array
metacharacters not quoted; ALTER SERVER OPTIONS not modelled; in-memory only.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: FDW/USER MAPPING OPTIONS
(reuse scanFDWOptionsList + optionsArrayLiteral on the FDW + mapping arms);
SERVER TYPE/VERSION; array-metachar quoting in optionsArrayLiteral; range types
(CREATE TYPE AS RANGE); aggregates; operators; text-search configs; CREATE COLLATION.
