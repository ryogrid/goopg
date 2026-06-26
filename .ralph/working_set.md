Last landed (loop #75): `stats` rung 6 (M0118-0009, design 0118-0128) — relation
tuple statistics. First divergence advanced **L2180 → L2704**. All 7 non-2PC
table-stats permutations + the 2PC COMMIT PREPARED permutations match PG 18.3.

Files: internal/executor/pgstat_relations.go (NEW: relationStatsManager `relStats`
+ recordScan/Insert/Update/Delete + flush/get/dropTable/resetAll + shouldTrackCounts
+ recordRelScan + tableOIDFromCatalog), internal/executor/pgstat_relations_test.go
(NEW), internal/executor/expr.go (9 pg_stat_get_* relation getters; flush+reset
also drive relStats), internal/executor/operators_storage.go (seqScanOp.statReturned
+ Close record; scanMatching gains statOID param + 4 call sites; insert/update/
deleteOp.Close record rowsAffected), internal/executor/operators_ddl.go
(dropTableByRefImmediate → relStats.dropTable). Docs: design 0118-0128 + README row +
fix_plan note.

Gates: go test ./internal/executor/ PASS; new pgstat_relations_test.go PASS;
TestPort_IsolationStats soft probe L2180→L2704; build clean; pgbench smoke
445305 txns 0 failed; tpch-spotcheck infra-FAIL (stale systemd scope, known WSL2
issue — query output unchanged by counter-only hooks).

Key model: numscans/tuples_returned NON-transactional (counted as scans run);
ins/upd/del + live/dead deltas recorded at op.Close (= "applied at commit" for the
autocommit simple-query path). INSERT +1 live/row; DELETE +1 dead −1 live/row;
UPDATE +1 dead/row, live unchanged (no HOT). Getters return 0 (not NULL) for absent
OID. track_counts (boot on) gates all counting.

NEXT rung for `stats` (Effort-L; spec stays `defer`): **L2704 — transactional-counter
abort/2PC reconciliation for relation stats.** The new first divergence is the
`s1_begin … s1_prepare_a … s1_rollback_prepared_a` permutation (expected
live=1/dead=8, current 3/6). Needs:
- Stage tuples_inserted/_updated/_deleted + live/dead deltas PER TRANSACTION
  (mirror PG_Stat_TableXactStatus), fold into shared at commit, and on
  abort/ROLLBACK PREPARED: aborted insert+update tuples become DEAD (no live
  increment); follow AtEOXact_PgStat_Relations rules incl. the `truncdropped` path
  for in-txn TRUNCATE/DROP (later permutations L2775/L2815/L2861/L2901/L2947).
- 2PC handoff of the staged relation counters to the prepared txn so cross-backend
  COMMIT/ROLLBACK PREPARED applies them (mirror function-stats 2PC, design 0118-0127).
- This requires replacing the op.Close "apply immediately" model with per-txn
  staging for the transactional counters (non-transactional scan counters stay as-is).
Then later: index-scan tuples_fetched, VACUUM vacuum_count/live-dead recompute, SLRU
stats (pg_stat_slru).
