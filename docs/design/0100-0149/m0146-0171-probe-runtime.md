# M0146-0171 — the per-probe runtime of PG-shaped parameterised index probes

Status: closed 2026-10-13. Slice 1 landed (`4e4b32af9`); the regression the
task names (PG-shaped probes slower than goopg's x2 shapes) no longer holds.
The remaining ~1.4x gap to PG on the isolated loop stays with M0146-0088's
ledgered probe residual.
Parent: M0146-0159. Evidence: `analysis/m0146/m0146-0171/`.

## Problem

After M0146-0159 retired the x2 index-probe multiplier, TPC-DS SF0.25 Q17,
Q25 and Q83 match PG's plans but run 1.2–1.26x slower than goopg's old
shapes. On Q17, EXPLAIN ANALYZE shows the `catalog_sales_pkey` probe on
`cs_item_sk`, run 7511 times:

| engine | ms per loop | total |
|---|---|---|
| PG 18.3 | 0.013 | about 98 ms of 160 ms |
| goopg | 0.049 | 366 ms of 565 ms |

Each probe reads about 26 heap tuples and filters them on
`cs_bill_customer_sk`.

## Slice 1 — deform to the consumer bound, not to the index keys

`deformIndexLeafBound` unioned every index key column into an index scan's
heap deform bound. PG deforms a fetched tuple only up to the highest
attribute its quals and target list read (`slot_getsomeattrs`). A btree index
scan never re-reads its key columns from the heap tuple: the descent reads
the index tuple, and every recheck is a Cond that is already folded into the
bound. The Q17 probe therefore deformed 18 of 34 columns for a Filter on
attribute 4.

- **Change.** A consumer-derived bound (≥ 0) is final. The key-column
  widening stays only where no consumer bound is recorded: None, or the
  count(*)-style Zero, which an index leaf reads as full width.
- **Safety.** In a throwaway build with the test-only deform poison armed,
  all 99 TPC-DS SF0.25 queries ran with no deform-bound violation.
- **Result** (isolated probe loop, 20000 nested-loop probes of
  `catalog_sales_pkey` on a private SF0.25 clone):

  | | HEAD | candidate | PG 18.3 |
  |---|---|---|---|
  | probe loop | 628–708 ms | 501–596 ms | 392 ms |
  | `decodeRowRangeInfo` share of profile | 21.2% | 6.4% | — |
  | Q17, warm | 0.75 s | 0.68 s | — |

  Values are identical (sf025, the acceptance arm), and the 105 strict
  isolation tests pass.

## Remaining profile (after slice 1)

`analysis/m0146/m0146-0171/perf-probe-after.txt`:

| function | share | what it does |
|---|---|---|
| `parseHeapTupleAlias` | 9.7% | heap tuple header parse |
| `deformPGIndexTupleInto` | 7.5% | index tuple deform |
| `evalExprSlot` | 6.5% | expression evaluation |
| `comparePGIndexTuples` | 6.5% | index key comparison |
| `decodeRowRangeInfo` | 6.4% | heap tuple deform |
| `PagePruneXIDSet` | 6.2% | the opportunistic-prune check on every heap page fetch |
| `Pool.Pin` / `bufmap.Lookup` | 3.1% / 2.1% | buffer pin and lookup |

These are the next slices' candidates. PG's `heap_page_prune_opt` returns
early on a cheap `PageIsFull` / `prune_xid` test.

## Close-out (2026-10-13)

- **Re-measure on HEAD** (same private SF0.25 clone):

  | query | HEAD now | x2 shape when filed | PG shape when filed |
  |---|---|---|---|
  | Q17 | 0.55–0.90 s | 0.80 s | 1.01 s |
  | Q25 | 0.35–0.73 s | 0.78 s | 0.98 s |
  | Q83 | 1.72 s | 2.01 s | 2.41 s |

- **Slice 2, measured and not landed.** An incremental attribute compare in
  `comparePGIndexTuples` (`_bt_compare`'s early exit) measured flat,
  540–565 ms in both arms. An equality probe's leaf compares tie on the
  single search column, so no attribute is skipped.
- **What is left.**
  - The inclusive profile is spread out: join-filter evaluation 16%,
    HOT-chain follow 15%, leaf compares 14%, heap header parse 11% (cache
    misses on first touch).
  - Buffer touches per probe match PG (about 30 vs 28.6).
  - No single structural waste remains. The constant-factor gap is M0146-0088's
    ledgered residual.
