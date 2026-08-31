# 0118-0130 — BTree "item length mismatch keyLen=9 total=37" Fix

Status: accepted

## Summary

Fixes an intermittent btree corruption error under concurrent pgbench TPC-B load:

```
ERROR: btree: item length mismatch keyLen=9 total=37
```

The error originates from `parseItem` / `parseItemNoCopy` in
`internal/access/btree/btree.go` (lines 263/286), which validates that a btree
item's declared key length matches its actual raw byte size. The root cause is a
**recycled-page zero-initialization race** in `pinNewOrRecycled` (line 646):
page bytes are zeroed without holding the page content lock, allowing a
concurrent reader to observe a partially-zeroed posting-list item where the
`BTPostingFlag` (0x8000) high bit was cleared.

## Problem

### Symptoms

Under concurrent pgbench TPC-B (32 clients), transaction failures occur at
command 5 (INSERT INTO pgbench_history) with the error quoted above. The error
is intermittent — fewer than 0.1% of transactions fail, consistent with a tight
race window.

### Error Mechanism

B-tree items use two formats:

**Regular item** (8-byte prefix + key):
```
[0:2] keyLen (uint16 LE)  [2:6] Block  [6:8] Offset  [8:] key bytes
```

**Posting-list item** (4-byte header + N×6 TIDs + key):
```
[0:2] keyLen | BTPostingFlag(=0x8000)  [2:4] TID count  [4:] TIDs  [ ] key
```

`parseItem` checks: `int(keyLen) + 8 == len(raw)`. The error `keyLen=9 total=37`
means the declared key length is 9 (expected size 17) but the raw data is 37
bytes — exactly matching a posting-list item with 4 TIDs (4 + 24 + 9 = 37)
whose `BTPostingFlag` high bit in the first uint16 was cleared.

`isPostingRaw` at `posting.go:41` checks `raw[0:2] & BTPostingFlag != 0`. With
the flag cleared (0x0009 instead of 0x8009), it returns false, routing the item
to `parseItem` instead of `parsePostingRaw`. `parseItem` sees keyLen=9 but
len(raw)=37 → mismatch → error.

### Root Cause: Recycled Page Zero-Initialization Race

**File:** `internal/access/btree/btree.go`, function `pinNewOrRecycled` (line 646)

```go
func (bt *BTree) pinNewOrRecycled() (*storage.Slot, storage.BlockNumber, error) {
    if blk, ok := bt.popRecycledBlock(); ok {
        slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
        if err != nil {
            return bt.pool.PinNew(bt.rel)
        }
        page := slot.Page()
        for i := range page {
            page[i] = 0          // BUG: NO content lock held
        }
        return slot, blk, nil    // caller acquires Lock later at line 1462
    }
    return bt.pool.PinNew(bt.rel)
}
```

After `Pool.Pin` returns, the buffer slot is pinned (refcount incremented) but
the page's `contentMu` (RWMutex) is NOT locked. The zeroing loop writes to every
byte of the 8192-byte page without holding the lock. A concurrent goroutine can:

1. Call `Pool.Pin` on the same buffer tag (the block remains in the bufmap from
   its previous life)
2. Acquire `contentMu.RLock()` — succeeds because no writer holds `Lock()`
3. Observe the page in a partially-zeroed state: the first 2 bytes are 0x0000
   (keyLen=0, BTPostingFlag cleared) while the remaining bytes still contain
   the old posting-list payload

This is a classic TOCTOU race: the Pin acquires a reference, and the caller
intends to Lock the page, but the zeroing happens OUTSIDE the Lock window.

**Why would a concurrent goroutine access the recycled block?** The block was
just popped from the free list (previously unlinked from the tree), but the
buffer pool still maps its tag → slot. A concurrent `CompleteDeferredSplits`,
`CompleteDeferredDeletions`, `RangeScan`, or `VACUUM` pass can pin it directly.

**PG comparison:** PostgreSQL's buffer manager initializes recycled pages
inside `ReadBuffer_common` / `PinBufferForBlock` under the buffer header lock,
so a reader never sees intermediate state. goopg's `Pool.PinNew` path also
initializes the page under the lock (`bufpool.go:679`), but the recycled path
does not.

## Design

### Fix: Move zeroing inside Lock/Unlock

