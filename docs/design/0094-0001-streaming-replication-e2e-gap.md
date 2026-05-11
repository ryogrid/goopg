# 0094-0001 — Streaming Replication E2E Gap

**Status:** draft
**Date:** 2026-05-11
**Milestone:** M0094-0001

## Problem

`TestE2E_PhysicalReplication` in `internal/testport/e2e_replication_test.go` is
hard-skipped with two reasons:

1. **No pre-clone hook.** `replcluster.Setup()` starts the primary, then
   immediately clones the data directory to the standby. Any SQL run after
   `Setup()` returns (e.g., `CREATE TABLE`, `INSERT`) generates WAL records that
   should stream to the standby — but if those records also touch system catalog
   pages that were not part of the base clone (e.g., a freshly allocated
   `pg_class` row page), the standby must replay those heap-insert records to
   reconstruct the catalog. The test needs to run setup SQL *before* the clone to
   guarantee the table exists in the base snapshot.

2. **DDL WAL record handling uncertain.** Even with the pre-clone hook, the WAL
   stream replayer on the standby must correctly replay all WAL record types
   emitted by DDL. These include heap inserts into system catalog tables
   (`pg_class`, `pg_attribute`, etc.), heap page initialisation (`FPI` records),
   and B-tree index inserts for system indexes.

## Root Cause Analysis

`replcluster.Options` has no field for a pre-clone callback. `Setup()` calls
`startPrimary()` and immediately `cloneToStandby()`. There is no window to run
SQL.

The WAL stream replayer (`internal/wal/stream_replayer.go`) calls
`writer.ApplyRecord()` for every WAL record received from the primary. The
`ApplyRecord` dispatcher in `internal/wal/recovery.go` handles the record kinds
known at the time it was written. Unknown record kinds are silently skipped.
Whether DDL-related record kinds (heap inserts into catalog tables, index inserts
for system indexes) are covered needs to be audited.

## Chosen Design

### 1. Pre-Clone Hook in replcluster

Add one field to `replcluster.Options`:

```go
// PreCloneHook is called after the primary starts but before the standby
// data directory is cloned. Use it to create tables or populate data that
// the standby should inherit via the base backup.
PreCloneHook func(conn *Conn) error
```

`Setup()` calls `hook(primaryConn)` (if non-nil) between `startPrimary()` and
`cloneToStandby()`. The hook receives a `*Conn` already connected to the primary.
On hook failure, `Setup()` returns the error unchanged and stops.

This is backward-compatible: callers that do not set `PreCloneHook` see no
behaviour change.

### 2. WAL Replayer Audit

Audit `recovery.go::ApplyRecord` against the full set of `RecordKind*` constants
in `internal/wal/format.go`. For each kind:

- **Already handled:** `RecordKindHeapInsert`, `RecordKindHeapDelete`,
  `RecordKindHeapHotUpdate`, `RecordKindXactCommit`, `RecordKindXactAbort`,
  `RecordKindCheckpoint`, `RecordKindHeapPruneOpt`, `RecordKindPageImage` (FPI).
- **Silently skipped (verify safety):** `RecordKindBtreeInsert`,
  `RecordKindBtreeSplit`, `RecordKindBtreePageDel`, `RecordKindHeapVacuum`.

For each skipped kind, verify that skipping on the standby is correct:
- `RecordKindBtreeInsert` / `RecordKindBtreeSplit` / `RecordKindBtreePageDel`:
  Physical replication replays the underlying heap and FPI records, so index
  pages are reconstructed via full-page images. Skipping the high-level btree
  records is safe if every split/insert is preceded by an FPI record (which
  goopg emits unconditionally). **Confirm this invariant.**
- `RecordKindHeapVacuum`: Vacuum's dead-tuple removal writes heap-update records
  for the affected pages. Skipping the vacuum-marker record is safe; the page
  changes come through as heap records. **Confirm.**

If any kind is found to be incorrectly skipped, fix `ApplyRecord` and add a
targeted unit test in `internal/wal/recovery_test.go`.

### 3. Un-Skip TestE2E_PhysicalReplication

Replace the `t.Skip(...)` call with a real test body:

```go
// PreCloneHook creates the table on the primary before the clone.
hook := func(conn *Conn) error {
    _, err := conn.Exec(ctx, "CREATE TABLE repl_t (id int)")
    return err
}
rc, _ := replcluster.New("e2e_phys_repl", replcluster.Options{
    // ...
    PreCloneHook: hook,
})
rc.Setup()
defer rc.Stop()

// Insert on primary after standby is streaming.
runSQLSimple(t, rc.Primary, "INSERT INTO repl_t VALUES (42)")

// Wait up to 15 s for standby to replay the insert.
var lastErr error
for i := 0; i < 30; i++ {
    time.Sleep(500 * time.Millisecond)
    rows, err := rc.Standby.Query(ctx, "SELECT id FROM repl_t WHERE id = 42")
    if err == nil && len(rows) > 0 {
        return // pass
    }
    lastErr = err
}
t.Fatalf("standby never saw the row after ~15s: %v", lastErr)
```

## Key Files

| File | Change |
|------|--------|
| `internal/testutil/replcluster/replcluster.go` | Add `PreCloneHook` to `Options`; call it in `Setup()` |
| `internal/wal/recovery.go` | Audit `ApplyRecord`; fix any incorrectly-skipped kinds |
| `internal/testport/e2e_replication_test.go` | Un-skip `TestE2E_PhysicalReplication`; add hook |

## Tests

- `TestE2E_PhysicalReplication` (un-skipped) — primary/standby end-to-end.
- Regression: existing `replcluster_test.go` tests still pass (nil hook → no-op).
- Unit: if any `ApplyRecord` fix is needed, add a targeted case in
  `internal/wal/recovery_test.go`.

## PostgreSQL Reference

- `postgres/src/backend/replication/walsender.c` — WAL sender main loop,
  `XLogSendPhysical`.
- `postgres/src/backend/replication/walreceiver.c` — standby WAL reception.
- `postgres/src/backend/access/transam/xlog.c` — `RmgrTable`, record dispatch.
