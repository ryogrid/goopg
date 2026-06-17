(idle — nothing in flight)

Last landed: DU-002 slice 174 (loop #141) — parenless SQL niladic value-function
column DEFAULT (`DEFAULT CURRENT_TIMESTAMP`) round-trips through pg_dump as the bare
uppercase keyword. CLOSED a regression slice 173 introduced.

Bug: slice 173's generic `*FuncCall` renderer deparsed a parenless niladic value
function as `current_timestamp()` (parens INVALID on restore). goopg parses
CURRENT_TIMESTAMP/CURRENT_DATE/CURRENT_USER/CURRENT_SCHEMA/SESSION_USER/LOCALTIMESTAMP/…
(parser.IsNoParenFuncName set) as a zero-arg *FuncCall. PG (18.3, verified) stores
these as SQLValueFunction → pg_get_expr deparses the bare UPPERCASE keyword, no parens;
now() (real FuncExpr) keeps parens.

Fix: both default renderers gain a guard before the generic call arm — zero args +
no schema + name in parser.IsNoParenFuncName → strings.ToUpper(name). Niladic set
EXPORTED from parser so parse classifier + both render twins
(catalog.formatExprForAttrdef on pg_dump path, executor.defaultExprToSQL on
proargdefaults path) share one source of truth. Display-only.
Known limit: AST has no with-parens flag → genuine current_schema() renders as
keyword CURRENT_SCHEMA (benign).

Files: internal/parser/select.go (export IsNoParenFuncName + update caller),
internal/catalog/catalog.go + internal/executor/operators_ddl.go (niladic guard),
internal/catalog/catalog_test.go (niladic cases),
internal/testport/pgdump_connsetup_test.go (touched col + 2 assertions),
docs/design/0110-0001-pg-dump-tap-port.md (slice 174), .ralph/fix_plan.md.
Gates: gofmt OK; go vet ./internal/testport/ clean; go build ./internal/... OK;
catalog+executor+parser suites PASS; TestPort_PgDumpConnectionSetup PASS (2.85s,
not skipped); pgbench pre-commit smoke on commit.

Next (slice 175 candidates): (1) function-call default with literal args end-to-end
(`DEFAULT lpad('x',5)` — unit-tested but no e2e). (2) deferred MINVALUE/MAXVALUE
keyword-AST-node (HIGHER RISK: partition routing). (3) column STORAGE/COMPRESSION
dump fidelity (needs parser keywords).
