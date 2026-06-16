Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 60 COMPLETE
(committing this loop). NEXT loop starts on slice 61.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 60 (MATERIALIZED VIEW round-trip) ===
Gap (confirmed via pg_dump.c read + first-run PASS): a matview made pg_dump
abort the WHOLE dump with `definition of view "v" appears to be empty (length
zero)`. pg_dump dumps a matview's AS clause via the SAME createViewAsClause ->
pg_get_viewdef path as a plain view (pg_dump.c dumpTableSchema RELKIND_MATVIEW
branch: `CREATE MATERIALIZED VIEW … AS\n<body>\n  WITH NO DATA;`). goopg
surfaces matviews in pg_class as relkind='m' (TestSyntax_DDL_MatViewPgClass) but
execCreateMatView captured the body only as the SELECT AST (tbl.View, for
REFRESH) — tbl.ViewDef stayed "" → pg_get_viewdef returned NULL. Exact slice-57
plain-VIEW bug, repeated for the matview parse path.
FIX (3 sites, all additive):
  - internal/parser/ast.go: CreateMatViewStmt gains RawDef string.
  - internal/parser/ddl.go parseCreateMatViewTail: bodyStart := p.cur() before
    parseSelect; stmt.RawDef = p.captureSrcSpan(bodyStart.Pos, p.cur()) after
    (excludes the trailing WITH [NO] DATA clause). Mirrors parseCreateViewTail.
  - internal/executor/operators_ddl.go execCreateMatView: tbl.ViewDef = s.RawDef
    (right after tbl.View = s.Query). pg_get_viewdef keys on View!=nil &&
    ViewDef!="", so it now echoes the body unchanged.
Files: ast.go, ddl.go, operators_ddl.go, internal/parser/view_test.go
(TestParseCreateMatViewRawDef), internal/testport/pgdump_connsetup_test.go
(foo_mv fixture + slice-60 asserts), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; vet clean; build ./... ok; parser+executor+catalog suites
PASS; matview subset PASS; TestPort_PgDumpConnectionSetup PASS first run
(foo_mv round-trips); tpch-spotcheck SKIPPED (no data loaded — graceful);
pgbench CI-parity smoke via pre-commit hook.

=== NEXT STEP — DU-002 slice 61 ===
Enrich the fixture to find the next REAL pg_dump gap. Candidates still open:
  - IDENTITY column / SEQUENCE / serial — blocked: sequences are skipped from
    pg_class virtual view (Virtual && no View → getTables never sees relkind='S').
    Larger slice — sequence-as-relkind='S' support first.
  - RECURSIVE view (CREATE RECURSIVE VIEW) — parseCreateRecursiveViewTail builds
    a wrapped CTE AST but sets NO RawDef → pg_get_viewdef returns NULL → likely
    aborts the dump (verify). Fix: capture/synthesize a RawDef for the recursive
    form. NOTE PG reconstructs recursive views canonically; goopg echoes verbatim.
  - Array-typed column (e.g. tags text[]) — check format_type path round-trips.
  - ALTER TABLE ... SET DEFAULT / column-level non-trivial DEFAULT.
ALWAYS: add ONE fixture element, run TestPort_PgDumpConnectionSetup, inspect the
actual dump, confirm goopg doesn't already handle it before assuming a gap.
Known-working: CHECK, DEFAULT now(), typmods, FKs, comments, ordered indexes,
plain+renamed VIEWs, GENERATED STORED cols, MATERIALIZED VIEW.
Known orthogonal: plpgsql user funcs can't dump (plpgsql absent from pg_language).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
