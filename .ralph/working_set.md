(idle — nothing in flight)

Last landed: DU-002 slice 165 (loop #132) — `RETURNS TABLE(...)` functions now
round-trip through pg_dump in the upstream form. REAL DIVERGENCE FIXED.

Root cause: goopg's parser desugars `RETURNS TABLE(col type, ...)` into trailing OUT
args (mode 'o') + `RETURNS SETOF record`. pg_dump renders from the server-side deparsers
(pg_get_function_arguments + pg_get_function_result, both verbatim), so the table cols
leaked into the arg list and the result was `SETOF record` → dump emitted
`ret_tab(OUT id integer, OUT label text) RETURNS SETOF record` instead of
`ret_tab() RETURNS TABLE(id integer, label text)`. PG stores TABLE cols as proargmode='t';
print_function_arguments excludes them, pg_get_function_result renders `TABLE(...)`.

Fix (contained, zero execution-path risk): a `ReturnsTable bool` marker threaded
parser→executor→catalog.Routine. NOT a new 't' argmode (would force every mode=="o"
consumer — planner OUT-column expansion at planner.go:3139/3336, CALL exec — to learn it,
risking a silent result-column regression). Table cols stay OUT args (planner expansion
unchanged); only 3 deparsers in expr.go change, gated on r.ReturnsTable:
buildFunctionArguments (skip table cols + no IN/OUT prefix flip), pg_get_function_result
(new buildTableResult → TABLE(...)), buildFunctionDef (pg_get_functiondef sibling).

Files: internal/parser/ast.go, internal/parser/function.go, internal/catalog/routines.go,
internal/executor/operators_ddl.go (propagate at the 5566 Routine literal),
internal/executor/expr.go (3 deparsers + buildTableResult helper),
internal/executor/pg_get_function_identity_arguments_test.go (2 unit tests),
internal/testport/pgdump_connsetup_test.go (fixture ~1764, assertions ~2475),
docs/design/0110-0001-pg-dump-tap-port.md (slice 165), .ralph/fix_plan.md (loop #132).
Verified: gofmt OK; go build ./internal/... OK; go vet clean;
TestPort_PgDumpConnectionSetup PASS (2.73s, not skipped); executor+parser suites PASS;
pgbench pre-commit smoke on commit.

Next direction (slice 166): the only remaining function-attribute gap is TRANSFORM FOR
TYPE (protrftypes always NULL — genuine feature gap). Better to pivot to a NEW object
class for pg_dump round-trip: column COLLATE, table STORAGE/COMPRESSION (ALTER COLUMN SET
STORAGE), triggers, or ACL/GRANT dumping — all currently untested (0 occurrences in
pgdump_connsetup_test.go). Probe goopg support before picking.
