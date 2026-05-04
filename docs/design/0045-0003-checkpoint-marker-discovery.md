# 0045-0003 — Checkpoint Marker Discovery (No `pg_control`)

**Status:** draft
**Parent milestone:** M0045
**Date:** 2026-05-04

## 1. Objective

Find the LSN of the most recent checkpoint marker in the
retained WAL, without depending on a `pg_control` file. M0045-0002's
recovery driver needs that LSN as the starting point of the
replay phase; M0045-0001 has just removed the segment-0
requirement, so we are free to scan whatever segments are on
disk.

## 2. Why no pg_control yet

`internal/initdb/initdb.go:6` explicitly notes that `pg_control`
isn't written:

```go
// no system catalog or write a pg_control file — those land
// alongside [later milestones]
```

`internal/server/replication.go:75-76` confirms the placeholder
status:

```go
// lives in pg_control's `system_identifier` field; until the
// pg_control file lands in initdb, a fixed placeholder keeps
```

Adding pg_control is M0030 / M0014 territory and out of scope
here. Recovery must work against whatever durable state exists,
which is "the WAL stream itself".

## 3. Approach: scan retained WAL backwards

The retention contract guarantees the segment containing the
most recent checkpoint LSN is preserved (see
`internal/wal/retention.go::SlotAwareRetainer.Retain` — `keepLSN`
is computed from the latest checkpoint LSN, and segments
strictly before the segment containing `keepLSN` are unlinked).

Recovery walks the retained segments **in reverse** (newest
first), reading record headers, and stops at the first
checkpoint-record-type tag it sees. That LSN is the
`lastCkptLSN` for M0045-0002's replayer.

Pseudo-code:

```go
func discoverLastCheckpointLSN(walDir string, segSize int64) (uint64, error) {
    segNos, err := listSegmentNumbers(walDir)   // sorted ascending
    if err != nil { return 0, err }
    if len(segNos) == 0 {
        return 0, errors.New("wal: no segments — fresh cluster, nothing to recover")
    }
    // Walk newest segment first, then older ones, until we find
    // a checkpoint marker.
    for i := len(segNos) - 1; i >= 0; i-- {
        ckptLSN, found, err := scanForCheckpointInSegment(walDir, segNos[i], segSize)
        if err != nil { return 0, err }
        if found {
            return ckptLSN, nil
        }
    }
    return 0, fmt.Errorf(
        "wal: no checkpoint marker found in retained WAL " +
        "(consider --reset; data may be unrecoverable)")
}
```

Each segment scan reuses the existing record-iteration logic
(`internal/wal/iterator.go`), reading from the segment's start
to its end and remembering the LATEST LSN at which a record of
type "checkpoint" was found.

## 4. Identifying a checkpoint record

`internal/wal/format.go` (and adjacent files) defines the WAL
record-type tag enum. The implementor of M0045-0003 must:

1. Locate the existing checkpoint record type — search
   `internal/wal/` for `Checkpoint` / `CHECKPOINT_*` / `XLOG_CHECKPOINT_*`.
   Goopg already emits them via `internal/wal/checkpointer.go`
   when a checkpoint completes.
2. Document the record-type byte / tag in this design doc once
   identified (during implementation, not now — the implementor
   has direct file access).

The scan returns the LSN of the *start* of the latest checkpoint
record found in the segment.

## 5. Worked example

Run-007 hard-kill, retained segments `[0x23F .. 0x4D2]`:

- Walk segment 0x4D2 (last; partially full): scan its records
  for a checkpoint tag. Latest-checkpoint-in-segment is the
  candidate.
- If 0x4D2 has a checkpoint: return that LSN. Done.
- If 0x4D2 has no checkpoint (e.g., new segment opened just
  before the kill): walk 0x4D1, then 0x4D0, …, until found.

In practice the checkpoint cadence (`checkpoint_timeout = 15
min` for the TPC-H tests) means a checkpoint marker exists every
~16 MiB of WAL at SF=1 schema-build pace. A backwards scan of at
most 1-2 segments will find one.

## 6. Performance

Cost is bounded by the size of one or two segments
(16 MiB each by default). For the TPC-H workload that's a
sub-second cost on commodity hardware, and the operation runs
exactly once at startup. No special optimisation needed in v0.

If the cost becomes prohibitive in the future (e.g., if checkpoint
cadence is raised so a backwards scan of many segments is
plausible), an obvious optimisation is to write a tiny "last
checkpoint LSN" file alongside `pg_wal/` — but that's effectively
the start of pg_control, so the right answer is M0030 rather
than a v0 hack.

## 7. Failure modes

- **No checkpoint marker found in any retained segment**.
  Possible causes:
  - Operator manually deleted segments containing the marker
    (treat as data corruption; abort with diagnostic).
  - Cluster was killed before the very first checkpoint
    completed (almost impossible — `initdb` writes a startup
    checkpoint marker before returning).
  - Bug in the retainer that unlinked too aggressively (would
    indicate a regression in M0005 work).
  Recovery aborts with a clear error message recommending
  `--reset` or external WAL-archive recovery (not yet
  implemented). The cluster is **not** silently brought up
  with potentially-incorrect state.

## 8. Verification

- `internal/wal/recovery_test.go::TestDiscoverLastCheckpointLSN_LatestSegment`
  — synthesised WAL with a checkpoint marker in the last segment.
- `internal/wal/recovery_test.go::TestDiscoverLastCheckpointLSN_OlderSegment`
  — synthesised WAL where only an older segment has the marker.
- `internal/wal/recovery_test.go::TestDiscoverLastCheckpointLSN_NoMarker`
  — asserts the diagnostic error message.

## 9. Out of scope

- Persisting `pg_control` (separate milestone).
- Compatible interpretation of upstream PostgreSQL pg_wal
  segment files (M0014).
- Replay itself (M0045-0002 covers it).
