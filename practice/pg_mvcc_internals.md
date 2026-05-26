# PostgreSQL MVCC Internals and Visibility Optimization

## Overview

PostgreSQL uses a Multi-Version Concurrency Control (MVCC) architecture together with Snapshot Isolation.  
Rather than updating rows in place, PostgreSQL creates new tuple versions and determines visibility based on transaction snapshots.

Internally, PostgreSQL is highly storage-oriented and optimized around reducing the cost of visibility checks.

---

# Storage Structure

A PostgreSQL heap table is structured as:

```text
Relation (table)
  └─ File
      └─ Block/Page (typically 8KB)
           └─ Line Pointer Array
                └─ Tuple (row version)
```

A page roughly looks like:

```text
+----------------------+
| PageHeaderData       |
+----------------------+
| LinePointer[0]       |
| LinePointer[1]       |
| ...                  |
+----------------------+
| free space           |
+----------------------+
| Tuple N              |
| Tuple N-1            |
| ...                  |
+----------------------+
```

Important characteristics:

- Tuples are allocated from the end of the page.
- Access goes through line pointers rather than directly to tuple bodies.
- Structures are disk-persistent and WAL-safe.

---

# CTID and Physical Addressing

Each tuple has a `ctid`:

```text
(block_number, line_pointer_index)
```

Example:

```text
(12345, 7)
```

This means:

- Page/block 12345
- Line pointer 7

This is not a CPU memory pointer.

Instead, it is:

- Disk-persistent
- WAL-replayable
- Replication-safe
- Serializable to storage

PostgreSQL avoids embedding raw memory pointers into on-disk structures.

---

# MVCC Tuple Chains

When an UPDATE occurs, PostgreSQL does not overwrite the old tuple.

Instead:

1. A new tuple version is appended somewhere in the heap.
2. The old tuple links to the new tuple via `ctid`.

Example:

Old tuple:

```text
xmin=100
xmax=200
ctid=(8,3)
```

New tuple:

```text
xmin=200
xmax=0
ctid=(8,3)
```

Thus PostgreSQL forms version chains.

---

# Tuple Header

Each tuple contains MVCC metadata:

```c
typedef struct HeapTupleHeaderData
{
    TransactionId t_xmin;
    TransactionId t_xmax;

    ItemPointerData t_ctid;

    ...
}
```

Meaning:

- `xmin`
  - Transaction that created the tuple
- `xmax`
  - Transaction that deleted or updated the tuple
- `ctid`
  - Pointer to the latest tuple version

---

# Visibility Checks

A snapshot contains information such as:

```text
snapshot.xmin
snapshot.xmax
snapshot.in_progress[]
```

Tuple visibility checks involve:

- Is xmin committed?
- Is xmax committed?
- Is the transaction still running?
- Is the tuple too new for this snapshot?

Naively, this would be very expensive.

Therefore PostgreSQL includes multiple layers of optimization.

---

# Major Visibility Optimizations

## 1. Hint Bits

This is one of the most important optimizations.

Tuple headers contain cached visibility flags such as:

```c
HEAP_XMIN_COMMITTED
HEAP_XMIN_INVALID
HEAP_XMAX_COMMITTED
HEAP_XMAX_INVALID
```

Once PostgreSQL checks a transaction status in `pg_xact`, it stores the result directly inside the tuple header.

Example:

Initially:

```text
xmin=500
```

After checking commit status:

```text
xmin=500
HEAP_XMIN_COMMITTED
```

Future reads can avoid `pg_xact` lookups entirely.

Benefits:

- Fewer SLRU accesses
- Reduced locking
- Lower I/O overhead
- Faster visibility checks

---

## 2. Visibility Map

PostgreSQL maintains a bitmap indicating pages whose tuples are fully visible.

Benefits:

- VACUUM can skip pages
- Enables Index-Only Scans

Without it:

```text
Index lookup
  -> Heap fetch
     -> Visibility check
```

With visibility map:

```text
Index lookup only
```

Heap access becomes unnecessary.

---

## 3. HOT (Heap-Only Tuple) Updates

If indexed columns are unchanged during UPDATE:

- PostgreSQL avoids updating indexes
- New tuple versions stay on the same page

Structure:

```text
index
  ↓
tuple1 → tuple2 → tuple3
```

Benefits:

- Reduced index bloat
- Less WAL generation
- Better cache locality

---

## 4. Snapshot Optimization

Snapshots maintain information about running transactions.

PostgreSQL optimizes this using:

- ProcArray optimization
- Transaction horizon tracking
- Fast-path visibility rules

Important concepts:

```text
RecentGlobalXmin
OldestXmin
```

These help quickly determine whether tuples are obviously visible or dead.

---

## 5. Reducing pg_xact Accesses

Transaction commit states live in `pg_xact`.

Accessing it frequently is expensive.

PostgreSQL reduces lookups using:

- Hint bits
- Shared caches
- Grouped transaction lookups

---

## 6. Page Pruning

PostgreSQL can compact dead tuple chains before VACUUM runs.

Example:

```text
A → B → C
```

If A and B are obsolete:

```text
C
```

can become the remaining visible tuple.

This reduces traversal overhead.

---

## 7. Freeze

Transaction IDs are 32-bit and eventually wrap around.

To avoid wraparound issues, PostgreSQL marks very old tuples as frozen.

Conceptually:

```text
FrozenXID = permanently committed
```

Benefits:

- Eliminates future visibility checks
- Prevents XID wraparound failures

---

# Line Pointers and Tuple Movement

PostgreSQL uses:

```text
CTID → line pointer → tuple body
```

instead of directly pointing to tuple storage.

This allows tuple bodies to move during page compaction while keeping CTIDs stable.

Without line pointers:

- VACUUM compaction would invalidate references

Line pointers are therefore critical to PostgreSQL's storage design.

---

# PostgreSQL vs Undo-Log Databases

PostgreSQL uses append-style tuple versioning.

By contrast, systems like InnoDB use:

```text
Latest version in-place
Old versions in undo log
```

PostgreSQL advantages:

- Readers do not block writers
- Simpler recovery logic
- Simpler WAL redo behavior

Disadvantages:

- Table bloat
- VACUUM requirements
- Lower cache density

---

# Overall Architecture

Although PostgreSQL appears to perform tuple-by-tuple visibility checks, in practice it operates as a layered visibility caching system:

```text
Visibility Map
    ↓
Hint Bits
    ↓
Snapshot Fast Path
    ↓
pg_xact lookup
```

The core MVCC model:

```text
xmin/xmax + tuple version chains
```

has remained largely stable for many years because it is considered a highly successful design.
