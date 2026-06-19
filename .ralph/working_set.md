(idle — nothing in flight)

Last landed: DU-002 slice 269 (loop #36) — a child-level `SET DEFAULT` on an
INHERITED column round-trips through pg_dump as a SEPARATE ALTER. NEW production
support: `ALTER TABLE ... ALTER COLUMN ... SET DEFAULT`/`DROP DEFAULT` were a
parser no-op before. Inherited columns are suppressed from the child's CREATE
TABLE body (attislocal=false), so pg_dump.c marks attrdefs[].separate
(!shouldPrintColumn, pg_dump.c:9527) and emits a standalone
`ALTER TABLE ONLY public.idfa_child ALTER COLUMN pid SET DEFAULT 7;` (dumpAttrDef,
pg_dump.c:18028). Verified end-to-end vs real pg_dump 18.3 against goopg.

Files:
- internal/parser/ast.go — AlterTableSetDefault/AlterTableDropDefault kinds +
  AlterTableAction.DefaultExpr field.
- internal/parser/ddl.go — SET DEFAULT branch (parseExpr) in ALTER COLUMN SET
  block + DROP DEFAULT branch.
- internal/executor/operators_ddl.go — AlterTableSetDefault (validateDefaultExpr,
  set Column.DefaultExpr, heap re-sync via deleteCatalogRowsForOID +
  syncTableToCatalogHeap) + AlterTableDropDefault (clear).
- internal/parser/alter_test.go — TestParseAlterTableSetDropDefault.
- internal/testport/pgdump_connsetup_test.go — idfa_parent/idfa_child fixture +
  ALTER...SET DEFAULT 7 + block-scoped assert (local `extra`, inherited
  pid/pname ABSENT) + separate-ALTER assert.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 269 section + Next note.
- .ralph/fix_plan.md — slice 269 progress (loop #36).

Gates: gofmt clean; go build ./... clean; TestParseAlterTableSetDropDefault PASS;
go test ./internal/executor/ PASS (1.47s); TestPort_PgDumpConnectionSetup PASS
(3.78s); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 270+): a child-level `SET NOT NULL` on an INHERITED column. PG 18
NOT NULL is a pg_constraint (contype='n'); pg_dump emits it as a separate
ALTER ... SET NOT NULL or a CONSTRAINT ... NOT NULL clause — a distinct catalog
path from this attrdef slice.
