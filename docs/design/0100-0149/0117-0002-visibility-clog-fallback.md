# 0117-0002 — Runtime CLOG-consulting visibility fallback

Status: accepted
Milestone: M0117-0002 (CLOG ↔ PostgreSQL alignment, gap G4; P1 / correctness)
Author: Ralph
Date: 2026-06-15

## Problem

PostgreSQL decides whether a tuple's `xmin`/`xmax` transaction is visible by
consulting **two** sources, in order:

1. The in-memory proc array snapshot (`XidInMVCCSnapshot`) — is the XID still
   running relative to this snapshot's horizon?
2. The **commit log** (`TransactionIdDidCommit` / `TransactionIdDidAbort`,
   `postgres/src/backend/access/transam/transam.c`) — for an XID that is *not*
   in-progress relative to the snapshot, did it actually commit or abort?

`HeapTupleSatisfiesMVCC` (`postgres/src/backend/access/heap/heapam_visibility.c`)
runs step 2 for every XID that is older than the snapshot's xmax and not listed
in the running array. The clog is authoritative: a transaction that the snapshot
no longer considers "running" might still have **aborted**, and its rows must
stay invisible.

goopg's `Snapshot.SeesCommittedXID` (`internal/mvcc/snapshot.go`) only does step
1 plus a lightweight in-memory `Aborted` array (M0100-0002):

```go
if s.HasAborted(xid) { return false }   // in-memory abort list
if xid < s.Xmin       { return true  }   // assumed committed
if xid >= s.Xmax      { return false }   // future
return !s.HasInProgress(xid)             // in-window: committed iff not running
```

The last line is the gap (**G4**): an in-window XID that is **not** in
`InProgress` and **not** in the snapshot's `Aborted` array is *assumed
committed*. The `Aborted` array is captured from the live `Manager.abortedXIDs`
slice, which is **rebuilt empty on every restart** — it is not the durable
commit log. So after a crash/restart (or any path where the in-memory abort list
does not list an XID the persistent CLOG knows aborted), a rolled-back row can
incorrectly read as committed. Recovery already consults the CLOG when it
re-scans the heap (`internal/initdb/open.go:1835` etc.), but the **runtime
snapshot path has no CLOG fallback**.

## Design

Add an **optional** CLOG reference to `Snapshot` and consult it in the residual
in-window case, keeping the in-memory arrays as the fast path. The contract is
deliberately conservative so it is a strict no-op wherever CLOG status is not
populated:

```go
// in-window residual case (xid in [Xmin, Xmax), not InProgress):
if s.clog != nil && (oldest == 0 || !xidPrecedesOldest) {
    switch s.clog.GetStatus(xid) {
    case TxnStatusAborted:   return false   // authoritative abort → invisible
    // TxnStatusCommitted / TxnStatusUnknown → fall through (assume committed)
    }
}
return !s.HasInProgress(xid)   // unchanged v0 default
```

### Why "only explicit Aborted overrides"

