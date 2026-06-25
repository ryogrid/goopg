(idle — nothing in flight)

Last loop (#52) COMPLETE: M0118-0009 — prepared-transactions PROMOTED to pass-required
(design 0118-0112). SSI dangerous-structure check at PREPARE TRANSACTION time; PREPARED
peer treated as committed-first in conflict hooks. All 1500 perms byte-identical to PG
18.3 (TestPort_IsolationPreparedTransactions strict PASS, 137s; -race ./internal/mvcc/...
green). Committed + pushed.

Remaining M0118-0009 (Effort-L unbuilt subsystems): intra-grant-inplace (pg_class rowmark
locking; perm1 done in 0118-0109), stats (pg_stat_* cumulative subsystem on top of 2PC).
Other failing M0118 specs: index-only-bitmapscan, predicate-gin/gist, deadlock-parallel,
fk-partitioned-1/2 (distinct unbuilt subsystems).
