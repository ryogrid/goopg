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

## Slice 105 — C25: the bpchar blank trim drops whole words of blanks (Q67 −4.0 % instructions)

Loop #203 re-profiled Q67 after slice 104, this time sampling `instructions:u` (the bar's metric) instead of
cycles. The cycle profile ranks the sort comparator first: `lessKeyVals` is 12.9 %, but most of its cycles are
cache misses on each row's key slice, which do not count as instructions. By instructions:

- the final Sort is 14.7 %, the worker Sort 7.5 %;
- `lessKeyVals` is 9.3 % and `cmpbody` 6.7 %;
- `strings.TrimRight` is 6.5 %.

**Symptom.** bpchar comparison, hashing and grouping all work on the value without its trailing blanks (PG
`bcTruelen`, varchar.c). goopg spells that as `strings.TrimRight(s, " ")`, which for a one-byte cutset is a byte
loop. Q67's grouping keys are char(50) columns (i_category, i_class, i_brand, i_product_name) holding ~10 bytes of
text and ~40 blanks. They are trimmed once per sort-key value (`trimStringDatum` from `evalSortKeyValueSlot`) and
once per grouping set per column in the aggregate's group key (`appendGroupColKey`, nine sets).

**Change.** A new helper, `bcTrimRight` (`bctrim.go`):

- it drops whole 8-byte words of blanks first, then finishes byte by byte;
- the 8-byte test against the constant `"        "` compiles to one load and one compare;
- it returns exactly what `strings.TrimRight(s, " ")` returns.

All 19 blank-trim sites in `internal/executor` now call it, so the sibling paths (codec, scalar compare, IN-list
hash, DISTINCT, aggregate key) cannot disagree.

**Measured** (`tools/ds-instr.sh`, 5 runs × 2, a fresh A/B against `8432307f1`): Q67 21.471 / 21.485 →
20.618 / 20.618 Ginstr/q, **−4.0 %**, which meets the 3 % bar (predicted ~5 %). Q67's output is byte-identical.

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 with plans 99/99 same; fire-set PASS (no fires on
either corpus); RegressSuite per-case counts equal to the slice-104 run except the plpgsql flapper, with `strings`,
`groupingsets`, `aggregates` and `join` byte-identical. The regress suite was run because `codec.go` is touched
(hard-won rule 5).

Test: `TestBcTrimRightMatchesTrimRight` covers padding lengths 0–41 around the stride, and bodies with tabs, NUL,
multi-byte UTF-8 and leading blanks next to the padding.

**Left on Q67.** The aggregate still trims each bpchar column once per grouping set; trimming once per row would
need `allVals` pre-trimmed per column. The final Sort's comparator copies 48-byte Datums per key, and each row's key
slice is a separate allocation reached through `perm`. PG keeps the first key inline in the SortTuple array
(`datum1`, tuplesort.c), and that is the cycle-side lever.

## Slice 106 — C26: the sort comparator reads key Datums in place (Q67 −7.6 % instructions)

Loop #204 took an instructions:u profile of Q67 at `08a69974e`:

- the final Sort (255 k rollup rows by `i_category, sumsales DESC`) is 17.9 %, the worker Sort 8.9 %;
- `sortOp.lessKeyVals` alone is 11.4 % self, and `cmpbody` 7.6 %.

**Symptom.** `lessKeyVals` copied both 48-byte Datums of every key into locals (`av, bv := a[i], b[i]`). It then
tried `fastCompareDatum`, which declines every string pair. For a character-string key it then called the
value-receiver `StringValue`, which copies each Datum again before `strings.Compare`. Each key comparison moved
about 200 bytes before comparing anything.

**PG.** tuplesort compares with `ApplySortComparator` over the SortTuple's datums, using the comparator that
SortSupport resolved for the key once (sortsupport.h; `varstrfastcmp_c` for text in the C collation). Nothing is
copied per comparison.

**Change** (`operators.go`, `datum.go`):

- `lessKeyVals` takes pointers into the two key slices and tests NULLs on the Kind byte.
- A pair of strings under a character-string key goes straight to `strings.Compare` over `datumStringInPlace`. That
  helper aliases Buf-backed bytes without copying the Datum; arena-backed values still go through `StringValue`.
- Every other pair takes `fastCompareDatum`, then `compareDatumTyped`, as before.
- The order of decisions is unchanged, because `fastCompareDatum` never decided a string pair.