goopg's default runtime commit/abort path **does not write the CLOG** — the
in-memory commit model skips it (`internal/mvcc/manager.go:352,461` "no clog
write"); only recovery (`xact_recovery.go`, `open.go`) and bootstrap
(`initdb.go`) populate it. Therefore at steady-state runtime the CLOG returns
`TxnStatusUnknown` for freshly-committed XIDs. If the fallback treated `Unknown`
as aborted it would make **every** committed row invisible — catastrophic. By
acting **only** on a positive `TxnStatusAborted`, the fallback:

- is a strict no-op for runtime-new XIDs (status `Unknown` → unchanged), so it
  cannot regress the live OLTP/TPC-H path; and
- correctly makes a **recovered** aborted XID invisible even when the in-memory
  `Aborted` array (rebuilt empty after restart) has forgotten it — closing G4.

The clog is authoritative for aborts, so flipping to invisible on an explicit
abort is always correct.

### Truncation guard

A CLOG whose oldest retained XID has advanced past `xid` has truncated that
XID's status away; below `OldestClogXid` an XID is older than every relfrozenxid
and must be treated as committed/frozen (per `CLog.OldestClogXid`'s contract).
The fallback skips the CLOG consult when `xid` precedes `OldestClogXid` (using
the wraparound-safe `storage.XIDPrecedes` from M0117-0001), leaving the existing
"assume committed" behaviour for sub-horizon XIDs.

### Scope: in-window only

The task (and this change) covers the `[Xmin, Xmax)` residual window — exactly
"in-window XIDs not classified by the in-memory `InProgress`/`Aborted` arrays".
The `xid < Xmin` shortcut is **not** routed through the CLOG: it is the hottest
path (every old committed tuple), it is already gated by the
`HeapXminCommitted` hint bit in `TupleVisibleSubxact` (so `SeesCommittedXID` is
not even called for hint-bit-set xmins), and recovery's heap re-scan already
stamps invisibility for sub-horizon aborts. Extending the fallback below Xmin is
left as a follow-up (would need hint-bit-backed caching to stay cheap).

### Sibling-path audit (visibility.go ↔ subxact_visibility.go)

`SeesCommittedXIDWithSubxacts` (`subxact_visibility.go`) — the subxact-aware twin
— resolves an XID to its top-level ancestor and then **delegates** to
`snap.SeesCommittedXID(topXid)` (lines 177, 180). It therefore **inherits** the
CLOG fallback automatically; no separate edit to the abort/commit decision is
needed there. `TupleVisibleSubxact`'s xmax branch likewise routes through
`SeesCommittedXIDWithSubxacts` / `SeesCommittedXID`, so xmax visibility inherits
the same fallback. This is the required twin update: both visibility entry points
consult the same `SeesCommittedXID`, so the single change covers both.

`Snapshot.Clone()` is updated to carry the new `clog` pointer so cloned
snapshots (EPQ re-evaluation, origSnap) keep the fallback.

### Wiring

- `Snapshot.WithCLog(c *CLog) Snapshot` returns a copy of the snapshot with the
  fallback CLOG attached (value-type friendly; nil restores current behaviour).
- `Manager.SetCLog(c *CLog)` stores the CLOG on the Manager; `captureSnapshot`
  attaches it to every snapshot it builds. With no `SetCLog` call the field is
  nil and behaviour is byte-identical to today, so every existing Manager caller
  and test is unaffected.

The live server/`initdb.Open` wiring that calls `Manager.SetCLog` with the
recovered CLOG is the integration step; until it is called the mechanism is
dormant (nil CLOG). This keeps the visibility-layer change self-contained and
independently testable.

## Testing

`internal/mvcc/snapshot_clog_fallback_test.go`:

- nil CLOG → `SeesCommittedXID` identical to pre-change (in-window not-running →
  visible).
- in-window XID with CLOG status `Aborted` → invisible (the G4 fix).
- in-window XID with CLOG status `Committed` → visible.
- in-window XID with CLOG status `Unknown` → visible (runtime-new XID, no
  regression).
- XID below `OldestClogXid` → CLOG consult skipped, treated committed.
- `Clone()` preserves the CLOG so the clone makes the same decision.
- subxact twin: `SeesCommittedXIDWithSubxacts` with a CLOG-aborted top-level
  ancestor → invisible (inherited fallback).

Gates: `go test -race ./internal/mvcc/...`; build; TPC-H spot-check
(`scripts/tpch-spotcheck.sh`, canonical Q12=2/Q13=33) to confirm the live
visibility path is unchanged (CLOG dormant in the bench → no row-count shift).

## Upstream references

- `postgres/src/backend/access/heap/heapam_visibility.c`
  (`HeapTupleSatisfiesMVCC` — clog consult after the running-array check).
- `postgres/src/backend/access/transam/transam.c`
  (`TransactionIdDidCommit` / `TransactionIdDidAbort`).
- `postgres/src/backend/access/transam/clog.c` (`TransactionIdGetStatus`).
