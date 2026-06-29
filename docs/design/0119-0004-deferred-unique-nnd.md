# 0119-0004 — Deferred UNIQUE with NULLS NOT DISTINCT (NULL-keyed rows) (M0119-0004)

Status: accepted

## Problem

[[0119-0004-deferred-unique]] landed deferred `UNIQUE`/`PRIMARY KEY` checking: a
`DEFERRABLE INITIALLY DEFERRED` constraint (or one deferred at runtime via
`SET CONSTRAINTS … DEFERRED`) queues its uniqueness re-probe to COMMIT instead of
raising at the firing INSERT/UPDATE. That work re-probes the **btree** for the
candidate key (`recheckDeferredUniqueKey` → `RangeScan(key,key)`, ≥2 live tuples
= 23505).

A `NULLS NOT DISTINCT` (PG 15+) unique index treats NULL key values as equal, so
two rows whose key columns are all NULL collide. NULL-keyed rows are **never
stored in the btree** (`encodeIndexKeyFromCols` returns a nil key), so NND
collisions are found by a heap scan instead (`checkNullsNotDistinctViaHeapScan`,
[[0119-0004]] NND-enforcement work, loops #14–#17).

These two features did **not compose**. In `checkUniqueIndexesForInsert` /
`checkUniqueIndexesForUpdate`, the `key == nil` arm (a NULL key column on an NND
index) ran the immediate heap-scan raise **unconditionally** — it never consulted
`uniqueCheckDeferred`. So a `UNIQUE NULLS NOT DISTINCT … DEFERRABLE INITIALLY
DEFERRED` constraint:

- wrongly raised 23505 **immediately** on a transient NULL duplicate that the
  transaction would have resolved before COMMIT (a false positive — breaks the
  deferred contract), and
- never queued a commit-time recheck for NULL-keyed rows.

### Oracle (PG 18.3, `./postgres/local_install`)

```sql
CREATE TABLE t1 (a int, b int, UNIQUE NULLS NOT DISTINCT (a) DEFERRABLE INITIALLY DEFERRED);
-- transient NULL dup resolved before commit → SUCCEEDS (goopg: wrongly errors at 2nd INSERT)
BEGIN; INSERT INTO t1 VALUES (NULL,1); INSERT INTO t1 VALUES (NULL,2); DELETE FROM t1 WHERE b=2; COMMIT;  -- 1 row
-- NULL dup survives to commit → ERROR at COMMIT
BEGIN; INSERT INTO t1 VALUES (NULL,1); INSERT INTO t1 VALUES (NULL,2); COMMIT;
--   ERROR: duplicate key value violates unique constraint "t1_a_key"
--   DETAIL: Key (a)=(null) already exists.
```

Multi-column NND `(a,b)`: `(1,null)` and `(1,null)` collide; `(null,1)` and
`(null,2)` do **not** (distinct NULL patterns). `SET CONSTRAINTS ALL DEFERRED`
defers an `INITIALLY IMMEDIATE` NND constraint identically. All confirmed against
local PG 18.3.

## Design

Make the `key == nil` NND arm deferral-aware, and add a heap-scan recheck at
COMMIT that counts NULL-pattern matches (≥2 = violation), exactly mirroring the
btree path's structure.

### 1. Carry the candidate NULL pattern on the deferred check

`DeferredUniqueCheck` (session.go) gains one field:

```go
type DeferredUniqueCheck struct {
    TableName  string
    IndexName  string
    Key        []byte                // btree key (non-NND, or NND with a non-NULL key); nil for NND-NULL
    NNDKeyCols []DeferredNNDKeyCol   // non-nil ⇒ NND check with ≥1 NULL key column → heap-scan recheck
    Detail     string
}
type DeferredNNDKeyCol struct {
    ColName string // index key column name
    Null    bool   // candidate value is NULL for this column
    Key     []byte // encoded btree key for the column (nil when Null)
}
```

`AddDeferredUniqueCheck`'s dedup widens from `(IndexName, Key)` to also compare
`NNDKeyCols` (via a small `sameNNDKeyCols` helper), so two distinct NULL patterns
on the same index queue separately while an identical one dedups. The candidate
key-column representation is self-contained (no live catalog/Row pointers held
across statements).

### 2. Enqueue at the NND arm

In both `checkUniqueIndexesForInsert` and `checkUniqueIndexesForUpdate`, inside
the existing `idx.NullsNotDistinct && rowHasNullKeyColumn(...)` branch, **before**
the immediate `checkNullsNotDistinctViaHeapScan` raise:

```go
if uniqueCheckDeferred(ctx, idx) {
    queueDeferredNNDUniqueCheck(ctx, tbl, idx, cols, row)   // newRow for UPDATE
    continue
}
```

`queueDeferredNNDUniqueCheck` resolves each index key column's candidate
NULL-ness / encoded value (the same resolution `checkNullsNotDistinctViaHeapScan`
does) into `[]DeferredNNDKeyCol` and queues with `Detail = nndDetail(...)`. It
falls back to no-op (leaves the immediate path to run) only on a structural
problem (expression key) — but `rowHasNullKeyColumn` already guarantees a plain
column key here.

### 3. Recheck at COMMIT

The immediate heap scanner is refactored so the deferred path reuses it. The
per-column descriptor `nndKeyCol` is lifted to package scope and the scan loop is
extracted to:

```go
// scanNNDLiveMatches seq-scans rel counting live heap tuples whose index key
// columns match keyCols' NULL pattern + non-NULL key bytes, stopping at stopAt.
func scanNNDLiveMatches(ctx *Context, tbl *catalog.Table, rel storage.RelFileNode,
    keyCols []nndKeyCol, stopAt int) (count int, first storage.ItemPointer)
```

- `checkNullsNotDistinctViaHeapScan` builds `keyCols` from the live Row+cols and
  calls `scanNNDLiveMatches(..., stopAt=1)` → returns `(first, count>=1)`
  (unchanged immediate semantics: candidate not yet inserted, any match = dup).
- `recheckDeferredNNDUniqueKey` (deferred_unique.go) rebuilds `keyCols` from the
  queued `[]DeferredNNDKeyCol` (resolving `tblOrd`/`*catalog.Column` by name
  against the live table) and calls `scanNNDLiveMatches(..., stopAt=2)` → ≥2 live
  matches is a 23505 (candidate row is itself one of them, mirroring the btree
  path's ≥2 rule).

`runAllDeferredUniqueChecks` branches per check: `c.NNDKeyCols != nil` →
`recheckDeferredNNDUniqueKey`; else the existing `recheckDeferredUniqueKey`. Both
run under the already-installed `mvcc.Manager.FreshSnapshot()` and the shared
`isLiveForUniqueCheck` visibility classifier, so own-uncommitted writes are live
and an UPDATE's stamped-dead old version is excluded — identical to the btree
path.

### 4. SET CONSTRAINTS … IMMEDIATE

`setConstraintsOp` already drains the matching subset via
`TakeDeferredUniqueChecksMatching` + `runAllDeferredUniqueChecks`; because the
recheck now branches on `NNDKeyCols`, NND-NULL checks made immediate by
`SET CONSTRAINTS … IMMEDIATE` are enforced at that statement with no extra wiring.

## Blast radius

Every new branch is gated on `idx.NullsNotDistinct && rowHasNullKeyColumn(...)`
**and** `uniqueCheckDeferred(...)` (which itself requires `idx.Deferrable` + an
explicit transaction). A non-NND index, a non-NULL key, a NOT-DEFERRABLE
constraint, and the entire pgbench/TPC-H hot path are byte-identical to before
(the immediate NND heap scan is unchanged for the non-deferred case; the scan
core was only extracted, not altered). The deferred-unique btree path is
untouched.

## Tests

- `internal/executor/nulls_not_distinct_deferred_test.go`
  (`TestPort_InitiallyDeferredNNDUniqueCommit`): transient NULL dup resolved →
  commit succeeds; surviving NULL dup → 23505 at COMMIT with
  `Key (a)=(null) already exists.`; distinct NULL patterns coexist; multi-column
  NND `(a,b)` transient dup via UPDATE; `SET CONSTRAINTS ALL DEFERRED` on an
  INITIALLY IMMEDIATE NND constraint. Golden values captured from PG 18.3.
- Regression: full executor suite + `-race` executor/mvcc + the deferred-FK /
  deferred-unique isolation group + pgbench smoke.

## Oracle citation

`postgres/src/backend/access/nbtree/nbtinsert.c` (`_bt_check_unique`,
`UNIQUE_CHECK_PARTIAL` for deferred indexes); NULLS NOT DISTINCT handling in
`index_form_tuple` / `_bt_isequal` NULL equality; deferred-trigger recheck in
`postgres/src/backend/commands/trigger.c`. Behaviour compared against
`./postgres/local_install` PG 18.3 (cases above).
