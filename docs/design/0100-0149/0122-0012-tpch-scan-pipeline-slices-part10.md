# M0122-0012 — scan pipeline slices, part 10

Continues [part 9](0122-0012-tpch-scan-pipeline-slices-part9.md), split at slice 102 under the 800-line design-doc
rule (D3). The task, its return-first rule, Candidate pool and Below-bar list live in the M0122-0012 entry of
`.ralph/fix_plan.md`; the census these slices target is `analysis/m0122/m0122-0012/census-20261027/`.

## Slice 102 — C19: pg_checksum_block over in-place 32-lane rows (Q17 −13.2 % instructions)

Loop #198, 2026-10-27. The G3 profile of TPC-H Q17, G2 #4, puts `pageChecksumBlock` at 12.25 %. That is data
checksum verification on every buffer read; Q17 reads lineitem twice. Both goopg and release PG run with
checksums on.

Q14's index-scan string copies (`slicebytetostring`, ~3 %) were also noted but not taken: an arena would cross
the borrow and lifetime rules, the P0-E12 class.

**Change.** `pageChecksumBlockRows` replaces the per-word byte-assembling loop for whole, aligned pages on
little-endian hosts. The old form remains as the fallback and as the test oracle. Details are in
`0102-0019-initdb-data-checksums.md` §"The rows form of pg_checksum_block".

**Target (written before measuring).** TPC-H SF1 Q17, instructions:u per query (`tools/h-instr.sh`, 5 runs).

| run | before | after |
|---|---|---|
| 1 | 32.104 G | 27.848 G |
| 2 | 32.082 G | 27.862 G |

That is **−13.2 %**, which meets the bar.

Gates: storage `-race` (checkptr on the unsafe view); units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 with
plans 99/99 same; TPC-H census 22/22; RegressSuite = baseline (plpgsql flap); isolation PASS; checksum and
amcheck port tests PASS.

## Slice 103 — C21: the bitmap heap scan joins the scan borrow (Q17 −2.06 %, below the bar)

Loop #199, 2026-10-27. Profile of TPC-H Q17 after slice 102:

| share | site |
|---|---|
| 21.9 % | `decodeRowRangeInfo` |
| 9.35 % | `NumericInt64FromStoredPayload` |
| 8.1 % | `cloneRowOwned` |

**C20, not attempted.** Its line profile puts the goopg-only legacy-text probe mostly in call overhead and in the
payload's first touch, which the decode pays anyway. The prediction was about 1–2 %.

**C21.** All of Q17's `cloneRowOwned` comes from `bitmapHeapScanOp.emitRow`. Q17's SubPlan is Aggregate(avg) over
a Bitmap Heap Scan on lineitem, and the scan cloned every fetched row although `aggregateBorrowsScanRows` already
proves the aggregate keeps nothing. `markScanBorrowChain` handed the borrow only to seq scans. PG's bitmap heap
scan stores a buffer tuple in the slot (`ExecStoreBufferHeapTuple`).

**Change.**

- `markScanBorrowChain` sets `bitmapHeapScanOp.borrowRows`.
- `emitRow` then yields `scanRow` itself unless the plan emits extra ctid columns.

The contract is the seq scan's:

- the reused row is consumed before the next `Next()`;
- arena-backed values live until the per-page arena resets, which happens inside a later `Next()`.

**Measured.**

| run | before (slice 102) | after |
|---|---|---|
| 1 | 27.860 G | 27.299 G |
| 2 | 27.877 G | 27.289 G |

That is **−2.06 %**, which misses the 3 % bar. The profile share was mostly allocation and GC cycles, which
instructions:u does not count.

The change is landed as plan-neutral and recorded on the Below-bar list. The miss counter is 1.

Gates: units; spotcheck; acceptance 24 MATCH (Q17 MATCH); sf025 PASS=99 with plans 99/99 same; TPC-H census
22/22; RegressSuite = baseline (plpgsql flap); isolation PASS.

Test: `TestBitmapHeapScanBorrowMatchesClone`. Over a 6000-row multi-page table, planned with seq and index scans
off:

- plain and grouped aggregates (count/sum/avg plus text min/max) give identical results with the borrow on and
  off;
- the borrow is asserted armed.

## After slice 103 — C22 measured and dropped; one-task yield (2026-10-27)

**C22.** The aim was to give a seq scan at the bottom of a consumer-complete chain a read set, by adding a SeqScan
case to `buildCompleteChild` and teaching `consumerLiveColumns` lowered sublinks (`deformSublinkArgs`, the rule
`deformScanVisit` already applies). Q17's join filter `l_quantity < (SubPlan 1)` had made the consumer walk
decline.

**Measured.** Q17 instructions came out unchanged (27.30 G either way). A Build trace showed why: the chain is
Join → Project → Seq Scan lineitem, and the Project already narrows the scan's `readCols` to the three columns read
(l_extendedprice, l_quantity, l_partkey, slice 19). Q17's remaining decode cost is the three NEEDED NUMERICs. The
change was dropped.

