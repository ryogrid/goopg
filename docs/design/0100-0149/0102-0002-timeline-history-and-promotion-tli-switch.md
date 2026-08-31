# 0102-0002 — TIMELINE_HISTORY Wire-Protocol + Promotion-Time TLI Switch

**Status:** accepted (2026-05-14)
**Date:** 2026-05-13
**Milestone:** M0102-0003
**Upstream reference:** `postgres/src/backend/access/transam/timeline.c:463` (`writeTimeLineHistoryFile`), `postgres/src/backend/replication/walreceiver.c:760` (writeTimeLineHistoryFile call on standby), `postgres/src/backend/replication/walsender.c` (TIMELINE_HISTORY command handler), `postgres/src/include/access/timeline.h`.

## Problem

When a standby is promoted to a primary, it must:

1. **Increment its timeline ID** (TLI = previous TLI + 1).
2. **Write a `pg_wal/000000NN.history` file** describing the previous timelines'
   end LSNs (each line: `<prev_tli> <switch_lsn> <reason>`).
3. **Respond to TIMELINE_HISTORY <tli>** wire-protocol requests so subsequent
   standbys / walreceivers can fetch the history and verify the LSN ranges
   they have replayed.

goopg currently does none of these (TLI is hardcoded to 1, no `.history` files,
no TIMELINE_HISTORY command). This is fine for goopg↔goopg replication where
both sides ignore the issue, but a heterogeneous failover where a PG standby
attempts to reconcile its replayed WAL against a freshly-promoted goopg's
timeline will fail without it.

## Upstream contract

### Timeline history file

From `postgres/src/backend/access/transam/timeline.c:463` `writeTimeLineHistoryFile`:

- Filename: `<datadir>/pg_wal/<TLI>.history` (TLI in `%08X` hex, e.g., `00000002.history`).
- Content: text lines, one per pre-existing timeline:
  ```
  <parent_tli>  <switch_lsn_hex>  <reason text>
  ```
- Sorted chronologically (oldest first); the latest line is the most recent switch.
- File mode 0o600; durable write (rename-from-temp).

### TIMELINE_HISTORY command

From `postgres/src/backend/replication/walsender.c` BASE_BACKUP block:

- Client sends `TIMELINE_HISTORY <tli>`.
- Server sends a single result-set with two columns: filename (text) and
  content (bytea, the raw .history file bytes).
- If the requested TLI is 1, the file does not exist; server returns an empty
  result (PG returns an empty content row rather than an error).

### Promotion flow

From `postgres/src/backend/access/transam/xlogrecovery.c:4475` `CheckForStandbyTrigger`:

1. Standby detects promote trigger (signal file or pg_ctl promote).
2. End recovery: drain WAL receiver, ensure all replay finished.
3. Determine end-of-recovery LSN (`EndOfLog`).
4. Increment TLI: `ThisTimeLineID = recoveryTargetTLI + 1`.
5. Write the new `<TLI>.history` file via `writeTimeLineHistoryFile`,
   appending one line: `<previous_tli> <EndOfLog> "no recovery target specified"`.
6. Mark cluster as primary; resume accepting writes.

## Solution

### `internal/wal/timeline_history.go` (new)

```go
type TimelineHistoryEntry struct {
    TLI       uint32
    SwitchLSN uint64
    Reason    string
}

func ReadHistory(walDir string, tli uint32) ([]TimelineHistoryEntry, error)
func WriteHistory(walDir string, tli uint32, entries []TimelineHistoryEntry) error
```

Reuse `internal/wal/xlog_page.XLogFileName`-style `%08X` formatting for the
filename. Use atomic rename (`os.WriteFile` to `.tmp` then `os.Rename`) for
durability.

### Promote path integration (`cmd/goopg/standby.go`)

In `standbyController.Promote(ctx)` (existing), after the replay drain and
before clearing `standby.signal`:

```go
oldTLI := rt.Cfg.WAL.TimelineID  // currently 1
newTLI := oldTLI + 1
endLSN := rt.WAL.LastReplayedLSN()
entry := wal.TimelineHistoryEntry{
    TLI: oldTLI, SwitchLSN: endLSN, Reason: "no recovery target specified",
}
prev, _ := wal.ReadHistory(rt.WAL.Dir, oldTLI)
entries := append(prev, entry)
if err := wal.WriteHistory(rt.WAL.Dir, newTLI, entries); err != nil {
    return err
}
rt.Cfg.WAL.TimelineID = newTLI
// Continue with existing standby.signal removal, etc.
```

The next WAL segment written by the new primary uses `newTLI` in its segment
filename (`XLogFileName(newTLI, segno, segSize)` already does the formatting).

### TIMELINE_HISTORY handler

In `internal/server/replication.go`, add:

