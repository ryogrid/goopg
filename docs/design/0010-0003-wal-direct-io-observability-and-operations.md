# WAL Direct-I/O & Walsender Ring Observability (M0010)

- status: accepted
- date: 2026-04-29
- supersedes: —

## Goal

Surface the M0010-0001 (`wal_direct_io`) and M0010-0002 (in-memory
WAL ring) runtime state through SQL-queryable views and structured
log lines so an operator can answer two questions without reading
`strace`:

1. "Is `wal_direct_io=on` actually doing direct I/O?" —
   `pg_stat_wal_io.direct_io_active = 't'`, `direct_writes` is
   bumping, `tail_rmw_writes / direct_writes` ratio is the
   alignment-induced overhead.
2. "Is the walsender ring earning its keep?" —
   `pg_stat_wal_io.send_buffer_hits` ≫ `send_buffer_misses`. If
   the miss rate is high, the ring is too small for the sender lag;
   bump `wal_sender_memory_buffer`.

The view + extended `pg_stat_replication` columns + startup log
lines together close the M0010 observability gap.

## File map

| File | Role |
| --- | --- |
| `internal/wal/writer.go` | New `directIOCounters` struct (atomic `directWrites` / `tailRMWWrites`); shared between `Writer` and `state`. `Writer.DirectIOWrites()` / `Writer.TailRMWWrites()` accessors. `state.writeAtDirectIO` bumps both counters per region (RMW counter only when the user range wasn't block-aligned). |
| `internal/wal/mem_ring.go` | New `MemRing.BytesResident()` — current resident byte count under RLock. |
| `internal/initdb/wal_io_views.go` | `registerStatWALIOView(cat, *wal.Writer)` — installs `pg_catalog.pg_stat_wal_io`. Eight columns: `direct_io_active`, `direct_io_fallback_reason`, `direct_writes`, `tail_rmw_writes`, `send_buffer_capacity_bytes`, `send_buffer_bytes_resident`, `send_buffer_hits`, `send_buffer_misses`. Exactly one row when a writer is attached. |
| `internal/initdb/replication_views.go` | `registerStatReplicationView` gains a `*wal.Writer` parameter and three new columns: `send_buffer_hits`, `send_buffer_misses`, `send_buffer_bytes_resident`. Cluster-wide values; every per-sender row carries the same trio (the ring is per-cluster, not per-sender). |
| `internal/initdb/open.go` | Wires both views — passes `walWriter` into `registerStatReplicationView` and calls the new `registerStatWALIOView`. |
| `internal/initdb/wal_io_views_test.go` | View tests: `TestStatWALIOEmptyWithoutWriter`, `TestStatWALIORendersWriterCounters` (capacity / resident move with appends), `TestStatWALIOActiveWhenProbeOk` (active=t when probe succeeded; direct_writes / tail_rmw_writes bump after a small Append). |

## Counter semantics

- **`direct_writes`**: total number of `writeAtDirectIO` regions
  that committed via `O_DIRECT pwrite`. One Append may touch
  multiple regions when its byte range exceeds
  `directIOScratchCap` (1 MiB); each region bumps the counter
  once. Always 0 when `wal_direct_io=off` or the probe fell
  back.
- **`tail_rmw_writes`**: subset of `direct_writes` where the
  user range wasn't already block-aligned and the writer paid
  for a real read-modify-write (head pad and/or tail pad
  non-empty). Compare against `direct_writes` to gauge
  alignment-induced overhead — when the ratio is near 1.0 every
  direct-I/O write is RMW, which is the per-write-RMW phase's
  expected baseline. A future Phase 2.b write-buffering slice
  amortises this and the ratio should drop.
- **`send_buffer_hits` / `send_buffer_misses`**: lifetime
  RecordIterator hits / misses against the ring. Hits never
  perform a syscall; misses fall through to the per-segment
  pread loop. Hit-rate = `hits / (hits + misses)`; aim for
  > 95 % under steady-state replication.
- **`send_buffer_capacity_bytes`**: the
  `wal_sender_memory_buffer` GUC value (in bytes). 0 when the
  ring is disabled. Stable for the writer's lifetime.
- **`send_buffer_bytes_resident`**: current `tail - head` —
  bytes actually in the ring (≤ capacity). Snapshot under
  RLock; coherent against an in-flight Append.

## Per-row vs cluster-wide on `pg_stat_replication`

The ring is a process-wide resource: every walsender reads from
the same buffer. Strict normalisation would put the ring counters
in `pg_stat_wal_io` only and leave `pg_stat_replication`
per-sender. We chose to also surface the trio on every
`pg_stat_replication` row because:

1. Operators already `\watch pg_stat_replication` to track
   sender health; co-locating ring counters means one query
   instead of two.
2. The redundancy is harmless (the values are identical across
   rows) and makes joining with sender state in ad-hoc queries
   trivial.
3. Upstream PG 18 doesn't have these columns at all, so we're
   not breaking compatibility with `pg_stat_replication`'s
   per-sender contract.

## Operator playbook

### "wal_direct_io is enabled but I'm seeing high page-cache pressure"

```
SELECT direct_io_active, direct_io_fallback_reason, direct_writes
FROM pg_stat_wal_io;
```

If `direct_io_active = 'f'`, the probe rejected `O_DIRECT` —
check `direct_io_fallback_reason` (typically "filesystem does
not support O_DIRECT" if pg_wal lives on tmpfs / overlayfs).
Move the data directory to ext4 or XFS.

If `direct_io_active = 't'` but `direct_writes` isn't bumping
under load, the writer isn't seeing traffic — check
`pg_stat_replication` (no senders?) or the WAL writer log.

### "Is the in-memory ring sized right?"

```
SELECT send_buffer_hits, send_buffer_misses,
       send_buffer_bytes_resident, send_buffer_capacity_bytes
FROM pg_stat_wal_io;
```

- Hit rate < 95 % → ring is too small for the sender's lag
  window. Bump `wal_sender_memory_buffer` (32 MiB, 64 MiB) and
  restart.
- `send_buffer_bytes_resident` close to `send_buffer_capacity_bytes`
  AND high miss rate → the ring is full but evicting too fast.
  Same fix.
- `send_buffer_capacity_bytes = 0` and `wal_direct_io = on` →
  misconfiguration. Direct I/O bypasses the page cache, so
  every sender read pays disk cost. Set
  `wal_sender_memory_buffer >= 16 MiB`.

### "When NOT to enable wal_direct_io"

- Single-sender low-throughput setup. The page cache is
  serving the sender just fine; direct I/O adds RMW overhead
  for no benefit.
- Filesystems that don't honour O_DIRECT (tmpfs, overlayfs,
  some FUSE backends). The probe falls back automatically and
  emits `event=wal_direct_io_fallback`, but you also lose the
  ability to compare `tail_rmw_writes / direct_writes` with
  the buffered baseline.
- Workloads where WAL bytes ARE the working set the page
  cache is helping (e.g. crash recovery). Direct I/O is for
  steady-state writes; recovery rereads make the cache useful.

## Tests

- `TestStatWALIOEmptyWithoutWriter` — view registered with
  nil writer yields zero rows. Pins the "view exists, just
  empty" contract.
- `TestStatWALIORendersWriterCounters` — view emits one row
  with the right column shape; `send_buffer_capacity_bytes`
  matches the GUC; `send_buffer_bytes_resident` is 0 before
  the first Append and > 0 after.
- `TestStatWALIOActiveWhenProbeOk` — when DirectIO probe
  succeeds, `direct_io_active = 't'` and a small Append bumps
  both `direct_writes` and `tail_rmw_writes` (a 3-byte payload
  is never block-aligned). `t.Skip` on probe fallback so the
  test passes under tmpfs / overlayfs / non-Linux.

The pg_stat_replication extension rides on the existing
`TestStatReplicationRendersRegisteredSenders` suite — the new
columns are additive, so no test changes required beyond
plumbing the writer parameter (passed as nil in the existing
test, which exercises the no-ring path).

## Cross-references

- `docs/design/0010-0001-wal-direct-io-write-path.md` — the
  direct-I/O subsystem this view observes.
- `docs/design/0010-0002-walsender-in-memory-wal-handoff.md` —
  the ring this view observes.
- `docs/design/0009-0004-aio-observability.md` — the
  `pg_stat_aio` / `pg_aios` shape these counters mirror.
- `docs/design/0005-0003-replication-observability.md` — the
  `pg_stat_replication` view this slice extends.

## Upstream references

- `postgres/src/backend/utils/adt/pgstatfuncs.c` — `pg_stat_*`
  view definitions. PG 18 doesn't have an equivalent
  `pg_stat_wal_io` (no in-tree direct-I/O WAL path); these
  counters are goopg-specific.
- `postgres/src/backend/replication/walsender.c` — upstream's
  walsender flow. The page cache is upstream's implicit "ring";
  goopg's explicit ring + counters are the goopg-specific shape.
