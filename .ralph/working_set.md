(idle — nothing in flight)

Last loop (#32): M0119-0004 **`ON DELETE SET NULL|DEFAULT (column_list)` round-trip
in pg_dump** (DU-002 slice 311) — LANDED. Design
`0119-0004-fk-on-delete-set-cols-roundtrip.md`.

PG15 lets an `ON DELETE SET NULL/DEFAULT` action be restricted to a subset of the
FK's referencing columns (`pg_constraint.confdelsetcols`); pg_get_constraintdef
appends ` (col, …)` after the ON DELETE clause (ruleutils.c:2376). goopg's
`parseFKAction` consumed SET NULL/DEFAULT but never the trailing column list →
silently degraded into a whole-key SET NULL on restore (semantic change).

Threaded end-to-end (mirroring slice-309 MATCH FULL):
- ddl.go parseFKAction: now returns (FKAction, []string, error); parses optional
  `(col_list)`, recorded only on isDelete branch. 3 callers updated.
- ast.go: new OnDeleteSetCols []string on ColumnDef/TableForeignKeyDef/AlterTableAction.
- catalog.go: ForeignKey.OnDeleteSetCols; pg_constraint builder projects
  confdelsetcols (row[23], attnum array via colOrd; NULL when whole-key).
- operators_ddl.go: 3 FK build sites copy the field.
- expr.go buildForeignKeyDefString: append ` (cols)` after ON DELETE when SET
  NULL/DEFAULT + non-empty list.

Gates: DU-002 slice 311 in TestPort_PgDumpConnectionSetup (sfk_child_fk →
`ON DELETE SET NULL (b);`) PASS vs real pg_dump 18.3 (4.65s); new unit
TestForeignKeyOnDeleteSetColsRoundTrip PASS; parser+catalog+executor suites PASS;
`go build ./...` clean; pgbench smoke = pre-commit hook.

NEXT loop — remaining open under M0119-0004 (probe TestPort_PgDumpConnectionSetup
for the next getter-battery gap): pg_dump 002–010 catalog-view parity battery
candidates — collation/opclass on index columns, comment round-trip via
pg_description on more object types, GENERATED column STORED expr edge cases.
Extended-protocol commit-time deferral is architecturally entangled (auto-commit-
per-statement; see memory). Other M0119: M0119-0002 (CLOG store swap Part B —
full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).
