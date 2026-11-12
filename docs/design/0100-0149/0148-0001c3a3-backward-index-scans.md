# M0148\-0001c3a3 — Backward ordered index scans

**Parent:** M0148\-0001c3a2 · **Status:** slice 1 landed 2026\-11\-12 \(nbtree reverse range cursor\); slice 2 landed 2026\-11\-12 \(executor `IndexScan.Backward` \+ EXPLAIN label\); slice 3 landed 2026\-11\-12 \(planner backward paths\); Index Only Scan Backward open

## Problem

PG builds a second IndexPath with `BackwardScanDirection` whenever the reversed
index order is useful. Examples are `ORDER BY a DESC` on an `(a)` index, or
a merge join wanting descending input \(build\\_index\\_paths, indxpath.c\). The
executor then walks the btree from right to left \(\\_bt\\_first, \\_bt\\_readpage,
\\_bt\\_readnextpage, nbtsearch.c\). EXPLAIN prints `Index Scan Backward` /
`Index Only Scan Backward`; 26 such lines are missing from goopg's regress
output \(btree\\_index, create\\_index, inherit, join, partition\\_join\).

goopg could not produce one at any layer:

- `indexPathOrdering` can build backward pathkeys, but no producer asks.
- `createIndexScanPlan` panics on a non\\-forward direction.
- The plain IndexScan executor has no reverse mode.
- nbtree had only `ScanFromEnd`, the planner's endpoint probe, which may
  skip or repeat entries under a concurrent split.

## Slice 1 — the reverse range cursor \(nbtree\)

`internal/access/nbtree/reverse_scan.go`:

- **`NewReverseScanCursor(lo, hi, loEx, hiEx)` positions the scan:**
  - with `hi`, it descends to hi's leaf;
  - without, it goes to the rightmost leaf \(`rightmostLeaf`, \\_bt\\_get\\_endpoint\);
  - then it moves right past every leaf whose high key is at or below `hi`.
    Equal and prefix\\-equal entries can continue there. Moving one page too
    far only costs a read; stopping short would lose entries.
- **`Next(fn)` reads one leaf per call,** like `ScanCursor.Next`. The entries
  go to fn from the highest key down:
  - an entry above `hi` is skipped;
  - the first entry below `lo` ends the scan;
  - a posting list's TIDs come out descending.

  `scanLeafItemsReverse` uses `scanLeafItems`' own bound comparisons, so a
  forward and a backward scan of one range admit the same entries.
  Tombstones \(P\\_IGNORE\) are stepped over.
- **The left step is `_bt_lock_and_validate_left`** \(`lockAndValidateLeft`\).
  A split moves entries right, so the left sibling read off the current page
  may have split since, leaving a page the scan still owes between them.
  - The candidate is accepted only when its right\\-link still names the page
    just read.
  - Otherwise the walk moves right from it, at most four hops.
  - Failing that, it recovers from the page just read. If that page was
    deleted, the walk restarts from the left link of the first live page to
    its right. If not, it restarts from its moved left link.
  - **One deviation from PG:** when that left link is unchanged, PG raises
    "could not find left sibling". goopg instead walks right without the
    hop limit \(`walkRightToLeftOf`\), because a left sibling that split more
    than four times is still to the right of where the walk started.

### Verification

- `TestReverseScanCursorMirrorsForward`: 11 bound shapes — unbounded,
  closed, exclusive, one\\-sided, past either end, empty, single. Each must
  equal the forward scan reversed.
- `TestReverseScanCursorDuplicates`: duplicate runs spanning leaves, and
  posting lists.
- `TestReverseScanCursorEarlyStop`.
- `TestReverseScanCursorConcurrentSplits`: between leaves, 1000\\+ inserts
  split the pages ahead of \(to the left of\) the scan. Every original key
  must arrive exactly once, in descending order, and
  `reverseScanLeftRecoveries` must show the recovery path ran. With the
  validation replaced by a naive left step, the test loses keys \(key 10200
  delivered 0 times\).

## Slice 2 — executor \(`IndexScan.Backward`\)