**Measured** (`tools/ds-instr.sh`, 5 runs × 2, a fresh A/B against `08a69974e`): Q67 20.647 / 20.616 →
19.071 / 19.065 Ginstr/q, **−7.6 %**, which meets the 3 % bar (predicted ~5 %). Q67's output is byte-identical.
Q38 (INTERSECT over sorts) drops from 11.648 to 11.333 Ginstr/q.

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 with plans 99/99 same; fire-set PASS (no fires);
RegressSuite per-case counts equal to the slice-105 run except the plpgsql flapper. Eleven actual files differ
byte-wise between runs:

- nine are temp-file names in psql error lines;
- `foreign_key` reorders `\d` lines and differs between every pair of earlier runs too;
- `triggers` flips between two states seen on unchanged code (c26 is byte-identical to c21).

Reviewing that flap found P0-E14 (S2, filed with an escalation): SET ROLE is not undone by ROLLBACK, and an
INITIALLY DEFERRED constraint trigger fires at the statement instead of at COMMIT.

Test: `TestLessKeyValsInPlaceMatchesCopy` pins the in-place comparator to the copying one over random text, int
and numeric keys with NULLs, under every Desc / NullsFirst combination.

**Left on Q67.**

- The sort still reaches each row's keys through `perm` → `keyvals[p]` → a per-row slice, so the comparator's
  cycles are mostly cache misses. PG keeps the first key inline in the SortTuple array (`datum1`).
- Sort-key evaluation (8 %) re-trims the rollup's untrimmed char(n) group values.
- The aggregate drain's per-set hash lookups (C24).

## After slice 106 — C24 measured and dropped (2026-10-27)

Loop #205 first took instructions:u profiles of DS Q14 and Q23, the two largest absolute excesses:

- B-tree descents (`leafScanStart`, `findChildBlockDirect`, key compares) are ~20 % of each, the ground C18
  already covered;
- the rest is heap decode and buffer lookups;
- prune-on-access already runs once per heap block, as in `heapam_index_fetch_tuple`.

No new mechanism reached 3 %.

**C24.** Q67's aggregate drain is 30.6 % of its instructions. The per-row, per-set `groups[string(gkBuf)]` lookup
(operators_join_agg.go:2876) is 4.0 %, and the emit loop's `groups[key]` re-lookup another 1.5 %. The attempt
compared each row's key for a set with the current group's key and went to the map only on a change, after PG's
`agg_retrieve_direct`, which compares the row with the current group's first tuple (`execTuplesMatch`).

**Measured.** Q67 went from 19.066 / 19.055 to 18.990 / 18.978 Ginstr/q, **−0.4 %**. The detail sets have
nearly one group per input row (255 275 groups from 133 k rows), so almost every row changes group in them and
still reaches the map. The change was dropped and added to the Below-bar list. The miss counter is 1.

**The structural lever (ledgered).** A sorted rollup could keep no hash table at all: every key change founds a
group, as in PG, and the output is emitted from a group list. That would also remove the map insert and the emit
re-lookup, about 7 % together. It would, however, drop the reopen check that is goopg's only guard against a sort
key and a group key that disagree on equality (bpchar trimming, numeric scale, collation). That agreement has to
be proven first.

## Slice 107 — C27: a grouping-sets aggregate evaluates each argument once per row (Q67 −4.5 % instructions)

Loop #206 reused census z (`acd4ff2d7` touched no code). The miss counter was 1.

**Profiling note.** Sampled on `instructions:u`, `trimStringDatum`'s 4 % sits almost entirely on the instruction
after its 8-byte load. That is sampling skid, so per-symbol instruction shares still lean towards stalls. This loop
therefore looked for repeated work instead.

**Symptom.** The hash drain routes each input row into every grouping set. For each set it runs every aggregate's
`applyAgg`, and `applyAgg` evaluates the aggregate's argument (`aggEvalList(o.aggArgNodes, …)`). Q67's
`sum(coalesce(ss_sales_price*ss_quantity, 0))` was therefore computed nine times per input row.

**PG.** `ExecBuildAggTrans` (execExpr.c:3934–3943) evaluates a transition's arguments once and then calls the
transition function "once for each concurrently evaluated grouping set".

**Change** (`operators_join_agg.go`):

- A multi-set aggregate arms a per-row argument cache: `aggArgShare`, plus `aggArgRow` advanced once per input row
  in the drain, and `aggArgSeen` / `aggArgVal` per aggregate.
