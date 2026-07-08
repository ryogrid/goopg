# WAL segment recycling (M0122-0009)

status: accepted (partial — min_wal_size floor only)
date: 2026-07-09
supersedes: none

## Source

`.ralph/fix_plan.md` M0122-0009 ("WAL / recovery / crash-consistency infra"):
WAL segment recycling was one of the bucket's still-open items after
`pg_subtrans` truncation landed earlier the same day
(`docs/design/0122-0009-pg-subtrans-truncation.md`). `internal/wal/retention.go`'s
file header already named the gap explicitly: goopg's v0 retention "follows
the same shape [as upstream], minus min_wal_size preallocation (we delete
instead of recycle-by-rename)".

## Problem

`Writer.RemoveOldSegments` (called by `SlotAwareRetainer.Retain` after every
checkpoint) unlinked every WAL segment strictly before the keep-LSN's
segment. Upstream PostgreSQL instead recycles some of those obsolete
segments — renames them into not-yet-created future segment slots — so a
later `XLogFileInit` for that slot skips the create+zero-fill+directory-fsync
it would otherwise pay synchronously on the write path
(`postgres/src/backend/access/transam/xlog.c:RemoveXlogFile`/
`InstallXLogFileSegment`). Always deleting means every future segment pays
that cost inline in `openSegment`, which can show up as write-latency spikes
under sustained load.

## Upstream reference

`RemoveOldXlogFiles` → `RemoveXlogFile` → `InstallXLogFileSegment`
(`postgres/src/backend/access/transam/xlog.c:3861-4079,3559-3608`):

- `RemoveXlogFile` decides per obsolete segment whether to recycle (rename)
  or remove (unlink), gated on `*endlogSegNo <= recycleSegNo`.
- `InstallXLogFileSegment(&endlogSegNo, tmppath, find_free=true, recycleSegNo,
  tli)` finds the first *free* slot at or after `endlogSegNo` (skipping any
  segno that already has a file) and renames the obsolete file into it,
  bumping `endlogSegNo` on success so the next candidate targets the next
  slot.
- The recycle target ceiling (`recycleSegNo`) comes from `XLOGfileslop`
  (`xlog.c:2230-2268`), which takes the **max** of a `min_wal_size` floor and
  a checkpoint-distance estimate, capped by a `max_wal_size` ceiling.
- Crucially, upstream does **not** zero-fill a recycled segment — it renames
  the file as-is, leaving its previous WAL bytes in place. Recovery relies on
  each record's CRC (`XLogRecordHeader.xl_crc`) to detect "end of valid WAL":
  stale bytes from the segment's previous life are vanishingly unlikely to
  form a record whose CRC matches its own header by coincidence.

## Why goopg's implementation diverges: zero-fill is required

goopg's recovery/replay reader (`internal/wal/reader.go`) does **not** rely
solely on per-record CRC to bound the scan. Its graceful-end-of-WAL
heuristic (`isZeroHeader`/`afterCorruptIsZeroTail`/`isPreallocatedTail`,
all in `reader.go`, doc'd at
`docs/design/0007-0001-wal-segment-preallocation.md` and
`docs/design/0088-0001-...` for the CRC-mismatch-plus-zero-tail case)
distinguishes "corruption" from "clean end of the writer's committed data"
by checking whether the bytes *after* a bad/absent record are all zero.

A segment recycled the upstream way — renamed but left with its old content
— would fail this assumption: the leftover bytes are a **real, well-formed
record from the segment's previous life**, complete with a CRC that
correctly validates against its own (stale) content. `decodeRecord` would
happily decode it as if it were live WAL, and a recovery/standby replay
walking past the writer's true end-of-data would replay stale history as
current. Zero-filling the recycled segment before it re-enters service closes
this gap: `isZeroHeader` sees a clean all-zero region exactly like a
never-touched preallocated segment.

This makes goopg's recycling a "renamed pre-touch of the writer's future
create+zero-fill" (it moves the cost from the write-critical `openSegment`
path to the checkpoint/retention background path) rather than upstream's
"skip both the create and the zero-fill entirely" — a real but narrower slice
of the upstream benefit, chosen for correctness under the existing reader
design rather than changing the reader to a full CRC-only EOS model (a much
larger, higher-risk change out of scope here).

## Implementation

