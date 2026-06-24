(idle — nothing in flight)

Loop #39 COMPLETE: M0118-0009 plpgsql `EXECUTE … INTO STRICT` ADDED
(horizons.spec enabler, design 0118-0101 — NOT a spec promotion).

What landed — the next horizons rung after 0118-0100 (JSON `->`):
- ast.go: `ExecuteStmt.Strict bool`.
- parser.go parseExecute: optional `STRICT` after `INTO` (unreserved word →
  plain ident, same scan as the static SELECT … INTO STRICT path).
- plpgsql_runtime.go *ExecuteStmt case: when Strict, pull a 2nd row to detect
  multi-row, then enforce 0 rows→P0002 "query returned no rows" / >1→P0003
  "query returned more than one row" (codes the no_data_found/too_many_rows
  exception-name map already keys off). First INTO datum copied before 2nd
  Next(). Non-STRICT EXECUTE … INTO byte-identical (binds first row).
- Tests: TestParseExecuteIntoStrict (parser: STRICT flagged / plain non-strict),
  TestPlpgSQLExecuteIntoStrict (executor: one-row bind, P0002, P0003,
  non-strict multi-row binds first).
- docs/design/0118-0101 + README index; fix_plan note + deferral ledger row.

Validation: re-probed horizons.spec — first divergence advanced from the
`EXECUTE … INTO STRICT` setup-helper failure to the EXPLAIN-result `Heap
Fetches` pruning counts (expected "2"/"0", actual ""). Setup function now runs.

horizons REMAINING blockers (in order, all Effort-L):
(1) EXPLAIN (FORMAT json) emit `Heap Fetches` for index-only scans
    (operators_explain.go); (2) MVCC core — index-only-scan heap-fetch count
    reflecting pruning + prune/VACUUM respecting a concurrent older snapshot
    for permanent vs temp tables.

Gates: parser+executor STRICT units PASS; full internal/plpgsql +
internal/executor suites PASS no regression; go vet clean; state guard
repaired→consistent. pgbench smoke = pre-commit hook.

NEXT: no cheap isolation-spec promotion remains (all 12 are Effort-L). Either
keep laddering horizons (EXPLAIN FORMAT json `Heap Fetches` next) or start
intra-grant-inplace perm1 (reuse 0118-0098 GRANT-xmax-wait on the pg_class row
for ALTER TABLE ADD PRIMARY KEY relhasindex inplace update — but it's only 1 of
11 perms; the rest need a full pg_class catalog-tuple row-lock matrix).
