(idle — nothing in flight)

Last loop (#65) COMPLETE + committed: `stats` spec enabler rung 1 (M0118-0009,
design 0118-0123). `stats.spec` is the LAST of the 4 remaining failed isolation
specs (all Effort-L unbuilt subsystems: also predicate-gin/predicate-gist =
GIN/GiST AMs + new types; deadlock-parallel = parallel-worker lock groups).

This loop: every `stats` permutation aborted in global setup at
`SELECT pg_stat_force_next_flush()` (42883 — pg_proc seed had the rows but
`evalFuncCall` had no case → fell to `evalStoredRoutineFuncCall`), and the
`SET track_functions/track_counts/stats_fetch_consistency` steps hit
unregistered GUCs. Landed: (1) registered those 3 GUCs in
`internal/config/defaults.go` (+ `postgresql.conf.sample` lines for the M0108
`TestSampleConfigCoversRegistry` parity gate) mirroring guc_tables.c;
(2) `pg_stat_force_next_flush()` + `pg_stat_clear_snapshot()` → faithful void
no-ops in `evalFuncCall` (`internal/executor/expr.go`, return
`NewStringDatum("")`). First divergence advanced perm-0 setup-failure → first
permutation's `pg_stat_get_function_calls does not exist`. Spec stays defer.

Files: internal/config/{defaults.go, postgresql.conf.sample, stats_gucs_test.go},
internal/executor/{expr.go, pg_stat_flush_test.go}, docs/design/0118-0123 +
README, fix_plan, ledger.

NEXT rung for `stats` (each Effort-L; pick one per loop):
- runner: echo a global/session SETUP query's result block (PG's isolationtester
  prints the setup `SELECT pg_stat_force_next_flush()` block once before steps;
  goopg's runner does not — this is the L4 divergence). Probe via a throwaway
  `internal/testport/zz_probe_test.go` running RunAndCompare on stats.spec.
- function stats: a cluster-global per-function counter store keyed by func OID,
  incremented on user-function invocation gated by `track_functions`, +
  `pg_stat_get_function_calls/total_time/self_time(oid)` +
  `pg_stat_reset_single_function_counters(oid)`. This is the heart of the spec.
- then: relation tuple stats + pg_stat_get_xact_*; pg_stat_reset(); real
  stats_fetch_consistency snapshot caching; 2PC stats (rides 0118-0110).

Gates run this loop: build clean; TestPgStatFlushSnapshotVoidNoops +
TestStatsGUCs + TestSampleConfigCoversRegistry PASS; go test
./internal/config/ ./internal/executor/ PASS; ralph-state-guard (pending);
pgbench smoke = pre-commit hook.
