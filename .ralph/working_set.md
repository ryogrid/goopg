(idle — nothing in flight)

Loop #19 COMPLETE: M0119-0004 DU-002 slice 380 — `CREATE FOREIGN DATA WRAPPER …
OPTIONS (name 'value', …)` round-trip THROUGH pg_dump.

Completes the FDW/SERVER/MAPPING OPTIONS trilogy (378/379/380). Slice 375 dumped
an FDW but discarded fdwoptions (always NULL). pg_dump's getForeignDataWrappers
expands fdwoptions server-side via the same array_to_string(ARRAY(SELECT
quote_ident(name)||' '||quote_literal(val) FROM pg_options_to_table(fdwoptions)
ORDER BY option_name), E',\n    ')) shape → CREATE FOREIGN DATA WRAPPER <name>
OPTIONS (\n    %s\n);. Options sorted by name (debug<delimiter).

Fix (3 layers, mirrors slice 378/379):
- parser (ddl.go): CREATE FOREIGN DATA WRAPPER arm now scans for OPTIONS token,
  consumes via shared scanFDWOptionsList → CompatNoopStmt.Options (HANDLER/
  VALIDATOR clauses still skipped).
- catalog (catalog.go): ForeignDataWrapper.Options []string;
  RegisterForeignDataWrapper signature now (name,options) (idempotent re-register
  refreshes only when non-empty); fdwoptions cell renders optionsArrayLiteral(f.Options).
- executor (operators_ddl.go): foreign-data wrapper arm threads s.Options.

Files: internal/parser/ddl.go, internal/parser/op_compat_test.go
(new TestParseCreateFDWOptions), internal/catalog/catalog.go,
internal/catalog/fdw_registry_test.go (fdwoptions assert + 6 caller sig fixes),
internal/executor/operators_ddl.go, internal/testport/pgdump_connsetup_test.go
(goopg_fdw_opt fixture + assert), docs/design/0110-0001-pg-dump-tap-port.md
(Slice 380), fix_plan, ledger.

Gates: TestParseCreateFDWOptions + TestForeignDataWrapperRegistry + full
parser/catalog suites PASS; TestPort_PgDumpConnectionSetup PASS (byte-identical
vs pg_dump 18.3) + NEGATIVE CONTROL confirmed (slice-380 assertion fired when
fdwoptions render broken). go build ./... clean. pgbench smoke = pre-commit hook.
No TPC-H (metadata-only virtual-catalog change).

GOTCHA THIS LOOP: `git checkout catalog.go` to revert a negative-control sed
ALSO wiped the real uncommitted edits — had to re-apply all 3 catalog changes.
Use targeted sed-revert, not git checkout, on uncommitted files.

Next loop: fresh M0119-0004 pg_dump slice. FDW/SERVER/MAPPING OPTIONS now
COMPLETE. Candidates: array-metachar quoting in optionsArrayLiteral (cross-cutting
w/ reloptions); pgQuoteIdent reserved-keyword fix (cross-cutting); ALTER … OPTIONS
ADD/SET/DROP; SERVER TYPE/VERSION; range types; aggregates; operators; text-search
configs; CREATE COLLATION.
