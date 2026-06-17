(idle — nothing in flight)

Last landed: DU-002 slice 140 (loop #105) — DEFERRABLE INITIALLY DEFERRED on a
NAMED table-level UNIQUE constraint (`CONSTRAINT tudef UNIQUE (a) DEFERRABLE
INITIALLY DEFERRED`) round-trips through pg_dump.
Fixed a HARD PARSE ERROR: the named table-level UNIQUE parser case parsed NO
trailing DEFERRABLE (like the anonymous form before slice 139), so a trailing
DEFERRABLE was unexpected-token → whole CREATE TABLE failed. 3 sites: parser
named-UNIQUE case captures `[NOT] DEFERRABLE [INITIALLY DEFERRED|IMMEDIATE]` (+
bare INITIALLY DEFERRED) into new TableConstraintDef.Deferrable/InitiallyDeferred;
executor NamedConstraints loop threads both onto the backing index; deparse +
pg_constraint UNCHANGED from slice 139 (shared from the index).
Scope: pure dump-fidelity — deferred CHECKING not implemented (per-row enforce).
Files: internal/parser/ddl.go, internal/parser/ast.go, internal/parser/ddl_test.go,
internal/executor/operators_ddl.go, internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md.
Verified: TestParseTableNamedUniqueDeferrable PASS; TestPort_PgDumpConnectionSetup
PASS (2.55s); parser/catalog/executor suites green; gofmt/build/vet OK. Committed.

Next direction (slice 141): DEFERRABLE on the INLINE-column UNIQUE form
(`a int UNIQUE DEFERRABLE …`, threads via ColumnDef) and/or the PRIMARY KEY forms
(anonymous + named + inline — all still discard the flag), OR an exclusion-
constraint (`EXCLUDE USING gist`) dump surface. Deferred-check EXECUTION (check at
COMMIT not per-row) remains a separate transaction-machinery milestone (ledger).
