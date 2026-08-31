# 0101-0001 — WAL Page Header Compatibility: Enable PG-Compatible Format by Default

> **perf-optimize3-dash S4 note (2026-07-13)**: PageHeaders (the FRAME format
> this doc establishes) stays ON and is now independent of canonical record
> CONTENT emission (`EmitCanonical`, default off — native-only stream).

**Status:** accepted
**Date:** 2026-05-13
**Milestone:** M0101-0001, M0101-0002

## Problem

goopg's WAL writer has two modes:

- **Legacy mode** (`PageHeaders=false`, default) — flat `len + CRC32-IEEE` stream,
  magic `0x200E`, no XLOG page/long headers.
- **PG-compatible mode** (`PageHeaders=true`) — full XLOG page headers, magic
  `0xD118`, `XLP_LONG_HEADER`, `xlp_tli=1`, `xlp_seg_size=16MiB`. Implemented by
  M0014-0003 in `internal/wal/xlog_emit.go`.

The PG-compatible implementation is complete and correct. The only issue is that
`Config.PageHeaders` is never set to `true` in production code. All clusters —
including the Ralph autonomous-loop regression clusters — write legacy-format WAL
that `pg_waldump` cannot parse.

The root cause was identified from the hex dump of
`/tmp/ralph_regress_data/pg_wal/000000000000000000000000`:

```
Offset 0x00: 0e 20  → magic 0x200E (legacy, not 0xD118)
Offset 0x02: 00 00  → xlp_info = 0 (no XLP_LONG_HEADER flag)
```

`pg_waldump` reads `xlp_seg_size` at offset 32 and gets `0x00280000` (2,621,440)
from the legacy format's garbage at that position — a non-power-of-two → error.

## Solution

### 1. Set `PageHeaders = true` in `internal/initdb/open.go`

The single production call site where `wal.Config` is constructed is
`internal/initdb/open.go:232`. Add two fields:

```go
walCfg := wal.Config{
    // ... existing fields ...
    PageHeaders: true,
    SystemID:    <cluster system identifier — see §2>,
}
```

`TimelineID` does not need to be set explicitly: `writer.go:205-206` already
auto-sets it to `1` when `PageHeaders=true` and `TimelineID==0`.

### 2. Generate and persist `system_identifier`

PostgreSQL's `xlp_sysid` is a random `uint64` generated at `initdb` time and
stored in `pg_control`. goopg does not yet have a `pg_control` file.

Minimum viable approach for M0101:

**a. Generation** — at `OpenCluster` (first run, no existing WAL), generate:

```go
systemID := uint64(time.Now().UnixNano()) ^ (uint64(os.Getpid()) << 32)
```

Or use `crypto/rand` for a more robust value. Store in the cluster directory as
`global/system_identifier` (a flat 8-byte binary file) so it survives restarts.

**b. Read-back** — on every subsequent `OpenCluster`, read the file and pass the
value as `SystemID` in `walCfg`.

**c. pg_control integration** — full `pg_control` file support is deferred to
M0014's broader scope. For M0101, the 8-byte stub is sufficient: `pg_waldump`
uses `xlp_sysid` only for cross-checking across segments (not for parsing) and
does not require a matching `pg_control` file when invoked with `--path` alone.

### 3. No changes to `internal/wal/`

`EncodeXLogLongPageHeader` (`xlog_page.go:157-196`) already:
- Forces `XLPLongHeader (0x0002)` into `Info` (line 165).
- Writes `xlp_magic = 0xD118` (line 163).
- Writes `xlp_seg_size` from `h.SegSize` (verified = `DefaultSegmentSize = 16MiB`).
- Writes `xlp_xlog_blcksz = 8192`.

`XLogFileName` (`xlog_page.go:199-217`) already uses `%08X%08X%08X` format and
auto-sets TLI=1 (via the Config safeguard at `writer.go:205-206`). Segment names
become `000000010000000000000000` etc. — PG-compatible.

### 4. Legacy-format detection

`format_detect.go` already detects legacy format via the non-`0xD118` magic byte.
When `PageHeaders=true` is the new default, any cluster initialized with an older
goopg binary will fail at WAL replay with a clear legacy-format error. Document this
as expected behavior; no transparent migration is attempted.

## Files touched

| File | Change |
|---|---|
| `internal/initdb/open.go:232` | Add `PageHeaders: true`, `SystemID: sysID` to `walCfg` |
| `internal/initdb/open.go` | Add `loadOrCreateSystemID(dir string) (uint64, error)` helper |
| `internal/initdb/open.go` | Call `loadOrCreateSystemID` before `walCfg` construction |
| `internal/initdb/open_test.go` | Assert `PageHeadersEnabled()` returns true on new clusters |

No changes to `internal/wal/` — the format implementation is complete.

## Reference (upstream)

- `postgres/src/backend/access/transam/xlog.c` — `BootStrapXLOG` generates
  `system_identifier` via `gettimeofday` + PID + random bits.
- `postgres/src/include/access/xlog_internal.h` — `XLogPageHeaderData` struct,
  `XLOG_PAGE_MAGIC = 0xD118`, `XLP_LONG_HEADER = 0x0002`.

## Verification

- Fresh cluster: `xxd <pg_wal/segment> | head -4` shows `18 d1` at bytes 0-1
  (magic 0xD118 LE) and `02 00` at bytes 2-3 (XLP_LONG_HEADER set).
- `./postgres/local_install/bin/pg_waldump <segment>` exits 0.
- `go test ./internal/wal/... ./internal/initdb/...` passes (legacy path still
  exercised by tests that explicitly set `PageHeaders=false`).

## Risks

- **Existing test clusters break on WAL replay.** Test clusters in `/tmp/`
  are ephemeral and recreated per test; no migration needed. The Ralph regression
  clusters (`/tmp/ralph_regress_data*`) must be deleted and recreated by the test
  harness after this change lands.
- **WAL replay path must also handle PG-compatible format.** The `RecordIterator`
  / `ReplayFromDirWithMgr` paths already branch on `pageHeaders` (same flag).
  Verify that recovery from a PG-compatible WAL segment works correctly; add a
  crash-recovery test if absent.
- **pg_waldump may reject records it cannot decode.** goopg emits WAL records
  with Rmgr IDs that match PG (0,1,2,9,10,11) but the payload encoding may differ
  from what PG's Rmgr decoders expect. `pg_waldump --quiet` suppresses per-record
  output and only fails on structural errors; use `--quiet` in the compatibility
  test to avoid Rmgr payload decode failures being counted as compatibility bugs.
