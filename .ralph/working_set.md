Task: DU-002 slice 270 (loop #37) — COMPLETE & committed.

Last landed: a child-level `SET NOT NULL` on an INHERITED column of a legacy
INHERITS child round-trips through pg_dump as a standalone `NOT NULL <col>`
constraint item INSIDE the CREATE TABLE body (NOT a separate ALTER — the NOT
NULL twin of slice 269's DEFAULT). NEW production support: `ALTER TABLE ...
ALTER COLUMN ... SET NOT NULL`/`DROP NOT NULL` were a parser no-op before.

Catalog path (verified vs real pg_dump 18.3): PG18 records NOT NULL as a
pg_constraint contype='n' row; pg_dump's getTableAttrs LEFT-JOINs pg_constraint
(contype='n' AND conkey=array[attnum], pg_dump.c:9260) → notnull_constrs/
notnull_islocal. A LOCAL (conislocal='t') NOT NULL on a SUPPRESSED inherited
column (!shouldPrintColumn) is emitted inside the body as `NOT NULL pid`
(pg_dump.c:17213-17232); auto-name matches default → unnamed "" form. Output:
`CREATE TABLE public.idfn_child (\n    NOT NULL pid,\n    extra integer\n)
INHERITS (public.idfn_parent);` — NOT NULL precedes extra in attnum order.

Files:
- internal/parser/ast.go — AlterTableSetNotNull/AlterTableDropNotNull kinds.
- internal/parser/ddl.go — SET NOT NULL branch in ALTER COLUMN SET block +
  DROP NOT NULL branch in DROP block.
- internal/executor/operators_ddl.go — AlterTableSetNotNull (set Column.NotNull,
  AddNotNull isLocal=true inhCount=0 idempotent, heap re-sync via
  deleteCatalogRowsForOID + syncTableToCatalogHeap) + AlterTableDropNotNull.
- internal/parser/alter_test.go — TestParseAlterTableSetDropNotNull.
- internal/executor/operators_ddl_named_check_test.go — TestAlterTableSetDropNotNull
  (catalog state: contype='n' conislocal, idempotence, DROP).
- internal/testport/pgdump_connsetup_test.go — idfn_parent/idfn_child fixture +
  SET NOT NULL on inherited pid + body-scoped assert (NOT NULL pid + extra
  present, full inherited cols absent) + INHERITS assert.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 270 section + Next note.
- .ralph/fix_plan.md — slice 270 progress (loop #37).

Gates: gofmt clean; go build ./... clean; TestParseAlterTableSetDropNotNull PASS;
TestAlterTableSetDropNotNull PASS; go test ./internal/executor/ PASS (1.48s);
TestPort_PgDumpConnectionSetup PASS (3.63s); pgbench pre-commit smoke (enforced
by .githooks/pre-commit on commit).

Next (slice 271+): a *named* NOT NULL via `ALTER TABLE ... ADD CONSTRAINT <name>
NOT NULL <col>` on an inherited column — exercises the `CONSTRAINT <name> NOT
NULL <col>` form (notnull_constrs carrying a non-default name), the named
counterpart of this slice's unnamed "" path.
