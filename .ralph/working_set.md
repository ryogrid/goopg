(idle — nothing in flight)

Last landed (loop #73): `stats` rung 4 (M0118-0009, design 0118-0126) —
implemented `stats_fetch_consistency = 'cache'/'snapshot'` per-transaction
stat-value caching. First divergence advanced **L1587 → L2036**.
Files: internal/executor/pgstat_functions.go (funcStatSnapshot type, copyAll,
fetchFuncStat single read entry point), internal/executor/session.go
(BasicSession.statsSnapshot + ensureStatsSnapshot/ClearStatsSnapshot; cleared in
EndExplicitTransaction), internal/executor/expr.go (3 getters → fetchFuncStat;
pg_stat_clear_snapshot now clears). Test: TestFetchFuncStatConsistency.
Gates: executor+config units + vet PASS (also -race); TestPort_PLpgSQL* PASS;
TestPort_IsolationStats soft probe L1587→L2036; build clean; pgbench smoke via
pre-commit hook.

Key insight: snapshot/cache distinction only observable across statements of the
same txn with a concurrent flush between → only inside an explicit txn. In
autocommit / 'none' the getters read live (trivial single-read perms unchanged).

NEXT rung for `stats` (each Effort-L; spec stays `defer`):
- **L2036 — 2PC stats**: `s1_prepare_a` / `s{1,2}_{commit,rollback}_prepared_a`
  (`PREPARE TRANSACTION 'a'` then COMMIT/ROLLBACK PREPARED) — goopg errors
  "prepared transaction … does not exist"; rides 0118-0110 same-backend 2PC.
- Later: relation tuple stats (pg_stat_get_numscans/_tuples_*,
  pg_stat_get_xact_*, L2130+), SLRU stats (pg_stat_slru).
