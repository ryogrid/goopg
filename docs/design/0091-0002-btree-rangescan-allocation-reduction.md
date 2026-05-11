# Design 0091-0002 — btree.RangeScan allocation reduction

**Status:** authoritative for M0091-0002 implementation.
**Milestone:** [M0091](../milestones/0091-select-only-tps-regression-recovery.md).

## Problem

`internal/access/btree/btree.go::RangeScan` (lines 1923-1990)
allocates a per-slot `[]byte` for every line pointer on every
leaf-page visit — ~400 byte-slice allocations per point lookup
on the scale-100 pgbench pkey. Combined with the `rawSlots`
slice and `parseItem` allocations, this drives ~230 MB / 30 s
allocation rate (13 % of total allocations in the select-only
pprof).

Current loop:

```go
count, _ := storage.PageLinePointerCount(slot.Page())
type rawSlot struct{ raw []byte }
rawSlots := make([]rawSlot, 0, count)                // alloc 1
for s := uint16(1); s <= uint16(count); s++ {
    r, _ := storage.PageGetItemRaw(slot.Page(), s)   // alloc 2..(count+1)
    rawSlots = append(rawSlots, rawSlot{
        append([]byte(nil), r...),                   // alloc — explicit copy
    })
}
bt.unpinR(slot)
for _, rs := range rawSlots { ... fn(...) }
```

The pattern was designed so `fn` could re-enter the btree
without deadlocking against the held pin — but no current
production caller re-enters.

## Caller audit (CAT-1 verified)

Per the M0091 Explore agent investigation, all 4 production
`RangeScan` call sites are read-only:

| caller | file:line | what `fn` does |
|---|---|---|
| `indexScanOp.Rescan` | `operators_index.go:292` | Pins HEAP relation, follows HOT chain, decodes row, appends to `o.rows`. Never re-enters btree. |
| `indexOnlyScanOp.Rescan` | `operators_indexonly.go:114` | Decodes row from key (fast path) or pins HEAP. No btree calls. |
| `upsertOp.probeArbiter` | `operators_upsert.go:224` | Pins HEAP, reads + visibility-checks tuple. No btree calls. |
| update path index-probe | `operators_storage.go` | Pins HEAP, optionally takes a lockmgr tuple lock. No btree calls. |

Test callers (~15 sites in `btree_test.go` etc.) all use
trivial / read-only callbacks.

**Zero callers retain `key []byte` or `ptr` past `fn`'s return.**

## Approach

**Rewrite `RangeScan` to parse-while-pinned and invoke `fn`
with page-aliased bytes.** The pin is released only after the
leaf-page's last item has been fed to `fn` (or `fn` returned
stop / error).

```go
func (bt *BTree) RangeScan(lo, hi []byte, fn func(key []byte, ptr storage.ItemPointer) (bool, error)) error {
    cur, _, err := bt.descendToLeaf(lo)
    if err != nil {
        return err
    }
    for cur != storage.InvalidBlockNumber {
        slot, err := bt.pinR(cur)
        if err != nil {
            return err
        }
        op := readOpaque(slot.Page())
        if lo != nil && keyExceedsHighKey(op, lo) {
            next := op.Next
            bt.unpinR(slot)
            cur = next
            continue
        }
        count, _ := storage.PageLinePointerCount(slot.Page())
        nextBlk := op.Next  // capture before fn runs
        stop := false
        var fnErr error
        for s := uint16(1); s <= uint16(count); s++ {
            r, rawErr := storage.PageGetItemRaw(slot.Page(), s)
            if rawErr != nil {
                continue
            }
            if isPostingRaw(r) {
                key, tids, perr := parsePostingRaw(r)
                if perr != nil { continue }
                if lo != nil && CompareKeys(key, lo) < 0 { continue }
                if hi != nil && CompareKeys(key, hi) > 0 { stop = true; break }
                for _, tid := range tids {
                    ok, ferr := fn(key, tid)
                    if ferr != nil { fnErr = ferr; stop = true; break }
                    if !ok { stop = true; break }
                }
                if stop { break }
            } else {
                it, perr := parseItem(r)
                if perr != nil { continue }
                if lo != nil && CompareKeys(it.key, lo) < 0 { continue }
                if hi != nil && CompareKeys(it.key, hi) > 0 { stop = true; break }
                ok, ferr := fn(it.key, it.ptr)
                if ferr != nil { fnErr = ferr; stop = true; break }
                if !ok { stop = true; break }
            }
        }
        bt.unpinR(slot)
        if fnErr != nil { return fnErr }
        if stop { return nil }
        cur = nextBlk
    }
    return nil
}
```

