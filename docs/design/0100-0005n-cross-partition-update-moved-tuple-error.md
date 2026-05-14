# 0100-0005n — Cross-partition UPDATE: moved-tuple EPQ error

Status: accepted (2026-05-15, M0100-0005n / loop 30)

## Problem

`partition-key-update-1.spec` (and several other partition-key-update
specs) drives the upstream PostgreSQL error path:

```
ERROR:  tuple to be locked was already moved to another partition
        due to concurrent update
```

That error fires when transaction T2 walks an EPQ retry to a heap
tuple whose `t_xmax` was committed by transaction T1, and T1's UPDATE
moved the row to a different partition relation entirely.  Because
the new version lives in another relfile, the old version's `t_ctid`
cannot carry a successor pointer; PostgreSQL stamps the upstream
sentinel `MovedPartitionsOffsetNumber` (`0xFFFD`, paired with
`InvalidBlockNumber`) into `t_ctid` so EPQ retries can detect the
move and raise the distinguishing error instead of silently skipping
the row.

Prior to this change goopg's cross-partition UPDATE deleted the old
row (xmax stamp) and inserted the new row in the destination
partition with no marker, so any concurrent UPDATE/DELETE/SELECT FOR
UPDATE on the old key saw the deleted tuple, ran `epqFollowHOT`
(which returns `not-found` because the chain ends), and skipped the
row.  All `partition-key-update-1.spec` permutations whose expected
output contains the moved-partition `ERROR:` line therefore deferred.

## Solution

Three coordinated changes — none touch the wire protocol.

### 1. Storage primitive (`internal/storage/heap.go`)

Add the upstream sentinel value and a stamping helper:

```go
const MovedPartitionsOffsetNumber uint16 = 0xFFFD

func IsMovedToAnotherPartition(ctid ItemPointer) bool {
    return ctid.Block == InvalidBlockNumber &&
           ctid.Offset == MovedPartitionsOffsetNumber
}

func PageSetHeapTupleMovedPartition(p Page, slot uint16, xmax TransactionID) error
```

`PageSetHeapTupleMovedPartition` is the partition-move sibling of
`PageSetHeapTupleXmax`: it writes `xmax` AND overwrites `t_ctid`
with `(InvalidBlockNumber, MovedPartitionsOffsetNumber)` in one
operation, then clears any prior `HEAP_XMAX_LOCK_ONLY` / lock-mask
bits.  Returns `ErrUnsupportedItem` if the slot isn't `LP_NORMAL`
and `ErrInvalidSlot` for out-of-range slots — same contract as the
sibling helpers.

### 2. Cross-partition write site (`internal/executor/operators_storage.go`)

Both the SeqScan and idxScan UPDATE paths already compute the
destination partition's relfile via `routeToPartition` and write to
the destination.  Re-order so the destination is computed *before*
stamping xmax, then choose the helper based on whether the move
crosses relfile boundaries:

```go
isCrossPartitionMove := false
if imW, ok := ctx.Catalog.(*catalog.InMemory); ok && len(tbl.PartitionKey) > 0 {
    if destPart := routeToPartition(tbl, pu.newRow, imW); destPart != nil {
        destRel := ctx.Catalog.RelFileNode(destPart)
        if destRel != puRel {
            isCrossPartitionMove = true
        }
        ...
    }
}
var stampErr error
if isCrossPartitionMove {
    stampErr = storage.PageSetHeapTupleMovedPartition(s.Page(), pu.slot, ctx.Tx.XID)
} else {
    stampErr = storage.PageSetHeapTupleXmax(s.Page(), pu.slot, ctx.Tx.XID)
}
```

Same-partition updates are unaffected — they keep the plain
`PageSetHeapTupleXmax` and continue to set up HOT chains where
eligible.

### 3. EPQ retry detection (`internal/executor/operators_storage.go`)

Add a helper that re-reads the old slot's `t_ctid` after EPQ wait:

```go
func epqSlotMovedToAnotherPartition(ctx *Context, rel storage.RelFileNode,
    blk storage.BlockNumber, slot uint16) bool
```

and a canonical error constructor that matches upstream's MESSAGE
byte-for-byte:

