# M0106-0010 Step 3l — Populate `pg_opclass_oid_index` with btree index tuples

**Status:** accepted (2026-05-17)
**Milestone:** [M0106 — PG Relcache Init File Compatibility](../milestones/0106-pg-relcache-init-file-compat.md)
**Predecessors:** Steps 3a–3k of M0106-0010 (heap-row + metapage bootstrap).

## Problem

After Step 3k landed PG-conformant btree metapages, vanilla PG's standby
boot advances past the `"is not a btree"` FATAL but next FATALs with:

```
FATAL: could not find tuple for opclass 1986
```

from `postgres/src/backend/utils/cache/relcache.c:1766
LookupOpclassInfo()`. Step 3b seeded the 12-row `pg_opclass` heap
(`base/{1,5}/2616`) including opclass `1986 = name_ops`, but the matching
nailed index `pg_opclass_oid_index` (OID **2687**) was still an empty
btree (Step 3k metapage with `btm_root = P_NONE`). PG's `LookupOpclassInfo`
issues `SearchSysCache1(CLAOID, 1986)`, which walks 2687; the empty btree
returns zero rows; the syscache lookup returns NULL; relcache.c FATALs
because the relcache init file's `indclass` vector for every nailed index
references `name_ops`.

## Scope (this step)

Only `pg_opclass_oid_index` (OID 2687) is populated this step. The other
22 nailed indexes remain Step-3k empty btrees; subsequent E2E reruns will
surface the next concrete `LookupOpclassInfo` / `_bt_search` blocker and
we'll populate the next index in the same shape. Doing all 23 in one
shot would couple unrelated row-encoding work (variable-width keys,
multi-column keys, name/cstring opclass keys, `int2vector`/`oidvector`
keys) onto one Ralph loop; deferring to "as-needed" surfaces real
upstream requirements one at a time and keeps each step bounded.

## Approach

Three new helpers in `internal/initdb/btree_index_bootstrap.go`:

1. `pgBuildIndexTupleOidKey(heapBlk, heapOff, oid) []byte` — encodes a
   16-byte IndexTuple matching PG's `index_form_tuple` output for a
   single-column oid-keyed index with no nulls:
   - 4-byte LE block id + 2-byte LE item offset (`ItemPointerData`)
   - 2-byte LE `t_info` storing size 16 (`INDEX_SIZE_MASK & 16`)
   - 4-byte LE oid key
   - 4-byte MAXALIGN zero pad
   `size = MAXALIGN(sizeof(IndexTupleData) + sizeof(Oid)) = MAXALIGN(8+4) = 16`.

