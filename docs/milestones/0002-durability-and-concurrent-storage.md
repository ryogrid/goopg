# Milestone 0002 — Production-Grade Checkpointing & Concurrent B-tree

**Status:** planned
**Depends on:** Milestone 0001 (foundational, pgbench-able server)
**Blocks:** Milestone 0003 (TPC-H benefits significantly from real concurrent B-tree)

## Context

Milestone 0001 brought up a server that runs `pgbench`. The checkpointing and B-tree implementations there are deliberate placeholders: a checkpoint goroutine on a fixed interval (per `GOAL_AND_REQUIREMENTS.md` §5.3) and a B-tree sufficient for pgbench's index needs. Both are correctness-first, performance-second. This milestone replaces them with PG-compatible, production-grade implementations.

## In Scope

### Checkpointing

Implement PG-compatible checkpoint mechanics:

- GUCs: `checkpoint_timeout` (default 5min), `checkpoint_completion_target` (default 0.9), `max_wal_size`, `min_wal_size`, `full_page_writes` (default on). Names, units, and defaults must match upstream per `REQUIREMENTS.md` §6.2.
- Spread/smoothed checkpoint writes over the configured fraction of the next checkpoint cycle, rather than one synchronous burst.
- Triggers: timeout, WAL volume crossing `max_wal_size`, explicit `CHECKPOINT` SQL command, and the dedicated CLI subcommand introduced under `GOAL_AND_REQUIREMENTS.md` §3.3 if checkpoint-on-demand is exposed there.
- Full-page-image WAL records for the first modification of a page after each checkpoint when `full_page_writes` is on.
- Statistics surfaced through `pg_stat_bgwriter` / `pg_stat_checkpointer` (whichever upstream version goopg is targeting; see the `server_version` decision recorded under M1).

Reference reading in the oracle: `postgres/src/backend/access/transam/xlog.c`, `postgres/src/backend/postmaster/checkpointer.c`, `postgres/src/backend/storage/buffer/bufmgr.c` (for the dirty-page flushing path).

### Concurrent B-tree

Replace the M1 B-tree with a concurrent design based on Lehman-Yao with PG's modifications:

- High keys plus right-sibling pointers so descents don't require holding parent locks.
- Per-page latches (Go `sync.RWMutex` on each buffered page) for short critical sections; coupling/crabbing only where required (e.g., during splits).
- Atomic page splits with proper WAL logging and replay.
- Concurrent inserts, point lookups, and range scans without a global tree lock.
- Index-only scans where the visibility map permits.
- Vacuum integration: B-tree page deletion and recycling consistent with MVCC visibility.

Reference reading: `postgres/src/backend/access/nbtree/`, especially `nbtree.c`, `nbtinsert.c`, `nbtsearch.c`, and the README at `postgres/src/backend/access/nbtree/README` (the README is the single highest-leverage document for this subsystem).

## Out of Scope

- Other index types (GIN, GiST, BRIN, hash, SP-GiST) — deferred.
- Parallel B-tree builds. The build path remains single-threaded; concurrency in this milestone is about runtime access.
- `CREATE INDEX CONCURRENTLY`. Desirable but deferred.
- Replication-aware restart points. Standby support is not yet built; restart-point design must not be precluded but need not be implemented here.

## Required Design Docs

Place under `docs/design/` with sequential numbering at creation time:

- `NNNN-checkpointing.md`
- `NNNN-btree-concurrency.md`
- `NNNN-wal-full-page-images.md` (only if the existing M1 WAL design doc does not already cover full-page writes)

## Definition of Done

1. `pgbench` at `-c 32 -j 8` (or higher, on suitable hardware) runs to completion with stable throughput. CPU/contention profiles must show no global B-tree mutex as the dominant bottleneck.
2. The GUCs `checkpoint_timeout`, `checkpoint_completion_target`, `max_wal_size`, `min_wal_size`, and `full_page_writes` are honored with PG-compatible semantics. Each has a regression-style test that perturbs it and observes the expected behavior.
3. The `CHECKPOINT` SQL command performs a synchronous checkpoint. The CLI subcommand equivalent (per `REQUIREMENTS.md` §3.3) does the same.
4. `pg_stat_bgwriter` (or its upstream successor in the targeted version) is queryable and returns non-trivial statistics during a workload.
5. A crash-recovery test passes: kill the server with `SIGKILL` mid-workload, restart, and verify both physical consistency (no torn pages because of full-page writes) and logical consistency (committed transactions survive, in-flight ones don't).
6. Both required design docs are merged with status `accepted` and link forward from any M1 docs they refine.