- `wal.Config.MinWALSize` (bytes): new field. `<= 0` (the default) disables
  recycling entirely — every obsolete segment is unlinked, byte-identical to
  pre-M0122-0009 behaviour. Wired from the `min_wal_size` GUC
  (`internal/config/defaults.go`, already registered, previously unread) via
  `internal/initdb/open.go`'s `OpenOptions.WALMinSize` →
  `wal.Config.MinWALSize`, read at startup in `cmd/goopg/main.go` the same
  way `max_wal_size` is (`v.Display()` is the GUC's MB-scaled string; `* 1024
  * 1024` converts to bytes).
- `state.removeOldSegments` (`internal/wal/writer.go`) now returns
  `(removed, recycled int, err error)`. Obsolete segments are sorted
  newest-first (mirrors upstream's approach of recycling segments closest to
  the current write position first) and the first
  `ceil(MinWALSize/SegmentSize)` of them are recycled via the new
  `recycleSegmentFile` helper (rename + `preallocateSegment` zero-fill +
  fsync, reusing the exact zero-fill routine `openSegment` already uses for
  brand-new segments); the rest are unlinked as before.
- The recycle target slot is the lowest-numbered free segment at or after
  `keepSeg`, found by linearly probing `existing[nextTarget]` — the direct
  analog of `InstallXLogFileSegment`'s `find_free` scan — so a recycle can
  never clobber a segment that's still in use or already recycled earlier in
  the same pass.
- One directory fsync at the end of the pass (only if anything was recycled)
  makes the renamed dirents durable, matching `openSegment`'s existing
  post-creation directory fsync for brand-new segments.
- `Writer.RemoveOldSegments` and `SlotAwareRetainer.Retain`
  (`internal/wal/retention.go`) thread the new `recycled` count through;
  the retention summary log gained a `segments_recycled` field alongside the
  existing `segments_removed`.

## Non-goals / deferred

Only the `min_wal_size` floor half of upstream's `XLOGfileslop` sizing is
implemented. Not implemented (ledger row filed in
`.ralph/deferral_ledger.md`):

- The checkpoint-distance-estimate term (`CheckPointDistanceEstimate` /
  `CheckPointCompletionTarget`) that lets upstream recycle *more* than the
  `min_wal_size` floor when write volume is high.
- The `max_wal_size` ceiling on the recycle target (upstream never recycles
  past `maxSegNo`; goopg's `spares` count is driven by `MinWALSize` alone, so
  a very large `min_wal_size` setting could recycle further ahead than
  upstream would allow under a small `max_wal_size`). In practice this only
  changes how many segments are held as *recycled spares* vs *unlinked* — it
  does not affect correctness, since either an obsolete segment is retired
  and its future segno is created by `openSegment` when first needed, or it
  is pre-touched via recycling; the total on-disk footprint difference is
  bounded by `min_wal_size`.
- Timeline-aware recycling / `RemoveNonParentXlogFiles` (no timeline-switch
  support yet in goopg).
- `wal_recycle`-style opt-out GUC (goopg's off-switch is
  `min_wal_size <= 0`, which also matches upstream's own `min_wal_size=0`
  meaning "always remove, never recycle" per `XLOGfileslop`'s `minSegNo`
  floor collapsing to `endLogSegNo - 1`).

## Tests

- `TestRemoveOldSegmentsRecyclesUpToMinWALSize` (`internal/wal/retention_test.go`):
  5 segments, `MinWALSize` = 2 segments, keep the last 2 → confirms exactly 2
  of the 3 obsolete segments are recycled (newest first) into the first free
  slots after the keep segment, the oldest is unlinked, and — the load-bearing
  correctness check — the recycled files' bytes are genuinely all zero, not
  the donor segment's leftover WAL content.
- `TestRemoveOldSegmentsRecycleCapExceedsObsoleteCount`: `MinWALSize` larger
  than the obsolete segment count → every obsolete segment is recycled, none
  unlinked, no panics from the smaller-than-cap case.
- Pre-existing `TestRemoveOldSegments*` tests (`Config{}` with
  `MinWALSize` unset, i.e. 0) continue to pass unchanged, pinning the
  recycling-disabled default.

## Gates

`go build ./...` clean; `go vet ./...` clean; `go test ./internal/wal/...`
and `go test -race ./internal/wal/... ./internal/mvcc/...` PASS;
`go test ./internal/initdb/...` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`
PASS (0 failed transactions, all 3 pgbench workloads).
