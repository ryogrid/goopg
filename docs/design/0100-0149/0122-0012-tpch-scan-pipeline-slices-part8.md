# M0122-0012 — scan pipeline slices, part 8 (TPC-DS executor gaps, continued)

Continued from [part 7](0122-0012-tpch-scan-pipeline-slices-part7.md) (slices 69–79), which was split at the
800-line limit.

**Measurement method.** All slices here are measured on the SF0.25 clone `tmp/q64-clone`, as in part 7, mostly by
instructions:u per query (`tmp/ds-instr.sh`).

**Reference timings.** Gaps are now ranked against a *release* (non-cassert) PG 18.3 build. Its per-query medians
are in `analysis/timing-after-planprity-match/data/tpcds-sf025-pgrel.tsv`. The `:65438` reference binary is a
cassert build, so ratios against it understate goopg's gap: Q64 is 9.9× against release PG but 6.6× against
cassert PG.

**Two-statement files.** Q14, Q23, Q24 and Q39 each hold two statements. An `explain` prefix covers only the first,
so measure each statement separately.

## Slice 80 — an index scan forms its probe key from resolved columns and re-aims one cursor per rescan

**Target.** Q14 is the largest absolute gap against release PG: 8.9 s against 4.8 s. Its statement 1 takes 5.3 s
(cassert PG 3.25 s) and statement 2 takes 4.8 s (cassert PG 4.4 s).

**Diagnosis.**

- **Same plans and rows.** Both statements are the same plan as PG's, with the same row counts at every node.
- **Parameterised probes.** The time is in parameterised nested loops: `store_sales_pkey`, `catalog_sales_pkey` and
  `web_sales_pkey` index scans rescanned 16 173 times each, then a `date_dim_pkey` probe per fetched row.
- **Profile.** `indexScanOp.Next` and its children are 51 % of the profile, `Rescan` 19 %.
- **The rescan.** About 62 % of `Rescan` is the descent (`NewScanCursor`) and about 35 % is `lookupKey`. Of
  `lookupKey`:
  - 85 % was `ctx.indexProbeKey`: `pgIndexKeyColumns` re-resolving every key column by name, three `make` calls,
    and the general `pgIndexTupleKey` builder;
  - the rest is a by-name `Catalog.LookupColumn` per rescan.
- **The leaf walk.** `nextLeafBatch` is 53 % of the index scan, mostly the leaf binary search (`leafScanStart`)
  through the generic comparator.

**PG.**

- `ExecIndexBuildScanKeys` builds a scan's ScanKey array once, at `ExecInitIndexScan`.
- `ExecIndexEvalRuntimeKeys` only stores each rescan's datums into it.
- `btrescan` reuses the scan's one `BTScanOpaque`.

**Change** (`internal/executor`):

- **`indexProbeForm`** (`index_probe_form.go`) is a per-operator cache:
  - `colAt` resolves key position i's column once.
  - `form` is `ctx.indexProbeKey`. The first probe it forms is kept as a template when its attributes are all
    fixed-width and it has no null bitmap (`keepTemplate`). From then on, a probe with the template's attribute
    count is the template copied, with each value's `encodeValuePG` image written at its fixed offset (`fill`).
  - A value whose image is not the template's width, a TOAST pointer, or a different attribute count falls back
    to `indexProbeKey`.
  - The bytes are the same because a fixed-width, null-free index tuple's header and layout do not depend on the
    values (the reasoning behind slice 56's `groupBoundsFixed`).
- **Both siblings.** `indexScanOp` and `indexOnlyScanOp` use it in `lookupKey` and `lookupKeys`.
- **One cursor.** `indexScanOp`'s equality and range rescan re-aims one cursor (`curSpare`,
  `BTree.ResetScanCursor` from slice 77) instead of allocating a cursor per rescan.

**Pins.**

- `TestIndexProbeFormMatchesIndexProbeKey` checks that the form's bytes equal `indexProbeKey`'s on every call:
  one- and two-attribute prefixes of an int index (templated), alternating attribute counts, and a prefix holding
  a varchar (never templated, `tmpl == nil` asserted).
- Writing every image at the first attribute's offset fails it.

**Measured** against `71a749a8e`:

