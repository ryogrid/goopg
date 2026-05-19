# 0100-0005: Clear HeapXmaxInvalid when Stamping Real xmax

## Problem

`TestPort_IsolationPartitionKeyUpdate1` and `TestPort_IsolationPartitionKeyUpdate4`
were regressing from PASS to SKIP on the external-cluster test path (canonical WAL)
after M0106-0010 introduced canonical-WAL heap inserts.

Root cause: `writeHeapRowReturning` and `writeHeapRowReturningPG` set
`HeapXmaxInvalid (0x0800)` on freshly inserted tuples when `ctx.LogCanonical != nil`:

```go
if ctx.LogCanonical != nil {
    tuple.Header.Infomask |= storage.HeapXmaxInvalid  // marks "xmax is not a deleter"
}
```

This flag is correct for a freshly inserted row (xmax = InvalidTransactionID means
"no deleter yet"). However, `PageSetHeapTupleXmax` and `PageSetHeapTupleMovedPartition`
— called when a DELETE or cross-partition UPDATE stamps a real xmax — did not clear
this flag. They only cleared `HeapXmaxLockOnly | HeapXmaxLockMask`.

As a result, `isConcurrentlyUpdated` saw `HeapXmaxInvalid` set and short-circuited:

```go
if h.Infomask&storage.HeapXmaxInvalid != 0 {
    return false  // "xmax is not a deleter" — skips EPQ wait!
}
```

A concurrent DELETE or UPDATE on the same row would find no concurrent update and
complete immediately without waiting for the in-flight transaction, producing no
`<waiting ...>` output in the isolation test and no "moved to another partition" error.

This only affected the **canonical WAL path** (external cluster started with
`goopg init`). In-process server tests use `startCopyExecServer` which sets
`LogCanonical = nil`, so `HeapXmaxInvalid` was never set and the bug was invisible.

## Fix

Both stamp functions now also clear `HeapXmaxInvalid`:

```go
infomask &^= HeapXmaxLockOnly | HeapXmaxLockMask | HeapXmaxInvalid
```

This mirrors PostgreSQL's `heap_update` / `heap_delete` which clear
`HEAP_XMAX_INVALID` when re-stamping xmax on a tuple.

Note: `PageSetHeapTupleLockOnly` already cleared `HeapXmaxInvalid` (line 986 in
the original code). The fix makes `PageSetHeapTupleXmax` and
`PageSetHeapTupleMovedPartition` consistent with it.

## Files Changed

- `internal/storage/heap.go`: `PageSetHeapTupleXmax` and `PageSetHeapTupleMovedPartition`
  clear `HeapXmaxInvalid` in addition to `HeapXmaxLockOnly | HeapXmaxLockMask`
- `internal/storage/heap_test.go`: two regression tests pin the fix:
  - `TestPageSetHeapTupleXmaxClearsHeapXmaxInvalid`
  - `TestPageSetHeapTupleMovedPartitionClearsHeapXmaxInvalid`
- `internal/server/pku1_wait_test.go`: server-level integration test
  `TestPartitionKeyUpdate1_BlocksDeleteAndUpdate`

## Test Coverage

`TestPort_IsolationPartitionKeyUpdate1` and `TestPort_IsolationPartitionKeyUpdate4`
now PASS on the external-cluster path.