- **`IndexScan.Backward` \(plan.go\):** `indexScanOp`'s main probe path —
  equality, equality prefix, range, NullTest — computes its bounds exactly as
  forward. It then opens `tree.NewReverseScanCursor(lo, hi, loExcl, hiExcl)`
  instead of `ResetScanCursor`. `nextLeafBatch` pulls one leaf per batch
  from it; the entries are already descending.
- **Refused for now:** SAOP, skip\-scan and parallel \(leaf\-filtered\)
  backward scans return an `XX000` planner\-bug error. PG runs all three
  backward, but no goopg producer emits them yet \(ledgered\).
- **EXPLAIN:** prints `Index Scan Backward using …` \(`indexScanDirection`,
  ExplainIndexScanDetails\).
- **`TestIndexScanBackward`:** for each probe shape, a forward plan from the
  planner, flipped by hand, must return exactly the forward rows reversed.
  - The shapes are a leading range in each direction, a closed range, an
    equality prefix with duplicates, prefix plus bound, a DESC index and an
    equality.
  - Each runs with and without NULL\-keyed entries; the open ends meet the
    NULL\-group stop.
- **No caller yet:** no producer sets `Backward`, so no plan changed.
  sf025 99/99, fire\-set fires=none, acceptance 24 MATCH.

## Slice 3 — planner backward paths

- **Producers:** `usefulBackwardIndexPathkeys` is build\_index\_paths step 5,
  "if the index is ordered, a backwards scan might be interesting". It builds
  the index's pathkeys for BackwardScanDirection \(`indexPathOrdering(…,
  true)`\) and truncates them to what the query can use
  \(`truncateUselessPathkeys` over `pathkeyUsefulnessFor(rel.Relids)`: query
  ordering, grouping, distinct and merge keys\). Empty means no path, as in
  PG.
  - The ordered \(full\-scan\) producer and the restriction producer add the
    twin of their forward path with those keys and `BackwardScanDirection`,
    at the same cost, since cost\_index prices both directions alike.
  - Not for SAOP or skip probes, and no partial twin, because the executor
    walks only the plain probe path backward.
- **Lowering:** `createIndexScanPlan` sets `IndexScan.Backward` instead of
  panicking. A backward index\-only, skip or SAOP path is still a producer
  bug and panics.
- **IOS promotion:** `tryPromoteIndexOnlyScan` declines a backward IndexScan.
  `IndexOnlyScan.Backward` is the S6 max rewrite's materialised reverse, not
  a reverse walk.
- **Verification:**
  - Probe against PG 18.3: 6 statements identical, plans and values —
    `Index Scan Backward` under ORDER BY DESC, behind a restriction, and
    under a LIMIT.
  - `TestBackwardIndexPathsPlanned` pins three of PG's plans with rows equal
    to a sorted twin's.
  - Two producer tests updated:
    - a merge clause now yields both directions of a DESC index, as PG's
      pathkeys\_useful\_for\_merging is direction\-blind;
    - the createplan panic case is now a backward skip probe.
  - Regress btree\_index 499→457, create\_index 3159→3143; no spurious
    backward plan anywhere.
  - Of the 24 PG `… Backward` lines still missing, 15 are `Index Only Scan
    Backward`.
  - sf025 99/99; fire\-set fires=none on sf025, sf1 and TPC\-H; acceptance
    24 MATCH.

## Next slices

1. Executor:
   - `IndexScan.Backward` / `IndexOnlyScan.Backward` \(the latter today
     means only the S6 max\\-rewrite materialised reverse\);
   - `indexScanOp` reverse leaf batches from `ReverseScanCursor`, for range
     and equality\\-prefix probes, through `indexRangeBounds`;
   - the Pin/heap\\-fetch path unchanged.
2. Planner: producers call `indexPathOrdering(…, backward=true)` where PG's
   build\\_index\\_paths would. `createIndexScanPlan` sets the direction instead
   of panicking.
3. EXPLAIN ` Backward`, and the regress A/B on the 26 lines.

Not covered yet: a page deleted under the scan. The `lastcurr`\\-deleted
recovery arm is ported but has no test — VACUUM's unlink would have to run
mid\\-scan.
