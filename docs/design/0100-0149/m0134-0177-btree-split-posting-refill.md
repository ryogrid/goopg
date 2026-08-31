# M0134-0177 — B-tree split must re-form posting lists, not expand them

**Status:** accepted (2026-08-29)
**Milestone task:** M0134-0177 (`test_setup.sql`)
**Code:** `internal/access/nbtree/{btree.go,posting.go,bulkload.go}`,
`scripts/pg-regress-runner.sh`
**Guard:** `internal/access/nbtree/split_posting_refill_test.go`

## What the task was, and what it actually found

`test_setup.sql` is upstream's regression *prerequisite* — it creates
`onek`/`tenk1`/`person`/`road` and friends that most other cases read. Its
status in the target inventory was `not-tried`, and the case turns out to
already match PG 18.3 **byte for byte** (237 lines, 0 divergence). It was
`not-tried` because it could not be *run*: `scripts/pg-regress-runner.sh`
excludes `test_setup.sql` from `--all` discovery (correctly — it is the
prerequisite, not a case), and if you name it explicitly the runner executes it
**twice**: once as its own setup prerequisite, then again as the test.

The second pass is what mattered. It `COPY`s the same rows into an
already-populated `onek`/`tenk1` whose indexes `create_index.sql` had just
built, and the backend **crashed**:

```
panic="storage: not enough free space in page"
  nbtree.mustInsertItemSorted            btree.go:3448
  nbtree.(*BTree).insertIntoBlock        btree.go:2960
  nbtree.(*BTree).Insert                 btree.go:2585
  executor.maintainUniqueIndexesForInsert operators_storage.go:7996
  executor.(*CopyFromExecutor).insertSourceRow copy.go:553
```

So the deliverable is not a `test_setup.sql` fix. It is an engine-wide B-tree
defect that any duplicate-heavy index reaches: **`COPY` into a table with a
low-cardinality index can take the backend down, and — as the regression test
shows — can silently lose index entries before it gets there.**

## Root cause

`pageItems` (`btree.go`) **expands** every posting-list line pointer into one
`item` per heap TID, so callers like `insertItemSorted` can reason about
individual entries. `insertIntoBlock`'s split path then:

1. reads the page with `pageItems` → the expanded form,
2. splices in the new item (`appendSorted`),
3. drops exact `(key, ptr)` duplicates (`dedupConsolidate`),
4. picks a midpoint (`byteAwareSplitLoc`), and
5. refills both halves with `mustInsertItemSorted`, i.e. **one plain line
   pointer per expanded entry**.

Steps 1 and 5 disagree. A posting pays the key overhead **once per run**; the
expanded form pays it **once per TID**. Instrumenting the crashing split
measured the gap directly:

```
SPLITDBG blk=1 nAll=1098 compactAll=21960 occupied=20 freeBudget=8132 mid=549
         compactLeft=10980 compactRight=10980 hkFoot=20
```

A leaf whose data budget is 8132 bytes expanded to **21960** bytes. Half of
that is 10980 — over a page — so *both* halves overflowed and the refill
panicked. Normal splits in the same log show `compactAll≈8700`, i.e. exactly
"one page plus the new item", which is the invariant the split path silently
assumes.

`byteAwareSplitLoc` compounded it: it priced an item as
`SizeOfIndexTupleData + len(key)` — no line pointer, no MAXALIGN, no posting
awareness — a *different* expression from `itemEncodedSize`, which is the
single source of truth `pageHasSpaceFor` and `PageInsertItemRawAt` share. A
space estimate that disagrees with the code consuming it is the same shape as
root-0040, one layer up.

Upstream never has this problem: `_bt_split` moves **raw on-page items** and
never expands a posting. The only posting a split touches is the one the new
item lands inside, handled by `_bt_swap_posting`, which preserves the byte
length precisely so the page arithmetic stays valid
(`postgres/src/backend/access/nbtree/nbtinsert.c`, `nbtsplitloc.c`,
`nbtdedup.c`).

## The fix

Rather than restructure the split around raw items, goopg keeps the expanded
working form and **re-forms postings on the way out**, which restores the same
invariant. Three pieces:

### 1. One chunking rule, two callers (`posting.go`)

