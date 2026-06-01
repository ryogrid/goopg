# MVCC Optimization Design Bundle

This directory contains the design documents for the MVCC visibility
optimization milestones derived from `practice/pg_mvcc_internals.md`.

## Background

PostgreSQL's MVCC layer is not a naïve per-tuple status lookup. It is a
layered caching system where each layer eliminates the need to proceed to the
next:

```
Frozen-XID fast path (xmin == 2 → always visible)
    ↓
Hint Bits (HEAP_XMIN_COMMITTED / HEAP_XMIN_INVALID cached in tuple infomask)
    ↓
Snapshot horizon (xid < snapshot.Xmin → visible)
    ↓
In-progress check (linear scan of ProcArray)
    ↓
pg_xact SLRU lookup (disk I/O — avoided when all above hit)
```

goopg has implemented the Visibility Map, HOT Updates, opportunistic page
pruning, VACUUM Freeze, and the ProcArray. The remaining gaps from
`practice/pg_mvcc_internals.md` are:

| Optimization | PG Source | Status |
|---|---|---|
| Hint Bits | `heapam_visibility.c` `SetHintBits` | **not implemented** → M0115 |
| Frozen-XID fast path | `TransactionIdIsNormal(xmin)` check | **not implemented** → M0115 |
| Multi-column IOS (Visibility Map + index cover) | `nodeIndexonlyscan.c` | **single-column only** → M0116 |
| Visibility Map | `visibilitymap.c` | implemented (M0046-0004) |
| HOT Updates | `heapam.c` `heap_hot_search` | implemented (M0046-0001) |
| Page Pruning | `pruneheap.c` | implemented (M0046-0002) |
| VACUUM Freeze | `vacuumlazy.c` | implemented (M0046-0005) |
| ProcArray / Snapshot | `procarray.c` | implemented (M0030) |

## Documents

| File | Milestone | Title |
|---|---|---|
| [0115-0001-hint-bit-caching.md](0115-0001-hint-bit-caching.md) | M0115 | Hint Bit Caching in Heap Tuple Visibility |
| [0116-0001-multi-column-ios.md](0116-0001-multi-column-ios.md) | M0116 | Multi-Column Index-Only Scan Key Decoding |
| [0116-0004-regression-check.md](0116-0004-regression-check.md) | M0116-0004 | Regression check: single-column IOS path + pgbench select-only TPS |
| [0115-0007-benchmark-gate.md](0115-0007-benchmark-gate.md) | M0115-0007 | Benchmark gate: pgbench -T 60 -c 10 -M simple -S before/after M0115 |
