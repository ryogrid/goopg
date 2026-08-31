# 0101-0003 — WAL xl_prev restart-seeding fix (prevRecPtr 0-based)

Status: accepted
Milestone: M0101 (PG-compatible WAL) / surfaced by M0110-0002 (pg_waldump port)

## Problem

The upstream `pg_waldump` binary could not walk goopg's on-disk WAL record
chain on a freshly-booted cluster. It aborted at the **second** record with:

```
pg_waldump: error: ... incorrect prev-link 0/1000029 at 0/10000A0
```

`0/10000A0` is the first record goopg appends after boot; its `xl_prev`
(`0/1000029`) is exactly **+1** past the correct value `0/1000028` (the 0-based
RecPtr of the bootstrap checkpoint record, which sits at byte `0x1000028` =
segment 1 start + the 40-byte long page header).

This made the pass-required oracle test `W-001`
(`TestPort_WALPgWaldumpCompat`) silently red and blocked
`TestPort_PgWaldump002SaveFullpage` from ever reaching its assertions.

## Root cause

goopg has two code paths that establish the `prevRecPtr` carried into the next
record's `xl_prev` field, and they disagreed on the LSN base:

| Path | Site | Stored value | Base |
|---|---|---|---|
| Live append | `state.append` / `AppendXLogPayload` → `resetPosition(end, start-1)` | `start - 1` | **0-based** (correct) |
| Restart seed | `detectWritePos` → `prevRecPtr = lastRecPtr` (`writer.go:917`) | `lastRecPtr` from `scanLastSegmentEnd` | **1-based** (bug) |

`scanLastSegmentEnd` returns the record's *public* start-LSN, which in goopg's
convention is **1-based** (`start = writePos + leading + 1`, the same value
`state.append` returns to callers). The live path converts that back to the
0-based RecPtr before storing it as `prevRecPtr` (`start-1`); the restart-seed
path assigned the 1-based value **verbatim**, violating the documented contract
of the field (`writer.go:396`: "prevRecPtr stores the upstream-style **0-based**
RecPtr").

Because the bootstrap WAL is written by `initdb` and the server's first append
happens *after* `detectWritePos` runs at `NewWriter`, every cluster's first
server-appended record inherited the +1 `xl_prev` — which is precisely the
"second record" pg_waldump rejected.

This was invisible to goopg itself: goopg's own WAL iterator / recovery decode
**never validates `xl_prev`** (it advances by record length, not prev-link), so
only an external 0-based reader (pg_waldump, or a PG standby's `xlogreader`)
ever notices.

## Fix

Convert the 1-based start-LSN to the 0-based RecPtr at the seed site, exactly
mirroring the live path's `resetPosition(end, start-1)`
(`internal/wal/writer.go`, `detectWritePos`):

```go
writePos += usedBytes
if lastRecPtr > 0 {
    prevRecPtr = lastRecPtr - 1
} else {
    prevRecPtr = 0
}
```

The `> 0` guard preserves `InvalidXLogRecPtr` (0 = "no previous record") for an
empty/zeroed last segment.

### Why this is low blast-radius despite being in the WAL writer

- `writePos` (the next write position, and hence every client-visible LSN —
  `pg_current_wal_lsn`, replication start positions, `pg_control`) is
  **unchanged**. Only the *seeded prev pointer* changes.
- The only on-disk effect is the `xl_prev` field of records appended *after a
  boot/restart*. goopg recovery ignores `xl_prev`, so replay is unaffected and
  no data-dir re-init is required for goopg's own correctness.
- For heterogeneous replication (PG standby reading goopg WAL), PG's
  `xlogreader` *does* validate prev-links; the fix makes post-restart records
  pass that validation, so it strictly **improves** goopg→PG compatibility.

## Sibling-path note

The live-append path was already correct (`resetPosition(end, start-1)`); the
restart-seed path is its sibling and is now textually parallel. `encodeRecordXLog`
(`format.go:263`) writes `prev` verbatim and is correct for both paths — no
encode/decode change was needed (decode does not consume `xl_prev`).

## Tests / gates

- `W-001` `TestPort_WALPgWaldumpCompat` — **repaired and now PASS.** It also had
  a stale-test bug (parsed the 24-hex native PG segment name with
  `ParseUint(…,16,64)`, which overflows uint64 → every segment skipped → "no WAL
  segments found"). Rewritten to reuse the `listWALSegments`/`isHex24` helpers
  and run `pg_waldump -p <walDir> <segment>` directly on native-format names
  (no alias rewriting, no manual `-s`). The "incorrect prev-link" structural
  check is the regression guard for this fix.
- `TestPort_PgWaldump002SaveFullpage` — prev-link blocker resolved (pg_waldump
  now walks the full chain to clean end-of-WAL). A prev-link error is now
  asserted as a regression; the test self-skips on the genuinely separate
  remaining blocker: goopg emits no PG-decodable full-page-image records (all
  non-checkpoint records route through `RmgrXLog`/`0xF0`, opaque to PG).
- `go test ./internal/wal/` + `go test -race ./internal/wal/ ./internal/mvcc/`
  — green.
- `TestE2E_PhysicalReplication` — green (replication walk unaffected).

## Oracle

pg_waldump prev-link validation: `postgres/src/bin/pg_waldump/pg_waldump.c`
(record loop) and `postgres/src/backend/access/transam/xlogreader.c`
(`ValidXLogRecord` / prev-link check). RecPtr convention (0-based byte offset of
the XLogRecord header) per `postgres/src/include/access/xlogdefs.h`.
