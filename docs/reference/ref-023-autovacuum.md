# REF-023: Autovacuum

## Overview

Autovacuum automatically runs VACUUM and ANALYZE on tables based on configurable trigger thresholds. It prevents table bloat and keeps planner statistics up to date without manual intervention.

## goopg Implementation

**Package:** `internal/autovacuum/`

### Key Types

- `Launcher` — periodic ticker that evaluates each table and
  dispatches vacuum/analyze workers.
- `vacuum.Vacuum` — scans all heap pages, reclaims dead tuples,
  and updates visibility.
- `vacuum.Analyze` — scans a sample of heap pages to estimate
  row count and per-column null fraction.

### Launcher Lifecycle

```
Launcher.Run(ctx)
  ├─ ticker fires every NapInterval (default 60s)
  ├─ Load all user tables from catalog
  ├─ For each table:
  │    ├─ If lastVacuumAge > MinVacuumAge → VACUUM
  │    └─ If lastAnalyzeAge > MinAnalyzeAge → ANALYZE
  └─ Sleep until next tick
```

### Trigger Policy

goopg's autovacuum uses time-based throttling:
- `MinVacuumAge` (default 5 min) — minimum time between vacuums.
- `MinAnalyzeAge` (default 5 min) — minimum time between analyzes.
- Tables with `Stats == nil || RowCount == 0` are skipped.

PostgreSQL uses threshold + scale-factor triggers:
- `autovacuum_vacuum_threshold` (default 50 tuples).
- `autovacuum_vacuum_scale_factor` (default 0.2 = 20% of rows).
- `autovacuum_analyze_threshold` (default 50 tuples).
- `autovacuum_analyze_scale_factor` (default 0.1 = 10% of rows).

### Vacuum

`vacuum.Vacuum` iterates every block of the relation:

1. For each block:
   - Pin the buffer page.
   - Collect dead heap slots (tuples with `xmax < OldestXmin`).
   - Reclaim dead slots via `VacuumHeapPageBySlots`.
   - Mark dirty + WAL-log the vacuum.
2. Return `Stats` (pages visited, live tuples, dead tuples).

### Analyze

`vacuum.Analyze` walks every block and counts visible tuples:

1. Begin a ReadCommitted transaction.
2. For each block:
   - Pin the page.
   - Count visible tuples (filtered by the snapshot).
   - Compute average tuple width.
3. Store `TableStats` (row count, pages, avg width) via
   `catalog.SetTableStats`.

## PostgreSQL Implementation

PostgreSQL's autovacuum (`autovacuum.c`) is significantly more
sophisticated:

- **Launcher process** — a dedicated `autovacuum launcher` process
  wakes periodically (default 1 min) and evaluates all databases.
- **Worker process** — the launcher spawns a `autovacuum worker`
  per table. Workers run with a dedicated transaction, cost-delay
  throttling, and anti-wraparound priority.
- **Cost-based delay** — `autovacuum_vacuum_cost_limit` (default
  200) and `autovacuum_vacuum_cost_delay` (default 2 ms) throttle
  I/O during vacuum to avoid saturating the disk.
- **Anti-wraparound** — when `age(relfrozenxid)` exceeds
  `autovacuum_freeze_max_age` (default 200 million), a
  compulsory anti-wraparound vacuum runs even if autovacuum is
  disabled for the table.
- **Per-table overrides** — each table can override global
  autovacuum settings via storage parameters (`autovacuum_enabled`,
  `autovacuum_vacuum_threshold`, etc.).

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Trigger formula | Time-based (MinAge) | Threshold + scale factor |
| Worker spawning | Same goroutine (serial) | Separate worker process per table |
| Cost-based delay | Not implemented | `vacuum_cost_limit` / `vacuum_cost_delay` |
| Anti-wraparound | Not implemented | compulsory freeze vacuum |
| Per-table overrides | Not implemented | Storage parameters |
| Statistics update | After each analyze | After each analyze |
| Logging | Simple INFO lines | Detailed `pg_stat_progress_vacuum` |

## Potential Optimisations or Corrections

- **Dead-tuple-based triggers** (threshold + scale factor) would
  make autovacuum responsive to actual table churn rather than
  wall-clock time.
- **Cost-based delay** would prevent vacuum from saturating disk
  I/O on large tables.

## References

- goopg: `internal/autovacuum/launcher.go`
- goopg: `internal/vacuum/vacuum.go`
- PG autovacuum: `postgres/src/backend/postmaster/autovacuum.c`
- PG vacuum: `postgres/src/backend/commands/vacuum.c`
