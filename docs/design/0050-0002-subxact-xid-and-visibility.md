# Subxact XID Allocation and Visibility — M0050-0002

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

When subtransactions write rows, the snapshot manager must answer "is XID X
visible to snapshot S?" correctly for subxact XIDs. Visibility depends on
both the subxact's own status (individually rolled back?) and its parent's
top-level status.

## 2. Design

### 2.1 Subxact map (`internal/mvcc/subxact_visibility.go`)

The `Manager` embeds a `subxactFields` struct:

```go
type subxactFields struct {
    subxactMu      sync.RWMutex
    subxactParents map[storage.TransactionID]storage.TransactionID  // subxid → parent xid
    subxactAborted map[storage.TransactionID]bool                   // aborted subxids
}
```

Maps are lazily initialised on first use. The Manager exposes:

```go
func (m *Manager) RegisterSubXid(subxid, parentXid storage.TransactionID)
func (m *Manager) MarkSubxactAborted(subxid storage.TransactionID)
func (m *Manager) TopLevelXid(xid storage.TransactionID) storage.TransactionID
func (m *Manager) IsAborted(xid storage.TransactionID) bool
func (m *Manager) IsSubxact(xid storage.TransactionID) bool
```

### 2.2 `SubxactResolver` interface

`Manager` satisfies `SubxactResolver`:

```go
type SubxactResolver interface {
    TopLevelXid(xid storage.TransactionID) storage.TransactionID
    IsAborted(xid storage.TransactionID) bool
    IsSubxact(xid storage.TransactionID) bool
}
```

### 2.3 `SeesCommittedXIDWithSubxacts`

Visibility rules for subxact XIDs:

1. If `xid` is individually aborted → **invisible** (ROLLBACK TO SAVEPOINT).
2. Resolve `xid` to its top-level ancestor via `TopLevelXid`.
3. Apply normal `Snapshot.SeesCommittedXID` against the top-level XID.

With `nil` resolver, degrades to standard `SeesCommittedXID`.

### 2.4 `TupleVisibleSubxact`

`TupleVisibleSubxact(h, snap, currentXID, resolver)` replaces
`SeesCommittedXID(h.Xmin)` and `SeesCommittedXID(h.Xmax)` calls with
`SeesCommittedXIDWithSubxacts(snap, xid, resolver)`. With `nil` resolver it
matches `TupleVisible` exactly (existing callers unaffected).

### 2.5 Lazy XID allocation

`SubTransactionState.SubXid` starts at 0. When a subxact first writes,
the heap-write path (M0050-0003/0004) calls `Manager.RegisterSubXid(subxid, parentXid)`.
Until then the subxact has no XID and its writes ride on the parent's XID.

## 3. Visibility matrix

| subxact status | parent status | snapshot sees? |
|---|---|---|
| active | in-progress | no (parent in InProgress) |
| committed (RELEASE) | committed | yes (parent past Xmin) |
| aborted (ROLLBACK TO) | committed | **no** (IsAborted = true) |
| aborted | in-progress | no (parent in InProgress) |
| active | committed | yes (subxact not aborted, parent committed) |

Matches upstream `HeapTupleSatisfiesMVCC` subxact branch.

## 4. Tests (`internal/mvcc/subxact_visibility_test.go`)

| Test | Coverage |
|---|---|
| `TestSubxactVisibilityMatrix` | **DoD**: 3 matrix cells (active/parent-inprog, committed/parent-committed, aborted/parent-committed) |
| `TestTopLevelXidChain` | Multi-level parent walk |
| `TestSeesCommittedXIDWithSubxactsNilResolver` | Nil resolver degrades correctly |
| `TestTupleVisibleSubxactDegrades` | TupleVisibleSubxact matches TupleVisible with nil |
| `TestSubxactAbortHidesRowAfterParentCommit` | Aborted-subxact row invisible even after parent commit |
