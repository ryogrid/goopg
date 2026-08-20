(idle — nothing in flight)

Last completed: M0134-0037 (join_hash.sql) PARKED and committed
(866cbc26 impl + 48e68067 bookkeeping) — sized at HEAD via
`scripts/pg-regress-runner.sh --verbose join_hash` (no env-sensitivity):
1001 raw diff lines / 21 `^+ERROR` / 0 `^-ERROR`, five root-cause buckets.
Shipped the one CONTAINED bucket (A2, 4/21 errors): `json_extract_path`,
`json_extract_path_text`, `jsonb_extract_path`, `jsonb_extract_path_text`
were seeded in pg_proc but had no case in the executor builtin-function
dispatch (`internal/executor/expr.go`) — added shared path-walk primitive
`jsonPathStep` (mirrors PG's `get_path_all`/`get_worker`,
postgres/src/backend/utils/adt/jsonfuncs.c) and reused `evalJSONArrow`'s
rendering tail (factored into `jsonElemAsJSONDatum`/`jsonElemAsTextDatum`,
pure refactor). Verified live: `hash_join_batches('select 1')` no longer
42883s on the missing builtin.

Left unshipped, all ledgered 2026-08-20 under M0134-0037:
- Bucket A (DOMINANT, ~71% of errors): `RETURNS TABLE` plpgsql function
  used unaliased in FROM fails 42703 on explicit OUT-column refs at plan
  time, and even `SELECT *` returns NULL values — engine-wide (repro'd
  with a throwaway 2-line function), traced to
  `internal/optimizer/planner.go`'s `isSimpleSingle` fast path (~1001-1054)
  but not pinned to one line. **Top re-arm priority** — needs a live-debug
  trace session (print `b.table.Columns` post-`planScanRangeVar` vs what
  `resolveColumnRefAt` sees), likely affects other regress cases using
  `RETURNS TABLE` too.
- Bucket B: no Parallel Hash/Parallel Hash Join executor node (REFACTOR,
  sibling of M0134-0023's parallel-worker gap).
- Bucket C: FULL JOIN planned Merge not Hash — same cost-model territory
  as the CLOSED M0126 dead end (`q9_costdriven_mhj_cannot_be_cost_forced`);
  do not re-attempt without new evidence.
- Bucket D: correlated-subplan-as-HashCond planning + EXPLAIN VERBOSE
  subplan rendering (REFACTOR, engine-wide).
- Bucket E: LATERAL subquery's nested JOIN ON-clause can't see the
  lateral sibling's column — likely CONTAINED once traced, not
  investigated to a fix location this loop.
Design doc not needed (mechanical, same size class as prior shipped
buckets — thin wrapper around an existing rendering primitive).

Next loop: per fix_plan.md banner, select M0134-0038 (json.sql, status
`failed`) — same sizing pattern as 0006..0037 (researcher sizes at HEAD
first, confirm not stale, bucket root causes CONTAINED vs REFACTOR-tier,
ship the smallest CONTAINED bucket or PARK with ledger rows). Also worth
noting: Bucket A above (RETURNS TABLE column resolution) may resurface
independently in json.sql/jsonb.sql if either uses RETURNS TABLE
functions — check for it opportunistically while sizing.

Gates run this loop: go build ./... PASS; go test
./internal/executor/... ./internal/parser/... ./internal/catalog/... PASS;
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS (worker
round); live-server smoke of all 6 AC queries + AC-8 PASS (worker round);
pgbench pre-commit smoke PASS on both commits; make ralph-state-guard TBD
(run before finishing this loop).

In-flight: none.