- `applyAgg` reuses a value already evaluated for the current row.
- Only inline scalars are kept (`Buf == nil && ArenaID == 0`). A value whose bytes live in `Buf` or an arena may
  sit in scratch that the next aggregate's evaluation reuses, so it is evaluated again per set, as before.
- Single-set aggregates and the sorted single-set paths (`openSorted`, `openSortedPartialEmit`) never arm the
  cache: they return before the sets are built, and they require `GroupingSets == nil`.
- `aggArgShareDisabled` is a test knob.

A volatile argument (`random()`) now gives one value per row across the sets, as in PG; it used to give one per
set.

**Measured** (`tools/ds-instr.sh`, 5 runs × 2, a fresh A/B against `acd4ff2d7`): Q67 19.065 / 19.081 →
18.221 / 18.221 Ginstr/q, **−4.5 %**, which meets the 3 % bar (predicted 4–5 %).

**Values.** All 11 grouping-set TPC-DS queries (Q5, 14, 18, 22, 27, 36, 67, 70, 77, 80, 86) give byte-identical
output on the two binaries.

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 with plans 99/99 same; fire-set PASS (no fires);
RegressSuite counts equal to the slice-106 run except the plpgsql flapper, with `groupingsets`, `aggregates` and
`window` byte-identical.

Test: `TestAggArgSharedAcrossGroupingSets` compares shared and per-set evaluation:

- ROLLUP, GROUPING SETS and CUBE;
- inline (int, numeric, float, CASE, coalesce) and non-inline (text) arguments, plus FILTER;
- under both the hash and the sorted strategies.

A temporary probe counted 11 588 cache hits in it.

**Left (ledgered).**

- FILTER clauses are still evaluated once per set; PG evaluates them once per transition too.
- Text and other non-inline arguments are still evaluated once per set.

## Slice 108 — C28: B-tree binary-search probes compare the tuple in place (Q72 −6.9 % instructions)

Loop #207. Census aa (`83bfa2945`) is exhausted. A G3 by instructions:u at HEAD found:

- Q23's map work in `aggregateOp.Open` is 5.2 %: the drain lookup 2.3 % and the emit loop's `groups[key]`
  re-lookup 1.8 %. That stays at or below 3 % even with PG-style table pre-sizing, because the plan carries no
  numGroups estimate.
- Q72 is ~70 % B-tree probing.

A release-PG instruction baseline for Q72 needed the private `bench/pg-release` cluster started; the permission
classifier denied that, and it was not retried.

**Symptom.** Both binary-search sites send every probed tuple through `parseNoCopy` → `parseAliasing` →
`parseItemBody`:

- `findChildBlockDirect` (internal pages);
- `leafScanStart` (leaf pages, non-posting tuples).

`parseItemBody` validates the tuple, extracts the downlink TID and the body slice, and builds an `item`. In the
tuple format, however, `parseAliasing` returns `key: raw`: the compared key is the probed bytes themselves.
Under `findChildBlockDirect` that parse layer was ~3.3 % of Q72 (`parseItemBody` 2.3 %, `parseAliasing` 1.0 %),
before counting its inlined part.

**PG.** `_bt_binsrch` hands `_bt_compare` the line pointer's tuple in place (nbtsearch.c).

**Change** (`btree.go`):

- `probeTupleOK` holds exactly `parseItemBody`'s rejection conditions: too short, a `t_info` size that disagrees
  with the length, a posting tuple, a heap-TID pivot too short for its TID.
- In the tuple format both probes compare `raw` directly after that check.
- The blob format keeps `parseNoCopy`, because its key is the body after the header.
- The final downlink of `findChildBlockDirect` is still parsed and validated as before.

**Measured** (`tools/ds-instr.sh`, 5 runs × 2, a fresh A/B against `83bfa2945`): Q72 26.166 / 26.168 →
24.351 / 24.352 Ginstr/q, **−6.9 %**, which meets the 3 % bar (predicted ~3 %; the leaf site and the inlined
parse were not in the estimate). The outputs of Q72, Q14 and Q23 are byte-identical.

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 (total −3.0 %) with plans 99/99 same; fire-set PASS
(no fires); RegressSuite counts equal to the slice-107 run except the plpgsql flapper, with `create_index` and
`indexing` byte-identical.

Test: `TestProbeTupleOKMatchesParseItemBody` runs 200 000 random tuples biased to reach every branch (short,
size mismatch, plain, posting, pivot with and without room for its TID). `probeTupleOK` must accept exactly what
`parseItemBody` accepts.