| query | HEAD | candidate |
|---|---|---|
| Q14 instructions | 151.33 G | 143.95 G (−4.9 %) |
| Q14 allocation per run | 5.15 GB, 58.4 M objects | 4.47 GB (−13 %), 46.0 M (−21 %) |
| Q17 | 2.804 G | 2.677 G (−4.5 %) |
| Q25 | 2.800 G | 2.719 G (−2.9 %) |
| Q72, Q54 | — | within ±0.7 % |

Q14's wall time is noisy (8–12 s per run): the run still allocates 4.5 GB.

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Left.**

- **The leaf walk.** `leafScanStart`'s binary search through the generic comparator (`compare` →
  `comparePGIndexTuples`) is about 12 % of Q14.
- **The descent.** It is per rescan, as in PG, but costs more per level.
- **Allocation.** Q14 still allocates 4.5 GB and 46 M objects per run.

## Slice 81 — a re-opened inner index scan keeps its batch, and a set-op drain forms one key per distinct row

**Target.** Q14's allocation: 4.46 GB and 46.0 M objects per run after slice 80. I attributed allocation samples
(the first non-runtime frame above `mallocgc` / `growslice`) in a 20 s profile. The top sources:

1. `indexScanOp.scanAppendEntry`, 80 % of it `growslice`.
2. `decodePhysicalPGValueLowered`: string columns decoded with no arena, one Go string each.
3. `drainSetOpInput`: `appendRowKeyTrimmed`'s `strconv` growth, one key string per row.

**Diagnosis.**

- **Batch regrowth.** The lateral join stream re-opens its right subtree for every outer row (`stepOuter` →
  `right.Open`, `finishOuter` → `Close`). `indexScanOp.Open` and `Close` both set the TID batches `tids` / `poss` to
  nil, so every probe regrew both from zero, about 7 doublings for Q14's 42-row probes. PG's `ExecReScan` keeps the
  scan state.
- **Wasted keys.** `computeBuffered` drains both set-op inputs with `drainSetOpInput`, which builds a key string for
  every row. The left input's counts are discarded (`leftRows, _, err`). The right input's 190 k rows have 3.8 k
  distinct keys.

**Change** (`internal/executor`):

- **Keep the batch.** `indexScanOp.Open` / `Close` truncate `tids` / `poss` to length 0 instead of setting them to
  nil.
- **One key per distinct row.** `drainSetOpInput` takes `wantCounts`, so the left input forms no keys. The right
  input forms each key in one reused buffer, looks it up without a conversion in a key → slot index, and increments
  the slot. A key string is allocated once per distinct key, and the map it returns is the same.
- **Key format unchanged.** The integer key stays decimal text: `appendRowKeyTrimmed` canonicalises an int and a
  numeric spelling of one value to one key (`1` ≡ `1.00`).

**Pins.**

- `TestDrainSetOpInputCountsMatchRowKeys`: the counts equal the per-row `rowKeyTrimmed` counts on a fixture whose
  7 rows collapse to 4 keys through int/numeric, bpchar-trim and NULL canonicalisation. A drain without counts
  returns every row and no map.
- Not incrementing a duplicate fails it.

**Measured** against `afdfe9869`:

| query | HEAD | candidate |
|---|---|---|
| Q14 objects allocated per run | 45.96 M | 32.57 M (−29 %) |
| Q14 bytes allocated per run | 4.46 GB | 4.20 GB (−6 %) |
| Q14 instructions | 143.99 G | 139.86 G (−2.9 %) |
| Q38, Q87 | 12.23, 12.46 G | 12.10, 12.33 G (−1.1 %) |
| Q8 | — | flat |

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Left** (ledger row).

- **Index scans decode dead string columns.** An index scan decodes every column up to its deform bound with no
  arena, so `item_pkey`'s probes copy `i_item_id`, `i_item_desc`, `i_brand` and `i_class` into new strings per row.
  The seq scan walks past dead columns (`colTypeInfo.skip`, `readCols`), but the read set reaches only a scan
  directly under a Project. An index scan on a nested loop's inner side has no such parent; it needs a join-aware
  read set.
- **Bytes per run.** The 4.2 GB per run is dominated by these decoded rows and the sort and set-op row copies.

## Slice 82 — the lateral stream copies the outer row's cells, and an index-only scan decodes into reused scratch

