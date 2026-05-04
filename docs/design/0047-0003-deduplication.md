# B-tree Leaf Deduplication — M0047-0003

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

On columns with many duplicate values (e.g. `l_shipmode` with 7 distinct values
across 6M rows in TPC-H SF=1), every row produces a separate B-tree leaf entry
`(key, TID)` even though the key bytes are identical. This wastes both disk space
and buffer-pool slots: key bytes are repeated N times instead of stored once.

PostgreSQL's B-tree deduplication (introduced in PG 13) groups entries with the
same key into a **posting list**: a single leaf item `(key, [TID1, TID2, …, TIDn])`.

## 2. Design

### 2.1 Posting item format (`internal/access/btree/posting.go`)

A posting item is detected by a flag in the first two bytes of the raw page item:

```
[0:2]  keyLen | BTPostingFlag(0x8000)   (uint16 LE)
[2:4]  TID count N                      (uint16 LE)
[4:4+N*6]  N x (Block=4B LE, Offset=2B LE)
[4+N*6:]   key bytes (keyLen bytes)
```

A regular item has `keyLen` without the high bit set (existing format unchanged).

Key functions:
- `marshalPosting(key, tids)` — serialise a posting list
- `isPostingRaw(raw)` — detect posting items by checking bit 15 of raw[0:2]
- `parsePostingRaw(raw)` — deserialise key and TID slice
- `postingKeyOf(raw)` — extract key without allocating a TID slice

### 2.2 BulkCreate deduplication (`internal/access/btree/bulkload.go`)

`BulkCreate` now calls `deduplicateToRawItems(items)` before `buildLevelRaw`:

- After sorting entries by key, consecutive runs with the same key are collapsed.
- A run of 1: emitted as a regular item (`item.marshal()`, 8+keyLen bytes).
- A run of N >= 2: emitted as a posting item (`marshalPosting`, 4+N*6+keyLen bytes).

`buildLevelRaw([]rawItem, ...)` replaces `buildLevel([]item, ...)` for the leaf
level. A `rawItem` is a pre-serialised `{raw []byte, key []byte}` pair.
`buildLevelRaw` uses `len(ri.raw)+4` (item bytes + line pointer) for capacity
checks so posting items fill pages correctly.

`BulkCreateNoDedup` is a test-only variant that skips deduplication, enabling
fair "before vs. after" comparisons in the DoD test.

### 2.3 RangeScan posting-item expansion (`internal/access/btree/btree.go`)

`RangeScan` no longer calls `pageItems` to read leaf pages. Instead it copies
raw page slots before unpinning the buffer and iterates them directly:

- **Posting item**: calls `fn(key, tid)` once per TID in the posting list.
- **Regular item**: calls `fn(key, ptr)` once (unchanged behaviour).

`pageItems` (used internally for `insertItemSorted`, split logic, and index
vacuum) is updated to **expand posting items** into individual `(key, tid)`
pairs. This ensures `Insert` and page-split code work transparently with
deduplicated pages, at the cost of losing compaction on pages that receive
subsequent `Insert`s. Dedup is re-applied only at the next `BulkCreate` /
REINDEX.

### 2.4 Limitations

- Posting items must fit on one 8 KiB page: max N*6 + keyLen + 4 < 8168 bytes,
  so at most ~1361 TIDs per key for a 30-byte key.
  Columns with millions of duplicates per key would require TOAST-backed overflow,
  deferred to a follow-up.
- `Insert` always creates single-TID items (no in-place posting-list growth).
  Dedup is only achieved via `BulkCreate` (i.e. on initial `CREATE INDEX`).
- No `XLOG_BTREE_DEDUP` WAL record; pages use FPI (same as other BulkCreate pages).

## 3. Space savings

For an index on a column with K distinct values and N rows per value, with
encoded key length L:

| Variant | Bytes per TID |
|---|---|
| Regular (per entry) | 8 + L + 4 (LP) = 12+L |
| Posting (large N, amortised) | ~6 |

Ratio at large scale -> 6/(12+L). For L=30 bytes: 6/42 = 14%, well under the 25% DoD.

## 4. Tests (`internal/access/btree/posting_test.go`)

| Test | Coverage |
|---|---|
| `TestPostingMarshalRoundTrip` | `marshalPosting` + `parsePostingRaw` are exact inverses |
| `TestIsPostingRaw` | Distinguishes posting from regular raw bytes |
| `TestDeduplicateToRawItems` | Duplicate keys -> posting items; unique keys -> regular |
| `TestBulkCreateDeduplication` | **DoD**: 7 keys x 1000 TIDs, dedup blocks <= 25% of baseline |
| `TestBulkCreateDeduplicationInsertAfter` | `Insert` works on a deduplicated tree |
| `TestPostingKeyOf` | Key extraction without TID allocation |