**Left (ledgered).** The leaf probe of a POSTING tuple calls `parsePostingRaw`. That allocates the TID array and
a restamped key copy on every probe, only to compare and discard them. PG's `_bt_compare` reads a posting tuple's
key attributes in place, with the first heap TID as the tiebreak.

## Slice 109 — C29: a hash aggregate emits its groups directly, without re-keying (TPC-H Q18 −5.7 % instructions)

Loop #208 reused census aa (`e35fff402` touched only nbtree). After C28, Q14 is flat: nbtree 34 % and index-scan
heap fetch 31 %, spread over small symbols. TPC-H Q18 (G2 #6) was profiled by instructions:u for the first time.

**Symptom.** Q18's hash aggregate on `l_orderkey` (~1.5 M groups) runs in three steps:

- It files groups in the int64 lane (`igroups[ik]`, 10.3 %, the lookup PG pays too).
- At the end of every drain, `demoteIntGroups` moves all of them into the string map: an `igroups` re-lookup
  (2.9 %), a `canonicalNumericKey` string allocation per group, and string-map inserts and rehashing.
- The emit loop then walks `order` (key strings) and looks every group up again with `groups[key]` (4.0 %).

Both of the last two steps exist only to give the emit loop a key per group.

**PG.** `agg_retrieve_hash_table` iterates the hash table's entries (`ScanTupleHashTable`). Nothing is re-keyed or
looked up again.

**Change** (`operators_join_agg.go`):

- `founded` lists every group in founding order: the pre-founded empty-set groups, int-lane groups and
  string-path groups. That is the order `order` lists their keys in, because int-lane groups are always founded
  before any string-path group (demotion happens at the first non-int key, or at the end).
- In Simple mode the emit loop walks `founded`, and the end-of-drain demotion is skipped.
- Partial, Final and row-transport aggregates publish or rebuild by key and keep `order`.
- `aggEmitFoundedDisabled` is a test knob.

**Measured** (`tools/h-instr.sh`, 5 runs × 2, a fresh A/B against `e35fff402`): Q18 44.813 / 44.962 →
42.328 / 42.304 Ginstr/q, **−5.7 %**, which meets the 3 % bar (predicted ~7 %). TPC-H acceptance: Q18 MATCH and
24 MATCH overall.

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 (total −3.1 %) with plans 99/99 same; fire-set PASS
(no fires); RegressSuite counts equal to the slice-108 run except the plpgsql flapper, with `groupingsets`,
`aggregates` and `join` byte-identical.

Test: `TestAggEmitFoundedMatchesKeyedEmit` compares the founded emit with the keyed emit over:

- the pure int64 lane;
- a lane demoted mid-drain by a fraction and by a NULL;
- string and multi-column keys;
- grouping sets;
- an ungrouped aggregate;
- empty input.

A temporary probe confirmed both the founded emit and an undemoted lane were reached.

**Left (ledgered).** PG sizes the hash table from the planner's group estimate (`hash_choose_num_buckets`), but
goopg's maps start empty and rehash as they grow (1.5 % of Q18). The plan node carries no numGroups.

## Slice 110 — C30: the ordered-merge comparator gets the sort comparator's arms (Q47 −5.1 % instructions)

Loop #210 reused census ab (`2910f8451` is analysis-only). G3 by instructions:u on the census-20261027b
candidates:

- Q39: `evalFastExpr` 9.7 %, split between `filterOp.nextPerRow` (8.4 %) and hash-probe key evaluation
  (`evalHashKeyDatumSlot` 7.7 %). The latter is a column reference through `evalFastExpr` plus the first touch of
  the probe row, with no lever.
- Q78: flat.
- Q47: Q67's shape, dominated by string sort and merge comparisons.

**Symptom.** `mergeKeysLess` is the comparator that Gather Merge's heap and the ordered-merge setOp share. It must
agree with `sortOp.lessKeyVals`, because the merges order streams that those Sorts produced. C26 (slice 106) gave
`lessKeyVals` in-place access and a character-string arm; the merge comparator kept the old form. That form copies
both Datums per key, tries `fastCompareDatum` (which declines every string pair), and then calls
`compareDatumTyped` → `textShapeAmbiguous` / `compareDatum`. That was 4.6 % of Q47.

**PG.** The Gather Merge heap compares with the key's SortSupport comparator (`heap_compare_slots` →
`ApplySortComparator`), the same one the Sorts below use.