**Target selection.** A fresh warm census at `e9c61e18b` (all 99 SF0.25 queries, second pass, on one server)
against release PG 18.3 put the total at 67.6 s against 85.8 s.

- **Q47 was an artefact.** It read 4.8 s in that pass, against 1.2 s before. Run alone it is 2.5 s while the
  server warms and 1.2 s once warm, and its instructions did not change (19.6 → 19.3 G).
- **Allocation drives the wall times.** Q23 allocates 3.9 GB and 21.5 M objects per run, Q78 3.1 GB, Q47 2.0 GB.
- **The target.** Q23 runs 2.6 s against release PG's 1.3 s. Its statements take 1.7 s and 1.3 s, against 0.93 s
  and 0.89 s on cassert PG. Both are dominated by parallel nested loops probing `item_pkey` (index scan) and
  `customer_pkey` (index-only scan), 414 k times each.

**Allocation attribution** (first non-runtime frame above the allocator, 15 s profile of statement 1):

- **Dead string columns.** The index scan's string decode is the slice-81 ledger row.
- **A throwaway outer row.** `VirtualSlot.Row` in `lateralJoinStream.stepOuter`: `slotRow` materialised the
  join's VirtualSlot into a fresh row for every outer row, only to copy it again into the stream's reused
  `outerRow`.
- **Index-only scan decode.** `pgIndexTupleKeyDatums` in `indexOnlyScanOp.decodeRowFromKey` (about 1 470
  samples) allocated, per matched entry:
  - `pgIndexKeyColumns` and a `names` slice;
  - `desc.Physical()`'s copy;
  - `DeformPGIndexTuple`'s value and null slices;
  - the `[]Datum` result.

  PG's `index_deform_tuple` writes into the scan's slot.

**Change.**

- **The outer row.** `lateralJoinStream.stepOuter` fills `outerRow` straight from the slot's cells
  (`appendSlotCells`) and detaches arena-backed cells in place. The stream already reused that buffer when
  nothing was arena-backed.
- **The deform** (`nbtree.DeformPGIndexKeyInto`): `DeformPGIndexTuple` over `desc.Attrs`, into caller-owned
  slices.
- **The decode** (`pgIndexTupleKeyDatumsInto`): `pgIndexTupleKeyDatums` with caller-owned scratch and output.
  The allocating form is now that function over fresh slices, so the checks and the per-attribute decode cannot
  drift apart.
- **The index-only scan** resolves its key columns, names and scratch once per descriptor (`keyDesc` … `keyVals`)
  and decodes every entry through them. `projectKeyVals` still copies each entry into its own row, because the
  eager rescan keeps every row.

**Pins.**

- `TestKeyDatumsIntoMatchesAllocatingDecode`: entries with int and varchar attributes and NULLs, decoded through
  reused scratch, must equal the allocating decode. Leaving a NULL attribute unwritten fails it, because the
  previous entry's value survives.
- `TestAppendSlotCellsMatchesSlotRow`: for a VirtualSlot, a materialised slot and nil, onto a dirty reused buffer.

**Measured** against `e9c61e18b`:

| query | instructions | bytes per run | objects per run |
|---|---|---|---|
| Q23 | 67.31 → 65.67 G (−2.4 %) | 3.91 → 3.43 GB (−12 %) | 21.5 M → 15.8 M (−26.5 %) |
| Q14 | 139.82 → 136.81 G (−2.1 %) | 4.19 → 3.91 GB (−7 %) | — |
| Q72 | flat | 97 → 77 MB (−21 %) | — |

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Left.**

- **Dead string columns** in index scans: the slice-81 ledger row.
- **Lateral re-open.** The lateral stream still re-opens the whole right subtree per outer row, where PG uses
  `ExecReScan`.
- **Remaining volume.** Q23 still allocates 3.4 GB per run.

## Slice 83 — a sort read only by a Gather Merge stores compact rows

**Target.** Q78 allocates 3.0 GB per run in 5.7 M objects, about 500 bytes each. The allocation-sample
attribution put the top sources at:

- `materializeLiveRow` in the worker and leader sorts: slice 71's live rows, still full width with the dead cells
  NULL;
- `transferRowForQueueLive` in the Gather Merge (also 8.3 % of all instructions);
- the aggregate above.