```go
case "TIMELINE_HISTORY":
    tli := parseUint32(args[0])
    name := fmt.Sprintf("%08X.history", tli)
    path := filepath.Join(s.cfg.WAL.Dir, name)
    content, err := os.ReadFile(path)
    if errors.Is(err, fs.ErrNotExist) {
        content = nil // PG-parity: empty content for unknown TLI
    } else if err != nil {
        return wireError(err)
    }
    return writeOneRowResultSet(w, name, content)
```

The result-set has two columns: filename (text), content (bytea); PG's
upstream wire shape, parseable by libpqwalreceiver.

## Files to create / modify

| File | Change |
|---|---|
| `internal/wal/timeline_history.go` | New: `ReadHistory`, `WriteHistory`, `TimelineHistoryEntry` |
| `internal/wal/timeline_history_test.go` | New: round-trip + format compatibility test |
| `internal/server/replication.go` | Add `TIMELINE_HISTORY` dispatcher arm |
| `cmd/goopg/standby.go` `Promote(ctx)` | Integrate TLI increment + history file write |

## Verification

```bash
# 1. Start goopg primary, promote-once (no streaming), verify .history file
./bin/goopg start -D /tmp/goopg_p ...
./bin/goopg promote -D /tmp/goopg_p
ls /tmp/goopg_p/pg_wal/   # expect 00000002.history present
cat /tmp/goopg_p/pg_wal/00000002.history
# expect: "1\t<hex-LSN>\tno recovery target specified"

# 2. Walreceiver TIMELINE_HISTORY fetch from goopg primary
./postgres/local_install/bin/psql \
  "host=127.0.0.1 port=<goopg> replication=database" \
  -c "TIMELINE_HISTORY 2"
# expect: 1-row result, content matches file bytes
```

Unit test: round-trip a 3-timeline history through `WriteHistory`+`ReadHistory`.

## Risks

- **EndOfLog accuracy.** The history file claims the previous timeline ended
  at `EndOfLog`. If goopg's replay loop tracks the last applied LSN
  imprecisely (e.g., advances past the actual record boundary), the next
  timeline's WAL will appear to overlap. Mitigation: use the same LSN that
  `pg_last_wal_replay_lsn()` returns (the SQL view, not an internal counter).
- **TLI history file durability under crash.** A crash between writing the
  history file and incrementing TLI in pg_control-equivalent state would
  leave the file dangling. M0102 keeps the TLI state in the wal.Config
  passed to the writer; persist it in `global/system_identifier`'s file
  (M0101) or a sibling `global/timeline_id` file.

## Implementation notes (landed 2026-05-14)

- `internal/wal/timeline_history.go` provides `ReadHistory`, `WriteHistory`,
  `TimelineHistoryFileName`, and the `TimelineHistoryEntry` struct. Atomic
  writes use `os.WriteFile(.tmp) + os.Rename` and a best-effort directory
  fsync. Tab-separated `<TLI>\t<X/X>\t<reason>\n` format with comment / blank
  line tolerance on read.
- `internal/initdb/timeline.go` adds `LoadOrCreateTimelineID(dataDir)` and
  `WriteTimelineID(dataDir, tli)` — 4-byte little-endian uint32 in
  `global/timeline_id`, defaulting to TLI=1 on a fresh cluster. Wired into
  `internal/initdb/open.go` so the writer's `wal.Config.TimelineID` is
  seeded from disk on every start.
- `internal/server/replication.go` adds the `TIMELINE_HISTORY <tli>` command
  arm and the `oidBytea = 17` constant. Missing files (typically TLI=1)
  return a row with NULL content, matching the upstream walreceiver
  contract.
- `cmd/goopg/standby.go` `finalizePromotion` runs the M0102-0003 sequence:
  bump TLI from `LoadOrCreateTimelineID`, append a history entry anchored
  at the replayer's `ApplyLSN` (or `WrittenLSN` if replay never started),
  write `<newTLI>.history` via `wal.WriteHistory`, then persist newTLI via
  `WriteTimelineID`. The currently-running WAL writer keeps emitting on
  the old TLI for the rest of the process lifetime — an in-place
  `Writer.SetTimelineID()` is a planned follow-up; M0102-0003's verification
  gate only requires the on-disk artefacts and the wire path.
- New regression coverage:
  - `internal/wal/timeline_history_test.go` — round-trip, format pinning,
    missing-file behaviour, comment/blank-line tolerance.
  - `internal/initdb/timeline_test.go` — load default, write-then-load.
  - `cmd/goopg/standby_test.go::TestStandbyControllerPromoteWritesTimelineHistory`
    — full promote path produces `pg_wal/00000002.history` (line begins with
    `1\t`) and `global/timeline_id` advances to 2.
  - `internal/server/replication_test.go::TestReplicationTimelineHistoryReturnsFile`
    + `…MissingReturnsEmptyContent` — TIMELINE_HISTORY wire shape verified
    end-to-end against a live `Server` instance.

All affected packages pass with `-race`:
`go test -race -count=1 ./internal/wal/ ./internal/initdb/ ./internal/server/ ./cmd/goopg/`.
