(idle — nothing in flight)

Last landed: DU-002 slice 186 (loop #154) — closed the `validateDefaultExpr`
compound-expression recursion gap (column-DEFAULT validation correctness).

`validateDefaultExpr` (internal/executor/operators_ddl_partition.go) rejects
column refs / aggregates / subqueries / SRFs in a DEFAULT but only recursed into
FuncCall args, BinaryOp, UnaryOp, CastExpr. An offending leaf hidden inside a
compound node (ARRAY[…], CASE, row (a,b), IN-list, IS NULL, IS DISTINCT FROM,
COLLATE, subscript, EXTRACT) slipped through → goopg accepted DEFAULTs PG rejects
(42P17/42803/0A000). The defcol fixture (slice 181) round-trips exactly these
compound shapes, which surfaced the gap.

Fix: added recursion arms for ArrayConstructorExpr, RowExpr, CaseExpr (Operand +
each WHEN/THEN + ELSE), InExpr (Operand + List; populated Subquery → subquery
rejection), IsNullExpr, IsBoolExpr, IsDistinctFromExpr, CollateExpr,
ArraySubscriptExpr, ExtractExpr; folded ExistsExpr + ArraySubqueryExpr into the
existing SubqueryExpr rejection arm. Validation-only (no change to how a valid
DEFAULT is stored/evaluated).

Files: internal/executor/operators_ddl_partition.go,
internal/executor/default_validate_test.go (NEW — TestDefaultExprRejectsNestedColumnRefs
12 cases + TestDefaultExprAcceptsConstantCompounds over-rejection guard),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 186), .ralph/fix_plan.md (loop-154).
Gates: gofmt OK; go build ./... clean; full ./internal/executor/ PASS (1.38s);
new tests PASS; pgbench pre-commit smoke on commit.

Next (slice 187 candidates): (1) deferred MINVALUE/MAXVALUE keyword-AST-node slice
(HIGHER RISK: partition routing). (2) per-column COLLATE round-trip — parser
currently IGNORES column COLLATE (ddl.go:2446); needs pg_collation population
(VirtualRows returns nil today) so pg_dump resolves attcollation OID → name.
(3) attfdwoptions (foreign-table only, NULL today — needs RELKIND_FOREIGN_TABLE).
