Task: DU-002 slice 273 (loop #40) — COMPLETE, ready to commit.

Last landed: a NAMED inline NOT NULL whose name DIFFERS from PG's auto-name
(`c integer CONSTRAINT c_nn NOT NULL NO INHERIT`) round-trips through pg_dump as
the inline `<col> <type> CONSTRAINT <name> NOT NULL [NO INHERIT]` form. Slice 272
covered the UNNAMED inline form (name == default → bare NOT NULL). PRODUCTION FIX:
goopg's inline-CONSTRAINT parser switch had NO NotNull arm — `CONSTRAINT c_nn NOT
NULL` fell to the default skip and was silently dropped (dumped as plain
`c integer`). Verified byte-for-byte vs real pg_dump 18.3:
`c integer CONSTRAINT c_nn NOT NULL NO INHERIT,\n    e integer CONSTRAINT e_nn NOT NULL`.

Files:
- internal/parser/ast.go — new ColumnDef.NotNullConstraintName field.
- internal/parser/ddl.go:2761 — KwNot case in inline CONSTRAINT switch: captures
  name + optional NO INHERIT.
- internal/executor/operators_ddl.go:2464 — explicitNotNullName[col] map; custom
  name overrides auto-name in AddNotNull(...).
- internal/parser/alter_test.go — TestParseCreateTableColumnNamedNotNull.
- internal/testport/pgdump_connsetup_test.go — nninh2 fixture + body assert
  (named NO-INHERIT on c; plain named on e; negative check no spurious NO INHERIT).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 273 section + Next note.
- .ralph/fix_plan.md — slice 273 progress (loop #40).

Gates: gofmt clean; go build ./... clean; TestParseCreateTableColumnNamedNotNull
PASS; go test ./internal/parser/ ./internal/executor/ ./internal/catalog/ PASS;
TestPort_PgDumpConnectionSetup PASS (3.90s, byte-matches real pg_dump 18.3);
pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 274+): the `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col> NO
INHERIT` end-to-end dump on a STANDALONE table (rendered inline because the column
is local), or a named inline NOT NULL whose name EQUALS the auto-name (must
collapse back to the bare `NOT NULL` form, not the `CONSTRAINT <name>` form).
