Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 58 COMPLETE
(committing this loop). NEXT loop starts on slice 59.
NOTHING in flight after commit.

=== DONE (loop #13) — DU-002 slice 58 (VIEW explicit column-list round-trip) ===
Real gap (probed empirically): `CREATE VIEW v (col_a, col_b) AS SELECT id, name
FROM foo` dumped as `CREATE VIEW v AS SELECT id, name FROM foo` — the renamed
column names were LOST (restored view exposed id/name, not col_a/col_b). PG's
pg_get_viewdef bakes the names in as `expr AS cN`.
FIX (raw-text splice, NOT a deparser): executor pg_get_viewdef now calls new
`applyViewColumnAliases(rawDef, view.ViewColumnAliases)` when the view has an
explicit column list (ViewColumnAliases was already populated by CreateView).
It finds the top-level FROM boundary (findTopLevelFromKeyword — paren/bracket +
quote aware so EXTRACT(.. FROM ..)/subqueries are safe), splits the select list
on top-level commas (reused existing splitTopLevelCommas in plpgsql_runtime.go),
appends ` AS <name>` (quoteViewIdent quotes only non-simple-lowercase idents).
BAILS to raw text (names lost) when: item count != alias count, item is `*`/
`x.*`, or item already has a top-level AS alias (hasTopLevelAsAlias). Documented
fidelity gaps.
Files: internal/executor/expr.go (pg_get_viewdef wiring + 4 new helpers near the
array-literal quoting fns; NOTE splitTopLevelCommas already existed in
plpgsql_runtime.go — reused, did NOT redeclare),
internal/executor/viewdef_aliases_test.go (NEW TestApplyViewColumnAliases, 13
cases), internal/testport/pgdump_connsetup_test.go (foo_rview fixture + slice-58
asserts + header doc), docs/design/0110-0001-pg-dump-tap-port.md (slice 58 entry
+ guard paragraph + unit-guard list).
Gates: gofmt clean; vet clean (executor); executor+parser+catalog suites PASS;
TestApplyViewColumnAliases PASS; TestPort_PgDumpConnectionSetup PASS (exit-0,
renamed-col view round-trips); pgbench CI-parity smoke via pre-commit hook.

=== NEXT STEP — DU-002 slice 59 ===
Enrich the fixture to find the next REAL pg_dump gap. Candidates probed/known:
  - MATERIALIZED VIEW round-trip (no RawDef captured yet; needs its own parser
    path — CREATE MATERIALIZED VIEW likely a different AST node).
  - SEQUENCE / serial column — sequences skipped from pg_class virtual view
    (Virtual && no View), so getTables never sees relkind='S'; larger slice.
  - RECURSIVE view (WITH RECURSIVE … ) — RawDef capture path may differ.
  - GENERATED column / IDENTITY column round-trip.
ALWAYS: add ONE fixture element, run TestPort_PgDumpConnectionSetup, inspect the
actual dumped output (temporary PROBE t.Logf at the exit-0 return), confirm
whether goopg already handles it before assuming a gap (CHECK constraints,
DEFAULT now(), typmods, FKs, comments, ordered indexes ALL already work — seen in
the slice-58 probe dump).
Known orthogonal: plpgsql user funcs can't be dumped (plpgsql absent from
pg_language). Server SILENTLY SWALLOWS parse errors on COMMENT/DDL stmts.

NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
