Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 61 COMPLETE
(committing this loop). NEXT loop starts on slice 62.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 61 (RECURSIVE VIEW round-trip) ===
Gap (confirmed via code read + first-run PASS): CREATE RECURSIVE VIEW built the
wrapped-CTE AST (for execution) but set NO RawDef, so pg_get_viewdef returned
NULL → pg_dump aborts the WHOLE dump (`definition of view "v" appears to be
empty`). Exact slice-57 plain-VIEW bug, repeated for the recursive parse path.
FIX (1 site, additive): internal/parser/ddl.go parseCreateRecursiveViewTail —
capture verbatim body (bodyStart := p.cur() before parseSelect; rawBody :=
captureSrcSpan after), then synthesize stmt.RawDef = `WITH RECURSIVE name(cols)
AS (<rawBody>) SELECT cols FROM name`. PG stores recursive views as a regular
view over a WITH RECURSIVE CTE; pg_dump re-emits as plain CREATE VIEW. Outer
projection lists declared cols explicitly (no deparser — documented fidelity
gap). The "WITH" prefix means applyViewColumnAliases bails (only rewrites bodies
starting with SELECT), so col names come from the synthesized projection. CTE
self-reference in the fixture body is UNQUALIFIED (binds to CTE name).
Files: internal/parser/ddl.go, internal/parser/view_test.go
(TestParseCreateRecursiveViewRawDef), internal/testport/pgdump_connsetup_test.go
(foo_rec fixture + slice-61 asserts), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; vet clean; build ./... ok; parser+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS first run (exit-0 path, foo_rec round-trips);
pgbench CI-parity smoke via pre-commit hook.

=== NEXT STEP — DU-002 slice 62 ===
Enrich the fixture to find the next REAL pg_dump gap. Candidates still open:
  - Array-typed column (e.g. tags text[]) — formatTypeOID already maps 1009→
    text[], 1007→integer[], 1016→bigint[], 1005→smallint[]; UNVERIFIED whether
    buildUserPGAttributeRow sets atttypid to the array OID for `text[]` columns.
    Add fixture, run test, inspect dump. Likely the cleanest next slice.
  - IDENTITY column / SEQUENCE / serial — blocked: sequences skipped from
    pg_class virtual view (Virtual && no View). Larger slice (relkind='S' first).
  - ALTER TABLE ... SET DEFAULT / column-level non-trivial DEFAULT.
  - ENUM / composite / domain user type column.
ALWAYS: add ONE fixture element, run TestPort_PgDumpConnectionSetup, inspect the
actual dump, confirm goopg doesn't already handle it before assuming a gap.
Known-working: CHECK, DEFAULT now(), typmods, FKs, comments, ordered indexes,
plain+renamed VIEWs, GENERATED STORED cols, MATERIALIZED VIEW, RECURSIVE VIEW.
Known orthogonal: plpgsql user funcs can't dump (plpgsql absent from pg_language).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
