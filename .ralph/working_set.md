(idle — nothing in flight)

Loop #30 COMPLETE + COMMITTED: M0118-0009 temp-schema-cleanup.spec PROMOTED to
pass (runIsoSpecStrict) — both permutations byte-for-byte vs PG 18.3. Landed
perm-2 process-exit (design 0118-0093): pg_terminate_backend(pg_backend_pid())
self-termination (executor.ErrSelfTerminate → FATAL "terminating connection due
to administrator command" + close), backend-exit temp cleanup ordered before
advisory-lock release (Server.cleanupSessionTempObjects defer after
ReleaseAllAdvisoryLocks = LIFO runs first), Context.TerminateBackend peer path
(cancelReg.terminateByPID), harness lib/pq connection-death rendering.

Next loop: pick another M0118-0009 spec — horizons (EXPLAIN FORMAT json ANALYZE
Heap Fetches + IOS + temp-prune horizon; dollar-quote lexer), intra-grant-inplace
{,-db} (FOR UPDATE on virtual pg_class/pg_database rows + GRANT tuple-xmax lock +
VACUUM FREEZE inplace wait), stats (pg_stat_* infra), prepared-transactions{,-cic}
(2PC). Or other open M0118 groups: M0118-0002 (predicate-gin/gist/hash AMs),
M0118-0004 (deadlock-parallel lock-group), M0118-0005 (fk-deadlock, ri-trigger,
fk-partitioned), M0118-0007 (eval-plan-qual EPQ-over-join).
