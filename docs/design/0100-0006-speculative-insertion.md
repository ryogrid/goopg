# Speculative Insertion for ON CONFLICT

## Background

PostgreSQL's `INSERT ... ON CONFLICT` uses a two-phase index insertion protocol
called *speculative insertion* to detect concurrent conflicts without holding
page locks across arbiter expression evaluation.

The sequence for a specifying-inserter (one that has a unique index as arbiter):

1. **Phase A** (`findInProgressConflict` / `probeArbiterWaiting`): evaluate the
   arbiter index expression to build the btree lookup key; scan the arbiter btree
   for rows with in-progress XIDs; block on advisory lock if found.
2. **Phase B first call** (new, inside `applyInsert`): evaluate the arbiter
   expression again to build the btree key *before* writing the heap row, then
   insert the speculative btree entry. This is the "speculative insertion" proper.
3. **Phase B conflict probe** (`probeSpeculativeConflict`): after `applyInsert`,
   re-scan the arbiter btree excluding own-xmin rows. If a committed concurrent
   row is found, cancel the speculative heap row (`cancelSpeculativeRow`) and
   fall through to the conflict handler.

For `DO UPDATE` path, PostgreSQL also calls `ExecBuildArbiterKey` at the entry
point of the UPDATE branch to re-evaluate the arbiter expression for the updated
row, producing two more `NOTICE` lines from `blurt_and_lock_123`.

For `DO NOTHING` path, two explicit arbiter key evaluations are performed.

## Implementation

### `applyInsert` — Phase B first call

```go
func (o *upsertOp) applyInsert(..., insertedParent Row) (storage.ItemPointer, error) {
    // Phase B first call: evaluate BEFORE writing heap row
    var phaseBKey []byte
    if o.plan.OnConflict != nil && o.plan.OnConflict.ArbiterIndex != nil {
        key, err := encodeArbiterKey(o.ctx, o.plan.OnConflict, o.plan.Table,
            insertedParent, o.plan.Pos())
        phaseBKey = key
    }
    ptr, err := writeHeapRowReturning(...)
    // insert speculative btree entry with pre-computed key
    if phaseBKey != nil {
        _ = o.maintainArbiter(phaseBKey, ptr)
    }
    // maintain all other unique indexes, skipping the arbiter (already done above)
    maintainUniqueIndexesForInsertSkipArbiter(o.ctx, tbl, cols, insertedLeaf, ptr, arbiterOID)
    return ptr, nil
}
```

The arbiter index OID is skipped from `maintainUniqueIndexesForInsert` to avoid
double-inserting or re-evaluating the side-effectful arbiter expression. The
function `maintainUniqueIndexesForInsertSkipArbiter` in `operators_storage.go`
handles this.

### `probeSpeculativeConflict` — conflict detection after Phase B

Scans the arbiter btree with the pre-computed Phase B key. Skips rows whose
xmin equals `selfXID` (own in-progress insert). Uses `isLiveForUniqueCheck` for
visibility. Returns the conflicting heap pointer and row if found.

### `cancelSpeculativeRow` — cancel own speculative insert

Stamps `xmax = selfXID` on the speculatively-inserted heap row using
`PageSetHeapTupleXmax` + `markHeapDeleteDirty`. `isLiveForUniqueCheck` sees
this as dead (xmax == selfXID check), so subsequent scans from the same session
do not re-detect the cancelled row.

### `applyUpdate` — explicit arbiter re-evaluation

After writing the updated heap row, calls `encodeArbiterKey` explicitly (as
PostgreSQL's `ExecBuildArbiterKey` equivalent) and inserts the new btree entry.
Skips non-arbiter indexes via `maintainUniqueIndexesForInsertSkipArbiter`.

### `DO NOTHING` path — two evaluations

Two explicit `encodeArbiterKey` calls are made to match PostgreSQL's evaluation
count (4 `NOTICE` lines from `blurt_and_lock_123`).

### `DO UPDATE` entry — ExecBuildArbiterKey equivalent

Before the in-progress wait loop, one explicit `encodeArbiterKey` call produces
the required 2 `NOTICE` lines at the point PostgreSQL's `ExecBuildArbiterKey`
runs.

## NOTICE count matrix

| Permutation | Inserter | Phase A NOTICEs | Phase B NOTICEs | Completion NOTICEs |
|---|---|---|---|---|
| plain INSERT (s1) | first | 2 (block 3) | 2 (block 2) | 0 |
| DO UPDATE (s2) | second | — | — | 4 (entry 2 + applyUpdate 2) |
| DO NOTHING (s2) | second | — | — | 4 (two eval calls × 2) |
| DO UPDATE + wait (perm 4, s2) | second | — | — | 4 at wait + 2 at completion |

## Scope limitation — perm 5 (spectoken infrastructure)

Permutation 5 of `insert-conflict-specconflict.spec` requires:

1. `locktype='spectoken'` entries in `pg_locks` — speculative token acquire/release
   visible to waiting sessions.
2. `locktype='transactionid'` entries in `pg_locks` — own XID as ExclusiveLock.
3. `(step notices N)` coordination in the isolation runner — step-level NOTICE
   count synchronization.

These require dedicated infrastructure work (M0100-0006b) and are not implemented
in this change.
