(idle — nothing in flight)

Last landed: DU-002 slice 176 (loop #143) — cast/unary/binary/typed-string column
DEFAULTs now round-trip through pg_dump; sibling-path divergence with the executor
twin closed.

`validateDefaultExpr` accepts `*CastExpr`/`*UnaryOp`/`*BinaryOp` (and `*TypedStringLit`
passes through). CREATE TABLE stores the parsed AST verbatim in Column.DefaultExpr, so
`DEFAULT '{}'::jsonb` / `DEFAULT -1` / `DEFAULT 1 + 1` / `DEFAULT DATE '...'` all reach
pg_attrdef.adbin — but catalog.formatExprForAttrdef handled none of them (fell through to
fmt.Sprintf("%v", e), corrupting the dump). executor.defaultExprToSQL already handled all
four — a live divergence. Fix mirrors the executor twin line-for-line (cannot share code:
catalog is below executor in the import graph). Typmods on a cast are dropped (same as the
twin). Display-only.

Files: internal/catalog/catalog.go (4 new cases), internal/catalog/catalog_test.go
(TestFormatExprForAttrdefExpr), internal/testport/pgdump_connsetup_test.go (defcol gains
`meta jsonb DEFAULT '{}'::jsonb` + assertion), docs/design/0110-0001-pg-dump-tap-port.md,
.ralph/fix_plan.md.
Gates: gofmt OK; go vet clean; catalog suite PASS; TestPort_PgDumpConnectionSetup PASS
(3.15s, not skipped); pgbench pre-commit smoke on commit.

Next (slice 177 candidates): (1) deferred MINVALUE/MAXVALUE keyword-AST-node slice
(HIGHER RISK: partition routing). (2) column STORAGE/COMPRESSION dump fidelity (needs
parser keywords). (3) ARRAY[...] / RowExpr / ParenExpr column DEFAULT if any still fall
through formatExprForAttrdef (audit the remaining validateDefaultExpr-accepted node types
vs the renderer switch).
