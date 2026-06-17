(idle — nothing in flight)

Last landed: DU-002 slice 144 (loop #109) — COMMENT ON CONSTRAINT now round-trips
through pg_dump for ALL constraint kinds. execCommentOn's "constraint" case
previously scanned ONLY tbl.NamedChecks + tbl.NotNullConstraints, so a comment on
a PRIMARY KEY / UNIQUE / EXCLUDE (index-backed) or FOREIGN KEY constraint matched
nothing and returned without calling catalog.SetComment — accepted with no error
but silently dropped from pg_description (pg_dump had nothing to re-emit). Fix in
internal/executor/operators_ddl.go execCommentOn: after the CHECK/NOT NULL scans,
(1) iterate im.IndexesOnTable(tbl) for an index whose Name matches and backs a
constraint (IsConstraint || IsExclusion) — backing index OID IS the pg_constraint
OID → SetComment(2606, idx.OID, 0, desc) covers PK/UNIQUE/EXCLUDE; (2) iterate
tbl.ForeignKeys for a name match → SetComment(2606, fk.OID, 0, desc). No
catalog-schema change. pg_dump emits `COMMENT ON CONSTRAINT <name> ON
<schema>.<table> IS '...'` once the row is keyed under classoid=pg_constraint.
Key symbols: ddlOp.execCommentOn (operators_ddl.go ~L8392), InMemory.IndexesOnTable
(catalog.go:5910), InMemory.SetComment (catalog.go:1092), Table.ForeignKeys,
Index.IsConstraint/IsExclusion/OID/Name.
Files: internal/executor/operators_ddl.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.
Verified: gofmt/build/vet OK; parser+catalog+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS (2.40s, 4 new constraint-comment fixtures +
4 asserts: foo_pkey PK, foo_code_key UNIQUE, foo_mgr_fkey FK, exdef EXCLUDE).
Committed + pushed.

Next direction (slice 145): a fresh pg_dump catalog-surface gap. Candidates:
COMMENT ON {SCHEMA,SEQUENCE,VIEW,INDEX} round-trip through pg_dump — note the
execCommentOn "index" path is wired (SetComment on pg_class) but UNTESTED through
pg_dump; SCHEMA/SEQUENCE/VIEW comment kinds are NOT yet handled by execCommentOn
(parseCommentOnTail returns unsupported → server swallows them). Or the
deferred-check EXECUTION spike (validate at COMMIT, not per-row) — a larger
txn-machinery milestone, separate from dump-fidelity.
