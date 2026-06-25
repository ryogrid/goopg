(idle — nothing in flight)

Last landed (loop #71): COMMITTED stats rung 2 (M0118-0009, design 0118-0124) —
cumulative function-statistics subsystem (pgstat_functions.go) + isolation-runner
global-setup tuple-result echo (isolation_runner.go). Verification basis for the
commit: the runner change is provably output-only — execConnSetupCapture returns
""  for every COMMAND_OK (DDL/DML) setup, so setupResult is empty and output is
byte-identical for all `port` specs (a port spec whose setup returned rows would
already have been failing pre-change, since isolationtester.c echoes them too).
Build clean; executor+config units PASS; partial isolation run (TestPort_Isolation
Suite 2.47s + 10 dedicated) all PASS, 0 FAILs. Full 118-test serial dedicated suite
exceeds the go-test timeout — an infra limit, not an assertion failure.

NEXT rung for `stats` (each Effort-L; pick one per loop), spec stays `defer`:
- uncommitted-DROP cross-session visibility (L449 first divergence): a function
  dropped inside an open txn must stay callable by other sessions until commit —
  needs per-session MVCC catalog (shared gap with alter-table-4).
- 2PC stat-drops (rides 0118-0110); stats_fetch_consistency snapshot/cache models;
  relation tuple stats (pg_stat_get_numscans/_tuples_*, track_counts gating,
  pg_stat_get_xact_*); SLRU stats (pg_stat_slru).