**Change:**

- `mergeKeysLess(keys, keyText, a, b, errp)` reads through pointers and sends a pair of strings under a
  character-string key straight to `strings.Compare` (`datumStringInPlace`). Every other pair keeps
  `fastCompareDatum`, then `compareDatumTyped`.
- The flags come from `sortKeyTextFlags`, computed once per merge: `gatherMergeOp.keyText` at build time, and
  `setOp.mergeTextFlags()` on first use.
- The arm is exact for the same reason as in C26 (slice 60): under a character-string key, `compareDatumTyped`
  of two strings is `compareText`, which is `strings.Compare`.

**Measured** (`tools/ds-instr.sh`, 5 runs × 2, a fresh A/B against `2910f8451`): Q47 16.993 / 16.985 →
16.130 / 16.127 Ginstr/q, **−5.1 %**, which meets the 3 % bar (predicted 2.5–3 %). The outputs of Q47, Q67, Q14
and Q57 are byte-identical. The sweep's wall-time movers Q14 (+3 s) and Q74 (+2.5 s) run slightly fewer
instructions with the change (Q14 129.279 → 128.860, Q74 134.579 → 134.488 G), so they were host noise.

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 with plans 99/99 same; fire-set PASS (no fires);
RegressSuite counts equal to the slice-109 run except the plpgsql flapper, with `window` and `join`
byte-identical.

Test: `TestMergeKeysLessInPlaceMatchesCopy` pins the merge comparator to its old copying form over text, bpchar,
int and numeric keys with NULLs, under every Desc / NullsFirst combination. The text values include
shape-ambiguous ones (array, row, pg_lsn and uuid look-alikes), where `compareDatumTyped` takes its shape rule.

## Slice 111 — C31: integer variance and stddev finalised in fixed width (Q39 −11.2 % instructions)

Loop #211. Census ac (`b996b60d9`) is exhausted. G3 on DS Q39 (G2 #7) found big-number numeric work at 13.3 %
of its instructions:

- `exactIntVariance` 8.2 %: the per-group `stddev_samp` finalisation, with `nat.sqrt` 3.7 %, the `big.Int`
  allocations, and `intSums` 1.0 %;
- the filter's `stdev/mean` division 4.8 % (`numericDiv` already has a uint128 path).

**Symptom.** For int2/int4 inputs, goopg accumulates Σx and Σx² in int128 lanes, as PG's `Int128AggState` does.
At finalisation it converted both to `big.Int` (`intSums`) and computed PG's `numeric_stddev_internal` entirely
in `big.Int`: the numerator N·Σx² − (Σx)², `select_div_scale`, the rounded division, and `sqrt_var` through
`big.Int.Sqrt` (Newton from a bit-length guess). That meant several fresh `big.Int`s per group, for ~90 000
groups per statement. For Q39's quantities (0–1000, tens of rows per group) every intermediate fits fixed width:

- the numerator and denominator fit 64 bits;
- the variance mantissa at its rscale (≈ 12) fits below 2^63;
- `sqrt_var`'s argument q·10^(8f−rscale) (≈ 10^37) fits below 2^128.

**Change** (`variance_int128_fast.go`):

- `aggRuntime.intVariance` serves all four integer-variance finalisers: var_pop, var_samp, stddev_pop and
  stddev_samp.
- When the state lives wholly in the int128 lane, `intVarianceFixed` runs `exactIntVariance` step for step in
  fixed width:
  - the zero/negative-numerator result and the N ≤ 1 NULL;
  - `select_div_scale` from the NBASE weights and leading digits (`nbaseWeightFirstUint64`);
  - `div_var`'s half-away-from-zero rounding via `bits.Div64`;
  - for stddev, `pgNBaseWeightFirst(q, rscale)`, `sqrt_var`'s resWeight / resNdigits / f, the 128-bit argument
    (`mulPow10To128`), the integer square root and `round_var`.
- `isqrt128` seeds from float64, refines with Newton steps on 128/64-bit division, and corrects to the exact
  floor.
- Any bound it cannot meet returns ok=false, and `exactIntVariance` runs unchanged.
- `intVarianceDisabled` is a test knob.

