# M0106-0010 step 3ba — multi-leaf btree leaf P_HIKEY uses firstright, not lastleft

## Status

Accepted (2026-05-18)

## Context

Step 3az silenced the `_bt_check_natts` assertion at
`postgres/src/backend/access/nbtree/nbtsearch.c:707` by setting
`INDEX_ALT_TID_MASK` in the P_HIKEY pivot's `t_info` and `ip_posid = nkeyatts`.
The standby boots and accepts read-only connections, but every backend
immediately FATALs:

```
FATAL:  XX000: cache lookup failed for attribute 2 of relation 3593
LOCATION:  get_attoptions, lsyscache.c:1074
```

The error is raised from
`RelationGetIndexAttOptions → get_attoptions(3593, 2)`
(`postgres/src/backend/utils/cache/relcache.c:6008`) while the standby's
relcache is building `pg_shseclabel_object_index` (OID 3593, declared
`DECLARE_UNIQUE_INDEX_PKEY ... btree(objoid, classoid, provider text_ops)`).

Direct on-disk dumps of the standby's `base/{1,5}/2659` and `base/{1,5}/1249`
files (md5-identical to a freshly-`initdb`'d goopg datadir, so WAL replay
isn't disturbing them) showed:

- `pg_attribute_relid_attnum_index` (OID 2659) splits across two leaves:
  leaf 1 holds 406 sorted data tuples ending at `(3593, 1)` then
  `(3593, 2)`, leaf 2 starts at `(3593, 3)` then continues to the
  `pg_subscription` entries.
- Leaf 1's P_HIKEY pivot has key `(3593, 2)` — a verbatim copy (modulo
  the pivot encoding bits) of leaf 1's **last** data tuple.
- The heap tuple `(0, 60)` of `pg_attribute` decodes correctly to
  `attrelid=3593, attnum=2`, so the underlying row exists.

So the index has the right entry; the syscache scan just isn't finding it.

### Root cause — wrong pivot key

`pgBuildBtreeBulkLoad` (`internal/initdb/btree_index_bootstrap.go`)
chose the P_HIKEY by copying the **last** data tuple of the current leaf:

```go
if !isRightmost {
    highKey = pgBuildBtreeLeafHighKey(group[len(group)-1], nkeyatts)
}
```

PG's `_bt_truncate` (`postgres/src/backend/access/nbtree/nbtutils.c:3776`)
instead uses **firstright** — the first data tuple of the *next* leaf —
and truncates suffix attributes only when the two consecutive tuples tie
on a key prefix.

For a forward-direction `_bt_compare` of search key `(3593, 2)` against a
pivot keyed `(3593, 2)`, PG goes through nbtsearch.c:806–831:

```c
heapTid = BTreeTupleGetHeapTID(itup);
if (key->scantid == NULL)
{
    if (!key->backward && key->keysz == ntupatts && heapTid == NULL &&
        key->heapkeyspace)
        return 1;          /* scan key STRICTLY > pivot */
    return 0;
}
```

`BTreeTupleGetHeapTID(itup)` returns NULL for our pivot because we
explicitly clear `BT_PIVOT_HEAP_TID_ATTR` in `ip_posid` (no tiebreaker
attribute appended at the end of the tuple — the simplest correct pivot
form per step 3az's design note).

All four conditions hold (`keysz=2 == ntupatts=2`, `heapTid==NULL`,
`!backward`, `heapkeyspace`), so `_bt_compare` returns `1`. `_bt_moveright`
(nbtsearch.c:311):

```c
if (P_IGNORE(opaque) || _bt_compare(rel, key, page, P_HIKEY) >= cmpval)
{
    /* step right one page */
    buf = _bt_relandgetbuf(rel, buf, opaque->btpo_next, access);
    continue;
}
```

steps **right** to leaf 2. Leaf 2's smallest key is `(3593, 3) > (3593, 2)`,
the binary search finds no match, the syscache returns NULL, and
`get_attoptions` FATALs. The relation 3593 + attribute 2 in the error
message is just the first key whose binary-search landing spot happens to
be on the leaf boundary — any other "last data tuple of a non-rightmost
leaf" key would manifest the same way (it's just that nothing was
exercising those code paths yet).

`(3593, 1)` lookup works because it lands at slot N-1, not slot N — the
binary search descends to slot N-1 directly without `_bt_compare`-ing
against the HIKEY.

Step 3az's INDEX_ALT_TID_MASK + nkeyatts encoding remains correct (it
silenced the `_bt_check_natts` assert), it just paired with the wrong
*source tuple* for the pivot key.

## Decision

Switch the P_HIKEY source from `group[len(group)-1]` (lastleft) to
`leafGroups[li+1][0]` (firstright) in `pgBuildBtreeBulkLoad`, matching
PG's `_bt_truncate`. The pivot-encoding helper
(`pgBuildBtreeLeafHighKey`) is unchanged.

