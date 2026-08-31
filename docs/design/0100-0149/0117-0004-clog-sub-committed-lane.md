# 0117-0004 — `SUB_COMMITTED` (0x03) CLOG lane

- **Status:** accepted
- **Milestone:** M0117 (CLOG ↔ PostgreSQL subsystem alignment)
- **Gap:** G5 (`SUB_COMMITTED` portion) — see
  `docs/analysis/clog-goopg-gaps-and-remediation-2026-06-14.md`.
- **Builds on:** M0117-0003 (`pg_subtrans` restore-on-restart) for the
  parent-link store that resolves a `SUB_COMMITTED` lane.

## Problem

goopg's CLOG SLRU mirror (`internal/mvcc/clog.go`) is a PG-byte-compatible
`pg_xact/` directory written 2 bits per XID. PostgreSQL defines **four** lane
values (`postgres/src/include/access/clog.h:27-30`):

| bits | PG constant                       | meaning |
|------|-----------------------------------|---------|
| 0x00 | `TRANSACTION_STATUS_IN_PROGRESS`  | not finished |
| 0x01 | `TRANSACTION_STATUS_COMMITTED`    | committed |
| 0x02 | `TRANSACTION_STATUS_ABORTED`      | aborted |
| 0x03 | `TRANSACTION_STATUS_SUB_COMMITTED`| subxact committed; resolve via parent |

Before this change goopg's `TxnStatus` enum had only three values
(`Unknown`/`Committed`/`Aborted`); the 0x03 lane was never *written*, and
`loadFromSLRU` decoded any 0x03 it found as `Committed` with a defensive
comment treating it as an OR-artifact. That conflated PG's real
`SUB_COMMITTED` state with corruption and left the mirror unable to round-trip
a sub-committed subtransaction — blocking durable subxact resolution for an
attached PG standby, 2PC, and any reader that must resolve a subxact's fate
after the owning backend is gone (the consumers G5 enumerates).

## PG semantics (the oracle we mirror)

`SUB_COMMITTED` is the status PG stamps on a subtransaction's XID when its
**parent (top-level) transaction has not yet committed**. It is *not* a
terminal answer: a reader must consult the parent.
`postgres/src/backend/access/transam/transam.c` `TransactionIdDidCommit`:

```c
if (xidstatus == TRANSACTION_STATUS_SUB_COMMITTED)
{
    if (TransactionIdPrecedes(transactionId, TransactionXmin))
        return false;                 /* parent crashed without cleanup */
    parentXid = SubTransGetParent(transactionId);
    if (!TransactionIdIsValid(parentXid))
        return false;                 /* (+ WARNING) */
    return TransactionIdDidCommit(parentXid);   /* recurse to parent */
}
```

So a `SUB_COMMITTED` subxact is committed **iff** its top-level parent is
committed. The parent link is exactly what M0117-0003 made durable in the
`pg_subtrans` SLRU (`SubxactMap` / `SubtransSLRU`).

## Change

`TxnStatus` gains a fourth value and both halves of the encode/decode seam
learn the 0x03 lane.

1. **`TxnStatusSubCommitted TxnStatus = 3`** added to the enum (raw byte 3,
   matching the on-disk SLRU lane value so the flat-file and SLRU encodings
   agree).

2. **`pgClogStatusSubCommitted = 0x03`** added beside the existing PG XidStatus
   constants.

3. **Write path** — `mirrorToSLRUUnlocked` and the batched
   `mirrorTerminalRangeBatchedUnlocked` map `TxnStatusSubCommitted → 0x03`.
   A new public primitive `CLog.SetSubCommitted(xid)` routes through `setStatus`
   (flat file + SLRU mirror), exactly like `SetCommitted`/`SetAborted`.
   **Caller contract (documented on the method):** `SetSubCommitted` is for a
   committed subtransaction whose parent top-level XID is still in progress —
   the caller checks that condition; the CLog only records the lane.

4. **Read path** — `loadFromSLRU` decodes lane `0x03 → TxnStatusSubCommitted`
   (instead of the old "treat as committed" artifact handling). The defensive
   intent is *preserved* by the resolution policy below, not by the decode.