**Yield.** C21 (−2.06 %, landed) and C22 (±0 %) are two consecutive misses, so the next loop works one item-10
task: M0122-0013, the standby `DB_IN_ARCHIVE_RECOVERY` residual. It then returns here with the counter reset.

**Q17 after slices 102–103.** What remains is the needed-column numeric decode, the checksum (now 6 %), reads
(syscall 6 %) and the correlated SubPlan's per-row bitmap probe. The TID-bitmap entry sizing (goopg 2048 bits per
page, PG `MaxHeapTuplesPerPage` = 291) is a ~2 % item for a later pass.

## Slice 104 — C23: a sorted rollup's output in close order, not by a comparator (Q67 −13.3 % instructions)

Return to M0122-0012 after the M0122-0013 yield (loop #202), miss counter 0. A fresh G3 profiled TPC-DS SF0.25
Q23, Q67 and Q39 (perf record, three runs each):

- Q23's cost is spread thin. decodeRowRangeInfo is 5.3 %, and the B-tree probes (~10 %) already had C18.
- Q39 is expression evaluation (evalFastExpr 8.7 %).
- Q67 shows one goopg-only mechanism.

**Symptom.** Q67 is a `GROUP BY ROLLUP` over eight columns: nine grouping sets and 255 275 groups from 133 k input
rows. Its plan is a `GroupAggregate` over a Gather Merge sorted along the rollup. goopg's aggregate files every
group in a hash table. Afterwards, `aggregateOp.Open` re-sorts all output rows with a column comparator to rebuild
the order PG emits (M0146-0020a). The comparator does `map[int]bool` set-membership lookups and
`compareDatumTyped` / `textShapeAmbiguous` per column. It was `aggregateOp.Open.func4`, 9.5 % of Q67's samples.

**PG.** `agg_retrieve_direct` (nodeAgg.c) makes one pass over the sorted input. When the next row changes a
grouping column, every group whose set includes that column is finished and returned, the most detailed set first.
The empty set's group is returned at end of input. PG's output order is therefore the order in which groups
close, and no comparison of finished groups takes place.

**Change** (`operators_join_agg.go`):

- During the drain, a sorted single-rollup aggregate keeps the current group of each set (`curGroup`). A row that
  feeds a different group stamps the previous one's `closeSeq` with the row number.
- `rollupCloseOrder` orders the output by (close row, set length descending). Groups still open at the end close
  after the last row. Only two integers per pair are compared.
- The new path is taken when the strategy is Sorted, the mode is Simple, there is one rollup and no hashed
  rollups.
- If a group that has already closed is fed again, the input was not grouped along the rollup. The aggregate then
  falls back to the comparator, so no input can make the new order differ from the comparator's rule.
- `rollupCloseOrderDisabled` is a test knob.

**Why the orders agree.** Within one set, groups close in input order, and input sorted along the rollup is key
order. Groups that close on the same row all contain the previous row, so they share its prefix; the comparator
puts the longer set first (`aIn && !bIn`). The grand total has no columns and closes last.

**Measured** (`tools/ds-instr.sh`, 5 runs × 2, tmp/q64-clone): Q67 24.770 / 24.773 → 21.477 / 21.471 Ginstr/q,
**−13.3 %**, which meets the 3 % bar (predicted 7–9 %; the comparator's map lookups and the cache misses of the
row indirection were partly attributed elsewhere). Sweep Q67 1819 ms against the census median of 2226 ms. Q67's
output is byte-identical. The sweep's other movers (Q8, Q56, Q7) have identical instruction counts on both
binaries, so they were host noise.

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 with plans 99/99 same; fire-set PASS (no fires, SF1
parity 99/99); RegressSuite per-case counts equal to the baseline except the plpgsql flapper, with `groupingsets`,
`aggregates` and `window` outputs byte-identical.

Tests:

- `TestRollupCloseOrderMatchesComparator` runs six rollup and grouping-set queries with hash aggregation off,
  once on each path. The data has NULL keys, char(n) padding, duplicate rows, numeric keys of differing scale and
  an empty input. A temporary probe confirmed the close-order path fired on the knob-on runs.
- `TestRollupCloseOrder` pins the ordering rule on a hand-built input.

**Left.** The drain still hashes every input row into all nine sets' hash tables (appendGroupKey with trimmed
char(n) keys, aeshash, mapaccess2_faststr; about 8 % of Q67 together). PG's sorted path compares the row with the
current group instead (`agg_retrieve_direct`'s `execTuplesMatch` against the first tuple of the group). A
comparison against `curGroup[si]` would remove the hash lookup for the sorted case. That is the next candidate for
Q67. Q67's final Sort of the 255 k rollup rows (`sortOp.lessKeyVals` + `cmpbody`, ~20 %) is the other large
share.
