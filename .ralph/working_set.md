(idle — nothing in flight)

Last landed: DU-002 slice 138 (loop #103) — NAMED *table-level* UNIQUE
(`CONSTRAINT tuniq UNIQUE NULLS NOT DISTINCT (a)`) dump-fidelity round-trip.
Fixed the pre-existing gap: the named table-level UNIQUE parser case
(`CONSTRAINT name UNIQUE (cols)`, ddl.go ~1907) did NOT parse the optional
`NULLS [NOT] DISTINCT` clause that precedes the column list (unlike the
anonymous table-level form taught by slice 135), so the `(` lookahead landed on
`NULLS`, `acceptSymbol("(")` returned false, and the WHOLE named constraint was
silently dropped from the table (and dump). Parser now parses the clause before
the columns + records `TableConstraintDef.NullsNotDistinct` (field already
existed from slice 135); executor `NamedConstraints` loop now sets
`idx.NullsNotDistinct = nc.NullsNotDistinct` on the backing index →
`buildConstraintDefString` re-emits it. pg_dump emits `ADD CONSTRAINT tuniq
UNIQUE NULLS NOT DISTINCT (a)`.
Files: internal/parser/ddl.go, internal/executor/operators_ddl.go,
internal/parser/ddl_test.go (TestParseTableNamedUniqueNullsNotDistinct),
internal/testport/pgdump_connsetup_test.go (uniqtname fixture, count-guard 4→5,
negative guard), docs/design/0110-0001-pg-dump-tap-port.md, .ralph/deferral_ledger.md.
Verified: TestParseTableNamedUniqueNullsNotDistinct PASS;
TestBuildConstraintDefNullsNotDistinct PASS; TestPort_PgDumpConnectionSetup PASS
(2.43s); gofmt/build/vet OK. Committed + pushed.

Next direction (slice 139): enforcement of the slice-134–138 NULLS-equal
semantics at INSERT/UPDATE (encodeIndexKeyFromCols NULL-sentinel encoding gated
on idx.NullsNotDistinct — riskier multi-encoding-site executor change), OR an
exclusion-constraint (`EXCLUDE USING gist`) dump surface, OR a named table-level
CHECK with INCLUDE/expression edge cases.
