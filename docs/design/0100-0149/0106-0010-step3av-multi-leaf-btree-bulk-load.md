# M0106-0010 step 3av — Multi-Leaf Btree Bulk-Load Builder

## Status

LANDED 2026-05-18. Implementation in
`internal/initdb/btree_index_bootstrap.go`; test pins in
`internal/initdb/btree_index_bootstrap_test.go`.

Step 3au surfaced the blocker (`btree leaf overflow inserting tuple 407`
when pg_extension's 8 nailed-rel attrs push
`bootstrapPgAttributeRelidAttnumIndex` past one leaf). This step lands
the refactor scoped in `0106-0010-step3au-multi-leaf-btree-prereq.md`
so the pg_extension seed can ship in a subsequent loop without touching
the btree builder again.

## Summary

`pgBuildBtreeBulkLoad(sortedTuples [][]byte, nkeyatts uint16) ([]byte, error)`
returns a complete on-disk btree file (metapage + leaves + root) ready
to be written verbatim under `base/{1,5}/<oid>` and `global/<oid>`.

Behaviour depends on input size:

- **≤ 407 fixed-size tuples** (the single-leaf fast path): emits the
  byte-identical sequence
  `pgBuildBtreeMetapageWithRoot(1, 0) || pgBuildBtreeLeafRootPage(tuples)`.
  Pinned by
  `TestPgBuildBtreeBulkLoadSingleLeafByteIdenticalToLegacy` across four
  representative sizes (0, 1, 12 = pg_opclass cardinality, 407 = full
  single-leaf cap). This is what protects every existing caller of the
  legacy pair from a regression when a new caller migrates.

- **> 407 fixed-size tuples** (the bulk-load slow path): emits
  metapage at block 0, N leaves at blocks 1..N with `BTP_LEAF` +
  sibling links (`btpo_prev`/`btpo_next`) + `P_HIKEY` at slot 1 on
  every non-rightmost leaf, root at block N+1 with `BTP_ROOT` +
  `btpo_level=1` + N downlinks. Pinned by
  `TestPgBuildBtreeBulkLoadTwoLeafLayoutMatchesPG18` for a 500-tuple
  input.

Authoritative PG18 sources are cited inline in the function doc
comment and the design doc text:
- `postgres/src/backend/access/nbtree/nbtsort.c` — `_bt_buildadd`,
  `_bt_uppershutdown`, `_bt_slideleft`, minus-infinity lowkey at
  lines 1001–1008.
- `postgres/src/include/access/nbtree.h` — `BTPageOpaqueData`,
  `BTP_LEAF`, `BTP_ROOT`, `INDEX_ALT_TID_MASK = 0x2000`,
  `BT_OFFSET_MASK = 0x0FFF`, `P_HIKEY = 1`,
  `BTreeTupleSetDownLink`, `BTreeTupleSetNAtts`.

## Wire-format details

### Constants (new)

```go
indexAltTIDMask uint16 = 0x2000  // nbtree.h:460 INDEX_ALT_TID_MASK
btOffsetMask    uint16 = 0x0FFF  // nbtree.h:463 BT_OFFSET_MASK
pNone           uint32 = 0xFFFFFFFF
fixedIndexTupleSize          = 16  // size of every IndexTuple this file emits
leafPayloadBytes             = BlockSize − SizeOfPageHeaderData − sizeOfBTPageOpaque  // 8152
maxTuplesPerSingleLeafRoot   = leafPayloadBytes / (16 + 4)        // 407
maxTuplesPerNonRightmostLeaf = (leafPayloadBytes − 20) / 20       // 406
```

### Leaf packing

`pgBuildBtreeLeafPage(tuples, highKey, prev, next)`:

- `highKey != nil` (non-rightmost): writes `highKey` into slot 1
  (P_HIKEY) first, then data tuples into slots 2..N+1.
- `highKey == nil` (rightmost): writes data tuples into slots 1..N
  (mirrors `_bt_slideleft` in nbtsort.c — the P_HIKEY slot allocated
  by `_bt_blnewpage` is removed once the page is confirmed rightmost).

Opaque area at end of page: `btpo_prev`, `btpo_next`, `btpo_level=0`,
`btpo_flags=BTP_LEAF` (deliberately **not** `BTP_LEAF | BTP_ROOT`;
multi-leaf trees have a separate root page).

### Root packing