5. **Resolution policy** — `GetStatus` returns the raw lane
   (`TxnStatusSubCommitted` for a 0x03 slot), mirroring PG's
   `TransactionLogFetch`. A new helper `CLog.DidCommit(xid, parentOf)` mirrors
   `TransactionIdDidCommit`: `Committed → true`, `Aborted/Unknown → false`,
   and `SubCommitted →` recurse on `parentOf(xid)` (the M0117-0003 `SubxactMap`
   supplies `parentOf`; a nil/zero parent → `false`, matching PG's
   missing-`pg_subtrans`-entry WARNING-then-false branch). Visibility
   (`Snapshot.SeesCommittedXID`, M0117-0002) only flips on a *positive*
   `TxnStatusAborted`; a bare `SubCommitted` read there still falls through to
   the conservative "assume committed" default, so this change cannot make a
   live tuple *less* visible than before.

### Why the OR write semantics are kept (not switched to PG's clear-then-set)

PG's `TransactionIdSetStatusBit` clears the 2-bit field then sets it; goopg's
mirror uses strict OR. OR is intentional and **safer** for goopg's
cross-store seam: when the flat file is lost but the SLRU durably holds a
`COMMITTED` (0x01) lane, a stale in-memory `Aborted` must not *downgrade* the
durable commit. OR preserves the committed bit. The `0x03` produced by
`0x01 | 0x02` is precisely the "was committed, something later said aborted"
case — and PG's own resolution (`SUB_COMMITTED → check parent`, default
committed when the parent is committed/unknown) gives the same answer the old
defensive "treat as committed" comment wanted. The two interpretations
therefore unify: a 0x03 lane means "consult the parent; absent a definite
abort, it is committed".

The `in-progress → sub-committed` transition this task introduces is
`0x00 | 0x03 = 0x03`, which OR handles exactly. The only transition OR cannot
express is `sub-committed → committed` finalisation (`0x03 | 0x01 = 0x03`,
never `0x01`) — see Non-goals.

## State → code-path map (documents the encode/decode seam)

| `TxnStatus` | flat byte | SLRU lane | written by | read by |
|-------------|-----------|-----------|------------|---------|
| `Unknown`      | 0 | 0x00 | (default; never mirrored) | `GetStatus` |
| `Committed`    | 1 | 0x01 | `SetCommitted` → `setStatus` → `mirrorToSLRUUnlocked`; `InitializeAsCommitted` → `flush`/batched | `GetStatus`, `loadFromSLRU` |
| `Aborted`      | 2 | 0x02 | `SetAborted` → `setStatus` → `mirrorToSLRUUnlocked`; `MarkUnknownAsAborted` → batched | `GetStatus`, `loadFromSLRU` |
| `SubCommitted` | 3 | 0x03 | **`SetSubCommitted` → `setStatus` → `mirrorToSLRUUnlocked` (this change)** | `GetStatus`, `loadFromSLRU` (this change), `DidCommit` resolves via parent |

## Non-goals / known follow-ups

- **`SUB_COMMITTED → COMMITTED` finalisation.** PG's two-pass
  `TransactionIdSetTreeStatus` rewrites sub-committed lanes to committed once
  the parent commits. goopg does not (the OR mirror cannot downgrade bits, and
  goopg subxacts do not span a restart today). This is fine because resolution
  consults the parent: once the parent's lane is `COMMITTED`, `DidCommit`
  returns true for the still-`SUB_COMMITTED` child. A real finalisation pass
  (and the clear-then-set mirror it needs) is deferred until a runtime CLOG
  commit path exists.
- **No live commit-path caller wires `SetSubCommitted` yet** — like the
  M0117-0002 `SetCLog` and M0117-0003 `SetSubxactMap` primitives, this lands
  the encode/decode + resolution machinery; the runtime integration that calls
  it on subxact commit is a later step. The lane is exercised end-to-end by the
  extended dual-store consistency test.

## Tests

- `internal/mvcc/clog_dual_store_consistency_test.go`:
  - `writeStatuses` learns the `TxnStatusSubCommitted` case (`SetSubCommitted`).
  - `TestCLogDualStoreConsistency` adds `SubCommitted` XIDs to the
    page-straddling set so the flat-file ↔ SLRU round-trip is pinned for all
    four lane values (the sibling-path gate per
    `pattern_sibling_paths_must_agree`).
  - New `TestCLogSubCommittedResolvesViaParent` pins `DidCommit`: a
    `SubCommitted` child resolves to committed when its parent is committed,
    aborted/false when the parent is aborted/unknown, and false when no parent
    link exists (PG's missing-entry branch).
- Gate: `go test -race ./internal/mvcc/...` (WAL/MVCC practice card).

## Files

- `internal/mvcc/clog.go` — enum value, PG constant, write/read/resolve.
- `internal/mvcc/clog_dual_store_consistency_test.go` — extended round-trip.
- `docs/design/0117-0004-clog-sub-committed-lane.md` — this doc.
- `docs/design/README.md` — index row.
