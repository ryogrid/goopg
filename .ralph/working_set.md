Loop #46 COMPLETE: M0118-0009 — `timeouts.spec` PROMOTED to pass-required
(soft→strict), design 0118-0107. Committing + pushing.

What landed (zero engine change — test promotion + docs):
- internal/testport/isolation_port_test.go: new TestPort_IsolationTimeouts
  using runIsoSpecStrict on postgres/src/test/isolation/specs/timeouts.spec.
  Found via a throwaway zz_probe_test.go that ran the remaining deferred
  M0118-0009/0002/0004/0005 candidates and reported divergence cost — timeouts
  was already byte-identical (the only pass among the probed set). statement_timeout
  / lock_timeout vs table-level (LOCK TABLE) + row-level (DELETE behind concurrent
  UPDATE) lock waits; 8 permutations; shorter timeout fires first → 57014 stmt /
  55P03 lock; blocked steps (*)-marked upstream (10ms may fire before tester sees
  "waiting"); goopg runner's 300ms blocking threshold is independent so output stable.
- docs/design/0118-0107-timeouts-spec-promotion.md + README index row.
- CSV D-002 rationale appended; postgres-oracle-port-status.md regenerated via
  `go run ./cmd/gen-oracle-port-status`.
- fix_plan.md M0118-0009 entry updated.

Gates run (PASS): TestPort_IsolationTimeouts strict 8/8 perms, stable across
-count=3 then -count=5 (8 runs total); go build ./... clean; go vet
./internal/testport clean. Test-only change (no executor/codec path) → pgbench
smoke = pre-commit hook.

NEXT (remaining M0118, all Effort-L distinct unbuilt subsystems):
- intra-grant-inplace (pg_class): runtime shared-catalog MVCC-tuple row locks
  (ALTER TABLE ADD PRIMARY KEY <waiting> behind FOR KEY SHARE on pg_class).
- stats: pg_stat_force_next_flush + cumulative function-stats +
  stats_fetch_consistency + 2PC interaction.
- prepared-transactions{,-cic}: 2PC (PREPARE/COMMIT PREPARED) — also gates stats.
- Non-0009: deadlock-parallel (lock groups + "language internal" SQL funcs),
  fk-partitioned-1/2 (ATTACH PARTITION + partitioned FK), index-only-bitmapscan
  (BitmapOr plan), predicate-gin/gist (AM granularity + int-array/point types).
Probe results (difflen) for reference: deadlock-parallel 113 (lang internal),
predicate-gin 97 (int-array {1} parse), stats 101 (pg_stat_force_next_flush
missing), prepared-transactions 2434, intra-grant-inplace 2294.
