(idle — nothing in flight)

Loop #18 COMPLETE: M0119-0004 DU-002 slice 379 — `CREATE USER MAPPING …
OPTIONS (name 'value', …)` round-trip THROUGH pg_dump.

Slice 377 dumped a user mapping but discarded OPTIONS (umoptions always NULL).
This slice round-trips them, REUSING the slice-378 machinery wholesale. pg_dump's
dumpUserMappings expands umoptions server-side via the identical
`array_to_string(ARRAY(SELECT quote_ident(option_name)||' '||quote_literal(
option_value) FROM pg_options_to_table(umoptions) ORDER BY option_name),
E',\n    '))` → ` OPTIONS (\n    %s\n)`. Options sort by name (password<username).

Fix (3 layers, mirrors slice 378):
- parser (ddl.go): scanUserMappingForServer now ALSO returns options via the
  shared scanFDWOptionsList; CREATE arm stores in CompatNoopStmt.Options, DROP
  caller discards (`_`).
- catalog (catalog.go): UserMapping.Options []string; RegisterUserMapping signature
  now (user,server,options) (idempotent re-register refreshes only when non-empty);
  umoptions cell renders optionsArrayLiteral(m.Options).
- executor (operators_ddl.go): user-mapping arm threads s.Options.

Files: internal/parser/ddl.go, internal/parser/op_compat_test.go
(TestParseCreateUserMapping extended), internal/catalog/catalog.go,
internal/catalog/fdw_registry_test.go (umoptions assert + 3 caller sig fixes),
internal/executor/operators_ddl.go, internal/testport/pgdump_connsetup_test.go
(goopg_srv_um fixture + assert), docs/design/0110-0001-pg-dump-tap-port.md
(Slice 379), fix_plan, ledger.

Gates: TestParseCreateUserMapping + TestUserMappingRegistry + full parser/catalog
suites PASS; TestPort_PgDumpConnectionSetup PASS (byte-identical vs pg_dump 18.3)
+ NEGATIVE CONTROL confirmed assertion live. go build ./... clean. pgbench smoke =
pre-commit hook. No TPC-H (metadata-only virtual-catalog change).

DISCOVERY (ledger): goopg's pgQuoteIdent does NOT guard reserved keywords (comment
lies) → reserved-keyword option name `user` emits bare `user 'x'` vs pg_dump's
`"user" 'x'`; latent at every quote_ident site. Sidestepped with non-keyword names.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: FDW OPTIONS (fdwoptions —
reuse scanFDWOptionsList in CREATE FOREIGN DATA WRAPPER arm + optionsArrayLiteral);
pgQuoteIdent reserved-keyword fix (cross-cutting); array-metachar quoting in
optionsArrayLiteral; SERVER TYPE/VERSION; range types; aggregates; operators;
text-search configs; CREATE COLLATION.
