(idle — nothing in flight)

Last landed: DU-002 slice 136 (loop #101) — INLINE-on-column `UNIQUE NULLS NOT
DISTINCT` (`a integer UNIQUE NULLS NOT DISTINCT`) dump-fidelity round-trip. The
inline column-UNIQUE parser had no slot for the PG15+ clause after the column's
UNIQUE keyword, so the backing index's NullsNotDistinct stayed false and the
constraint dumped as a plain `UNIQUE (a)` (silent NULL-dedup loss). Threaded:
`ColumnDef.UniqueNullsNotDistinct bool` (parser captures `NULLS [NOT] DISTINCT`
after inline UNIQUE) → executor inline column-UNIQUE loop sets
`catalog.Index.NullsNotDistinct`. pg_dump emits the SAME index-backed constraint
as a table-level UNIQUE (`ADD CONSTRAINT uniqcnnd_a_key UNIQUE NULLS NOT DISTINCT
(a)`), so buildConstraintDefString is unchanged from slice 135.
Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/executor/operators_ddl.go, internal/parser/ddl_test.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/deferral_ledger.md.
Verified: TestParseColumnUniqueNullsNotDistinct PASS; TestPort_PgDumpConnectionSetup
PASS (2.53s); parser+executor pkgs PASS; gofmt clean. Committed + pushed.

Next direction (slice 137): enforcement of the slice-134/135/136 NULLS-equal
semantics at INSERT/UPDATE (encodeIndexKeyFromCols NULL-sentinel encoding, gated
on idx.NullsNotDistinct), OR `CONSTRAINT name UNIQUE NULLS NOT DISTINCT`
inline-named column form (NOTE pre-existing gap: ddl.go:~2537 column
CONSTRAINT-name UNIQUE absorbs the keyword WITHOUT setting col.Unique, so named
inline column UNIQUE creates no backing index at all), OR an exclusion-constraint
(`EXCLUDE USING gist`) dump surface.