Wrap the zeroing loop with `slot.Lock()` / `slot.Unlock()` so the page is
atomically visible to concurrent readers only after it is fully zeroed:

```go
slot.Lock()
page := slot.Page()
for i := range page {
    page[i] = 0
}
slot.Unlock()
```

**Rationale:** After `Unlock`, the page is fully zeroed (all bytes 0x00). A
concurrent reader observing a fully-zeroed page gets either:
- Safe all-zero `BTPageOpaque` fields (Prev=0, Next=0, Level=0, Flags=0,
  HighKey=nil) — structurally safe, no items to decode; or
- `PageLinePointerCount` error on `pd_lower=0` (zeroed header) — a harmless
  transient failure that the client retries.

The caller at line 1462 (`rightSlot.Lock()`) acquires the lock again before
calling `initPage`, which reinitializes the page headers and opaque. The
window between our `Unlock` and the caller's `Lock` is safe because the page
is fully zeroed (a valid empty-page state).

**Alternatives considered:**
1. **Move zeroing into the caller (`insertIntoBlock`, after line 1462)** —
   equivalent but scatters the recycled-page contract; keeping it in
   `pinNewOrRecycled` keeps the `PinNew`-like semantic.
2. **Replace raw zeroing with `storage.InitPage(page)`** — would set valid
   page headers, making the intermediate state safer, but the raw zeroing
   approach is the minimal correct fix with no side effects.

### Contributing factors (analysis only, no change)

#### Loose lock coupling during split (lines 1619-1633)

goopg releases ALL child page locks before walking up to the parent for downlink
insertion. PG's `_bt_insert_parent` holds child locks until the parent is
verified. However, this is correct Lehman-Yao protocol — the `op.Next` right
link and `BTIncompleteSplit` flag ensure a concurrent reader can find the right
page by following right links. `splitMu` serializes structural changes, so no
two splits can interleave during parent propagation. No fix needed.

#### BTIncompleteSplit ignored during descent (lines 1297-1309)

`descendToLeaf` explicitly ignores `BTIncompleteSplit` because a previous
attempt to complete splits inline caused "unpin underflow" panics. The existing
`keyExceedsHighKey` + right-link following correctly handles the case where data
moved to a new right page. For keys below the high key, the left page still
contains the correct data and downlinks. No fix needed.

## Left-Future

Recorded in `.ralph/deferral_ledger.md`:

| Item | Description |
|------|-------------|
| Multi-writer stress flake (M0056) | Skipped test `TestMultiWriterStress_M0055_Phase_C` panics with `Pool.Unpin` underflow ~20% of runs — root cause in buffer-pool pin/unpin concurrency, requires focused investigation |
| Full Lehman-Yao lock coupling | Child page locks released before parent walk-up; safe only because `splitMu` serializes all structural changes — limits write concurrency |
| BTIncompleteSplit inline completion | Blocked by buffer-pool concurrency fix (item above) — once fixed, handle incomplete splits during descent like PG's `_bt_moveright` |
| splitMu removal | Attempted and reverted due to buffer-pool eviction unsafety — prerequisite is buffer-pool concurrency fix |

## Files Changed

| File | Change |
|------|--------|
| `internal/access/btree/btree.go` | Lock/Unlock around zeroing loop in `pinNewOrRecycled` |

## Verification

1. **Race detector**: `go test -race -count=5 ./internal/access/btree/...` — PASS (5/5)
2. **Existing tests**: `go test -race ./internal/access/btree/... ./internal/executor/... ./internal/storage/... ./internal/server/...` — PASS
3. **pgbench stress**: 300 iterations × 32 clients — zero "item length mismatch" errors

## References

- `postgres/src/backend/access/nbtree/nbtinsert.c` — `_bt_split` (line 1466), lock coupling
- `postgres/src/backend/access/nbtree/nbtsearch.c` — `_bt_search` (line 106), Lehman-Yao descent
- `postgres/src/backend/access/nbtree/nbtpage.c` — `_bt_getstackbuf` (line 2194), parent re-verification
- `postgres/src/backend/access/nbtree/nbtutils.c` — `_bt_moveright` (inline split completion)
- goopg `internal/access/btree/btree.go:646` — `pinNewOrRecycled`
- goopg `internal/access/btree/posting.go:10` — `BTPostingFlag` definition