```go
if !isRightmost {
    highKey = pgBuildBtreeLeafHighKey(leafGroups[li+1][0], nkeyatts)
}
```

With HIKEY = firstright = `(3593, 3)`, the scan key `(3593, 2)`
compares strictly less (col 2: `2 < 3`), `_bt_compare` returns -1,
`_bt_moveright` stays on leaf 1, and `_bt_binsrch` finds the matching
slot.

### Why this is safe for all currently bulk-loaded indexes

Every nailed index that goes through `pgBuildBtreeBulkLoad` is **unique**
— `pg_attribute_relid_attnum_index`, the `pg_class_oid_index` family,
`pg_proc_proname_args_nsp_index`, etc. — so consecutive tuples have
distinct keys. PG's `_bt_keep_natts` therefore returns `nkeyatts` (no
heap-TID tiebreaker needed), so `_bt_truncate` produces a pivot identical
to firstright (with `nkeyatts` set in `ip_posid`). No suffix truncation,
no tiebreaker append.

A future non-unique bulk-loaded index would need
`BT_PIVOT_HEAP_TID_ATTR` and an appended `ItemPointerData` carrying
lastleft's MaxHeapTID — out of scope here.

### Why the single-leaf-root fast path is untouched

`pgBuildBtreeLeafRootPage` (≤406 input tuples) has no high key at all
(it's both leftmost and rightmost), so the firstright/lastleft distinction
doesn't arise. The fast-path callers
(`bootstrapPgOpclassOidIndex`, `bootstrapPgClassOidIndex`,
`bootstrapPgIndexIndexrelidIndex`) remain byte-identical.

## Regression pins

`internal/initdb/btree_index_bootstrap_test.go`:

- `TestPgBuildBtreeBulkLoadTwoLeafLayoutMatchesPG18` (updated) — the
  P_HIKEY source-tuple comparison now reads
  `tuples[maxTuplesPerNonRightmostLeaf]` (firstright = leaf 2's first
  tuple) instead of `tuples[maxTuplesPerNonRightmostLeaf-1]` (lastleft =
  leaf 1's last tuple). Comment now explains the
  forward-`_bt_compare`-steps-right failure mode that motivated the
  switch.
- `TestPgBuildBtreeLeafHighKeyMatchesPGPivotEncoding` (unchanged) — still
  pins the helper's byte transform (INDEX_ALT_TID_MASK, ip_posid =
  nkeyatts, key payload preserved verbatim). The helper is agnostic about
  what tuple is fed in.
- `TestPgBuildBtreeBulkLoadSingleLeafByteIdenticalToLegacy` (unchanged)
  — confirms ≤407-tuple callers still produce byte-identical output.

## Verification

`go build ./...` PASS.

`go test -count=1 -run 'TestPgBuildBtree|TestBootstrapPgAttribute|TestMakeBtreeRootPage' ./internal/initdb/`
PASS.

`go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/`
PASS.

`go test -count=1 ./internal/initdb/` — only `TestSynchronousCommitFlushesByDefault`
fails (pre-existing baseline failure carried through steps 3a*–3az,
tracked as M0106-0012); every other test, including the updated
`TestPgBuildBtreeBulkLoadTwoLeafLayoutMatchesPG18`, passes.

`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`:

- Before: every PG backend FATAL'd at
  `cache lookup failed for attribute 2 of relation 3593`
  (`get_attoptions, lsyscache.c:1074`).
- After: cache lookup succeeds; standby backends advance into normal
  catalog opening and surface the NEXT blocker —
  `FATAL: could not open relation with OID 2328`
  (= `pg_db_role_setting`, accessed during `process_settings`). Out of
  scope for step 3ba; step 3bb territory.

## Implications for future steps

- The `_bt_compare` behavior here (`heapTid == NULL` → scan key treated
  as "greater") is the standard PG pivot semantics, not a goopg-specific
  quirk. Future btree-encoding work (e.g. seeding a non-unique index, or
  introducing INCLUDE columns) must respect it.
- When the first non-unique bulk-loaded index lands, the simplest
  defensive choice is to always emit a heap-TID tiebreaker on the leaf
  high key (BT_PIVOT_HEAP_TID_ATTR set, `ItemPointerData` appended
  containing lastleft's MaxHeapTID). That keeps the search direction
  unambiguous even when consecutive tuples tie on every key.
- A symmetric concern exists for *internal* downlinks: PG also calls
  `_bt_compare` against internal-page pivot tuples. `pgBuildBtreeInternalDownlink`
  already uses `leafGroups[li][0]` (= firstright at the parent level)
  for the downlink key, so it was correct before this fix.