`postingChunkLens(key, n)` is now the single statement of how a run of `n`
same-key entries is cut into line pointers (chunk at `maxRawItemSize`, a
trailing one-TID remainder falls back to a plain item because a posting is
defined to hold ≥ 2 TIDs). `deduplicateToRawItems` **writes** from it and
`runFootprint` **budgets** from it, so the estimate and the writer cannot
drift. `TestRunFootprintMatchesDeduplicateToRawItems` pins the equality across
key widths and run lengths on both sides of the chunk limit.

### 2. The refill re-forms postings (`refillDeduplicated`, `btree.go`)

Both whole-page rewrites — the dedup-recovery no-split path and the two split
halves — now write through `deduplicateToRawItemsWithSpans`, emitting posting
lists for runs of same-key entries instead of a plain line pointer per TID.
This is M0055-0003 "Phase B's full landing", which `dedupConsolidate`'s comment
had explicitly deferred; that comment is corrected in place.

The dedup-recovery fit test moved from `compactRawSize` to the new
`compactFootprint` for the same reason, which also makes the no-split fast path
fire far more often on duplicate-heavy pages — the split is *avoided*, not just
survived.

### 3. The split point is fit-checked, and refuses rather than overflows

`compactSplitLoc(items, leftBudget, rightBudget)` replaces
`byteAwareSplitLoc`. It balances the halves on the **compact** footprint and
returns `ok=false` when no cut fits, so the caller errors out with the latch
coherent instead of resetting the page and discovering the problem partway
through writing it back. Run boundaries are computed once and prefix/suffix
sums make the scan O(n).

The separator charge follows `_bt_recsplitloc`: the first right item becomes
the left page's high key, so it counts against left as well as right, and
upstream declines to assume suffix truncation shrinks it ("we cannot assume
that suffix truncation will make it any smaller"). goopg adds one MAXALIGNed
heap TID on top, because `truncateSeparator` may append the tiebreaker TID when
no key attribute distinguishes the halves.

### Supporting change: `appendSorted` orders by (key, heap TID)

`sort.Search` alone landed a new entry at the **front** of its key run. That is
harmless while every entry is its own plain line pointer, but a re-formed
posting's TID array is defined ascending — `SwapPosting` calls a non-ascending
one corruption and amcheck checks it. Ordering by `(key, heap TID)` is also
what a heapkeyspace leaf means by "sorted" (`_bt_findinsertloc`).

`byteAwareSplitLoc` and `compactRawSize` are deleted rather than left in place:
an unreachable sibling that states the *wrong* size model is exactly the thing
a later loop mirrors by mistake.

## WAL

`DescribeSplitLeft` (`pgsplitleft.go`) reconciles the two halves against the
pre-split page and already anticipates this: its error list names "a posting
list the dedup pass merged". A re-forming split no longer matches upstream's
`XLOG_BTREE_SPLIT_L` description, so it falls back to a full-page image —
larger WAL for these splits, correct in every other respect. No encoder change.

## Verification

- `scripts/pg-regress-runner.sh -v test_setup` → **PASS, 237 lines, 100%
  parity** (was: "psql lost the connection — NOT a valid diff").
- The double-`test_setup` reproduction (test_setup → create_index →
  test_setup) runs to completion with **0 backend panics**; before, it died on
  the first `COPY onek`.
- `TestSplitRefillFitsPostingHeavyPage` — 24 keys × 900 duplicates through the
  real `Insert` path, then a full `RangeScan` readback. Revert-checked against
  HEAD, where it fails **`RangeScan returned 21396 entries, want 21600`**: the
  pre-fix split path did not only crash on some shapes, it **silently dropped
  204 index entries** on others. Only the readback distinguishes the two.
- `go test ./internal/access/nbtree/... ./internal/storage/...` PASS.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS.
- `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2 22.2s, Q13 rows=34 9.3s).
- `scripts/pg-regress-runner.sh` quick set, A/B against stashed HEAD:
  **byte-identical** — same 4/52 passes, same 48 diff files, same line counts.

## Deferred

See `.ralph/deferral_ledger.md` (2026-08-29, M0134-0177) for the remaining
divergence: goopg's split still works on the EXPANDED form and re-compacts,
where upstream partitions raw on-page items and splits a posting only via
`_bt_swap_posting`. The observable behaviour now agrees; the WAL description
does not (FPI fallback), and `postingoff`-style posting splits are still not
produced by goopg's own insert path.
