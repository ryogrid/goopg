# M0106-0010 Step 3k — Nailed-index files must start with a PG-valid btree metapage

Status: implemented (2026-05-17)

## Problem

After Step 3j (`relnatts` ↔ `indnatts` alignment) the PG-standby boot
advanced past the consistency FATAL but immediately hit, on the first
catalog index scan:

    FATAL: index "pg_opclass_oid_index" is not a btree

The check is in `postgres/src/backend/access/nbtree/nbtpage.c:152-158`,
inside `_bt_getmeta`:

    if (!P_ISMETA(metaopaque) ||
        metad->btm_magic != BTREE_MAGIC)
        ereport(ERROR, (errcode(ERRCODE_INDEX_CORRUPTED),
                 errmsg("index \"%s\" is not a btree",
                        RelationGetRelationName(rel))));

PG reads block 0 of every btree index it touches, treats it as a
`BTMetaPageData` carrying `BTREE_MAGIC` (`0x053162`), and FATALs if
the page does not also carry `BTP_META` in `btpo_flags`. Each nailed
index goopg seeds during `initdb` therefore needs a valid metapage at
block 0.

## Root cause

`internal/initdb/initdb.go::makeBtreeRootPage` was emitting a btree
**leaf-and-root** page, not a metapage:

- `btpo_flags = BTP_LEAF | BTP_ROOT` (`0x03`) — `P_ISMETA(opaque)`
  returned false.
- No `BTMetaPageData` was written, so the first 4 bytes of the page
  contents area were zeros, not `0x00053162`.

Both call sites in `bootstrapPostgresDatabase` write the same image to
every nailed-index relfile under `base/1/`, `base/5/` and `global/`
(23 + 6 OIDs each). Every single one would FATAL the moment PG opened
it for read.

The reason the bug had been latent through Steps 3a–3j: the earlier
catalog-tuple gaps caused different FATALs to trigger first. Step 3j
landed the last consistency check that PG performs purely from
relcache data, so the very next catalog operation — opening
`pg_opclass_oid_index` to look up `name_ops` for the pg_class init —
became the canary site.

## Fix

`makeBtreeRootPage` is rewritten to mirror upstream
`_bt_initmetapage` (`postgres/src/backend/access/nbtree/nbtpage.c:66`).
The function name is kept (it has one external user, the
`bootstrapPostgresDatabase` index-file seed; rename would be churn);
the comment block makes the metapage role explicit.

### `internal/initdb/initdb.go`

- New `math` import for `math.Float64bits` (sentinel `-1.0` payload).
- `makeBtreeRootPage` now writes:
  - `pd_lower = SizeOfPageHeaderData + sizeof(BTMetaPageData)` so xlog
    page-image compression preserves the metadata bytes (matches
    upstream nbtpage.c:94).
  - `pd_upper = pd_special = BlockSize - 16` (BTPageOpaqueData size).
  - `pd_pagesize_version = BlockSize | 4`.
  - `BTMetaPageData` in the contents area:
    - `btm_magic = BTREE_MAGIC (0x053162)` @ +0
    - `btm_version = BTREE_VERSION (4)` @ +4
    - `btm_root = P_NONE (0)` @ +8 — empty index sentinel
    - `btm_level = 0` @ +12
    - `btm_fastroot = P_NONE (0)` @ +16
    - `btm_fastlevel = 0` @ +20
    - `btm_last_cleanup_num_delpages = 0` @ +24
    - (4 bytes pad for float8 alignment @ +28)
    - `btm_last_cleanup_num_heap_tuples = -1.0` @ +32
    - `btm_allequalimage = false` @ +40
    - (7 bytes trailing pad to `sizeof == 48`)
  - `BTPageOpaqueData` at end-of-page: only `btpo_flags = BTP_META`
    is non-zero.

`btm_root = P_NONE` is the canonical PG signal for "index has no
root page yet"; an index scan returns zero rows, and PG would
lazily allocate a root on the first writer-side insert. For a
read-only standby this matches the behaviour of an index that has
never been INSERTed into — which is exactly what every bootstrap
nailed index is at standby attach time.

## Scope deliberately not addressed

The metapage fix lets PG **open** the index without FATAL. It does
not put any tuples into the btree, so every `systable_beginscan(rel,
opclassOidIndexId, …)` against a nailed index still returns zero
rows. The next blocker surfaced by the E2E re-run is therefore:

    FATAL: could not find tuple for opclass 1986

`postgres/src/backend/utils/cache/relcache.c:1766` raises this from
`LookupOpclassInfo` when the index scan returns no tuple for the
`name_ops` opclass (OID 1986). The full fix requires building real
btree index pages keyed on the heap tuples we already seeded — i.e.
each nailed index needs not just a metapage but a populated leaf
page (or chain) with index tuples pointing at the corresponding
heap (block, line-pointer offset). That is a substantial
sub-milestone in its own right (every nailed index, every column
key, with the correct collation-aware btree sort) and is filed
separately as Step 3l.

## Regression pin

`internal/initdb/btree_metapage_test.go`:

- `TestMakeBtreeRootPageMatchesPGMetapage` asserts every load-bearing
  field of the on-disk image against the values `_bt_initmetapage`
  writes: BTREE_MAGIC, BTREE_VERSION, `btm_root = P_NONE`, all four
  other BlockNumber/level fields zero, `cleanup_num_heap_tuples =
  -1.0` (rebuilt via `math.Float64frombits`), `btm_allequalimage =
  false`, `pd_lower` past metadata, and `BTPageOpaque.btpo_flags ==
  BTP_META` with all other opaque fields zero. The test prevents a
  future refactor from silently regressing the magic number or
  pulling the metapage layout off-by-eight without an explicit
  matching code change.

## Verification

- `go test -count=1 -run TestMakeBtreeRootPageMatchesPGMetapage
  ./internal/initdb/` → PASS.
- `go test -count=1 ./internal/initdb/` → the 14 pre-existing
  baseline failures (M0106-0012 `TestSynchronousCommitFlushesByDefault`
  + the previously-noted bootstrapped pg_class/pg_attribute readability
  cluster + migration/recovery suites) are unchanged. Confirmed by
  stashing `internal/initdb/initdb.go` (the new test file moved
  aside) and re-running: identical 14-failure list.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` → PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -run
  'TestE2E_FailoverGoopgToPG/async' ./internal/testport/` →
  advances past the "is not a btree" FATAL; new blocker
  "could not find tuple for opclass 1986" surfaces in
  `LookupOpclassInfo` (Step 3l scope).
