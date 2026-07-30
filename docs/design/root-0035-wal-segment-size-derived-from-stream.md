# root-0035 — startup replay must use the cluster's wal_segment_size, not 16 MiB

**Status:** implemented (2026-07-28)
**Area:** `internal/wal` (reader / recovery entry points)
**Regression closed:** `TestRestartAfterRetention` (`internal/server`), pass-required,
red at HEAD since `fa90714a` (root-0032)

## Symptom

`go test -run TestRestartAfterRetention ./internal/server/` failed
deterministically (1.9 s) at HEAD:

```
initdb.Open: goopg: wal replay: wal: replay record 0 lsn[301990201,301990520]:
  wal: xlog heap-insert apply: storage: not enough free space in page
```

The test builds a cluster with **1 MiB** WAL segments
(`retTestSegSize`, `initdb.OpenOptions.WALSegmentSize`), fills WAL until
retention prunes segments 0..17, hard-kills the server, and restarts it.

The LSN in the message is the tell. The checkpoints in the same log sit at
`17207361` and `18882449` — segment 16 and segment 18 of a **1 MiB** stream
(and `segments_removed=16` then `2` confirms that numbering). But replay's
record 0 claims LSN `301990201 = 18 × 16 MiB + 313`: the same segment number,
multiplied by the **default 16 MiB** segment size. Every replayed LSN was
**16× too large**.

## Root cause

`readAllUncached` anchors the decoded stream at

```go
baseOffset := firstSegNo * uint64(segmentSize)   // internal/wal/reader.go
```

and reports each record's `StartLSN`/`EndLSN` relative to it, so `segmentSize`
must be the cluster's real `wal_segment_size`. Three recovery entry points
passed `0` and every one of them resolved `0` to `DefaultSegmentSize` (16 MiB):

| caller | passed |
|---|---|
| `initdb.Open` → `wal.ReplayFromDirWithMgr(mgr, pg_wal, 0)` (`open.go:341`) | 0 |
| `initdb.replayCLogFromWAL` / `walHasXactRecords` → `wal.ReadAll(walDir, 0)` | 0 |
| `wal.DiscoverLastCheckpointLSN(walDir, 0)` | 0 |

Nothing on that path ever learns the size: `OpenOptions.WALSegmentSize` was
consumed only by the *writer* config (`open.go:388`), and pg_control's
`SegSize` is hardcoded to 16 MiB at bootstrap (see Deferred below).

The inflated LSN is not merely a cosmetic label — it **disarms replay's
idempotency check**. Redo skips a record when the target page's `pd_lsn` is
already ≥ the record's `EndLSN`. With every record LSN 16× too large, no page
ever looks up-to-date, so startup re-applied inserts the running server had
already applied and the page overflowed: `ErrNoSpaceInPage`. The cluster was
unstartable.

### Why root-0032 exposed it

The bug predates root-0032; root-0032 made the broken path *reachable*. Before
it, the stream began at the live run's first segment without skipping a leading
`XLP_FIRST_IS_CONTRECORD` payload, so decoding that segment's continuation
bytes as a record header produced garbage and the walk terminated with zero
records — the wrong-base LSNs were never used. root-0032 added the
contrecord skip (upstream's `XLogReadRecord`/`XLogFindNextRecord` behavior), so
real records finally decoded — against the wrong base.

## Fix

Derive the segment size from the stream itself, which is exactly what upstream
does when a WAL reader has no cluster context. `pg_waldump`'s
`search_directory()` reads the first file's first page and takes
`longhdr->xlp_seg_size`, validating it with `IsValidWalSegSize`
(`postgres/src/bin/pg_waldump/pg_waldump.c:250`); `XLogReaderValidatePageHeader`
cross-checks the same field against `wal_segment_size`.

goopg already writes that field correctly per cluster: every segment file
begins on a segment boundary, so `buildPageHeader` (`xlog_emit.go`) always
emits the **long** form there with `SegSize = uint32(segSize)` from the
writer's configuration.

Changes, all in `internal/wal`:

1. **`reader.go` — `IsValidWalSegSize(int64) bool`** — power of two in
   [1 MB, 1 GB], mirroring
   `postgres/src/include/access/xlog_internal.h`.
2. **`reader.go` — `detectSegmentSizeAt(walDir, segNo) int64`** — reads the
   40-byte long page header at the head of `segNo`, returns its `xlp_seg_size`,
   or `0` when unreadable / not a long header / fails `IsValidWalSegSize`.
3. **`reader.go` — `readAllUncached`** — the `segmentSize <= 0` default moved
   *below* `firstAvailableSegment` and now resolves in priority order:
   caller-supplied size → `detectSegmentSizeAt(walDir, firstSegNo)` →
   `DefaultSegmentSize`. Detection reads the **live run's** first segment, the
   same segment the stream is anchored on.
4. **`recovery.go` — `ReplayFromDirWithMgr` / `DiscoverLastCheckpointLSN`** —
   stop coercing `0` to `DefaultSegmentSize` before calling `ReadAll`, so `0`
   reaches the resolver and means "the cluster's own `wal_segment_size`".

No caller signature changed, so all ~30 catalog-recovery modules that call
`ReadAll(walDir, 0)` are fixed by the same change and stay mutually consistent
(they also keep sharing the `segmentSize == 0` recovery memoization in
`recovery_cache.go`, which an explicit-size plumbing fix would have bypassed).

A cluster built with the default 16 MiB is byte-for-byte unaffected: detection
returns 16 MiB, the value the old code assumed.

## Verification

- `TestRestartAfterRetention` (`internal/server`) — red at HEAD, green after.
- New `internal/wal/reader_segment_size_test.go`:
  - `TestReadAllDerivesSegmentSizeFromStream` — writes a 1 MiB-segment stream,
    deletes the leading segments (the retention state), and asserts
    `readAllUncached(dir, 0)` returns records whose `StartLSN`/`EndLSN` are
    identical to the explicit-size decode **and** to the LSNs the writer
    actually handed out. Negative control: with detection stubbed out it fails
    (`decoded 1921 records, explicit decoded 3000`).
  - `TestIsValidWalSegSize` — pins the accepted bounds.
- `go test ./internal/wal/ ./internal/initdb/ ./internal/server/` — pass.
- `go test -race ./internal/wal/` — pass.

## Deferred

`initdb.WriteBootstrapWAL` hardcodes `wal.DefaultSegmentSize` for the bootstrap
segment's name, file length, `xlp_pageaddr` and `xlp_seg_size`
(`internal/initdb/wal_bootstrap.go:52,64,74`), because `initdb.Init` has no
segment-size option at all — only `OpenOptions.WALSegmentSize` does, and that
is an Open-time setting. Upstream's `BootStrapXLOG` uses `wal_segment_size`
throughout. Consequence: on a freshly initdb'd non-default cluster, before
retention removes the bootstrap segment, `detectSegmentSizeAt` reads that
segment and reports 16 MiB. That window is no worse than the pre-fix behavior
(which assumed 16 MiB unconditionally), and it closes as soon as the bootstrap
segment ages out. Ledger row filed; the real fix is to thread a segment size
through `initdb.Init` (and a `--wal-segsize` CLI flag) so the cluster is
consistent from byte 0.
