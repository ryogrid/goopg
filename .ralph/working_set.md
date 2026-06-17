(idle — nothing in flight)

Last landed: DU-002 slice 143 (loop #108) — DEFERRABLE on an EXCLUDE constraint
now round-trips through pg_dump, the LAST index-backed constraint kind that
discarded the flag. Anonymous (`EXCLUDE USING btree (a WITH =) DEFERRABLE
INITIALLY DEFERRED` → auto name `excldef_a_excl`) and named (`CONSTRAINT exdef
EXCLUDE … DEFERRABLE …`). Two bugs fixed: (a) parseExcludeConstraint returned
after INCLUDE so the trailer was dropped; (b) buildConstraintDefString's EXCLUDE
branch returned BEFORE the shared DEFERRABLE append. 3 sites:
(1) parser ddl.go — both EXCLUDE call sites (anon TableExclusions + named
NamedConstraints) now call generic parseConstraintDeferrable; no new AST fields
(TableConstraintDef.Deferrable/InitiallyDeferred already existed).
(2) executor operators_ddl.go — all 3 exclusion index-build paths copy flags
onto idx.Deferrable/InitiallyDeferred (named btree-=, anon btree-=, and
createExclusionIndexStub for non-= ops). (3) deparse expr.go — EXCLUDE branch
appends DEFERRABLE after INCLUDE. pg_constraint already emitted
condeferrable/condeferred for contype='x' (shared row-builder). btree-= used so
method=btree round-trips (stub hard-codes btree — gist would NOT round-trip,
pre-existing gap). Pure dump-fidelity — deferred CHECKING still per-row.
Files: internal/parser/ddl.go, internal/parser/ddl_test.go,
internal/executor/operators_ddl.go, internal/executor/expr.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md.
Verified: TestParseExcludeDeferrable PASS; TestPort_PgDumpConnectionSetup PASS
(2.58s); parser/catalog/executor suites green; gofmt/build/vet OK. Committed.

Next direction (slice 144): the full UNIQUE+PK+EXCLUDE DEFERRABLE dump surface is
now complete. Pick a FRESH pg_dump catalog-surface gap — e.g. constraint COMMENT
round-trip (COMMENT ON CONSTRAINT), per-column COMMENT, or a different catalog
view. Deferred-check EXECUTION (validate at COMMIT, not per-row) for ALL
constraint kinds remains a separate txn-machinery milestone — larger than a
dump-fidelity slice.
