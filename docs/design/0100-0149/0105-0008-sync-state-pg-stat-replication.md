# 0105-0008 — `pg_stat_replication.sync_state` parity for sync E2E test

**Status:** accepted  
**Date:** 2026-05-20  
**Milestone:** M0105-0008 / M0102-0007 (goopg→PG sync failover E2E)

---

## Problem

`TestE2E_FailoverGoopgToPG/sync_remote_apply` failed at
`waitForPhysicalStreamingGoopgToPG` with:

```
physical replication did not reach streaming state within 45s (requireSync=true)
```

The test waits for:
```sql
SELECT sync_state FROM pg_stat_replication WHERE application_name = 'pg_standby'
```
to return `'sync'`. `pg_stat_replication.sync_state` was hard-coded to `"async"`
in `internal/initdb/replication_views.go:109`.

## Root cause

`registerStatReplicationView` had no knowledge of the `SyncRep` instance.
The comment at line 6 noted `sync_state` as a field "v0 doesn't track yet".

## Fix

Three files changed:

### `internal/wal/syncrep.go`

Added two helper methods on `syncRepRule` (unexported):

- `syncStateFor(appName) string` — returns `"sync"` for the first `count`
  names in FIRST mode, `"sync"` for any listed name in ANY mode,
  `"potential"` for listed-but-non-active names, `"async"` otherwise.
- `syncPriorityFor(appName) int` — 1-indexed position for FIRST,
  1 for any listed in ANY, 0 otherwise.

And two public methods on `*SyncRep`:

- `SyncStateFor(appName string) string` — thread-safe wrapper.
- `SyncPriorityFor(appName string) int` — thread-safe wrapper.

### `internal/initdb/replication_views.go`

- `registerStatReplicationView` signature gains `syncRep *wal.SyncRep`.
- `syncState(syncRep, appName)` and `syncPriority(syncRep, appName)` helper
  funcs replace the hard-coded `"async"` / `"0"` literals.
- Both helpers tolerate `syncRep == nil` (returns `"async"` / `"0"`) so
  the existing test call site `registerStatReplicationView(cat, senders, nil, nil)`
  compiles without further changes.

### `internal/initdb/open.go`

Call site updated: `registerStatReplicationView(cat, walSenders, walWriter, syncRep)`.

## Tests

Unit tests added to `internal/wal/syncrep_test.go`:
- `TestSyncStateForFirstMode` — bare-FIRST-1 rule: a=sync, b=potential, c=async.
- `TestSyncStateForAnyMode` — ANY-1: listed names are sync, unlisted are async.
- `TestSyncPriorityFor` — FIRST-1 priority ordering.
- `TestSyncStateForBareList` — `"pg_standby"` bare list → `"sync"`.

E2E gate:
- `TestE2E_FailoverGoopgToPG/async` — PASS (no regression)
- `TestE2E_FailoverGoopgToPG/sync_remote_apply` — **PASS** (was FAIL)