Q78's join rows are about 50 columns wide, and its readers need about half a dozen. PG's tuplesort stores a
MinimalTuple of the sort's narrow targetlist.

**Change** (`internal/executor/operators.go`, `executor.go`):

- **Marking.** `newGatherMergeNodeFor` marks a Sort at the root of its partial plan `compactOut`. That sort's only
  reader is the Gather Merge, which copies every row it pulls (`transferRowForQueueLive` in `runWorker` and
  `advanceRow` alike).
- **Compact storage.** A live sort so marked stores each row as its live cells alone (`liveIdx` order, detached,
  `rowChunk`-allocated), and `Next` expands it into one reused full-width row (`expandInto`; dead cells NULL, or
  the deform poison when armed).
- **Spills.** Before the first spill the stored rows are expanded back and compact mode ends for that Open
  (`expandCompactRows`), so the spill files and the merge only ever hold full-width rows.
- **Keys on detached cells.** Each row's keys are evaluated on its detached cells, laid out in the reused scratch
  row (not on the incoming slot).

**The bug the first cut had, and the invariant it exposed.**

- **The failure.** Evaluating the keys on the incoming slot made TPC-H Q22 return 1 510 groups instead of 7: every
  key was `"  "`.
- **The cause.** `Datum.StringValue()` of an arena-backed cell is an `unsafe.String` over the producer's arena. A
  value computed from it, such as `substr(c_phone, 1, 2)`, is a plain string with ArenaID 0 that still aliases the
  arena, so `MaterializeArena` cannot detach it. The seq scan's next page reset rewrote it.
- **The invariant.** Any value an operator keeps past its child's next row must be computed from detached cells.
  The old path held to it only because it evaluated keys on a materialised row.
- **What caught it.** The TPC-DS SF0.25 sweep (99/99, 61 ck) and the unit tests did not; the TPC-H acceptance
  arm's digests did.

**Pins.**

- `TestCompactSortMatchesLiveSort`: a 600-row sort, in memory (compact) and spilling (expanded first), returns
  the plain live sort's rows with every dead cell NULL. Two mutations fail it: no expansion before the spill, and
  cells written at the wrong columns.
- `TestCompactSortKeysSurviveTheArena`: an arena-page source resets its arena per row, under a `substr` key. The
  compact sort must order exactly as the plain live sort; evaluating the keys on the incoming slot fails it,
  reproducing Q22.
- `TestSortForConsumerCompleteReadersStoresLiveColumns` asserts that the Gather Merge marks its root sort.

**Measured** against `233658626`:

| query | instructions | bytes per run |
|---|---|---|
| Q78 | 21.74 → 20.68 G (−4.9 %) | 3.02 → 2.22 GB (−26 %) |
| Q47 | 19.06 → 18.32 G (−3.9 %) | — |
| Q43, Q51, Q57, Q67, Q97 | within ±0.6 % | — |

Q43's sort sits under a Partial GroupAggregate, not at the partial plan's root.

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH (it read 23 MATCH and 1 ROWS-DIFF on Q22 before the key fix);
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Left** (ledger row).

- **Other sorts.** A sort under any other reader still stores full-width rows.
- **The queue.** The Gather Merge queue rows are still full width.

## Slice 84 — the compiled evaluator applies AND / OR itself and compares a numeric with an int in place

**Target.** Q28 runs 2.9× slower than release PG. It is six parallel `store_sales` scans under
`ss_quantity BETWEEN … AND (ss_list_price BETWEEN … OR ss_coupon_amt BETWEEN … OR ss_wholesale_cost BETWEEN …)`.

- **Where the time goes.** `evalFastExpr` is 26.5 % of the profile and the scan decode 24 %.
- **A generic step per row.** Inside `evalFastExpr`, about 13 % went to `evalBinary` → `evalAnd` / `evalOr` and to
  `evalBinary` → `compareDatum`.
- **What a debug build showed.** The comparisons falling through to `compareDatum` were `numeric` columns against
  `int` constants (`KindNumeric` vs `KindInt`). `fastCompareDatum` declines mixed kinds.

**PG.** `ExecInitExprRec` compiles a BoolExpr into `EEOP_BOOL_AND_STEP*` / `EEOP_BOOL_OR_STEP*` steps, and a
numeric/int comparison is a single resolved comparison function.

