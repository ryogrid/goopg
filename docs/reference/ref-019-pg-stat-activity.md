# REF-019: pg_stat_activity & Wait Events

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### pgstat Shared Memory

PostgreSQL's pgstat subsystem (`pgstat.c`) maintains backend
status in a shared memory array (`PgBackendStatus`). Each
backend has a dedicated slot in the array, indexed by its
`backend_id`. The slot contains:

- `st_procpid` — OS PID.
- `st_activity` — current query text (up to
  `pgstat_track_activity_query_size` bytes, default 1024).
- `st_state` — `STATE_UNDEFINED`, `STATE_IDLE`,
  `STATE_RUNNING`, `STATE_IDLEINTRANSACTION`,
  `STATE_IDLEINTRANSACTION_ABORTED`, `STATE_FASTPATH`, etc.
- `st_wait_event_info` — current wait event (uint32 encoding).
- `st_xact_start`, `st_query_start`, `st_activity_start`,
  `st_state_start` — timestamps.

The array is read by `pg_stat_activity`'s `VirtualRows`
callback without locking (readers tolerate stale values).

goopg's `Registry` uses a `sync.RWMutex`-guarded map. This
serialises all snapshot reads with all updates.

### Wait Event Encoding

PostgreSQL encodes wait events as a single `uint32`:

```
Bit 31-24: Wait class (PG_WAIT_IO = 0x0A, PG_WAIT_LOCK = 0x03, …)
Bit 23-16: Reserved
Bit 15-0:  Event ID
```

Functions:
- `pgstat_get_wait_event_type(wait_event_info)` — extracts the
  class and returns a string (`"IO"`, `"Lock"`, etc.).
- `pgstat_get_wait_event(wait_event_info)` — extracts the event
  ID and returns the event name.

goopg uses two separate string fields (`wait_event_type` and
`wait_event`). This adds per-row storage overhead but is
simpler to implement.

### Dynamic Wait Event Registration (PG 17+)

PostgreSQL allows extensions to register custom wait events via
`WaitEventExtensionNew(name)`. The dynamic event is assigned a
unique event ID at registration time. This is used by extensions
like pg_stat_statements and PostGIS.

goopg's wait events are hard-coded in the `activity` package.

### Non-Client Backend Types

PostgreSQL reports 20+ backend types in `pg_stat_activity`:

```
client_backend, autovacuum_launcher, autovacuum_worker,
checkpointer, bgwriter, walwriter, walreceiver, walsender,
logical_apply_worker, logical_launcher,
logical_parallel_apply_worker, startup, archiver,
archiver_cleanup, stats_collector, slot_migration_worker,
replication_slot_cleanup_worker, wal_summarizer,
WAL background writer, parallel_worker
```

goopg currently supports `client_backend`, `checkpointer`, and
`walwriter`.

## goopg Improvement Analysis

### P2: Lock-Free Snapshot

Replace the RWMutex with a lock-free snapshot mechanism:
maintain two copies of the backend map (active and snapshot).
Readers atomically swap the snapshot pointer; writers update
the active map.

**Impact:** Eliminates read-lock contention on pg_stat_activity
queries.

### P2: Dynamic Wait Events

Implement a dynamic wait event registry using a `sync.Map`.
Allow `RegisterWaitEvent(type, name)` from any package.

**Impact:** Extensible wait event system for future features.

## References

- goopg: `internal/activity/activity.go`
- PG backend status: `postgres/src/backend/utils/activity/backend_status.c`
- PG wait event: `postgres/src/backend/utils/activity/wait_event.c`
- PG wait event names: `postgres/src/backend/utils/activity/wait_event_names.txt`
