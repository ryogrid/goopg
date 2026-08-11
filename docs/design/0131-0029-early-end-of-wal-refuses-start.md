# 0131-0029 — an early end of WAL must refuse the start, not warn

Milestone: M0131-S30.2
Date: 2026-08-12
Status: implemented

## The problem

Every stop of goopg's replay walk was reported as a clean end of WAL. The walk
logged a WARN (`endOfWAL`, `internal/wal/reader.go`) and returned the prefix it
had managed to decode; `initdb.Open` replayed that prefix and reported a
successful start. Both row-loss defects of the S30 series presented exactly that
way:

| defect | signature in the startup log | rows lost |
|---|---|---|
| S30.1 (`0131-0022`) | `reason="invalid record header" detail="padding bytes nonzero (0xff 0x07)"` | 4416 / 500000 |
| S30.1b (`0131-0028`) | `reason="invalid page header" detail="magic=0x0020 want 0xd118"` | 6762 / 500000 |

In both cases the truncation was in the log all along, the cluster came up
green, and the next append overwrote the surviving records — making the loss
permanent. The WARN was load-bearing evidence that nothing consumed.

## Why upstream does not need this

PostgreSQL treats a failed record as end of redo too
(`report_invalid_record`/`ReadRecord`,
`postgres/src/backend/access/transam/xlogrecovery.c`), and that is safe there:
a record is durable only if every byte before it was flushed first
(`ReserveXLogInsertLocation` + `XLogFlush`,
`postgres/src/backend/access/transam/xlog.c`), so "valid WAL behind invalid
WAL" is not a state the insert/flush protocol can produce. In goopg it is —
both S30 defects were writers leaving a hole — so the same rule that is
conservative upstream is silent data loss here.

## The rule

A walk stop is a real end of WAL only when nothing durable lies behind it.
`durableWALAfter` (`internal/wal/reader_early_end.go`) scans forward from the
first page boundary at or after the stop and accepts a page as durable evidence
only when **both** halves hold:

1. its header validates against the address it is stored at
   (`xlogPageValidator.check` — magic, long/short header placement, segment
   size and, decisively, `xlp_pageaddr`), which is what rejects the stale
   contents of a recycled segment; and
2. the first record position on that page carries a record whose own `xl_crc`
   checks out over the bytes `extractRecordBytes` assembles (`recordStartsAt`).

That is the same evidence `durableUnknownRecord` already uses to tell a real
record from crash garbage. A false positive costs a 2^-32 CRC collision on top
of a self-consistent page header; a false negative is the status quo — silent,
permanent loss of committed work.

When the scan finds something, `readAllPageAware` returns `ErrEarlyEndOfWAL`
instead of the truncated prefix, `initdb.Open` wraps it (`goopg: wal replay:
%w`, `internal/initdb/open.go:382`) and the server does not start.

Zeroed pages are **skipped, not stopped on**: a hole is precisely what the scan
is looking for. That also makes the ordinary all-zero page header that ends the
walk a checked stop (`stopQuiet`) rather than an unconditional break — with no
WARN, since it is the normal way a preallocated segment ends.

## Escape hatch

`GOOPG_WAL_ALLOW_EARLY_END=1` downgrades the refusal to a loud WARN and replays
the prefix. Refusing is the right default, but it must not brick a cluster whose
only remaining option is "start and accept the loss" — the role `pg_resetwal`
plays upstream.

## Guards

`internal/wal/reader_early_end_test.go`:

- `TestReadAllRefusesEarlyEndOfWAL` — a record torn in the middle of a
  three-page stream, pages behind it intact: `ReadAll` returns
  `ErrEarlyEndOfWAL`, and with the env override set it returns the (shorter)
  prefix instead.
- `TestReadAllAcceptsTrueEndOfWAL` — the ordinary crash tail (torn record,
  zero fill behind it) is still a clean end of WAL. Without this the refusal
  would make every crash an unstartable cluster. It tears a **page-internal**
  record on purpose: a record straddling a page boundary carries 24 bytes of
  the next page's header inside its byte range, and `xlp_rem_len` is not
  covered by the record CRC, so flipping a byte there is a no-op.
- `TestDurableWALAfterIgnoresZeroTail` — the classifier itself over an
  all-zero remainder.

`TestReadAll_StopsAtStalePageAddr` (`recycled_segment_test.go`) was updated in
the same change: it used to falsify a single page's `xlp_pageaddr`, which under
the new rule is a hole (valid pages behind a stale one) rather than a recycled
tail. A real recycled segment carries the previous cycle's addresses on *all*
of its pages, so the fixture now falsifies every page from the boundary on.

## Deferred

An end-to-end guard at the `initdb.Open` level (a data directory whose WAL
holds a hole must fail `Open`) is not in this change; the propagation is a
single `return err` shared with the pre-existing `ErrUnsupportedRecord`
refusal, and `analysis/crashprobe30.sh` exercises the whole path under a real
crash. Ledger row 2026-08-12.
