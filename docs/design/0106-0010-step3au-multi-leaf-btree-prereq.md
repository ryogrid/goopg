# M0106-0010 step 3au — pg_extension Nailed Rel Blocked by Single-Leaf Btree

## Status

INVESTIGATION (2026-05-18). Code is *not* landed; this doc captures the
finding that drove the loop and scopes the refactor that the next loop
must land before pg_extension can be seeded.

## Summary

After Step 3at the next PG-standby boot FATAL is
`could not open relation with OID 3079`. OID 3079 is `pg_extension` per
`postgres/src/include/catalog/pg_extension_d.h:23`
(`#define ExtensionRelationId 3079`). Following the steady-state pattern
of Steps 3w / 3aa / 3ag / 3ak / 3an / 3ar, the fix is to seed
`pg_extension` as a nailed local catalog rel with its 8 PG18-canonical
columns.

The fix cannot land in isolation: adding pg_extension's 8 pg_attribute
rows pushes `bootstrapPgAttributeRelidAttnumIndex`'s populated leaf-root
btree at file OID 2659 past its single-page capacity. The current
`pgBuildBtreeLeafRootPage` in `internal/initdb/btree_index_bootstrap.go`
caps at 407 tuples (8KB page − 24B header − 16B opaque ÷ 20B per
tuple+lp); we are currently at 407, and 8 more entries fail with
`btree leaf overflow inserting tuple 407`. This regression was confirmed
empirically: `git stash` of the pg_extension changes restores green
tests; reapplying triggers the overflow.

## The Refactor (scoped for Step 3av)

Goal: replace the single-leaf-root btree in
`bootstrapPgAttributeRelidAttnumIndex` with a 2-level (root + N leaves)
PG18-compatible btree.

### On-disk format (PG18 nbtsort.c bulk-load)

Authoritative sources:
- `postgres/src/backend/access/nbtree/nbtsort.c` —
  `_bt_buildadd`, `_bt_uppershutdown`, `BTreeTupleSetDownLink`
- `postgres/src/include/access/nbtree.h` —
  `BTPageOpaqueData`, `BTP_LEAF`/`BTP_ROOT`, `INDEX_ALT_TID_MASK`,
  `P_HIKEY`, `BTREE_VERSION = 4`

Page roles:
- Block 0: metapage (already implemented in `pgBuildBtreeMetapageWithRoot`).
- Block 1..N: leaf pages with `btpo_flags = BTP_LEAF` (no BTP_ROOT),
  `btpo_level = 0`, `btpo_prev` / `btpo_next` linked as siblings
  (P_NONE = 0xFFFFFFFF for the leftmost/rightmost edge).
- Block N+1 (the new root): `btpo_flags = BTP_ROOT`, `btpo_level = 1`,
  `btpo_prev = btpo_next = P_NONE`.

Leaf "high key" (item slot 1, P_HIKEY) for every non-rightmost leaf:
copy of the rightmost data tuple's key, suffix-truncated where possible.
For our fixed 16-byte oid_int2 composite-key index there is no truncation
opportunity, so we simply copy the full 16-byte tuple as the high key.

Internal-node downlink format (per `BTreeTupleSetDownLink` and
`BTreeTupleSetNAtts`, nbtsort.c:563 / nbtree.h:603):
- Same 16-byte IndexTupleData header as leaf tuples
- `t_tid.ip_blkid` = child leaf block number (encoded as bi_hi:bi_lo
  uint16 halves — same trap as Step 3s closed for heap TIDs)
- `t_tid.ip_posid` = number of key attributes (encoded with
  `BT_OFFSET_MASK = 0x0FFF`)
- `t_info |= INDEX_ALT_TID_MASK` (bit 0x2000) — *required* on BTREE
  version 4 pivots
- Lower 13 bits of `t_info` = tuple size (INDEX_SIZE_MASK)

Leftmost internal-node downlink ("minus infinity"): zero-key-attribute
pivot tuple, size = `sizeof(IndexTupleData)` = 8 bytes, downlink only.
Per nbtsort.c:1006–1008.

