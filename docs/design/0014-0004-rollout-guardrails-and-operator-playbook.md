# 0014-0004 — Rollout Guardrails and Operator Playbook

**Status:** accepted (steps 1-2)
**Milestone:** [0014 — PostgreSQL-Compatible WAL On-Disk Format](../milestones/0014-wal-compatibility-with-pg.md)
**Spans seam:** legacy-format detection, format-mode reporting, operator
diagnostics.
**Cross-links:**
[0014-0001](0014-0001-xlog-page-and-segment-layout-compat.md)
(`ErrInvalidPageHeader` typed sentinel),
[0014-0002](0014-0002-xlogrecord-header-and-rmgr-mapping.md)
(`ErrInvalidRecordHeader`).

## Context

M0014-0001 step 1 and M0014-0002 step 1 added typed sentinels
(`ErrInvalidPageHeader`, `ErrInvalidRecordHeader`) precisely so a
later loop could write a *legacy-format detector* that branches
cleanly between "this WAL was written by a pre-M0014 goopg" and
"this WAL is corrupt". This step 1 adds that detector — pure utility
code that callers (the writer's startup path, the future migration
tool) can invoke without committing to a switchover policy yet.

The detector is intentionally **read-only**: it inspects the first
bytes of the first segment file in a `pg_wal/` directory and returns
a tagged result. No mutation, no log lines, no panics.

## API

```go
// WALFormatVersion identifies which on-disk format a pg_wal/
// directory is using.
type WALFormatVersion int

const (
    WALFormatUnknown WALFormatVersion = iota // empty directory or no recognisable segment
    WALFormatLegacy                          // pre-M0014 goopg (length+CRC32-IEEE frame, no per-page headers)
    WALFormatPGCompat                        // M0014 PG18-compatible (XLogPageHeader + XLogRecord)
)

// DetectWALFormat inspects the first segment file in walDir and
// returns the on-disk format version. Empty / nonexistent dirs
// return WALFormatUnknown with nil error — the caller decides
// whether that's an error in context.
//
// Errors returned: filesystem errors (Stat failure, permission
// denied) propagate as-is. A directory containing only files that
// don't parse as either legacy or pg-compat segment names returns
// WALFormatUnknown with nil error (treats arbitrary user files in
// pg_wal/ as "no WAL produced yet").
func DetectWALFormat(walDir string) (WALFormatVersion, error)
```

## Detection rule

For the lowest-numbered file in `walDir` whose name parses as
either a legacy segment (24 hex chars via `parseSegmentName`) or a
pg-compat XLog filename (24 hex chars via `ParseXLogFileName`):

1. Read the first 40 bytes (`SizeOfXLogLongPHD`).
2. Try `DecodeXLogLongPageHeader`. If it succeeds → `WALFormatPGCompat`.
3. Try `DecodeXLogPageHeader` on the first 24 bytes. If it succeeds
   → `WALFormatPGCompat` (segment without long header — possible if
   the operator deleted segment 0 manually).
4. Both fail with `ErrInvalidPageHeader` wrap → `WALFormatLegacy`
   (the bytes don't match the magic, so they're a legacy
   length+CRC frame or zero-fill).
5. Any other error (I/O, truncated file shorter than 24 bytes
   without being all-zero) → propagate to caller.

The rule **doesn't** try to validate the legacy frame's CRC or the
pg-compat record's CRC — too expensive at startup, and the writer
will catch real corruption when it actually starts reading.

## Caller integration (deferred to step 2)

The actual wiring into `loadState` lands in M0014-0004 step 2 once
the writer/reader switchover is in flight. The contract that step
will follow:

- If `Config.PGCompatPages` is true and `DetectWALFormat` returns
  `WALFormatLegacy`, fail fast with an actionable error:
  `"WAL directory contains legacy goopg format; either downgrade goopg
  or run pg_resetwal to start fresh (pre-GA — no migration tool)"`.
- If `Config.PGCompatPages` is false and the detector returns
  `WALFormatPGCompat`, fail fast with:
  `"WAL directory contains PG-compatible format; set wal_format=pgcompat
  to use this data directory"`.
- Mismatch always fails — silently mixing formats would corrupt the
  segment files.

## Step 2 (landed 2026-04-30): caller wiring + observability

`loadState` now calls `DetectWALFormat(cfg.WALDir)` immediately
before `detectWritePos`. Decision matrix:

| detected     | cfg.PageHeaders | result                          |
|--------------|------------------|---------------------------------|
| Unknown      | any              | proceed (no signal — fresh dir) |
| Legacy       | false            | proceed (matching format)       |
| Legacy       | true             | `ErrWALFormatMismatch`          |
| PGCompat     | true             | proceed (matching format)       |
| PGCompat     | false            | `ErrWALFormatMismatch`          |

`Unknown` covers the empty-data-dir bootstrap path *and* the
"segment file is shorter than 40 bytes so the detector can't read
its quorum" case. The latter is rare in production (segments are
preallocated to 16 MiB) but common in unit tests with tiny segments
— treating it as no-mismatch keeps the gate cheap and lets the
existing `detectWritePos` record-level scan catch any deeper
incompatibility (record decode errors will surface a more specific
diagnostic).

A tiny additive `Writer.Format() WALFormatVersion` accessor
satisfies M0014 DoD #9 ("WAL format mode/version exposed at
runtime"): callers can ask the live writer which format it's
emitting. Returns `WALFormatPGCompat` when `cfg.PageHeaders=true`,
`WALFormatLegacy` otherwise. Static for the writer's lifetime.

Tests: `format_mismatch_test.go` —
`TestNewWriterRejectsLegacyDirInPGCompatMode`,
`TestNewWriterRejectsPGCompatDirInLegacyMode`,
`TestNewWriterAcceptsFreshDirInBothModes`,
`TestNewWriterAcceptsMatchingFormat`,
`TestWriterFormatExposesActiveMode`,
`TestNewWriterIgnoresNonSegmentFiles`. Full
`go test ./...` green.

## Out of scope (steps 1-2)

- Migration tooling — M0014 says "out of scope: full pg_upgrade
  compatibility" for pre-milestone clusters; pre-GA goopg simply
  requires a fresh data dir.
- A standalone `pg_resetwal`-equivalent — separate scope.
- SQL-level `pg_stat_wal_io.format_version` column wiring — the
  Go-level `Writer.Format()` accessor lands now; surfacing it
  through the existing pg_stat view is a follow-up cosmetic slice.
- Migration tooling — M0014 says "out of scope: full pg_upgrade
  compatibility" for pre-milestone clusters; pre-GA goopg simply
  requires a fresh data dir.
- A standalone `pg_resetwal`-equivalent — separate scope.

## Tests

- `TestDetectWALFormatEmptyDir` — empty `pg_wal/` →
  `WALFormatUnknown`, nil error.
- `TestDetectWALFormatLegacy` — synthesise a legacy segment file
  whose first bytes are an 8-byte length+CRC frame; detector
  returns `WALFormatLegacy`.
- `TestDetectWALFormatPGCompat` — synthesise a segment file whose
  first 40 bytes are a valid `XLogLongPageHeader` (use the
  `EncodeXLogLongPageHeader` helper); detector returns
  `WALFormatPGCompat`.
- `TestDetectWALFormatIgnoresGarbageFiles` — pg_wal/ with a
  `.gitignore` and a 5-byte random file (neither parses as a
  segment name) → `WALFormatUnknown`.
- `TestDetectWALFormatHandlesTruncatedSegment` — a segment file
  shorter than 24 bytes returns a clear error rather than
  panicking.
