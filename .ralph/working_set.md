(idle — nothing in flight)

Last landed (loop #72): `stats` rung 3 (M0118-0009, design 0118-0125) — made
DROP FUNCTION transactional + finished the function-stats lifecycle. First
divergence advanced **L449 → L1587**. Files: internal/catalog/routines.go
(ResolveByName/ResolveBySig), internal/executor/session.go (DeferredRoutineDrop
+ deferRoutineDrops list/methods), operators_ddl.go (execDropFunction defer +
autocommit stats-drop; execCreateFunction recreate guard; ApplyDeferredRoutine
Drops), operators_tx.go (commit apply / rollback discard / ROLLBACK-TO cancel),
server/dispatch.go (simple-query commit apply), pgstat_functions.go (dropFunction
+ resetSingle/resetAll zero-in-place). Tests: TestDeferredRoutineDropSession,
TestRoutinesResolveForDrop, updated TestFunctionStatsManager. Gates: executor/
catalog/server units + vet PASS; regress-port create_function_sql/drop_if_exists
PASS; TestPort_IsolationStats soft probe L449→L1587; build clean; pgbench smoke
via pre-commit hook.

NEXT rung for `stats` (each Effort-L; pick one; spec stays `defer`):
- **L1587 — `stats_fetch_consistency = 'cache'/'snapshot'`**: within a txn,
  once a backend reads a function stat it must CACHE the value for the rest of
  the txn (perm `s1_fetch_consistency_cache`: s1 reads 1, s2 flushes +1, s1 must
  still read 1, not 2). Needs a per-txn stat-value cache keyed by OID, populated
  on first getter read, cleared at txn end; default 'none' = current live-read.
- L2026 — 2PC stats: `s1_prepare_a`/`s2_rollback_prepared_a` (`PREPARE
  TRANSACTION 'a'` then COMMIT/ROLLBACK PREPARED) — goopg errors "prepared
  transaction … does not exist"; rides 0118-0110 same-backend 2PC.
- Later: relation tuple stats (pg_stat_get_numscans/_tuples_*,
  pg_stat_get_xact_*), SLRU stats (pg_stat_slru).
