# REF-023: Autovacuum

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### Cost-Based Delay

PostgreSQL throttles autovacuum I/O via a cost-based delay
system:

1. Each vacuum operation has a **cost** (per page read = 1,
   per page dirtied = 20, per page written during FPI = 10).
2. When the accumulated cost exceeds
   `autovacuum_vacuum_cost_limit` (default 200), vacuum sleeps
   for `autovacuum_vacuum_cost_delay` (default 2 ms).
3. The sleep is distributed across the vacuum operation (every
   few pages).

This prevents autovacuum from saturating the disk and impacting
concurrent queries.

goopg does not implement cost-based delay. Vacuum runs at full
speed, which can cause I/O spikes.

### Anti-Wraparound Autovacuum

PostgreSQL tracks `relfrozenxid` per table — the oldest XID
that has not been frozen. When `age(relfrozenxid)` exceeds
`autovacuum_freeze_max_age` (default 200 million), a compulsory
anti-wraparound vacuum is triggered, even if autovacuum is
disabled for the table.

The anti-wraparound vacuum freezes all tuples older than the
freeze horizon, allowing the XID space to be reused.

goopg does not freeze tuples or track `relfrozenxid`.

### Per-Table Storage Parameters

PostgreSQL allows per-table configuration of autovacuum via
storage parameters:

```sql
ALTER TABLE t SET (autovacuum_enabled = false);
ALTER TABLE t SET (autovacuum_vacuum_threshold = 1000);
ALTER TABLE t SET (autovacuum_vacuum_scale_factor = 0.1);
```

Parameters override the global GUC defaults for that table.
goopg does not support per-table autovacuum parameters.

### Worker Process Pool

PostgreSQL's autovacuum launcher (`autovacuum.c`) spawns
dedicated worker processes (`autovacuum worker`). Each worker
is a separate OS process that:

1. Is assigned the next table to vacuum/analyze.
2. Opens its own database connection.
3. Performs the vacuum/analyze.
4. Exits when done.

The launcher limits the number of concurrent workers via
`autovacuum_max_workers` (default 3).

goopg's autovacuum runs in the launcher's goroutine directly
(serial, no worker pool).

### Dead-Tuple Tracking

PostgreSQL tracks the number of dead tuples per table via the
statistics collector (`pg_stat_user_tables.n_dead_tup`). This
counter is incremented on UPDATE/DELETE and reset on VACUUM.
The autovacuum trigger formula uses `n_dead_tup` directly:

```
vacuum threshold = autovacuum_vacuum_threshold +
                   autovacuum_vacuum_scale_factor × reltuples
```

goopg does not track dead tuple counts. The launcher uses
time-based scheduling.

## goopg Improvement Analysis

### P1: Dead-Tuple Tracking

Add a dead-tuple counter to `catalog.TableStats`. Increment on
UPDATE/DELETE. Reset on VACUUM. Use the PG trigger formula
instead of time-based scheduling.

**Impact:** Autovacuum responds to actual table churn, not
wall-clock time.

### P2: Cost-Based Delay

Add a cost counter in `vacuum.Vacuum`. Accumulate costs per
page operation. When the cost limit is exceeded, sleep for the
delay duration.

**Impact:** Prevents autovacuum from saturating disk I/O.

### P2: Anti-Wraparound

Track `relfrozenxid` per table. When the freeze age exceeds
`autovacuum_freeze_max_age` (or a goopg-specific threshold),
trigger a compulsory vacuum that freezes tuples.

**Impact:** Prevents XID wraparound on long-running deployments.

## References

- goopg: `internal/autovacuum/launcher.go`
- goopg: `internal/vacuum/vacuum.go`
- PG autovacuum: `postgres/src/backend/postmaster/autovacuum.c`
- PG vacuum: `postgres/src/backend/commands/vacuum.c`
- PG cost delay: `postgres/src/backend/commands/vacuum.c`
  (`vacuum_delay_point`)
- PG freeze: `postgres/src/backend/access/heap/heapam.c`
  (`FreezeTuple`, `lazy_scan_heap`)
