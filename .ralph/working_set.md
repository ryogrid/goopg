(idle — nothing in flight)

Last landed: DU-002 slice 177 (loop #145) — ARRAY-constructor column DEFAULT now
round-trips through pg_dump; sibling-path divergence with the executor twin closed.

`validateDefaultExpr` rejects only column refs / subqueries / aggregate-or-SRF calls and
ACCEPTS every other node (returns nil), so `DEFAULT ARRAY[1, 2, 3]` on an `integer[]`
column reaches pg_attrdef.adbin verbatim. Neither catalog.formatExprForAttrdef nor the
executor twin executor.defaultExprToSQL had an `*ArrayConstructorExpr` arm → both fell
through to fmt.Sprintf("%v", e) (Go pointer string), corrupting the dump. Fix: both twins
gain an `*ArrayConstructorExpr` case rendering `ARRAY[e1, …]` (elements rendered
recursively, joined `, `), mirroring PG's pg_get_expr deparse. Display-only; can't share
code (catalog below executor in import graph). validateDefaultExpr still does NOT recurse
into array elements (pre-existing validation gap, out of scope for a display slice).

Files: internal/catalog/catalog.go (+1 case), internal/executor/operators_ddl.go (+1 case,
twin), internal/catalog/catalog_test.go (array constructor + empty cases),
internal/testport/pgdump_connsetup_test.go (defcol gains `vals integer[] DEFAULT ARRAY[1,
2, 3]` + assertion), docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.
Gates: gofmt OK; go vet clean; catalog + executor tests PASS; TestPort_PgDumpConnectionSetup
PASS (3.10s, not skipped); pgbench pre-commit smoke on commit.

Next (slice 178 candidates): (1) `*CaseExpr` column DEFAULT (`DEFAULT CASE WHEN true THEN 1
ELSE 0 END`) — audit if it still falls through both renderers. (2) deferred
MINVALUE/MAXVALUE keyword-AST-node slice (HIGHER RISK: partition routing). (3) column
STORAGE/COMPRESSION dump fidelity (needs parser keywords). (4) close the validateDefaultExpr
array/row-element recursion gap (executor semantic change — needs its own gates).