**Change** (`internal/executor/exprnode.go`):

- **AND / OR.** The BinaryOp arm applies Kleene AND / OR (`evalAnd` / `evalOr`) directly when there is no literal
  coercion and both operands are bool or NULL. `evalBinary` sends `OpAnd` / `OpOr` straight there, and nothing else
  on the general path (bpchar, pg_lsn, coercion) applies to bool operands.
- **numeric / int.** `fastCompareDatum` decides a numeric/int pair on the int64 lane in either order, as
  `compareDatum`'s NUMERIC ↔ INT arm does: `promoteCrossKind` leaves the pair alone, the int becomes
  `numericFromInt`, and `numericCmp` compares. A big-mantissa numeric still declines.

**Pins.**

- `TestCompiledBoolAndNumericIntMatchInterpreter`: compiled against interpreted over every comparison operator in
  both orders, scales 0–4, a big numeric, NULLs, and AND / OR over all true/false/NULL pairings. Two mutations fail
  it: swapped compare operands, and AND evaluated as OR.
- `TestFastCompareDatumMatchesCompareDatum` (slice 27's pin) is amended: numeric/int pairs on the int64 lane are now
  decided, and checked against `compareDatum` like every decided pair.

**Measured** (instructions:u per query, against `a05a55628`):

| query | HEAD | candidate |
|---|---|---|
| Q13 | 6.82 G | 6.18 G (−9.5 %) |
| Q48 | 7.12 G | 6.65 G (−6.6 %) |
| Q28 | 25.67 G | 25.02 G (−2.5 %) |
| Q85 | — | −1.1 % |
| Q7, Q26 | — | flat |

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Left.**

- **Scan decode.** Q28's remaining time is the scan decode: `decodeRowRangeInfo` with the numeric payload checks
  (`NumericInt64FromStoredPayload`, the legacy-spelling probes) is about 30 %.
- **The batch evaluator.** It still reaches AND / OR and numeric/int comparisons through `evalFastBinaryApply` →
  `evalBinary`. The results are the same, but the path is slower.

## Slice 85 — a Gather worker shipping every column reads a join's VirtualSlot once

**Target.** Q10 runs 2.5× slower than release PG. Its plan matches PG's, a nested loop over a 1.92 M-row
`customer_demographics` scan whose materialised inner side is empty. PG also reads every outer row, because
`ExecNestLoop` has no empty-inner exit, so the gap is per-row cost.

**Attribution.**

- **The double copy.** The worker's `cloneRowOwned` and `VirtualSlot.Row` samples all come from
  `transferRowForQueue`. For a Gather that reads every column (no live mask), a join's VirtualSlot was gathered
  into a scratch row by `Row()` and then cloned by `cloneRowOwned`: two row allocations per shipped row.
- **The live path already avoided it.** `transferRowForQueueLive`'s live path has read cells straight into a
  chunk row since slice 66.

**Change** (`internal/executor/parallel_runtime.go`): with no live mask, a VirtualSlot is read cell by cell into a
chunk row, each cell detached (`MaterializeArena`). The values are `transferRowForQueue`'s: that function returns
`Row()` as-is when no cell is arena-backed, and `cloneRowOwned` (`MaterializeArena` per cell) otherwise. Other slot
kinds keep `transferRowForQueue`.

**Pins.** `TestTransferRowForQueueFullReadsCellsOnce`: a VirtualSlot with an arena-backed cell ships
`transferRowForQueue`'s values and survives the producer arena's reset. Dropping the detach fails it.

**Measured** against `e9ba4cf5f`:

| query | bytes per run | objects per run | instructions |
|---|---|---|---|
| Q10 | 186.5 → 151.2 MB (−19 %) | 369.8 k → 342.3 k (−7 %) | −0.4 % |
| Q35 | — | — | −1.4 % |
| Q16, Q69, Q94, Q95 | — | — | flat |

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

This slice moves its target by less than 3 %. Under banner rule (b), the next M0122-0012 slice must move its own
target by at least 3 %, or M0122-0012 yields to M0147.

**Measured and not landed this loop.**

- **Skipping the NULL store for skipped scan columns.** A store happens only when the reused cell is not exactly
  NullDatum. The skip arm's 48-byte stores are the largest lines of a filtered `store_sales` deform (about a third
  of `decodeRowRangeInfo` in Q28), but the load and compare cost more instructions (+4.7 %) for −1 to −3 % cycles,
  within noise.
