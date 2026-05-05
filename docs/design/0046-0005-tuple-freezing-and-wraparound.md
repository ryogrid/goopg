# Tuple Freezing & Anti-Wraparound — M0046-0005

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

PostgreSQL's XID counter is a 32-bit unsigned integer (0 – 4,294,967,295).
XIDs 0, 1, 2 are reserved; normal XIDs start at 3. Visibility comparisons are
modular: XID A is "before" B if `(int32)(A − B) < 0` (within 2^31 of each
other). After roughly 2.1 billion transactions, the oldest live tuples can
appear to be "in the future" relative to a new snapshot — making them
invisible.

The fix is **tuple freezing**: VACUUM rewrites the `xmin` of sufficiently old
tuples to `FrozenTransactionId = 2`. Because `2 < FirstNormalXid (3) ≤ every
snapshot's Xmin`, the existing `SeesCommittedXID` check trivially returns
true for frozen tuples — **no change to `TupleVisible` is required**.

## 2. Design

### 2.1 FrozenTransactionID (`internal/storage/heap.go`)

`FrozenTransactionID = TransactionID(2)` is added to `storage` (mirroring the
existing `mvcc.FrozenTransactionID`) so the freeze code in the storage package
can use it without a circular import.

### 2.2 PageFreezeOldTuples (`internal/storage/freeze.go`)

`PageFreezeOldTuples(p Page, freezeBelow TransactionID) (PageFreezeStats, error)`:
- Walks all `ItemIDNormal` tuples.
- For each tuple with `0 < xmin < freezeBelow` (real, non-frozen, old):
  - Skips deleted tuples (`xmax != 0` and not lock-only) — they will be vacuumed away.
  - Rewrites `xmin ← FrozenTransactionID` in the raw page bytes.
- Returns `PageFreezeStats{Frozen int, MinUnfrozenXID TransactionID}`.
  `MinUnfrozenXID` is the lowest xmin NOT frozen; used to advance `relfrozenxid`.

No infomask change (`HEAP_XMIN_FROZEN`) is needed for v0: `FrozenTransactionID(2)` is
automatically visible under the existing `SeesCommittedXID` because every
snapshot's Xmin ≥ 3.

### 2.3 VacuumOptions + freeze pass (`internal/vacuum/vacuum.go`)

A new `VacuumOptions` struct replaces the growing function parameter list:

```go
type VacuumOptions struct {
    FSM         *storage.FSM
    VM          *storage.VisibilityMap
    FreezeBelow storage.TransactionID  // 0 = skip freezing
}
```

`vacuumCore` now runs a freeze pass after the dead-tuple reclamation pass.
`Stats` gains `Frozen int` and `NewFrozenXID storage.TransactionID`.

Backward-compatible wrappers `Vacuum`, `VacuumWithFSM`, `VacuumWithFSMAndVM`
are preserved; `VacuumWithOptions` is the new full-featured entry point.

The freeze page is marked dirty via `pool.MarkDirty` (conservative FPI). If the
server crashes before the next checkpoint the freeze will simply re-run on next
VACUUM — no data loss.

### 2.4 relfrozenxid tracking (`internal/catalog/catalog.go`)

`Table.RelFrozenXID storage.TransactionID` (new field) tracks the minimum
unfrozen xmin. Updated by `vacuumOp` after each freeze pass. Initialised to 0.

### 2.5 Freeze GUCs (`internal/config/defaults.go`)

| GUC | Default | Meaning |
|---|---|---|
| `vacuum_freeze_min_age` | 50,000,000 | Minimum XID age before freezing |
| `autovacuum_freeze_max_age` | 200,000,000 | Age threshold for forced anti-wraparound vacuum |

### 2.6 VACUUM SQL executor (`internal/executor/operators_vacuum.go`)

`vacuumOp.Next()` computes `freezeBelow = currentXID − FreezeMinAge` and calls
`vacuum.VacuumWithOptions(..., opts)`. After the pass it updates
`tbl.RelFrozenXID` from `stats.NewFrozenXID`.

`FreezeMinAge int64` is a new `executor.Context` field, set from the
`vacuum_freeze_min_age` GUC by `server/dispatch.go`.

### 2.7 Anti-wraparound trigger (`internal/autovacuum/launcher.go`)

`needsVacuum(tbl)` now checks:
```go
if currentXID − tbl.RelFrozenXID > autovacuumFreezeMaxAge {
    return true  // force anti-wraparound vacuum
}
```

The check is skipped when `RelFrozenXID == 0` (no freeze pass yet).

### 2.8 xidWarn/xidStop guards (`internal/mvcc/manager.go`)

`Manager.Begin()` refuses new transactions when
`nextXID > ^TransactionID(0) − xidStopAge (3,000,000)`, returning an error
that names the number of transactions remaining before forced shutdown. This
prevents the modular-arithmetic visibility inversion before it occurs.

## 3. Invariants

| Property | Guarantee |
|---|---|
| Frozen tuple always visible | FrozenTransactionID(2) < any snapshot Xmin(≥3) → `SeesCommittedXID` returns true |
| Deleted tuples not frozen | Only live (xmax=0 or lock-only) tuples are frozen |
| No TupleVisible change needed | Existing visibility logic already handles xmin=2 correctly |
| Crash-safe | Freeze uses MarkDirty (FPI); re-runs on next VACUUM if lost |
| Anti-wraparound enforced | xidStopAge guard prevents silent data loss from modular arithmetic |

## 4. Tests

| Test | Coverage |
|---|---|
| `TestPageFreezeOldTuples` | Old tuple frozen, recent unfrozen, already-frozen skipped |
| `TestPageFreezeSkipsDeleted` | Deleted tuples not frozen |
| `TestPageFreezeNoOpZeroThreshold` | FreezeBelow=0 is a no-op |
| `TestFrozenTransactionIDVisibility` | FrozenTransactionID=2 is always < snapshot Xmin |
| `TestTupleFreezeBasic` | End-to-end: freeze at 1M XIDs, row still visible at 2M |
| `TestTupleFreezeDoD` | **DoD**: 1B simulated XIDs → freeze → 5 rows still visible |
| `TestPageFreezeIntegration` | Storage-level freeze on executor-written page |
| `TestVacuumFreezeStats` | vacuumCore reports correct Frozen count + NewFrozenXID |
| `TestAutoVacuumAntiWraparoundTrigger` | Anti-wraparound vacuum triggered + tuples visible |
| `TestXIDWarnLimit` | Manager.Begin() fails near uint32 overflow |

## 5. Deferred

- **HEAP_XMIN_FROZEN infomask bit**: Performance optimisation (skip
  `SeesCommittedXID` for frozen tuples); not needed for correctness in v0.
- **Persistent relfrozenxid in pg_class heap**: M0030-0001 heap-catalog
  integration will persist the field across restarts.
- **Freeze WAL record (`XLOG_HEAP2_FREEZE_PAGE`)**: Currently uses FPI (MarkDirty);
  a logical freeze record would allow smaller WAL.