Allocations eliminated per leaf-page visit:
- `make([]rawSlot, 0, count)` — gone.
- `append([]byte(nil), r...)` × count — gone for non-posting
  items (the common case for pgbench pkey).

For posting items, `parsePostingRaw` still allocates a TID
slice + key copy. This is the rare branch for pgbench (the
pkey doesn't use posting); posting-specific optimisation is
deferred (out of M0091 scope).

## Safety / contract

**New contract documented above `RangeScan`:**

```go
// RangeScan visits every (key, ptr) in [lo, hi] in ascending
// order, invoking `fn` for each.
//
// CONTRACT: the `key []byte` and `ptr` passed to `fn` ALIAS the
// pinned btree leaf page. `fn` MUST NOT:
//   - retain `key` beyond its return (the page may be unpinned
//     and reused for a different page after the next call);
//   - re-enter THIS btree (would deadlock against our held
//     RLock on the leaf page).
//
// Callers that need to retain the key can clone it explicitly:
//   keyCopy := append([]byte(nil), key...)
//
// Callers that need to re-enter the same btree must build a
// pending-set under fn, return from RangeScan, then iterate
// the pending-set without the pin held.
//
// The 4 production callers (indexScanOp.Rescan,
// indexOnlyScanOp.Rescan, upsertOp.probeArbiter, and the
// non-HOT UPDATE index-probe) are all CAT-1 per
// docs/design/0091-0002 — they don't retain keys and don't
// re-enter the btree. Audited in M0091.
```

**Concurrency safety:**
- `pinR` acquires the page's `RLock` (`contentMu` read-lock).
  Multiple readers OK; blocks btree WRITERS on this leaf page
  only.
- Concurrent readers of the same leaf can both call
  `RangeScan` and each invoke their `fn`; the `RLock`s don't
  conflict.
- A concurrent writer (insert / split / vacuum) on this leaf
  page must wait for our `RLock` to release. For point
  lookups `fn` is fast (heap fetch + decode = ~µs on warm
  cache). Bounded write-starvation.

**Risks:**
- R1: cold-cache heap fetch inside `fn` could take ms (disk
  I/O). Acceptable trade-off; the historical baseline had the
  same effective latency window (the pin was released slightly
  earlier under the old code, but `fn` immediately re-pinned
  the heap and waited the same I/O time).
- R2: future caller adds re-entry. Mitigated by the doc-comment
  contract; CI lint could later assert no btree method is
  called from inside any `fn` passed to `RangeScan` (out of
  M0091 scope).

## Test coverage

- Existing `internal/access/btree/btree_test.go::TestRangeScan`
  (correctness) must continue to pass.
- Existing `bulkload_test.go`, `posting_test.go`,
  `btree_vacuum_test.go` correctness tests must continue to
  pass.
- Add a benchmark: `BenchmarkRangeScanPointLookup` that
  inserts 10K rows, then runs 1,000 point lookups; verify
  `allocs/op` is near zero on the non-posting path.

## Expected impact

- ~30 KB per query allocation eliminated (rawSlots + per-slot
  byte copies).
- ~100 MB/s allocation rate reduction at -c 10 × ~330 q/s.
- GC mark phase drops proportionally; combined with
  M0091-0001's removal of activity-tracking allocations, GC
  CPU share should drop below 30 %.

## Migration

Single commit; no API change (function signature stays the
same). Callers are unaffected because they already don't
retain `key`.