- **The nested-loop stream's outer-row copy** (`nlJoinStream.stepOuter`, the sibling of slice 82). It does not
  fire in Q10, whose outer rows are not VirtualSlots with arena-backed cells, so it is left as it is.

## Slice 86 — a seq scan judges the qual's leading conjunct on its own columns

**Target.** Q28 runs 2.9× slower than release PG. Its plan matches PG's: six `store_sales` scans, each with a qual of
the form `ss_quantity >= x AND ss_quantity <= y AND (list_price … OR coupon_amt … OR wholesale_cost …)`.

**Attribution** (25.0 G instructions per run). `evalFastExpr` is 27 %, `decodeRowRangeInfo` 25 %,
`NumericInt64FromStoredPayload` 6.7 % and `skipAttrLen` 3 %.

- The prefilter (slice 25) decodes all four qual columns before judging the row: `ss_quantity` is column 10, and the
  three numerics run up to column 19.
- About 95 % of rows then fail the quantity bounds.
- PG pays one deform up to the last attribute (EEOP\_SCAN\_FETCHSOME) and then runs the AND-list step by step. goopg's
  numeric and varlena decode costs more than PG's, so the deform of columns the first clause never reads dominates.

**Change** (`scan_prefilter.go`: `scanQualLead`, `planScanQualLead`, `leadRejects`; wired in `seqScanOp.Open` and
`Next`).

- **Choosing the lead.** In Open, the qual's AND spine and its compiled slab are walked together: an AND node's left
  operand is its `childA`. The lead is the first left operand that reads fewer columns than the whole qual.
- **Judging it.** Per row, the scan decodes only the lead's columns, using a skip info built like `pfInfo`, and
  evaluates the lead's slab node.
- **Rejecting.** A FALSE lead rejects the row, with the same `statReturned` / `filterRemoved` accounting as the
  prefilter's reject. That is the whole qual's verdict: the compiled AND evaluates its left operand first and returns
  a FALSE one (`evalFastExpr`'s OpAnd short-circuit), and the lead is what that evaluation computes first.
- **Every other outcome** (TRUE, NULL, an error, a decode error, a toasted cell in the prefix) decides nothing. The row
  takes the unchanged prefilter path, which re-decodes the prefix from column 0 and evaluates the qual whole, so
  errors surface where they did before.
- **The probe.** A lead that rejects fewer than a quarter of its first 4096 rows is switched off for the rest of the
  scan, because the rows it passes decode its columns twice.

**Pins.**

- `TestScanQualLeadMatchesWholeQual` compares rows and errors with the lead off (`scanSkipDisabled`) and with the early
  path off (the late position), with tail poison armed. It covers:
  - a selective lead ahead of numeric, float and text columns;
  - a lead that is NULL for some rows;
  - a lead that errors;
  - a later conjunct that errors on exactly the rows the lead rejects.
- `TestScanQualLeadProbeSwitchesOff` pins the probe in both directions.
- Two mutations fail the tests: aiming the lead at the AND's right operand (`b.Right`, `childB`), and pairing the left
  operand's expression with the right operand's node.

**Measured** against `44017260b` (instructions:u per run):

| query | before | after | change |
|---|---|---|---|
| Q28 | 25.02 G | 17.99 G | −28 % |
| Q13, Q48, Q88, Q96, Q7 | — | — | flat |
| Q9 | — | — | +0.2 % (noise) |

Q9's single-column `BETWEEN` has no narrower lead.

Values are identical:

- sf025 PASS=99 (61 ck, Q28 ck verified), plan shapes identical, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- spotcheck PASS;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

This moves its own target by at least 3 %, so the rule (b) yield watch resets.

**Not covered.**

- **A qual that reads the scan's last column** gets no prefilter (two-phase deform saves nothing there), so it gets no
  lead stage either. Extending the lead to the late position is a possible later slice.
- **An out-of-line cell in the lead's prefix,** even in a column the lead does not read, sends the row down the
  prefilter path uncounted. This is the same rule `needsDetoastPrefix` applies to the prefilter.
