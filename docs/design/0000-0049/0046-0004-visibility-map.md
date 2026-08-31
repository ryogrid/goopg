# Visibility Map (VM) — M0046-0004

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

Without a visibility map, every index scan must fetch the heap page to check
MVCC visibility. After VACUUM, all committed tuples on a page are visible to
all current and future snapshots — fetching the heap just to confirm visibility
is wasteful. PostgreSQL's Visibility Map (VM) eliminates this overhead: a 1-bit
per-page marker (`ALL_VISIBLE`) signals that no heap fetch is needed for
visibility during an index scan whose projected columns are covered by the index.

## 2. Design

### 2.1 VisibilityMap struct (`internal/storage/vm.go`)

An in-memory per-relation bit array keyed by `{DBOid, RelOid}`:

```go
type VisibilityMap struct {
    mu    sync.RWMutex
    pages map[vmKey][]bool  // [DBOid,RelOid] → ALL_VISIBLE per BlockNumber
}
```

Operations:
- `AllVisible(rel, blk) bool` — fast-path check for index-only scans
- `SetAllVisible(rel, blk)` — called by VACUUM after verifying all tuples
- `ClearBlock(rel, blk)` — called on any page modification (insert/delete/update)
- `DropRelation(rel)` — called on DROP TABLE to prevent stale bits
- All methods are nil-safe

### 2.2 PageAllVisible (`internal/storage/vm.go`)

`PageAllVisible(p Page, horizon TransactionID) bool` checks that every
`ItemIDNormal` tuple on `p` satisfies:
- `xmin != Invalid && xmin < horizon` (committed before all active snapshots)
- `xmax == Invalid` or `xmax` is lock-only (not deleted)

Used by VACUUM to decide whether to set the ALL_VISIBLE bit.

### 2.3 VACUUM sets VM bits (`internal/vacuum/vacuum.go`)

`VacuumWithFSMAndVM(pool, mgr, rel, fsm, vm)` extends the existing vacuum
core. After each page prune, if `PageAllVisible(page, horizon)` returns true,
the VM bit is set via `vm.SetAllVisible(rel, blk)`; otherwise it is cleared.

### 2.4 Index-only scan (`internal/planner/` + `internal/executor/`)

**Planner** (`planner.go`, `plan.go`):

A new `IndexOnlyScan` plan node (same fields as `IndexScan` plus `Covered
[]catalog.Column`) is generated when:
1. `planIndexScanFromWhere` produces an `IndexScan`.
2. All SELECT-list columns are covered by the index key.
3. There are no locking clauses (`FOR UPDATE` / `FOR SHARE`).

The promotion is done by `tryPromoteIndexOnlyScan(proj *Project) Node`.

**Executor** (`operators_indexonly.go`):

`indexOnlyScanOp.Open()` runs a B-tree RangeScan with two paths:

| Page VM bit | Action |
|---|---|
| ALL_VISIBLE | Decode row from B-tree key bytes — **zero heap reads** |
| Not set | Fetch heap page, follow HOT chain, check MVCC (full fallback) |

Key decode (`decodeBTreeKeyToDatum`) inverts the `btree.EncodeXxx` functions
for int4, int8, varchar, char, and timestamp column types.
DecodeVarchar + DecodeTimestamp are new helpers added to the btree package.

### 2.5 Page modification clears VM

Any write to a heap page clears the VM bit:
- `writeHeapRowReturning` (INSERT): `ctx.VM.ClearBlock(rel, blk)` after
  `PageAddHeapTuple` succeeds — in the existing `tryAppendToBlock` closure
  and in the `PinNew` path.
- UPDATE old-image / DELETE stamp (`markHeapDeleteDirtyAndClearVM`): a new
  helper wrapping `markHeapDeleteDirty` + `VM.ClearBlock`.

## 3. Invariants

| Property | Guarantee |
|---|---|
| Safe skip | VM bit is only set when all tuples are committed and pre-horizon |
| Cleared on write | Any heap page modification immediately clears the bit |
| Locking guard | IndexOnlyScan is not generated when FOR UPDATE/SHARE is present |
| Fallback | VM=false → full heap fetch + MVCC check; no data loss |
| nil-safe | All VM methods are nil-safe; no-op without VM wired |

## 4. Tests

| Test | Location | Coverage |
|---|---|---|
| `TestVMSetAllVisible` | `storage/vm_test.go` | Set / check / clear cycle |
| `TestVMClearBlock` | storage | ClearBlock + no-panic on unseen blocks |
| `TestVMDropRelation` | storage | DropRelation clears all bits |
| `TestVMNilSafe` | storage | Nil VM no-ops |
| `TestPageAllVisible` | storage | Fresh page + committed tuple = ALL_VISIBLE |
| `TestPageAllVisibleDeadTuple` | storage | Tuple with xmax set → NOT ALL_VISIBLE |
| `TestIndexOnlyScanAfterVacuum` | `executor/vm_test.go` | DoD: VACUUM sets VM, SELECT returns correct value via key decode |
| `TestVMClearedOnInsert` | executor | Insert clears VM bit |
| `TestPageAllVisibleIntegration` | executor | Committed page is ALL_VISIBLE after begin |
| `TestIndexOnlyScanFallbackWithoutVM` | executor | No VM → heap fallback, correct results |

## 5. Deferred

- **Disk persistence** (`_vm` fork): VM is in-memory only; reset on restart.
- **FROZEN bit**: ALL_FROZEN tracking deferred to M0046-0005 (tuple freezing).
- **Multi-column index keys**: `decodeRowFromKey` currently requires a
  single-column index (returns error for composite keys).
- **INCLUDE columns**: index-only scan only covers key columns; INCLUDE support
  is a follow-up that would extend coverage to non-key projections.
