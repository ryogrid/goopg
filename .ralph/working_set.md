(idle — nothing in flight)

Last landed: DU-002 slice 137 (loop #102) — INLINE *NAMED* column UNIQUE
(`a integer CONSTRAINT myuniq UNIQUE NULLS NOT DISTINCT`) dump-fidelity
round-trip. Fixed the pre-existing gap flagged in slice 136: the
`CONSTRAINT name UNIQUE` column-constraint case absorbed the UNIQUE keyword
WITHOUT setting col.Unique → no backing index created → constraint silently
dropped from the dump. Threaded `ColumnDef.UniqueConstraintName string` (parser
keeps the previously-discarded name, sets col.Unique, parses optional
NULLS [NOT] DISTINCT) → executor uses it as the backing-index/constraint name
(falls back to auto `tbl_col_key`). pg_dump emits `ADD CONSTRAINT myuniq UNIQUE
NULLS NOT DISTINCT (a)` under the user name. buildConstraintDefString unchanged.
Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/executor/operators_ddl.go, internal/parser/ddl_test.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/deferral_ledger.md.
Verified: TestParseColumnNamedUniqueNullsNotDistinct PASS;
TestPort_PgDumpConnectionSetup PASS (2.34s); parser+executor pkgs PASS;
go vet OK; gofmt clean. Committed + pushed.

Next direction (slice 138): enforcement of the slice-134/135/136/137 NULLS-equal
semantics at INSERT/UPDATE (encodeIndexKeyFromCols NULL-sentinel encoding, gated
on idx.NullsNotDistinct — riskier multi-encoding-site executor change), OR an
exclusion-constraint (`EXCLUDE USING gist`) dump surface, OR a named table-level
`CONSTRAINT name UNIQUE` form round-trip check.