2. `pgBuildBtreeLeafRootPage(sortedTuples [][]byte) ([]byte, error)` —
   assembles an 8192-byte btree leaf-root page with:
   - PageHeader at byte 0 (initialised via `storage.InitPage`)
   - 4-byte ItemId line pointers starting at byte 24, in caller-provided
     sorted order (PG's `_bt_binsrch` requires monotonic key ordering)
   - IndexTuples placed at the upper end of the page, growing backward
   - `BTPageOpaqueData` at bytes [8176..8192) with
     `btpo_flags = BTP_LEAF | BTP_ROOT` and all other fields zero
     (leaf-root has no high key).

3. `pgBuildBtreeMetapageWithRoot(rootBlk, level) []byte` — variant of
   the Step-3k `makeBtreeRootPage` that takes a non-empty root pointer.
   Encodes `btm_root = btm_fastroot = rootBlk` and
   `btm_level = btm_fastlevel = level`. `makeBtreeRootPage` stays
   unchanged so the other 22 nailed indexes keep their empty metapage.

`bootstrapPgOpclassOidIndex(dataDir)` ties them together:

- Iterates `pgOpclassInitialEntries()` to compute (oid, tid) pairs.
  The heap-row tid is `(block=0, offset=i+1)` because Step 3b's
  `writeMultiPageHeapRows` packs all 12 opclass rows onto block 0 in
  insertion order (each row is ~120 bytes vs the 8160-byte payload area).
- Sorts the pairs by oid (B-tree key order).
- Builds 12 index tuples and one leaf-root page from them.
- Builds the metapage with `btm_root = 1`.
- Writes the concatenated `[meta | leaf]` 2-block file to
  `base/{1,5}/2687` and `global/2687` — matching the three-location
  pattern Step 3k uses for the empty metapage placeholder.

Wired into `Init` immediately after `bootstrapPgIndexTuples` and before
`bootstrapCLog`, so the file lands after the heap is written and before
any caller could observe the placeholder.

## Why this is safe

- **No effect on goopg's own catalogs.** `pg_opclass_oid_index` is
  read by vanilla PG during standby boot only. goopg's own server uses
  its in-memory catalog at `internal/catalog/`; the on-disk pg_opclass
  btree is dead weight in normal goopg operation. Existing
  `./internal/server/`, `./internal/executor/`, `./internal/catalog/`,
  `./internal/storage/`, `./internal/mvcc/` test suites stay green.
- **Empty btrees for other indexes preserved.** Only OID 2687 is
  rewritten; the 22 other nailed indexes keep their Step-3k empty
  metapage placeholder.
- **Byte-exact PG layout.** Helpers are pinned by table-driven tests
  against PG18 reference offsets (`postgres/src/include/access/itup.h`,
  `postgres/src/include/access/nbtree.h`,
  `postgres/src/backend/access/common/indextuple.c`).

## Regression pins

In `internal/initdb/btree_index_bootstrap_test.go`:

| test | guards |
|---|---|
| `TestPgBuildIndexTupleOidKeyLayoutMatchesPG18` | tuple is 16 bytes; `ip_blkid`/`ip_posid`/`t_info`/`oid` at correct LE offsets; pad is zero |
| `TestPgBuildBtreeLeafRootPagePageHeader` | special at `BlockSize-16`; lower past line pointers; upper above tuples; `btpo_flags = BTP_LEAF|BTP_ROOT`; level/prev/next zero |
| `TestPgBuildBtreeMetapageWithRootEncodesRootPointer` | `btm_magic`, `btm_version`, `btm_root`, `btm_fastroot`, `btm_level`, `btm_fastlevel`, `btpo_flags = BTP_META` |
| `TestBootstrapPgOpclassOidIndexWritesPopulatedBtree` | file size = 2 blocks at all three on-disk locations; metapage points to block 1; leaf has exactly `len(pgOpclassInitialEntries())` items; OIDs read back in ascending order |

## Verification

```
go test -count=1 -run 'TestPgBuildIndexTupleOidKey|TestPgBuildBtreeLeafRoot|TestPgBuildBtreeMetapage|TestBootstrapPgOpclassOidIndex' ./internal/initdb/    # PASS
go test -count=1 ./internal/initdb/                                                  # 14 pre-existing failures unchanged (stash-baseline diff)
go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/    # PASS
```

## Open follow-ups

- The next standby-boot FATAL is likely `LookupOpclassInfo` hitting
  another opclass via `pg_amop_opr_fam_index` (2654) or
  `pg_amproc_fam_proc_index` (2655). Those indexes have multi-column
  keys (`(amopopr,amoppurpose,amopfamily)` and
  `(amprocfamily,amproclefttype,amprocrighttype,amprocnum)` respectively)
  with mixed oid + char + int2 columns — they need parallel builders
  (`pgBuildIndexTupleMultiKey` or per-shape helpers).
- `pg_class_oid_index` (2662), `pg_attribute_relid_attnum_index` (2659),
  `pg_proc_oid_index` (2690), `pg_index_indexrelid_index` (2679) — all
  load-bearing for nailed-relation lookup; some can reuse the
  oid-key helper, some need composites.
- Per-loop pattern: rerun `TestE2E_FailoverGoopgToPG/async`
  (`GOOPG_RUN_BLOCKED_M0102_E2E=1`), capture the next FATAL, populate
  the corresponding index, repeat.
- Once enough nailed indexes are populated to clear standby boot,
  consolidate the per-index `bootstrap*Index` functions into a
  table-driven driver keyed on `pgIndexInitialEntries`.