`pgBuildBtreeInternalRootPage(downlinks)` — same line-pointer / tuple
mechanics as the leaf builder, but no P_HIKEY (root is rightmost on
its level, slid left). Opaque area:
`btpo_prev=btpo_next=P_NONE`, `btpo_level=1`, `btpo_flags=BTP_ROOT`.

### Downlink encoding

`pgBuildBtreeMinusInfinityDownlink(childBlock)` emits the 8-byte
zero-attribute pivot per `nbtsort.c:1001-1008`:

- `ip_blkid` = `childBlock` (struct-order `bi_hi`/`bi_lo`, NOT a single
  LE uint32 — same trap closed for heap TIDs in Step 3s).
- `ip_posid` = 0 (zero key attributes; `BTreeTupleGetNAtts` returns 0).
- `t_info` = `sizeof(IndexTupleData) | INDEX_ALT_TID_MASK`.

`pgBuildBtreeInternalDownlink(dataTuple, childBlock, nkeyatts)` copies
the child page's first data tuple verbatim, then overwrites:

- `ip_blkid` = `childBlock` (struct-order halves).
- `ip_posid` = `nkeyatts & BT_OFFSET_MASK`.
- `t_info` = existing size bits | `INDEX_ALT_TID_MASK`.

`nkeyatts = 2` for the oid_int2 composite (`attrelid oid, attnum int2`),
which is the only multi-leaf caller landed in this step. Pure-oid
callers (single-leaf cap, fast path) do not look at `nkeyatts` because
the legacy two-byte sequence is emitted verbatim.

## Caller migration

Only `bootstrapPgAttributeRelidAttnumIndex` migrates in this step. The
other three legacy callers
(`bootstrapPgOpclassOidIndex`, `bootstrapPgClassOidIndex`,
`bootstrapPgIndexIndexrelidIndex`) keep the
`pgBuildBtreeLeafRootPage` + `pgBuildBtreeMetapageWithRoot` pair
because:

- Their cardinality is well below 407 (12 pg_opclass entries; nailed
  rels for pg_class; ~40 pg_index entries).
- The fast-path output of `pgBuildBtreeBulkLoad` is byte-identical to
  their current output (pinned), so migration is risk-free but adds no
  value; we leave them alone for minimum-blast-radius.

If any of those callers later approaches the 407 cap, the migration is
a one-line swap. The byte-equivalence pin
(`TestPgBuildBtreeBulkLoadSingleLeafByteIdenticalToLegacy`) makes that
future swap safe by construction.

## Forward chain

With the multi-leaf builder in place, the pg_extension seed (Step 3aw —
the original intent of Step 3au, renumbered) is a pure catalog-seed
change with no btree work:

- `pgExtensionAttrs()` returning the 8-column PG18 schema verbatim from
  `pg_extension.h:29-45`.
- `nailedLocalRels` entry
  `{3079, "pg_extension", 83, 'r', 8, false, pgExtensionAttrs()}`.
- 3079 added to the `bootstrapMappedLocalCatalogHeaps` OID list +
  `localRelMap`.
- Companion indexes 3080 (`pg_extension_oid_index`, UNIQUE PRIMARY) and
  3081 (`pg_extension_name_index`, UNIQUE on `extname name_ops`) follow
  the established single-OID rhythm in subsequent steps.

## Test gates executed

- `go build ./internal/initdb/` — clean.
- `go test -count=1 -run TestPgBuildBtreeBulkLoad ./internal/initdb/` —
  both new tests PASS.
- `go test -count=1 -run "TestPgBuildIndexTupleOidKey|TestPgBuildBtreeLeafRootPage|TestPgBuildBtreeMetapageWithRoot|TestBootstrapPgOpclassOidIndex|TestBootstrapPgClassOidIndex|TestPgBuildIndexTupleOidInt2Key|TestBootstrapPgAttributeRelidAttnumIndex|TestBootstrapPgIndexIndexrelidIndex" ./internal/initdb/`
  — all legacy btree tests PASS (confirms byte-equivalence of the fast
  path under all four pre-existing callers).
- `go test -count=1 ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
- Wider initdb package has pre-existing failures
  (`TestSynchronousCommitFlushesByDefault`,
  `TestMigrationFromLegacyJSONCluster`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestBootstrappedPGClassRowsReadable`, …) that are present on
  origin/HEAD before this change — confirmed via `git stash` baseline
  run. Not caused by Step 3av.