```go
func errMovedToAnotherPartition(pos int) *ExecError {
    return &ExecError{
        Code:    "0A000",
        Pos:     pos,
        Message: "tuple to be locked was already moved to another partition due to concurrent update",
    }
}
```

SQLSTATE `0A000` mirrors `errcode_for_partition` in upstream
`heapam.c`'s raise.  Three EPQ retry sites now consult the sentinel
before falling through to "chain not found, skip the row":

- `updateOp.updateViaIndex` (idxScan)
- `updateOp.Next` SeqScan body
- `deleteOp.Next` SeqScan body

In all three, the check is placed immediately before the call to
`epqFollowHOT` in the `Tx.Isolation == ReadCommitted` branch — i.e.
after we've established `xmax` is committed and the chain might
terminate.  If the sentinel is set, the row was cross-partition-moved
and we raise `errMovedToAnotherPartition`; otherwise the existing
HOT-chain code runs unchanged.

## Why the chain-follow comes after the sentinel check

`epqFollowHOT` is HOT-only (same-page successor).  A cross-partition
move never produces a same-page successor, so `epqFollowHOT` always
returns `not-found` for moved rows.  Checking the sentinel *before*
the HOT chain follow keeps the two cases cleanly separated:
- sentinel set → raise distinct error
- sentinel clear and chain ends → row is genuinely gone, skip

If the order were reversed, every cross-partition move would
short-circuit on `chainFound=false` and we'd lose the error.

## Why xmax stamp is still required on the old slot

The moved-partition sentinel does not replace the xmax stamp — it's
written *alongside* it.  Concurrent readers consult `t_xmax` for
visibility; an unstamped xmax would leave the row visible to readers
that arrived after the move and broke RC's "writers don't block
readers" property.  `PageSetHeapTupleMovedPartition` writes both
fields atomically under the page write lock.

## Scope deliberately not covered

- **Partition-child triggers**: `partition-key-update-1.spec` has a
  `BEFORE UPDATE` trigger on `footrg1` that sets `NEW.a = 2`, which
  would itself trigger a cross-partition move.  Our trigger-firing
  path currently looks up triggers on the parent table only, so the
  trigger doesn't fire and the move never happens.  Adding child-
  trigger lookup is its own follow-up; the moved-partition error
  path lands first because it's the load-bearing diff for the
  non-trigger permutations (s1u s2d / s1u s2u on `foo`).

- **SELECT FOR UPDATE / FOR KEY SHARE (`lockRowsOp`)**: same fix
  applies but isn't strictly required for `partition-key-update-1`
  (the spec uses UPDATE/DELETE only on `foo`).  The lockRows path is
  exercised by `foo_range_parted` permutations downstream and will
  get the same hook in a follow-up.

- **MERGE operator**: `operators_merge.go` calls `epqFollowHOT` at
  two sites for matched/not-matched recheck; cross-partition move
  detection there will land alongside MERGE-specific recheck work.

## Verification

Storage primitive tests
(`internal/storage/heap_test.go::TestPageSetHeapTupleMovedPartition`,
`TestPageSetHeapTupleMovedPartitionInvalidSlot`,
`TestIsMovedToAnotherPartitionNegatives`) cover the byte-level
sentinel write, lock-mask clearing, slot bounds, and the
identification predicate's positive + negative cases.

Executor regression pins
(`internal/executor/epq_moved_partition_test.go::TestEPQSlotMovedToAnotherPartitionDetectsSentinel`,
`TestEPQSlotMovedToAnotherPartitionRejectsPlainXmax`,
`TestErrMovedToAnotherPartitionShape`) verify the EPQ helper
distinguishes a sentinel stamp from a plain xmax stamp, and that the
canonical error MESSAGE + SQLSTATE are unchanged.

End-to-end: `go test -race ./internal/executor/ ./internal/storage/
./internal/server/ ./internal/mvcc/ ./internal/planner/
./internal/parser/` — all green.  `TestPort_IsolationPartitionKeyUpdate1`
diff narrows from 13 → 11 missing lines; the remaining 11 are all
inside trigger-driven permutations on `footrg` (separate scope, see
above).
