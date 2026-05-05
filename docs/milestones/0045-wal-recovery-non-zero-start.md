# Milestone 0045 — Crash recovery from non-zero starting WAL segment

**Status:** planned
**Depends on:** Milestone 0002 (concurrent B-tree durability /
correctness baseline), Milestone 0005 (slot-aware WAL retention —
the producer of the segment files we now have to learn to recover
from).
**Drives:** unblocking any operator who hard-kills goopg in the
middle of a long-running workload (TPC-H benchmark runs that hit
the harness timeout, ad-hoc testing, OOM-killer kills, host
shutdowns), and enabling integration tests that exercise crash-
and-restart cycles.

## Context

While running TPC-H against goopg under HammerDB (run-007 in
`analysis/tpch-hammerdb-run-007.md`), the test harness was
hard-killed mid-power-test by the orchestration timeout. The next
attempt to restart goopg with `bench/tpch/setup_goopg.sh` (no
`--reset`) failed:

```text
goopg start: goopg: wal: wal: first segment is 00000000000000000000023F,
expected 000000000000000000000000
```

`0x23F = 575`: WAL segments 0..574 had been recycled by the
slot-aware retainer (`internal/wal/retention.go`) during normal
operation, leaving segment 575 as the first on-disk segment when
the process was killed. The next restart's
`detectWritePos` (`internal/wal/writer.go:838-906`) hard-rejects
any data directory whose first segment isn't 0:

```go
if segNos[0] != 0 {
    return 0, 0, fmt.Errorf("wal: first segment is %s, expected %s",
        formatSegmentName(segNos[0]), formatSegmentName(0))
}
```

This is contradictory with the retention contract. Retention's
entire purpose is to unlink WAL segments that are no longer
needed for crash recovery — every segment strictly before the
last checkpoint LSN is fair game. After a single checkpoint cycle
on a busy cluster, segment 0 will not exist on disk. Requiring
segment 0 on restart means goopg becomes un-restartable after any
non-trivial workload.

PostgreSQL handles this via `pg_control`: it records the latest
checkpoint LSN, and recovery starts scanning from the segment
containing that LSN — segment 0 hasn't existed on disk for years
on any real cluster. Goopg's
`internal/initdb/initdb.go:6` explicitly notes that pg_control
isn't written yet ("no system catalog or write a pg_control file
— those land alongside [later milestones]"). This milestone fixes
the immediate restart bug without requiring the full pg_control
machinery.

## Goals

1. **`detectWritePos` accepts any contiguous segment-number
   range**, not just `[0, N]`. Gap detection still applies for
   the segments that *are* on disk (e.g., presence of segments 575
   and 577 but not 576 must still error, because that's actual
   corruption).

2. **`writePos` is the absolute LSN convention**. Segment 0 lives
   at byte offset 0; segment K lives at byte offset `K · segSize`.
   The fix is `writePos = firstSegNo * segSize +
   bytesUsedInLastSeg` instead of accumulating from segment 0.

3. **`prevRecPtr` reconstruction continues to work** —
   `scanLastSegmentEnd` only reads the LAST segment, so dropping
   pre-checkpoint segments doesn't affect it.

4. **Post-last-checkpoint WAL records are correctly handled**:
   either replayed against the buffer pool (if their effects
   weren't yet on disk at kill time) or skipped as redundant (if
   the buffer pool already received them). Goopg's WAL-before-data
   invariant in `internal/wal/checkpointer.go` guarantees the
   second case for pages flushed by the most recent checkpoint;
   the first case is the open question for the milestone.
   `StreamReplayer.Run` is the canonical replay driver; it is
   idempotent (replication standbys depend on this) so re-applying
   already-applied records is safe.

5. **Last-checkpoint-LSN discovery without pg_control**. We don't
   yet have a control file. Recovery walks the LATEST retained
   segment backwards (not segment 0 forwards) and re-iterates from
   the checkpoint marker found nearest the segment tail. The
   retention contract guarantees the segment containing the most
   recent checkpoint LSN is preserved.

