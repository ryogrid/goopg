# M0146-0088 — binary-search the leaf for an index scan's start

Status: done 2026-10-07 (f3509cbdd). Parent: M0146-0068. Evidence:
`analysis/m0146/m0146-0088/`.

## Problem

M0146-0068 found that with `GOOPG_INDEX_PROBE_MULT=1` goopg elects PG's
plan for TPC-DS Q17 and Q71, but runs the parameterised index probe
1.3–1.6x slower than the shapes the 2x multiplier elects. The multiplier
was calibrated to hide that executor gap.

A forced nested loop probing a 500k-row primary key 200k times took 1.70 s
on goopg and 0.39 s on PG 18.3. The profile showed most of the time in
`scanLeafItems`. That function walked the leaf from slot 1 and compared
every item against the lower bound until the first qualifying one, copying
each item it read.

## PG behaviour

`_bt_first` descends to the leaf and calls `_bt_binsrch` to find the first
item at or past the scan key (past it for a strict bound), then reads
forward from there. A probe costs O(log n) comparisons per leaf.

## Change

- **`leafScanStart`** (`internal/access/nbtree/btree.go`) binary-searches
  the leaf's data items for the first item not below the lower bound. For a
  strict bound it looks for the first item not at or below it.
  `scanLeafItems` starts its loop there.
  - It reads the key of LP_DEAD items as well. The scan still skips those
    items, so the dead-item handling is unchanged.
  - Posting items compare on their key.
  - Any parse error returns slot 1, the old walk.
  - Leaves with fewer than 8 items keep the linear walk.
  - `leafBinarySearch` is the test's switch between the two starts.
- **No-copy reads.** `storage.PageGetItemRawAllowDeadNoCopy` and
  `pgGetItemRawAllowDeadNoCopy` alias the page. Both the descent's
  child-block search (`findChildBlockDirect`) and the leaf search use them;
  each read is held only while the page is locked.

## Verification

- `TestLeafScanStartMatchesLinearWalk` checks the binary start against the
  linear walk.
  - Coverage: both key formats, 4000 entries over 700 keys (posting items),
    LP_DEAD marks on the first leaves, and inclusive and exclusive bounds.
  - Both starts must return the same entries at the same positions.
  - A deliberate off-by-one in the start makes the test fail.
- Micro-benchmark: 1.70 s → 0.74 s, against 0.39 s on PG.
- TPC-DS SF0.25 sweep:
  - Q72 went from 171–176 s over five earlier sweeps to 23.6 s; subset
    re-runs gave 23.9 s and 24.2 s.
  - The sweep total went from 418 s to 238 s.
  - All 99 plan shapes are unchanged.
- Other gates:
  - TPC-H acceptance arm: 24 MATCH.
  - Fire set: no changed plan.
  - ea-ratchet: 9/9.

## Remaining gap (ledgered)

goopg is still about 1.9x slower than PG per probe. After the change the
profile is spread over several costs, with no single dominant one:

| path | cumulative share |
|---|---|
| per-rescan `NewScanCursor`/`descendToLeaf` | 31% |
| the leaf search itself | 21% |
| child search | 17.6% |
| `keyExceedsHighKey` | 9% |
| repeated posting checks | 7.5% |
| allocation | 9.5% |

The Q17/Q71 mult=1 re-measurement is left to the owner's M0146-0068
decision.
