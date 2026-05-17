# M0106-0010 step 3az — multi-leaf btree leaf P_HIKEY pivot encoding + P_NONE sentinel fix

## Status

Accepted (2026-05-18)

## Context

Step 3aw (`pg_extension` nailed rel) pushed `pg_attribute_relid_attnum_index`
(OID 2659) past Step 3av's 407-tuple single-leaf-root cap, so the multi-leaf
slow path in `pgBuildBtreeBulkLoad` activated for the first time on a real
nailed catalog index. Every PG standby backend then aborted at
`postgres/src/backend/access/nbtree/nbtsearch.c:707` —

```
TRAP: failed Assert("_bt_check_natts(rel, key->heapkeyspace, page, offnum)"),
File: "nbtsearch.c", Line: 707
```

— during `RelationCacheInitializePhase3 → systable_getnext` against the
multi-leaf 2659 file. The same blocker affected both `base/{1,5}/2659` and
`global/2659`, and was reproducible end-to-end via
`pg_basebackup` + a vanilla PG standby (no goopg-side runtime needed).

The assertion has two independent root causes — fixing only one keeps the
abort firing.

### Bug 1 — leaf high-key was a verbatim data tuple, not a pivot

`pgBuildBtreeBulkLoad` set the P_HIKEY of every non-rightmost leaf to a
verbatim copy of the leaf's last data tuple:

```go
var highKey []byte
if !isRightmost {
    highKey = group[len(group)-1]
}
```

PG18 V4 heapkeyspace btrees require leaf high keys to satisfy
`BTreeTupleIsPivot()` — INDEX_ALT_TID_MASK (0x2000) set in `t_info`,
BT_IS_POSTING (0x2000 in `ip_posid`) clear. Verbatim data tuples have neither
flag, so `_bt_check_natts`'s heapkeyspace pivot branch
(`postgres/src/backend/access/nbtree/nbtutils.c:4163`) returned false:

```c
Assert(heapkeyspace);
if (!BTreeTupleIsPivot(itup))
    return false;
```

### Bug 2 — `pNone` constant was `0xFFFFFFFF`, not `0`

The `pNone` package constant in
`internal/initdb/btree_index_bootstrap.go` was declared as
`0xFFFFFFFF` with a comment claiming "P_NONE = InvalidBlockNumber = 0xFFFFFFFF
in PG". That comment conflated two distinct PG sentinels:

```c
/* postgres/src/include/access/nbtree.h */
#define P_NONE			0
#define P_LEFTMOST(opaque)  ((opaque)->btpo_prev == P_NONE)
#define P_RIGHTMOST(opaque) ((opaque)->btpo_next == P_NONE)

/* postgres/src/include/storage/block.h */
#define InvalidBlockNumber  ((BlockNumber) 0xFFFFFFFF)
```

`P_NONE` is the sentinel for btree-page opaque sibling pointers
(`btpo_prev`/`btpo_next`) and metapage `btm_root`/`btm_fastroot`.
`InvalidBlockNumber` is a different sentinel used by the buffer manager and
relation TIDs. They share neither value nor purpose.

Writing `pNone=0xFFFFFFFF` into `btpo_next` of the rightmost leaf
(`pgBuildBtreeLeafPage`) made `P_RIGHTMOST(opaque)` return false on the page
that goopg intended as rightmost. PG's `P_FIRSTDATAKEY` macro

```c
#define P_FIRSTDATAKEY(opaque) (P_RIGHTMOST(opaque) ? P_HIKEY : P_FIRSTKEY)
```

then treated slot 1 of the "rightmost" leaf as a high-key pivot. The actual
data tuple at that slot — without INDEX_ALT_TID_MASK — failed
`BTreeTupleIsPivot()` and the same `_bt_check_natts` branch returned false.

The fast path (`pgBuildBtreeLeafRootPage`) was unaffected because it never
explicitly wrote `btpo_prev`/`btpo_next` — they stayed at the
`make([]byte, BlockSize)` zero-fill, which is exactly the correct `P_NONE`
value. The bug only surfaced once `pgBuildBtreeBulkLoad`'s slow path emitted
real sibling links.

## Decision

Both bugs are surgical encoding fixes; no flow-level rework.

### Fix 1 — `pgBuildBtreeLeafHighKey` helper

New helper in `internal/initdb/btree_index_bootstrap.go`:

```go
func pgBuildBtreeLeafHighKey(dataTuple []byte, nkeyatts uint16) []byte {
    out := make([]byte, len(dataTuple))
    copy(out, dataTuple)
    le := binary.LittleEndian
    le.PutUint16(out[4:6], nkeyatts&btOffsetMask)      // ip_posid = nkeyatts
    tinfo := le.Uint16(out[6:8])
    le.PutUint16(out[6:8], tinfo|indexAltTIDMask)      // t_info |= 0x2000
    return out
}
```

