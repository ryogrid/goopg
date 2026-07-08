# pg_subtrans truncation (M0122-0009)

status: accepted
date: 2026-07-09
supersedes: none

## Source

`.ralph/fix_plan.md` M0122-0009 ("WAL / recovery / crash-consistency infra"):
`pg_subtrans` truncation was one of four named, fully unstarted items in the
bucket. `docs/design/0117-0003-pg-subtrans-restore-on-restart.md`'s "Known
follow-ups" line already flagged it: "no `pg_subtrans` truncation yet."

## Problem

`internal/mvcc.SubxactMap` (subxact_visibility.go) and its on-disk mirror
`internal/mvcc.SubtransSLRU` (subxact_slru.go) persist every subxact→parent
link for the lifetime of the process:

- `SubxactMap.parents`/`aborted` are plain Go maps with no eviction — every
  `Register`/`MarkAborted` call only ever adds entries.
- `SubtransSLRU` has `SetParent`/`GetParent`/`ScanParents` but no removal
  primitive — every `pg_subtrans/<segno>` segment file created since initdb
  stays on disk forever.

Both the in-memory map and the on-disk SLRU directory therefore grow without
bound on a long-lived cluster. This mirrors the pre-M0117-0001 CLOG gap (G1),
already fixed for `pg_xact/` via `CLog.TruncateCLOG`, wired from the
checkpointer's `TruncateCLOGFn` (`internal/initdb/open.go`).

## Upstream reference

`postgres/src/backend/access/transam/subtrans.c:TruncateSUBTRANS(oldestXact)`:

```c
void
TruncateSUBTRANS(TransactionId oldestXact)
{
    int cutoffPage = TransactionIdToPage(oldestXact);

    if (!SlruScanDirectory(SubTransCtl, SlruScanDirCbReportPresence, &cutoffPage))
        return; /* nothing to remove */

    SimpleLruTruncate(SubTransCtl, cutoffPage);
}
```

Called only from `xlog.c` (`CreateCheckPoint`/`CreateRestartPoint`) with
`GetOldestTransactionIdConsideredRunning()` as the horizon — i.e. every
checkpoint, not vacuum-driven. Unlike CLOG truncation, PG does **not**
WAL-log SUBTRANS truncation: `pg_subtrans` is disposable across a crash
(`StartupSUBTRANS` simply zeroes the active page on restart), so there is
nothing to make durable-ordered.

## Change

**`internal/mvcc/subxact_slru.go`**: `SubtransSLRU.TruncateBefore(oldestXact)`
— unlinks `pg_subtrans/<segno>` segment files whose highest page strictly
precedes the SLRU page containing `oldestXact`, mirroring `clog.go`'s
`truncateSLRUSegments` (same open-and-remove structure, reusing the existing
`parseSLRUSegName` helper from the same package). New
`SubtransPagePrecedes(page1, page2)` is `CLOGPagePrecedes`'s twin scaled to
`subtransXactsPerPage` (2048 vs. CLOG's 32768) — the wraparound-aware page
comparator `SimpleLruTruncate` needs. No WAL record is emitted, matching
upstream's non-durable treatment.

**`internal/mvcc/subxact_visibility.go`**: `SubxactMap.Truncate(oldestXact)`
— under `mu`, deletes every `parents`/`aborted` entry whose subxid strictly
precedes `oldestXact` (via `storage.XIDPrecedes`, wraparound-safe), then
(when persistence is enabled) calls `slru.TruncateBefore(oldestXact)`. A nil
`slru` (persistence never enabled) is a no-op after the in-memory prune —
matches every other `SubxactMap` method's nil-safety contract.

**`internal/wal/checkpointer.go`**: new `CheckpointerConfig.TruncateSubtransFn
func() error`, invoked from `runCheckpoint` immediately after
`TruncateCLOGFn`, with the same best-effort treatment (a non-nil error is
logged as a warning and does not fail the checkpoint — pg_subtrans is
reconstructible/disposable, unlike a `FlushCLOGFn` failure).

**`internal/initdb/open.go`**: wires `TruncateSubtransFn` to call
`subxactMap.Truncate(horizon)` using the exact same `horizon =
min(datfrozenxid, OldestXmin)` computation `TruncateCLOGFn` already uses.

### Why reuse the CLOG horizon instead of computing
`GetOldestTransactionIdConsideredRunning()` separately

Any subxid below the CLOG horizon belongs to a top-level transaction that
committed or aborted long enough ago that CLOG itself no longer needs its
detailed status (it's frozen in every user heap, or below `OldestXmin`). Once
a top-level transaction is that old, its final CLOG status is a direct
`Committed`/`Aborted` — never `SubCommitted` — so no visibility check will
ever need to walk that subxid's parent chain again. Reusing the CLOG horizon
is therefore always at least as conservative as PG's own
`GetOldestTransactionIdConsideredRunning` (which tracks only currently-running
transactions, a tighter/more aggressive bound); it truncates no more than
upstream would, and reusing an existing, already-tested horizon computation
avoids introducing a second, subtly-different one.

## Non-goals

- WAL logging the truncation — upstream doesn't either (see "Upstream
  reference" above).
- Rebasing/renumbering surviving XIDs — like CLOG, the SLRU stays
  XID-indexed; `GetParent` for a truncated-away XID simply reads back
  `InvalidTransactionID` (0), which is the correct "not a subxact" answer for
  any XID a caller should no longer be asking about.

## Tests

`internal/mvcc/subxact_truncate_test.go`:
- `TestSubtransSLRUTruncateBefore` — links spanning two SLRU segments
  (`subtransXactsPerSegment` = 65536), truncate at a cutoff inside the second
  segment; asserts the fully-below segment file is unlinked, the
  straddling/above segment survives byte-for-byte, truncated-away `GetParent`
  reads back `InvalidTransactionID`, and repeat/older-cutoff calls are no-ops.
- `TestSubxactMapTruncate` — end-to-end through `SubxactMap.Truncate` with
  persistence enabled: below-horizon entries vanish from both the in-memory
  map and a freshly-opened on-disk reader; at/above-horizon entries (parent
  link + abort status) survive.
- `TestSubxactMapTruncateNoPersistence` — `Truncate` on a pure in-memory map
  (persistence never enabled) prunes correctly and returns no error rather
  than panicking on a nil `slru`.

`internal/wal/checkpointer_test.go`:
- `TestCheckpointerCallsTruncateSubtransFn` — invoked once per successful
  checkpoint, alongside `TruncateCLOGFn`.
- `TestCheckpointerTruncateSubtransFnErrorIsNonFatal` — a hook error is
  logged as a warning and does not fail `runCheckpoint`.

## Gates run

- `go build ./...` clean.
- `go vet ./internal/mvcc/... ./internal/wal/... ./internal/initdb/...` clean.
- `go test ./internal/mvcc/... ./internal/wal/... ./internal/initdb/...` PASS.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
  failed transactions, all 3 pgbench workloads).
