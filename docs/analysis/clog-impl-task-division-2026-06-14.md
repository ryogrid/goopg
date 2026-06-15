# CLOG implementation — task division & API contract (2026-06-14)

Coordination artifact for the multi-agent CLOG build on branch `clog-impl-team`.
The FOUNDATION agent (this doc's author) lands first; agents A/B/C build on the
API contract below.

## File ownership

| Agent      | Owns (exclusive edit lane) |
|------------|----------------------------|
| Foundation | `internal/mvcc/clog.go`, `internal/wal/recovery.go`, `internal/initdb/xact_recovery.go`, `internal/initdb/open.go`, `internal/mvcc/manager.go`, `internal/autovacuum/launcher.go`, `internal/catalog/catalog.go`, `internal/wal/checkpointer.go`, this doc |
| A          | `internal/mvcc/subxact_visibility.go` |
| B          | `internal/mvcc/snapshot.go` |
| C          | `internal/mvcc/visibility.go` |

No agent edits another agent's files. No `*_test.go` files are edited by
Foundation. Gaps implemented here: **G1** (CLOG truncation / wraparound) and
**G9** (CLOG_TRUNCATE WAL record).

## API contract exposed by Foundation

These symbols are stable and may be consumed by A/B/C.

### `internal/mvcc` (clog.go)

```go
// Wraparound-safe page comparison: does page1 logically precede page2?
// Mirrors PG clog.c:CLOGPagePrecedes.
func CLOGPagePrecedes(page1, page2 int64) bool

// Lowest XID whose CLOG status is still retained. Below this, status has
// been truncated away and callers MUST treat the XID as frozen/committed
// (it is older than every relfrozenxid). Mirrors
// TransamVariables->oldestClogXid.
func (c *CLog) OldestClogXid() storage.TransactionID

// Monotonically advance oldestClogXid (never moves backward; wraparound-safe).
// Mirrors PG varsup.c:AdvanceOldestClogXid.
func (c *CLog) AdvanceOldestClogXid(xid storage.TransactionID)

// Remove in-memory banks, flat-file prefix, and pg_xact/ SLRU segment files
// entirely below the page containing oldestXid. Idempotent, wraparound-aware.
// Advances oldestClogXid and emits a CLOG_TRUNCATE WAL record via the
// truncate-logger hook (if wired). Keeps the partial page containing
// oldestXid and everything newer.
func (c *CLog) TruncateCLOG(oldestXid storage.TransactionID) error

// Install the WAL-writer hook used by TruncateCLOG to emit the
// CLOG_TRUNCATE record. nil-safe (no hook => no WAL emission).
func (c *CLog) SetTruncateLogger(fn func(oldestXid storage.TransactionID) error)
```

**Truncation-base contract (important for A/B/C):** after `TruncateCLOG`,
`GetStatus(xid)` for `xid < OldestClogXid()` returns `TxnStatusUnknown` because
the bank/byte is gone. Visibility code MUST short-circuit such XIDs as
committed/frozen BEFORE consulting `GetStatus` (they are older than every
table's relfrozenxid, hence definitely committed). This is the same invariant
PG enforces via `TransactionIdDidCommit`'s frozen short-circuit. The flat file
and banks remain XID-indexed (no rebasing); truncation only zeroes/removes the
prefix, so existing index math in A/B/C is unaffected for retained XIDs.

### `internal/mvcc` (manager.go)

```go
// OldestXmin() (unchanged) — horizon below which truncation is always safe.
```

### `internal/catalog` (catalog.go)

```go
// Min RelFrozenXID across user (non-virtual, non-system) tables, or 0 when
// none have a valid relfrozenxid. This is the cluster datfrozenxid candidate.
func (c *InMemory) DatFrozenXID() storage.TransactionID
```

### `internal/wal` (recovery.go)

```go
const RecordKindClogTruncate byte = 33 // wire: kind(1) | oldestXid(4) = 5 bytes

func EncodeClogTruncate(xid storage.TransactionID) []byte
func DecodeClogTruncate(payload []byte) (storage.TransactionID, error)
```

## Integration points wired by Foundation

- `open.go`: installs `clog.SetTruncateLogger(...)` (appends `EncodeClogTruncate`
  to the WAL) and ensures recovery replays CLOG_TRUNCATE via
  `replayClogFromWAL`/`replayCLogFromWAL`.
- `checkpointer.go`: new `TruncateCLOGFn func() error` config hook, called
  AFTER the checkpoint marker is durable (durable-ordered truncation). open.go
  wires it to truncate at the conservative horizon
  `min(DatFrozenXID, OldestXmin)`.
- `launcher.go`: anti-wraparound path documents that durable CLOG truncation is
  owned by the checkpointer hook (single integration point).

## Gate commands (run from worktree root)

```
go build ./...
go test ./internal/mvcc/... ./internal/wal/... ./internal/initdb/...
go test -race ./internal/mvcc/... ./internal/wal/...
```

All must pass before any agent reports done. This is a WAL-format change
(new record kind 33) — re-init any test data dir after pulling.

## Review outcomes (2026-06-14)

A review agent fact-checked the combined diff against `postgres/.../clog.c`,
`subtrans.c`, `slru.h`. Confirmed correct: truncation safety & PG-faithful
ordering, `CLOGPagePrecedes` wraparound math, `EncodeClogTruncate`↔`DecodeClogTruncate`
inverse + unique `RecordKindClogTruncate = 33`, no recursive WAL emit during
replay, `pg_subtrans` layout, lane discipline, idempotency, no leaks.

Fixed after review:
- **M1 (applied):** `MarkUnknownAsAborted` now floors the implicit-abort sweep at
  the lowest XID still covered by an on-disk pg_xact/ segment
  (`firstRetainedSLRUXID`), so after a prior `TruncateCLOG` + restart it no longer
  re-stamps truncated XIDs Aborted or recreates the unlinked SLRU segments.
  `mirrorTerminalRangeBatchedUnlocked(loXID, hi)` starts at the floor segment.

Deferred (minor follow-ups, recorded for resume):
- **M2:** horizon selection in `catalog.DatFrozenXID` and the checkpointer
  `TruncateCLOGFn` uses plain `<` rather than wraparound-aware `txnPrecedes`.
  Low likelihood (only near 2^32; goopg has a hard wraparound allocation guard),
  and a fix would require an exported XID-compare helper in `storage`/`catalog`
  outside the declared lanes. Resume: add `storage.XIDPrecedes` and use it in both
  spots.
- **pg_subtrans restore-on-restart:** parent links are persisted (write path) but
  not yet read back from SLRU at startup. Resume: load `pg_subtrans` into the
  `SubxactMap` during recovery.
- **G5 `SUB_COMMITTED`** CLOG-mirror lane (0x03): deferred (would edit
  Foundation's `clog.go`). Resume: add the 0x03 lane to the CLOG SLRU mirror.
- **G4/G6/G7/G8:** out of scope for this run (see gaps-and-remediation doc).
