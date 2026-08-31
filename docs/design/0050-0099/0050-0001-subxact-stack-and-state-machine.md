# Subtransaction Stack and State Machine — M0050-0001

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

The MVCC manager and session state today support a single flat transaction
(one XID, one snapshot). Savepoints require a stack of sub-transaction states
with snapshot capture, rewind, and lock-owner tracking semantics.

## 2. Design

### 2.1 Types (`internal/mvcc/subxact.go`)

```go
type SubTxnId uint32        // monotonic per top-level transaction
type SubXactStatus int       // active | committed | aborted

type SubTransactionState struct {
    Id     SubTxnId
    Name   string                   // SAVEPOINT name; "" for implicit
    SubXid storage.TransactionID    // 0 until first write (lazy)
    Snap   *Snapshot                // snapshot at push time
    Status SubXactStatus
    Parent *SubTransactionState
}
```

### 2.2 `SubxactStack` (`internal/mvcc/subxact.go`)

An ordered list of `*SubTransactionState`, oldest first. All methods are
nil-safe (zero-value session state before any SAVEPOINT).

| Method | Semantics |
|---|---|
| `Push(name, snap)` | Create entry with monotonic Id, copy snapshot, link Parent |
| `Top()` | Innermost active entry |
| `Find(name)` | Innermost active entry with given name |
| `Release(name)` | Mark named + inner entries `SubXactCommitted`, pop from slice; caller promotes locks to parent |
| `RollbackTo(name, newSnap)` | Mark named + inner entries `SubXactAborted`, pop, push fresh entry with same name; caller drops locks |
| `AbortAll()` | Mark all entries aborted, clear slice; called by top-level `ROLLBACK` |

### 2.3 Subxact xid allocation (lazy)

`SubTransactionState.SubXid` is 0 until the first write within the
subtransaction. M0050-0002/0003 wire the actual XID assignment call
(`Manager.AllocateSubXid`) into `writeHeapRow` and the WAL path.

### 2.4 Lock-owner tracking model

`SubTxnId` is the identifier the lock manager (M0050-0004) will use to
track lock ownership:

- **RELEASE**: released entries are `SubXactCommitted`; caller calls
  `LockManager.TransferLocks(subxactId, parentId)` for each.
- **ROLLBACK TO**: aborted entries are `SubXactAborted`; caller calls
  `LockManager.ReleaseBySubxact(subxactId)` for each.

The `SubTxnId` field on returned entries provides the precise
identity needed for these calls.

## 3. Correctness

- `Push` captures a snapshot copy so rewinding to a savepoint restores
  the correct snapshot without aliasing.
- `RollbackTo` pushes a fresh entry with a new (higher) `Id` so that
  post-rollback mutations in the same savepoint name do not collide with
  the aborted entry's `SubTxnId` in the lock manager.
- All methods are nil-safe: a session that has never issued `SAVEPOINT`
  has a nil `*SubxactStack` and all operations return the zero value.

## 4. Tests (`internal/mvcc/subxact_test.go`)

| Test | Coverage |
|---|---|
| `TestSubxactStackPush` | Monotonic Id, parent linkage, snapshot capture, active status |
| `TestSubxactStackRelease` | Named + inner entries committed; outer entries unchanged |
| `TestSubxactStackReleaseNotFound` | Error on missing name; stack unchanged |
| `TestSubxactStackRollbackTo` | Named + inner aborted; fresh entry pushed with higher Id |
| `TestSubxactStackRollbackToNotFound` | Error on missing name; stack unchanged |
| `TestSubxactStackAbortAll` | All entries aborted; stack cleared |
| `TestSubxactStackNilSafe` | All methods safe on nil receiver |
| `TestSubxactStackLockOwnerCorrectnessModel` | **DoD**: Released entries = lock-promote targets; aborted entries = lock-drop targets |
| `TestSubxactStackSnapshotCapture` | Each entry holds distinct snapshot; RollbackTo fresh entry gets new snapshot |
