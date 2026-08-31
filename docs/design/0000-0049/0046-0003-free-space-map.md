# Free Space Map (FSM) — M0046-0003

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

Without a Free Space Map, every `INSERT` attempts to append to the last
heap block. After `DELETE` + `VACUUM` reclaims dead slots, the freed pages
are invisible to the insert path: the relation is extended even though
existing blocks have room. This causes steady heap growth in update-heavy
workloads.

PostgreSQL addresses this with the FSM fork (`_fsm`): a per-relation file
that summarises free bytes per heap page, enabling the insertion code to find
a suitable existing page in O(log N) rather than scanning every block.

## 2. Design

### 2.1 In-memory FSM (`internal/storage/fsm.go`)

For v0 the FSM is a pure in-memory structure — no `_fsm` fork file is read
or written. The `FSMFork` constant and `_fsm` filename pattern are already
defined in `storage/page.go` and `storage/smgr.go` for future disk
persistence (M0046-0003 follow-up).

```go
type FSM struct {
    mu    sync.RWMutex
    pages map[fsmKey][]uint16   // [DBOid,RelOid] → freeBytes per BlockNumber
}
```

The key is `{DBOid, RelOid}` (no fork: the FSM always maps MainFork pages).
`uint16` captures 0–65535 bytes free; a `BlockSize`-page holds at most 8192
bytes so this is exact.

Key methods:
- `GetPageWithFreeSpace(rel, minFreeBytes) (BlockNumber, bool)` — linear scan
  of the slice; returns the first block with `free >= minFreeBytes`.
- `RecordFreeSpace(rel, blk, freeBytes)` — updates one entry; extends the
  slice if needed.
- `RecordFreeSpaceForPage(rel, blk, page)` — reads `PageHeader.FreeSpace()`
  and calls `RecordFreeSpace`; used by VACUUM and the insert path.
- `DropRelation(rel)` — removes all entries; called on DROP TABLE / TRUNCATE.

All methods are nil-safe: a `nil` `*FSM` silently no-ops, so test fixtures
that don't configure storage are unaffected.

### 2.2 VACUUM integration (`internal/vacuum/vacuum.go`)

`VacuumWithFSM(pool, mgr, rel, fsm)` wraps the existing `Vacuum` logic
through a shared `vacuumCore` implementation. After `VacuumHeapPageBySlots`
reclaims dead slots on each page, `fsm.RecordFreeSpaceForPage(rel, blk, page)`
records the updated free space. `Vacuum(...)` remains unchanged (passes
`fsm=nil`).

Autovacuum (`internal/autovacuum/launcher.go`) now carries a `FSM` field
and calls `VacuumWithFSM` so background vacuums populate the map without
any code change in callers.

### 2.3 Insert path integration (`internal/executor/operators_storage.go`)

`writeHeapRowReturning` consults the FSM **before** the "try last block" step:

```go
minFreeBytes := uint16(len(tupleBytes) + 4)  // 4 = line-pointer size
if ctx.FSM != nil {
    if fsmBlk, ok := ctx.FSM.GetPageWithFreeSpace(rel, minFreeBytes); ok {
        if appended, _ := tryAppendToBlock(fsmBlk); appended {
            return ptr, nil
        }
        ctx.FSM.RecordFreeSpace(rel, fsmBlk, 0) // invalidate stale entry
    }
}
// ... existing last-block + PinNew path
```

After every successful `PageAddHeapTuple` (in `tryAppendToBlock` and in the
`PinNew` path), `ctx.FSM.RecordFreeSpaceForPage` updates the FSM with the
page's remaining free space. This keeps the map accurate as the page fills.

### 2.4 VACUUM SQL dispatch (`internal/executor/`)

`vacuumOp` is a new executor operator dispatched for `*parser.VacuumStmt`.
It replaces the previous `utilityNoOp` path, running
`vacuum.VacuumWithFSM(pool, txnMgr, rel, ctx.FSM)` for each target
relation. Without explicit targets, it vacuums all non-virtual user tables
via `catalog.InMemory.AllTables()`.

### 2.5 Wiring

| Location | Change |
|---|---|
| `initdb.Runtime` | New `FSM *storage.FSM` field; created by `Open()` |
| `server.Config` | New `FSM *storage.FSM` field |
| `server/dispatch.go` | `ctx.FSM = s.cfg.FSM` |
| `cmd/goopg/main.go` | `cfg.FSM = rt.FSM` |
| `autovacuum.Launcher` | New `FSM *storage.FSM` field; used in vacuum dispatch |
| `executor.Context` | New `FSM *storage.FSM` field |

## 3. Invariants

| Property | Guarantee |
|---|---|
| No stale reuse | If FSM returns a full block, the insert invalidates the entry and falls through to the normal path — no duplicate writes |
| nil-safe | All methods no-op on a nil FSM |
| VACUUM population | `VacuumWithFSM` always calls `RecordFreeSpaceForPage` after each page scan |
| Insert population | After every successful `PageAddHeapTuple`, the FSM is updated with the remaining free space |

## 4. Tests

| Test | Coverage |
|---|---|
| `TestFSMBasic` | Round-trip RecordFreeSpace / GetPageWithFreeSpace; invalidation |
| `TestFSMDropRelation` | DropRelation clears entries |
| `TestFSMRecordFreeSpaceForPage` | Reads PageHeader.FreeSpace() |
| `TestFSMNilSafe` | Nil FSM no-ops |
| `TestFSMInsertReusesVacuumedPage` | DoD: INSERT + DELETE + VACUUM + INSERT → no relation extension |
| `TestVacuumUpdatesFSM` | VACUUM updates FSM with post-vacuum free space |
| `TestFSMInsertUpdatesFSM` | INSERT populates FSM with remaining page free space |
| `TestVacuumSQLDispatch` | VACUUM SQL routes to vacuumOp (not utilityNoOp) |
| `TestFSMMultiTransactionReuse` | Multi-tx lifecycle: fill → delete → vacuum → reuse |

## 5. Deferred

- **Disk persistence** (`_fsm` fork file): the FSM is in-memory only; it is
  empty after a server restart until VACUUM runs. Upstream's `pg_freespacemap`
  tree-of-pages format is reserved for a follow-up.
- **GetPageWithFreeSpace O(log N)**: current scan is O(N pages). The FSM's
  page count is bounded by the relation size; for typical workloads this is
  fast enough.  A binary heap or tree would improve worst-case for very large
  tables.
- **TRUNCATE integration**: `DropRelation` is not yet called on `TRUNCATE TABLE`.