Metapage update: `btm_root = (root block #)`, `btm_level = 1`,
`btm_fastroot = (root block #)`, `btm_fastlevel = 1`. Existing
`pgBuildBtreeMetapageWithRoot` accepts root block but currently the
callers pass `level=0`; the API takes `(rootBlock, leafLevel)` so an
adjustment to the caller is enough.

### Proposed Go surface

In `internal/initdb/btree_index_bootstrap.go`:

```go
// pgBuildBtreeBulkLoad packs sortedTuples into a multi-leaf btree file
// (metapage at block 0, leaves at blocks 1..N, root at block N+1).
// Returns the complete file bytes. Falls back to a single leaf-root
// page (current behaviour) when len(sortedTuples) fits in one page,
// preserving byte-exact output for every existing caller that fits.
func pgBuildBtreeBulkLoad(sortedTuples [][]byte) ([]byte, error)
```

Every existing caller of `pgBuildBtreeLeafRootPage +
pgBuildBtreeMetapageWithRoot` should migrate to `pgBuildBtreeBulkLoad`.
The migration is byte-equivalent for `<= 407`-tuple inputs and unblocks
the pg_extension seed plus any future nailed rel that pushes a populated
index past one leaf.

### Test coverage

New pins:
- `TestPgBuildBtreeBulkLoadSingleLeafByteIdenticalToLegacy` — confirms
  the new function emits the exact same bytes as the existing legacy
  path for any sub-407 input.
- `TestPgBuildBtreeBulkLoadTwoLeafLayoutMatchesPG18` — for a 500-tuple
  input, asserts:
  - file size = 3 × 8192 (meta + 2 leaves + root)
  - metapage `btm_root == 3`, `btm_level == 1`, `btm_fastroot == 3`
  - leaf 1 `btpo_prev = P_NONE`, `btpo_next = 2`, `btpo_level = 0`,
    `btpo_flags = BTP_LEAF`
  - leaf 1 has a P_HIKEY at item slot 1 matching leaf 2's first key
  - leaf 2 `btpo_prev = 1`, `btpo_next = P_NONE`, same flags
  - root `btpo_flags = BTP_ROOT`, `btpo_level = 1`
  - root has 2 downlinks; leftmost is zero-attribute (size 8); second
    downlink carries leaf 2's first key with `t_info & INDEX_ALT_TID_MASK`
    set and child block number 2 encoded as bi_hi/bi_lo
- Updated `TestBootstrapPgAttributeRelidAttnumIndexWritesPopulatedBtree`
  asserting per-entry round-trip through both the 1-leaf and 2-leaf path.

### Forward chain after Step 3av lands

With the multi-leaf btree refactor in place, the actual pg_extension
addition (Step 3au's original intent, now Step 3au-followup) is a pure
catalog-seed change:
- `pgExtensionAttrs()` returning the 8-column PG18 schema verbatim from
  `pg_extension.h:29-45`
- `nailedLocalRels` entry `{3079, "pg_extension", 83, 'r', 8, false, pgExtensionAttrs()}`
- 3079 added to the `bootstrapMappedLocalCatalogHeaps` OID list
- 3079 added to `localRelMap`
- Companion indexes 3080 (`pg_extension_oid_index`, UNIQUE PRIMARY) and
  3081 (`pg_extension_name_index`, UNIQUE on `extname name_ops`) follow
  the established single-OID rhythm in subsequent steps.

## Why one task per loop

The Ralph loop rule "ONE task per loop" plus the substantial scope of a
PG-compatible multi-leaf btree refactor (INDEX_ALT_TID_MASK pivot
tuples, P_HIKEY truncation, sibling-link bookkeeping, leftmost
minus-infinity pivot) argue against bundling the refactor with the
pg_extension seed in a single commit. The refactor is itself a non-
trivial subsystem change that deserves its own focused loop, its own
test pins, and its own design doc. This investigation doc records the
finding so the next loop can pick up the refactor with full context.
