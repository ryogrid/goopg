(idle — nothing in flight)

Last landed: DU-002 slice 178 (loop #146) — CASE-expression column DEFAULT now
round-trips through pg_dump; sibling-path divergence with the executor twin closed.

`validateDefaultExpr` accepts a `*CaseExpr` (falls through to `return nil`), so both the
searched form `DEFAULT CASE WHEN true THEN 1 ELSE 0 END` and the simple form `DEFAULT
CASE 1 WHEN 1 THEN 'x' ELSE 'y' END` reach pg_attrdef.adbin. Neither
catalog.formatExprForAttrdef nor the executor twin executor.defaultExprToSQL had a
`*CaseExpr` arm → both fell through to fmt.Sprintf("%v", e) (Go pointer string),
corrupting the dump. Fix: both twins gain a `*CaseExpr` case rendering the single-line
`CASE [operand] WHEN c THEN r [WHEN …] [ELSE e] END` (operand/arms/else rendered
recursively). PG's pg_get_expr pretty-prints CASE multi-line, but single-line is valid
re-parseable SQL that round-trips. Display-only; can't share code (catalog below executor
in import graph). validateDefaultExpr still does NOT recurse into CASE arms (same
pre-existing validation gap as array elements; PG rejects column refs in CASE at parse).

Files: internal/catalog/catalog.go (+1 case), internal/executor/operators_ddl.go (+1 case,
twin), internal/catalog/catalog_test.go (case searched/simple/no-else cases),
internal/testport/pgdump_connsetup_test.go (defcol gains `grade integer DEFAULT CASE WHEN
true THEN 1 ELSE 0 END` + assertion), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt OK; go vet clean; catalog + executor tests PASS;
TestPort_PgDumpConnectionSetup PASS (2.47s, not skipped); pgbench pre-commit smoke on commit.

Next (slice 179 candidates): (1) `*RowExpr` / ROW(...) column DEFAULT — audit if it still
falls through both renderers. (2) `*CoalesceExpr` / NULLIF / GREATEST / LEAST default
(if represented as distinct AST nodes). (3) deferred MINVALUE/MAXVALUE keyword-AST-node
slice (HIGHER RISK: partition routing). (4) close the validateDefaultExpr
array/row/CASE-element recursion gap (executor semantic change — needs its own gates).
