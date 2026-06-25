Loop #47 COMPLETE: M0118-0002 — `VACUUM (TRUNCATE …)` option parse fix
(index-only-bitmapscan enabler, NOT a promotion), design 0118-0108. Committing + pushing.

What landed (parser-only, low blast radius):
- internal/parser/parser.go: VACUUM option loop `truncate` case now accepts the
  KwTruncate keyword token (`p.acceptKeyword(KwTruncate) || p.acceptIdentKeyword("truncate")`).
  Root cause: `TRUNCATE` lexes as unreserved keyword KwTruncate (leads TRUNCATE TABLE),
  not TokenIdent, so `acceptIdentKeyword("truncate")` never matched → fell to default
  `unrecognised VACUUM option (got truncate)`. It's the ONLY VACUUM option word that
  is also a SQL keyword. NoTruncate recorded for parity; vacuumCore never physically
  truncates trailing empty pages so TRUNCATE false/true are behavioral no-ops today.
- internal/parser/parser_test.go: TestParseVacuumTruncateOption (false/FALSE/true/bare/
  mixed-with-VERBOSE/ANALYZE).
- docs/design/0118-0108-*.md + README index row.
- fix_plan.md M0118-0002 entry + deferral_ledger.md line.

Gates run (PASS): TestParseVacuumTruncateOption + TestParseVacuum; go vet
./internal/parser clean; go build ./... clean. Parser-only (no executor/codec path)
→ pgbench smoke = pre-commit hook. Re-probe of index-only-bitmapscan confirmed first
divergence moved past s2_vacuum to the EXPLAIN-DECLARE-CURSOR blocker.

NEXT (all remaining M0118 are Effort-L distinct unbuilt subsystems — no freebies left;
each loop lands one enabler):
- index-only-bitmapscan: now blocked on (1) EXPLAIN over *parser.DeclareCursorStmt
  (executor rejects it), then (2) a BitmapOr / Bitmap Heap Scan / Bitmap Index Scan
  plan for `a>0 OR b>0` that EXPLAIN must render byte-for-byte (goopg has no bitmap-OR
  plan). (2) is the Effort-L core.
- stats: pg_stat_force_next_flush() + cumulative stats subsystem (setup-fails today).
- predicate-gin: int4[] COLUMN type (CREATE TABLE `p int4[]` stored as int4 → INSERT
  array[1] errors "{1}" to integer) + GIN AM (setup-fails today).
- deadlock-parallel: LANGUAGE internal funcs + parallel query workers (setup-fails).
- prepared-transactions{,-cic} (2PC, difflen ~2434), intra-grant-inplace (runtime
  shared-catalog MVCC-tuple row lock on pg_class GRANT tuple, difflen ~2294).
Probe helper pattern: throwaway zz_probe_test.go in internal/testport using
framework.IsolationRunner.RunAndCompare → log result.Status/Diff (import
internal/testutil/cluster NOT internal/cluster).