Allocates a fresh buffer (the source tuple is also written verbatim as a
data tuple on the same leaf — mutating in place would corrupt that copy).
Sets INDEX_ALT_TID_MASK in `t_info`, encodes nkeyatts in `ip_posid` with no
status bits (no BT_IS_POSTING, no BT_PIVOT_HEAP_TID_ATTR — the high key carries
the key payload only, with no heap-TID tiebreaker). The key data at
bytes `[8..]` passes through unchanged.

Call-site update in `pgBuildBtreeBulkLoad`:

```go
if !isRightmost {
    highKey = pgBuildBtreeLeafHighKey(group[len(group)-1], nkeyatts)
}
```

### Fix 2 — `pNone` constant value

```go
// pNone is PG's "no sibling / no root / no parent" block-number sentinel ...
//   #define P_NONE     0
//   #define P_LEFTMOST(opaque)  ((opaque)->btpo_prev == P_NONE)
//   #define P_RIGHTMOST(opaque) ((opaque)->btpo_next == P_NONE)
pNone uint32 = 0
```

Every existing call site that wrote `uint32(pNone)` into a btree-page opaque
field now writes the correct value with no other code change. The single-leaf
fast path stays byte-identical because it never explicitly wrote
`btpo_prev`/`btpo_next`.

## Regression pins

`internal/initdb/btree_index_bootstrap_test.go`:

- `TestPgBuildBtreeLeafHighKeyMatchesPGPivotEncoding` (new) — pins the
  helper's byte layout: INDEX_ALT_TID_MASK set in `t_info`, `ip_posid` ==
  nkeyatts with zero status bits, key payload preserved verbatim, source
  buffer not mutated.
- `TestPgBuildBtreeBulkLoadTwoLeafLayoutMatchesPG18` (updated) — the
  P_HIKEY assertion now demands the pivot encoding (INDEX_ALT_TID_MASK +
  ip_posid==2 + size bits preserved) instead of verbatim equality with the
  source data tuple.
- `TestPgBuildBtreeBulkLoadSingleLeafByteIdenticalToLegacy` (unchanged) —
  still asserts byte-identical output for ≤407 inputs, confirming the
  single-leaf-root callers (`bootstrapPgOpclassOidIndex`,
  `bootstrapPgClassOidIndex`, `bootstrapPgIndexIndexrelidIndex`) remain
  untouched.

## Verification

`go build ./...` PASS.

`go test -count=1 -run 'TestPgBuildBtree|TestBootstrapPgAttribute|TestMakeBtreeRootPage' ./internal/initdb/`
PASS.

`go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/`
PASS.

`go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline failures
as Step 3ay (`TestMigration*`, `TestCreate*`, `TestBootstrappedPG*`,
`TestSynchronousCommitFlushesByDefault`, `TestOpenOldClusterWithoutM0030*`,
`TestSystemCatalogRelfilesAreValidHeapPages`,
`TestCommittedTableSurvivesCrashRestart`,
`TestRuntimeCloseTriggersFinalCheckpoint`, `TestMultipleTablesLoadFromHeap`),
confirmed unchanged via `git stash` baseline diff.

`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`:

- Before fix: every PG standby backend aborted at `nbtsearch.c:707` during
  `RelationCacheInitializePhase3 → systable_getnext`. The postmaster
  reinitialised every ~1s as backends SIGABRT'd. 0 successful queries.
- After fix: no `TRAP` events; standby reaches `database system is ready to
  accept read-only connections`; first backend query advances to a NEW
  blocker — `cache lookup failed for attribute 2 of relation 3593`
  (`pg_shseclabel_object_index`). Out of scope for Step 3az; Step 3ba
  territory.

## Implications for future steps

- The `pNone` value change touches every caller that ever passed it into a
  btree opaque or metapage field. `pgBuildBtreeBulkLoad`'s
  `pgBuildBtreeMinusInfinityDownlink` does NOT use `pNone` — it writes the
  actual child block number via `BlockIdSet` semantics. `makeBtreeRootPage`
  and `pgBuildBtreeMetapageWithRoot` rely on the zero-fill of
  `make([]byte, BlockSize)` for empty fields, not on the `pNone` constant.
- `InvalidBlockNumber` (`0xFFFFFFFF`) is the correct sentinel for heap TIDs
  in `BlockIdData` payloads (e.g. when emitting "invalid" TIDs in
  posting-list metadata). No existing code path uses such payloads — every
  populated index tuple references a real heap row — so this distinction
  needs no separate constant yet. If a future step seeds dead/posting
  tuples, introduce a separate `invalidBlockNumber uint32 = 0xFFFFFFFF`
  rather than reusing `pNone`.
- The leaf high-key encoding adopted here is the simplest correct form: no
  truncation, no heap-TID tiebreaker (BT_PIVOT_HEAP_TID_ATTR clear). If a
  future step needs the tiebreaker (to disambiguate equal keys across a
  split that places duplicates on different leaves), set the bit AND append
  an `ItemPointerData` at the end of the tuple, preserving `tupnatts ==
  nkeyatts`. See `_bt_truncate` in `postgres/src/backend/access/nbtree/nbtutils.c`.