**Measured** (`tools/ds-instr.sh`, 5 runs × 2, a fresh A/B against `b996b60d9`): Q39 21.717 / 21.752 →
19.308 / 19.304 Ginstr/q, **−11.2 %**, which meets the 3 % bar (predicted ~6 %). The outputs of every TPC-DS
variance query (Q17, Q35, Q39) are byte-identical. The sweep's flagged Q4 2.0× is host noise: equal instructions
on both binaries (42.172 / 42.176 G).

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 with plans 99/99 same; fire-set PASS (no fires);
RegressSuite counts equal to the slice-110 run except the plpgsql flapper, with `aggregates`, `window`,
`numeric` and `groupingsets` byte-identical.

Tests:

- `TestIntVarianceMatchesBig` runs ~3 000 groups through the fixed-width path and compares each with
  `exactIntVariance`'s output and scale: Q39-like quantities, small mixed-sign values, constant groups, int32
  extremes, one- and two-row groups, and a 20 000-row group of values up to 10^6.
- `TestIsqrt128` checks perfect squares, their neighbours and random 128-bit values.

## Slice 112 — C35: a run of skipped columns is walked in one tight loop (TPC-H Q17 −9.5 % instructions)

Loop #219. The S9 census `routing-20261028b` (at `39041efc5`) is exhausted. G3 on TPC-H Q17 (G2 #3) at HEAD,
with instructions:u:

| symbol | self |
|---|---|
| `decodeRowRangeInfo` | 15.3 % |
| `decodeScanRowRangeInfo` | 4.9 % |
| `skipAttrLen` | 3.7 % |

The lineitem probe scan's first pass (C32) walks about seven skipped columns per row to reach `l_partkey`, because
HammerDB puts `l_partkey` eighth.

**Symptom.** Each skipped column went through the full per-column loop:

- the `cols[i]` address, the stored-natts and null-bitmap tests;
- the `info[i]` loads (the lowered type-name string among them);
- `catalog.AttAlignPointer`;
- a non-inlined `skipAttrLen` call;
- a 48-byte `NullDatum` copy loaded from the global;
- the poison-flag test.

PG's step for an attribute it walks past (`slot_deform_heap_tuple`, execTuples.c) is `att_align_pointer` plus
`att_addlength_pointer`, with a by-reference value left as a pointer.

**Change** (`coltypeinfo.go`, `codec.go`):

- `resolveColTypeInfo` resolves `skipLen` per column, `skipAttrLen`'s rule decided once: the fixed width, −1 for a
  varlena, or 0 when the column cannot be walked.
- On reaching a skipped column, `decodeRowRangeInfo` calls `skipColumnRun`. It walks every consecutive skipped
  column before the window end and the stored natts in one tight loop:
  - a NULL column does not advance the offset;
  - a fixed-width column aligns and adds its width;
  - a varlena reproduces `AttAlignPointer` (align only on a zero pad byte) and `physicalPGVarlenaSize` (1-byte
    header, 4-byte header) inline.
- The run's cells are then cleared at once with `clear`, since `NullDatum` is the zero Datum. Under the debug
  deform poison they are poisoned instead.
- The run stops at a column that is not skipped, has no skip length, or does not measure: a TOAST pointer, or a
  truncated or exhausted tuple. That column then takes the general loop exactly as before.
- `skipColumnRunDisabled` is the test knob.

This is neither of slice 72's mechanisms on the Below-bar list. That slice tried cached attribute offsets and
conditional NULL stores per cell; this removes the per-column overhead of the walk itself.

**Measured** (`tools/h-instr.sh`, 3 runs × 2, a fresh A/B against `39041efc5`):

| query | before | after | change |
|---|---|---|---|
| TPC-H Q17 (Target) | 24.973 / 24.967 | 22.604 / 22.605 | **−9.5 %** |
| TPC-DS Q78 | 17.393 | 17.162 | −1.3 % |
| TPC-DS Q7 | 6.185 | 6.175 | −0.2 % |

The prediction was ~4 %; the result meets the 3 % bar. The SF0.25 sweep's total time moved −4.7 %.

Gates:

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99, with plans 99/99 same;
- fire-set: PASS, no fires;
- RegressSuite: counts equal to the C34 run except the `plpgsql` flapper;
- isolation suite: PASS.

**Test.** `TestSkipColumnRunMatchesPerColumnSkip` builds 4 000 random tuples with `EncodeRowPG`:

- every alignment class;
- short and 4-byte-header varlenas;
- NULLs anywhere;
- tuples shorter than the descriptor.

It decodes each with a random skip mask over a random window, which may resume mid-row, once with the run and once
without. It requires identical cells and resume offsets, and NULL in every skipped cell. A mutation that drops
the fixed-width alignment fails it.
