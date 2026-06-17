(idle — nothing in flight)

Last landed: DU-002 slice 175 (loop #142) — function-call DEFAULT with LITERAL
ARGUMENTS (`DEFAULT lpad('x', 5)`) round-trips end-to-end through pg_dump.

Slice 173 fixed the generic *FuncCall renderer but only tested a zero-arg call
(now()); the recursive arg-render path in catalog.formatExprForAttrdef had no e2e
coverage. Slice 175 added `label text DEFAULT lpad('x', 5)` to the defcol fixture +
an assertion. No renderer change needed — pure coverage (lpad('x',5) unit case
already existed in TestFormatExprForAttrdefFuncCall). validateDefaultExpr accepts a
non-aggregate, non-SRF *FuncCall of any arity; formatExprForAttrdef renders
'x'→'x', 5→5, joins with ", " → lpad('x', 5).

Files: internal/testport/pgdump_connsetup_test.go (label col + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (slice 175), .ralph/fix_plan.md.
Gates: gofmt OK; go vet ./internal/testport/ clean; TestPort_PgDumpConnectionSetup
PASS (2.53s, not skipped); pgbench pre-commit smoke on commit.

Next (slice 176 candidates): (1) deferred MINVALUE/MAXVALUE keyword-AST-node slice
(HIGHER RISK: partition routing). (2) column STORAGE/COMPRESSION dump fidelity
(needs parser keywords). (3) function-call default with a nested/schema-qualified
call (e.g. pg_catalog.lpad) or a cast arg.