6. **Integration test** reproduces the run-007 failure
   deterministically: load some data, fire `> N` checkpoints to
   drive retention, hard-kill goopg, restart, read the data back,
   assert byte-for-byte equality with what was written before the
   kill.

## Non-goals

- **Persisting pg_control** — separate milestone (M0030 catalog
  persistence and DDL WAL, M0014 PostgreSQL-compatible WAL
  on-disk format). Recovery here uses on-WAL-stream checkpoint
  markers only.
- **Torn-write recovery from arbitrary mid-record kills** — the
  existing `scanLastSegmentEnd` EOS-sentinel logic already
  handles it; this milestone does not regress that behaviour.
- **Full PostgreSQL recovery state machine** (`StartupPass1`,
  `redo`, `consistent recovery point`, `pg_resetwal` parity).
- **Compatible interoperation with upstream PostgreSQL pg_wal
  segment files** — M0014 territory.

## Required Design Docs

- `0045-0001-detect-write-pos-from-non-zero-segment.md` — the
  exact patch to the segment-loop in `detectWritePos` (the
  immediate fix for the run-007 failure).
- `0045-0002-restart-replay-of-post-checkpoint-records.md` — the
  recovery model: what records are replayed at startup,
  what guarantees `StreamReplayer.Run` already provides, and how
  the WAL-before-data invariant bounds what could possibly be
  un-applied at kill time.
- `0045-0003-checkpoint-marker-discovery.md` — how recovery
  finds the latest-checkpoint LSN without pg_control: walk the
  newest segment backwards, look for the checkpoint record
  type tag, fall back across previous retained segments if the
  newest segment has no checkpoint marker (e.g., a fresh
  segment opened just before the kill).
- `0045-0004-integration-test-kill-and-restart.md` — the
  regression test that closes the milestone.

## Definition of Done

1. `internal/wal/writer.go::detectWritePos` accepts a first
   segment number > 0; new unit tests cover the firstSeg=N case
   for several N including the run-007 value 0x23F.
2. `internal/wal/writer.go::detectWritePos` returns an absolute
   `writePos` matching the byte-offset-since-LSN-zero convention
   (segment K starts at byte K·segSize).
3. New helper `discoverLastCheckpointLSN(walDir, segSize)` walks
   the retained WAL and returns the LSN of the latest checkpoint
   marker, or an error pointing at `--reset` if none is found.
4. Startup wires `StreamReplayer.Run(checkpointLSN, endLSN)` into
   the recovery flow. Re-applying already-applied records is a
   no-op (idempotency confirmed by existing replication tests).
5. New integration test
   `internal/server/restart_after_retention_test.go` reproduces
   the run-007 failure on the pre-fix code (`go test … -run
   TestRestartAfterRetention` fails on `master`) and passes after
   the fix.
6. End-to-end TPC-H regression: re-run the run-007 hard-kill
   scenario (HammerDB power test mid-flight, hard-kill goopg,
   restart, query the SF=1 dataset). No data loss; no
   un-restartable cluster. Documented in
   `analysis/tpch-hammerdb-run-008.md` (or the next run report).
7. `TestTPCHResultParity` still identical=22 divergent=0
   errored=0.

## Workflow

1. Land `M0045-0001` — `detectWritePos` segment-loop fix +
   focused unit tests. This alone unblocks the run-007 restart
   if no replay is needed.
2. Land `M0045-0002` — `discoverLastCheckpointLSN` helper +
   unit tests over crafted WAL streams.
3. Land `M0045-0003` — wire `StreamReplayer.Run` into startup;
   end-to-end test that exercises a kill-after-write-before-flush
   path proves correctness of the replay phase.
4. Land `M0045-0004` — `restart_after_retention_test.go`
   integration test as the milestone-closing regression.
5. Land `M0045-0005` — TPC-H end-to-end regression for the
   run-007 reproducer.

Each landing is a self-contained commit on `perf-analysis`.
