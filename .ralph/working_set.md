Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 56 COMPLETE
(committing this loop). NEXT loop starts on slice 57.
NOTHING in flight after commit.

=== DONE (loop #11) — DU-002 slice 56 (secondary-index ASC/DESC + NULLS ordering) ===
Real gap: a plain `CREATE INDEX … (col DESC)` round-tripped through pg_dump as
ASCENDING — goopg's parseIndexColumnList parsed ASC/DESC + NULLS FIRST/LAST but
SILENTLY DISCARDED them (a descending index reads back ascending). Also fixed a
latent parser bug: `NULLS` (a bare TokenIdent) was mis-read as an opclass name in
`(col NULLS FIRST)`, so that form errored "expected ')'".
FIX (threaded end-to-end, mirrors PG pg_index.indoption):
 - parser: parseIndexColumnList captures per-col IndexColOrder{Descending,
   NullsFirst} → CreateIndexStmt.ColOrders; NullsFirst pre-resolved to Descending
   (NULLS FIRST default for DESC, LAST for ASC) unless explicit NULLS overrides.
   Opclass branch now skips a case-insensitive `nulls`.
 - catalog.Index: new parallel ColDescending/ColNullsFirst slices.
 - executor execCreateIndex (btree path): populates them ONLY when non-default
   (plain index keeps empty slices → dumps byte-identically). indexHasNonDefaultOrder helper.
 - catalog.BuildIndexDef: renders ` DESC`/` NULLS LAST`/` NULLS FIRST` with PG's
   default-suppression (ruleutils.c pg_get_indexdef_worker).
Files: internal/parser/ddl.go (parseIndexColumnList + caller + nulls-opclass guard),
internal/parser/ast.go (IndexColOrder type + ColOrders field),
internal/catalog/catalog.go (Index fields + BuildIndexDef render),
internal/executor/operators_ddl.go (execCreateIndex populate + helper),
internal/parser/ddl_test.go (NEW TestParseCreateIndexColOrders),
internal/catalog/index_def_order_test.go (NEW TestBuildIndexDefColOrder),
internal/testport/pgdump_connsetup_test.go (fixture: 4 indexes + slice-56 asserts + header),
docs/design/0110-0001-pg-dump-tap-port.md (slice 56 entry + guard list).
DURABILITY NOTE (deferred): indoption bits NOT persisted to on-disk pg_index, so
ordering is lost across restart; pg_get_indexdef reads in-memory AllIndexes so the
dump is faithful within a session (the test path). On-disk indoption = follow-up.
Gates: gofmt clean (touched files); vet clean; parser+catalog+executor+initdb
suites PASS; new unit tests PASS; TestPort_PgDumpConnectionSetup PASS (exit-0, all
4 index forms round-trip); pgbench CI-parity smoke via pre-commit hook.

=== NEXT STEP — DU-002 slice 57 ===
Enrich the fixture further to find the next REAL pg_dump gap. Candidates:
  - VIEW round-trip: CREATE VIEW v AS SELECT … — does pg_dump emit CREATE VIEW
    via pg_get_viewdef? Likely a real path to probe.
  - column DEFAULT expression (e.g. DEFAULT now()) — does the DEFAULT survive in
    the dumped CREATE TABLE / ALTER TABLE … SET DEFAULT?
  - COMMENT ON INDEX / CONSTRAINT (3-part / `ON table`).
  - SEQUENCE / serial column — sequences skipped from pg_class virtual view
    (Virtual && no View), so getTables never sees relkind='S'; larger slice.
ALWAYS: add ONE fixture element, run TestPort_PgDumpConnectionSetup, inspect the
actual dumped output (temporary PROBE t.Logf), confirm whether goopg already
handles it before assuming a gap (slices 56's plain+partial index already worked).
Known orthogonal: plpgsql user funcs can't be dumped (plpgsql absent from
pg_language). Server SILENTLY SWALLOWS parse errors on COMMENT stmts.

NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
