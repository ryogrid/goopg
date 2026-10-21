# M0122-0012 — TPC-H scan pipeline slices (part 2 of the vector filter pipeline recon)

Continues [0122-0012-vector-filter-pipeline-recon.md](0122-0012-vector-filter-pipeline-recon.md), which holds
the decision record and slices 2–16. Split out on 2026-10-21 under the 800-line design-doc limit; the slice
numbering is unchanged.

## Slice 17 (2026-10-16): Projects fold their targets, lowered sublinks fold their Args — TPC-H Q17

With Q1 at parity, the slice timed every TPC-H query on PG `:65432` (SELECT only) and warm on a private goopg clone.
Q17 was furthest behind: 6.19 s against 1.53 s. Q13 (4.05 / 1.44), Q12 (1.64 / 0.44) and Q10 (1.57 / 0.63)
followed. Q17's plan is PG's, node for node. In PG the lineitem seq scan under the Hash Join probe takes ~350 ms.
In goopg, `decodeRowRangeInfo` (13.6 %) and `cloneRowOwned` (11 %) dominated, both under the probe's `projectOp`.

A deform-bound trace (`deformBoundBelow` per node) showed the bound reset to full twice:

- **At the Project between the Aggregate and the join, and at the probe Project over lineitem.** The EX1-02 rule
  narrowed a non-identity Project only when its targets were bare and in column order, and only under a non-full
  bound from above. HammerDB's lineitem column order (`l_shipdate, l_orderkey, l_discount, l_extendedprice, …`) makes
  Q17's probe projection `(l_partkey, l_quantity, l_extendedprice)` reordered. So all 16 columns, including
  `l_comment`, were deformed and cloned for 6M rows.
- **At the join filter.** `l_quantity < (SubPlan 1)` contains a `SubqueryExpr`, which neither walker understood, so
  both join sides went full.

Fixes:

- **Projects.** `projectOp.Next` and the op-tree `OpProject` evaluate only `Targets` against the child slot. So every
  non-identity Project folds its targets alone, `deformFoldRefs(deformBoundNone, Targets...)`, whatever the parent
  reads. That is PG's projection rule: `ExecCreateExprSetupSteps` (`execExpr.c`) sets `last_scan` to the highest
  attnum in the target list and emits one `EEOP_SCAN_FETCHSOME`. Unknown target shapes still decline. The
  identity arm is unchanged. `isOrderedBareProject` and `deformProjectThrough` were folded into the rule and
  removed.
- **Sublinks.** A param-lowered `SubqueryExpr` (`ParParam` set, D4.1) reads the outer row only through `Args`,
  bound by `bindSubPlanParams`: the inner plan reads ParamExec slots, nothing is pushed on `OuterRows`, and the
  cache key is the bound values. PG's `expr_setup_walker` likewise visits a SubPlan's `args`. Both walkers now fold
  `Args` (`deformSublinkArgs`). An unlowered sublink still declines: it pushes the whole row and keys its cache by
  every column (`subqueryCacheKey`).

Pins: `TestScanDeformProjectThrough` (reordered, expression, const and full-parent shapes; unknown targets decline)
and `TestScanDeformLoweredSublinkArgs` (both walkers, lowered and unlowered). Each fails under the mutation that
restores the old behaviour.

**Measured:** Q17 on a private TPC-H clone, three rounds, fresh server per arm, last four of five runs. The first
of the four is still reading pages in:

| round | HEAD `fccdebfc7` (ms) | candidate (ms) |
|---|---|---|
| 1 | 7861 / 6287 / 6581 / 6373 | 6678 / 4376 / 4376 / 4431 |
| 2 | 8067 / 6300 / 6331 / 6425 | 5811 / 4390 / 4428 / 4459 |
| 3 | 7867 / 6587 / 7278 / 6845 | 6322 / 4369 / 4438 / 4411 |

That is a warm median of ~6400 → ~4400 ms, about −31 %, with no overlap among the warm runs. PG is ~1.53 s. What remains on Q17 is
`projectOp.Next` and `cloneRowOwnedPrefix` on the probe side (~8 % each, now over the narrowed prefix) and page
reads (`pageChecksumBlock` ~5 %).

**Gates:** units, tpch-spotcheck, acceptance arm (values identical), sf025 PASS=99 PLAN-SHAPE same=99,
`TestPort_RegressSuite`, and the `TestPort_Isolation*` family (narrowing now reaches many more scans, including
under EPQ-rechecking row locks).

## Slice 18 (2026-10-21): the scan borrow reaches through Projects and hash-join probes — TPC-H Q17

After slice 17, the warm Q17 profile on a private clone was led by `cloneRowOwnedPrefix`: 22.6 % cumulative, all
under the lineitem probe's `seqScanOp.Next`. That splits into the per-row full-width `acquireRow` (44 %) and
`MaterializeArena` (15 %).

The M0145-0008u borrow, where a scan hands up its reused row instead of a clone, applied only to a scan directly
under a borrowing aggregate. Q17's probe chain is Aggregate → Project → Hash Join → Project → Seq Scan, so all 6M
lineitem rows were cloned.

**Rule (`markScanBorrowChain`, scan\_borrow.go).** It replaces the direct-child test under a borrowing aggregate.
The borrow descends through operators that pass each row up before pulling the next and keep nothing of it. That is
PG's slot discipline: a scan slot is valid until the next fetch, and a node that keeps a tuple copies it
(`ExecMaterializeSlot`). Two kinds of operator qualify:

- **A Project.** `projectOp.Next` evaluates its targets into its own reused row.
- **The probe side of a non-lateral hash join** of type inner, left, semi or anti. The probe child is the right
  one only for an inner join that builds on the left. The join holds two probe rows, each only while the probe
  child is not advanced:
  - the prefetched first row, while it builds;
  - the current row, while it emits that row's matches.

  A row that a spilling batch defers is deep-copied (spill.go). SubPlan cache keys are formatted strings, and
  `SetParamExec` values are rebound before each use. The build side is kept in the hash table and never borrows.

Everything else stops the descent: merge and nested-loop joins, lateral, right/full joins, and Filter (its batch
path reads ahead).

**Pin.** `TestScanBorrowThroughHashJoinProbe` runs four queries over a multi-page probe table whose text and big
numerics live in the per-page arena: an inner join, a grouped join, a join with a filter reading both sides, and
an EXISTS semi join. For each, with tail poison armed:

- exactly the probe-side scan borrows;
- group keys and min/max survive every arena reset;
- every result equals the cloning path's (`scanBorrowDisabled`).

**Measured.** Q17 on a private TPC-H clone, HEAD `b5832b221` against the candidate, a fresh server per arm, three
interleaved rounds of five runs:

| round | HEAD (s) | candidate (s) |
|---|---|---|
| 1 | 8.60 / 7.04 / 4.29 / 4.29 / 4.25 | 3.56 / 3.39 / 3.44 / 3.43 / 3.41 |
| 2 | 8.55 / 7.03 / 4.22 / 4.30 / 4.36 | 3.59 / 3.44 / 3.42 / 3.48 / 3.42 |
| 3 | 8.32 / 7.15 / 4.23 / 4.31 / 4.29 | 3.54 / 3.46 / 3.40 / 3.40 / 3.40 |

- The warm median went from ~4.29 s to ~3.42 s (−20 %), with no overlap.
- HEAD's first two runs per server (8.6 / 7.0 s) disappear: with `GOGC=off`, its per-row clones filled the heap
  until the memory limit forced collection.
- PG is ~1.53 s.
- The acceptance sweep's single cold Q17 went 4.03 → 2.68 s. Its 22-query total went 30.2 → 26.7 s against the
  slice-17 arm; that comparison is indicative only, as five days of other commits lie between the two arms.

**Next lever.** Deform is now the largest cost on Q17's probe: `decodeRowRangeInfo` at 27 % cumulative, and
`NumericInt64FromStoredPayload` at 9.6 %.

**Not done (ledgered):**

- The `BuildFast` builder (`buildRec`, the opnode tree) sets no borrow at all; only `buildNode` does.
- Merge and nested-loop probe sides, and chains with a Filter between, still clone.

## Slice 19 (2026-10-21): a scan under a Project walks past the columns nobody reads — TPC-H Q17

After slice 18, deform led Q17's probe: `decodeRowRangeInfo` at 27 % cumulative. The lineitem probe's Project
reads `l_extendedprice`, `l_quantity` and `l_partkey`. Under HammerDB's column order those are the 4th, 6th and
8th columns, so the deform window is 8 columns and 5 of them were fully decoded for nothing: numerics converted to
int64, a date, a `char`.

PG's `slot_deform_heap_tuple` also walks every attribute before the last one it is asked for, because a heap tuple
has no offset array. A by-reference value costs it only a pointer store, so an unread numeric costs PG nothing.

**Rule.** For a non-identity Project directly over a seq scan (`markProjectScanReads`, in both the `buildNode` and
`buildRec` arms), the scan records the set of columns its targets read (`deformScanReadSet`). It is built over
`deformScanVisit`, the whitelist walker `deformScanRefs` now uses; an unknown target shape leaves the set nil.

The Project evaluates only its targets against the scan's row, and the row leaves the scan through nothing else:
no code outside `seqScanOp` reads `scanRow`. So every other column in the deform window is walked past undecoded.
`colTypeInfo.skip` is set in Open, and only when the scan has no absorbed qual and no GiST/GIN SSI hook, the
scan's own readers of the row.

`skipAttrLen` measures a skipped value by goopg's physical encoding:

- a varlena by its header (`physicalPGVarlenaSize`, which is VARSIZE\_ANY): the short form, or the 4-byte form,
  compressed or not;
- a by-value fixed type by its width.

Anything else is decoded as before: an external TOAST pointer, a truncated value, or a fixed by-reference type
such as uuid or interval.

**Pins:**

- `TestScanSkipMatchesFullDecode` builds an 18-column table spanning int2/4/8, numeric, text with NULLs, varchar,
  char, date, timestamp, timestamptz, float4/8, bool, uuid, interval, bytea, and text stored compressed inline,
  with a read column after every type. With tail poison armed, five projections must equal the full decode
  (`scanSkipDisabled`). Making varlena sizes off by one fails it.
- `TestPhysicalPGVarlenaSize` covers the header forms and the refusals.

**Measured.** Q17, HEAD `a9a9b1f55` against the candidate, a fresh server per arm, three interleaved rounds of
five runs:

| round | HEAD (s) | candidate (s) |
|---|---|---|
| 1 | 4.34 / 3.49 / 3.53 / 3.64 / 3.68 | 3.37 / 3.03 / 2.89 / 3.00 / 3.13 |
| 2 | 3.36 / 3.38 / 3.36 / 3.42 / 3.85 | 3.09 / 2.86 / 2.92 / 2.92 / 3.21 |
| 3 | 3.47 / 3.42 / 3.54 / 3.66 / 3.66 | 3.00 / 2.90 / 2.94 / 2.93 / 3.18 |

- The warm median went from ~3.5 s to ~2.95 s (−16 %), with no overlap.
- Across slices 17–19 Q17 went from ~6.4 s to ~2.95 s. PG is ~1.53 s.
- `decodeRowRangeInfo` fell from 27 % to 20 % cumulative, and the generic decoder left the top of the profile.

**Next levers** on Q17: `projectOp.Next` flat (12 %), `pageChecksumBlock` (9 %), and
`NumericInt64FromStoredPayload` for the three columns actually read (6.7 %).

**Not done (ledgered).** The walk-past applies only to a Project directly over a qual-less seq scan. It does not
cover:

- scans with a qual, where the qual's columns join the read set;
- consumers further up, through joins: a read set threaded like the deform bound;
- index and bitmap scans.

## Slice 20 (2026-10-21): the scan borrow reaches through right and full hash joins — TPC-H Q13

**Choosing the target.** A warm per-query census after slice 19 (tpch-runner, second pass on a private clone,
against the `:65432` reference; Q15 skipped) ranks the gap to PG by absolute seconds:

| query | goopg (s) | PG (s) | gap (s) |
|---|---|---|---|
| Q13 | 3.73 | 1.50 | 2.23 |
| Q12 | 1.62 | 0.47 | 1.15 |
| Q18 | 4.37 | 3.39 | 0.98 |
| Q21 | 1.35 | 0.53 | 0.82 |
| Q17 | 2.49 | 1.73 | 0.76 |
| Q10 | 1.29 | 0.62 | 0.67 |

Every other query is within 0.3 s of PG.

**Cause.** Q13's plan matches PG's: HashAggregate → Hash Right Join, with orders probing a hash of customer. Its
profile was led by `cloneRowOwned` at 27.6 % cumulative, all under the orders scan's `seqScanOp.Next`. The span
clearing that the per-row clones caused (`memclrNoHeapPointers` via `initHeapBits`) added 9 %.

Slice 18's descent (`joinBorrowProbeChild`) stopped at the RIGHT join, so all 1.5M orders rows were cloned.

**Rule.** The descent now admits a non-lateral hash join of any type. The probe child is the side the lazy-hash
runtime does not build (`hashBuildIsLeft`, which forces semi and anti builds onto the right), not the raw
`plan.BuildLeft`. A RIGHT or FULL join reads its probe side through the same loop as an inner join (`nextLazy`).
It holds only the prefetched first row and the current row, and a null-extended unmatched probe row is emitted
from the still-bound current row. The sweep after the last probe row emits build rows only: PG's
`HJ_FILL_INNER_TUPLES`, null-extended on the probe side. RIGHT SEMI and RIGHT ANTI never emit a probe row.

**Pin.** `TestScanBorrowThroughOuterHashJoinProbe` (scan\_borrow\_test.go):

- it plans `bd LEFT JOIN bp`, which comes out as a Hash Right Join probing the multi-page `bp`, with keys
  missing on both sides;
- it runs that plan as planned, and again retyped as FULL, because goopg never plans a FULL hash join
  (ledger M0134-0037 — PG 18.3 plans Hash Full Join for the same query; checked on a scratch PG);
- for each, exactly the probe-side scan borrows, and with tail poison armed the results equal the cloning
  path's.

Restoring the old join-type list fails it.

**Measured.** Q13, HEAD `2181e6060` against the candidate, a fresh server per arm, three interleaved rounds of
five runs:

| round | HEAD (s) | candidate (s) |
|---|---|---|
| 1 | 4.48 / 4.58 / 4.71 / 5.03 / 4.79 | 3.69 / 3.30 / 3.22 / 3.59 / 3.41 |
| 2 | 4.57 / 4.82 / 4.52 / 4.50 / 4.72 | 3.82 / 3.41 / 3.48 / 3.35 / 3.37 |
| 3 | 4.58 / 4.80 / 4.71 / 4.62 / 4.89 | 3.85 / 3.40 / 3.44 / 3.51 / 3.43 |

- The median went from ~4.71 s to ~3.43 s (−27 %), with no overlap.
- Five back-to-back runs on one server are slower than the census's second pass (3.73 s at HEAD); both arms ran
  under the same conditions.
- `cloneRowOwned` left the profile.

**Next lever on Q13.** The hash probe keys a numeric `o_custkey` by its canonical string: `mapaccess1_faststr`
(16 %) plus `mapaccess2_faststr` (4.7 %), split between the probe and `matchedStrBucket`'s RIGHT-join match
bitmap. HammerDB declares the TPC-H keys `numeric`, so they never take the int64 bucket map.

**Not done (ledgered):** the descent still stops at merge and nested-loop joins, Filter, and Sort, and the
`BuildFast` builder sets no borrow (slice 18's row).

## Slice 21 (2026-10-21): a value-list IN is judged early, and a literal list is evaluated once — TPC-H Q12

**Cause.** Q12 is the second-largest gap to PG in slice 20's census (1.62 s vs 0.47 s). Its plan matches PG's: a
Parallel Seq Scan on lineitem whose qual starts with `l_shipmode = ANY ('{MAIL,SHIP}'::bpchar[])`, then a nested
loop into orders. Two costs led its profile:

- `cloneRowOwned` was 23 % and `decodeRowRangeInfo` 25 %. The qual is absorbed into the scan, and the scan can
  judge it early, on the deformed prefix (PG's `slot_getsomeattrs` discipline), so that only survivors are fully
  decoded and cloned. But `PlanScanQual` declined every `InExpr`, the sublink form and the value-list form alike.
  So all 6M rows were fully decoded and cloned before the qual rejected about 99 % of them.
- `collectInValues` was 10 %, and the IN arm 20 % in all. A value list was evaluated item by item on every row. The
  `'{MAIL,SHIP}'` array text was re-parsed each time, and the per-item bpchar metadata slices were reallocated.
  PG's planner folds that list to a Const array (`eval_const_expressions`), and `ExecEvalScalarArrayOp` only walks
  it per row.

**Rule:**

- **`PlanScanQual` admits a value-list `InExpr`.** That is one with no Plan, no Subquery and no PARAM\_EXEC Args.
  Its operand and items are walked as same-scope children (`exprChildSlots`), so the column bound is exact, and an
  item that is itself ineligible (a function call, a param) still declines. The sublink form stays declined, even
  when its Plan is nil, for the reason the existing arm records.
- **`inListItems` evaluates a literal list once per statement.** A literal list is one whose every item is a
  literal or a cast of one (`inListIsLiteral`). The items and their bpchar metadata are kept in
  `Context.inListConsts`, keyed by the `InExpr`, with the values detached from any arena (`MaterializeArena`).
  - The Context is per statement, the same lifetime as the existing non-correlated sublink cache.
  - Each parallel worker starts from `NewContext`, so the map is never shared.
  - A list that errors is not cached, so the next row raises the error again.
  - Any other list is evaluated per row, as before.

**Pin.** `TestScanQualValueListIN` (scan\_inlist\_test.go) runs seven IN quals over a multi-page table. They cover:

- bpchar blank-insensitivity;
- typed date literals;
- `< ANY` over an int array;
- `NOT IN`, plus `NOT IN` with a NULL item;
- a column item.

Each must arm the early path and equal its OR/AND spelling, a different evaluator, with the early path on and off.
Two mutations fail it: caching every list (the column-item case returns 353 rows against 636), and restoring the
blanket `InExpr` decline. `TestPlanScanPrefilterWhitelist` gains six eligibility cases.

**Measured.** Q12, HEAD `54a37edcd` against the candidate, a fresh server per arm, three interleaved rounds of
five runs:

| round | HEAD (s) | candidate (s) |
|---|---|---|
| 1 | 5.29 / 1.92 / 1.61 / 1.59 / 1.57 | 3.09 / 0.84 / 0.86 / 0.85 / 0.85 |
| 2 | 3.95 / 1.99 / 1.63 / 1.63 / 1.63 | 3.04 / 0.84 / 0.81 / 0.80 / 0.81 |
| 3 | 4.00 / 1.98 / 1.63 / 1.61 / 1.59 | 3.11 / 0.82 / 0.85 / 0.84 / 0.82 |

- The warm median went from ~1.61 s to ~0.84 s (−48 %). The first run per server is cold on both arms.
- The early-path change alone measured ~1.09 s. PG is ~0.47 s.

**Not done (ledgered):**

- PG hashes a constant array of 9 or more elements (`MIN_ARRAY_SIZE_FOR_HASHED_SAOP`,
  `ExecEvalHashedScalarArrayOp`). goopg walks a long literal list linearly; it is now evaluated once, but still
  compared item by item.
- A list of params or stable functions is still evaluated per row. PG folds params into a custom plan's Const.

## Slice 22 (2026-10-21): a numeric hash-join key starts in the int64 lane — TPC-H Q13, Q17, Q18

**Census.** After slice 21, a warm per-query pass (goopg / PG, s) ranks:

| query | goopg (s) | PG (s) |
|---|---|---|
| Q13 | 2.96 | 1.50 |
| Q18 | 4.26 | 3.39 |
| Q21 | 1.30 | 0.53 |
| Q10 | 1.27 | 0.62 |
| Q17 | 2.27 | 1.73 |

Q12 is down to 0.80 s against PG's 0.47 s.

**Cause.** The lazy hash join has an int64 bucket lane (`lazyIntHash`, M0127-P0.3), chosen before the build from the
plan's key types (`Join.HashKeysAreInt64`). It admitted only machine integers. Its comment excluded `numeric` on
cost grounds: for numeric it is the values, not the type, that decide whether a key fits an int64.

HammerDB declares every TPC-H key `numeric`, so every TPC-H hash join keyed a string map by the canonical numeric
string. On Q13 that was `mapaccess1_faststr` plus `mapaccess2_faststr` at ~21 %, across the probe and the RIGHT
join's match bitmap.

**Rule.** `Join.HashKeysTakeInt64Lane` starts a single-key build in the int64 lane when each key is a machine
integer or a `numeric`. Neither direction can lose a match:

- **Build.** A build key that is not int64-representable (a fraction, a value past int64) demotes the table to the
  string lane once (`demoteIntHash`), re-keying the rows filed so far under the identical canonical string. The
  Parallel Hash merge already reconciles a participant that demoted (`parallel_hash_shared.go`).
- **Probe.** A probe key that is not representable is treated as equal to no build key. That holds only if
  `datumToInt64Key` maps every integral, in-range value of every numeric representation.

  The small lane already did that. The big lane did not: `1.0000000000000000000000` (mantissa 10^22, scale 22)
  answered "not representable" even though it equals 1. `bigNumericInt64Key` now strips the trailing zeros of the
  big mantissa and maps the value. The batch router (`joinBatchHash`) and the packed composite lane key on the
  same canonical bytes, so they stay consistent.

**Pins:**

- `TestBigNumericInt64KeyIsExact` covers `bigNumericInt64Key`.
- `TestNumericHashJoinIntLane` runs eight numeric-keyed hash joins against the same queries with hash joins
  disabled:
  - the join types are inner, both left orientations, semi, anti, and `numeric = int4`;
  - the keys are integral values at several scales (one past int64's mantissa), a fraction that demotes the
    build, negatives, zero and NULL.

  Restoring the big lane's old "not representable" answer drops the `1.0000000000000000000000` probe's match
  (363 rows against 364).

**Measured:**

- Q13, HEAD `022774301` against the candidate, a fresh server per arm, three interleaved rounds of five runs: the
  median went from ~3.24 s to ~2.93 s (−9 %), with no overlap in the warm runs.
- The profile shows the int lane engaged: `mapaccess1_fast64` and `matchedIntBucket`.
- A single warm pass over all queries went from 18.65 s to 17.29 s in total (−7 %): Q17 −0.39 s, Q18 −0.36 s, Q13
  −0.20 s, Q21 −0.10 s.

**Not done (ledgered):**

- Q13's remaining string-map cost is the aggregate's group key (`mapaccess2_faststr` ~5 %), which still keys a
  numeric group column by string.
- The packed multi-key lane (`execKeyPackInt`) still admits machine integers only.

## Slice 23 (2026-10-21): two more borrow roots — a hash join's build side and a Gather worker's tree — TPC-H Q10, Q21

**Cause.** Q21 and Q10 were the largest unprofiled gaps (1.20 / 0.53 s and 1.20 / 0.62 s after slice 22). Both
were led by the seq scan's per-row clone:

- Q21: `cloneRowOwnedPrefix` was 37 %, plus the span clearing it caused (`memclrNoHeapPointers`, 21 %);
- Q10: `cloneRowOwnedPrefix` was 26 %.

Their big lineitem scans sit under hash joins whose output feeds a (Parallel) Hash, or under a Gather. The borrow
descent started only below a borrowing aggregate, so it never reached them.

**Rule.** Two operators copy every row they keep before pulling the next one, so each is a borrow root of its own,
whatever sits above it:

- **A hash join's build side** (`joinBorrowBuildChild`, marked in `buildNode`'s Join arm). Every build path copies:
  - `buildLoopLeft` / `buildLoopRight` keep rows through `retainBuildRow`, which re-homes arena payloads into the
    join's build arena; Parallel Hash participants run these same loops;
  - the FOR UPDATE build clones in `drainRowsCtxCTID`.

  This is PG's `ExecHashTableInsert`, which copies the slot into the table.
- **A Gather worker's tree** (`gatherOp.runWorker`). Each row goes through `transferRowForQueue` before the next is
  pulled. That is a fresh row for a virtual slot (cloned when it holds arena data) and a clone for every other
  slot kind.

  The leader's own rows pass straight through `gatherOp.Next` uncopied, so the leader tree borrows only when the
  Gather itself is reached by a borrowing descent (`gatherOp.borrowLeader`). That is defensive: a Sort above a
  Gather materialises the rows it keeps anyway.

`scanBorrowDisabled` now gates every root in `markScanBorrowChain`.

**Pins:**

- `TestScanBorrowBuildSideAndGatherWorkers`:
  - a join under a Sort, where only the build-side scan borrows;
  - the same join gathered over three workers.

  Both tables span many pages with text in the per-page arena, tail poison is armed, and every result must equal
  the cloning path's. Making `retainBuildRow` keep its input uncopied fails it. The test raises
  `MinParallelTableScanBlocks`: the cooperative parallel build would otherwise rebuild the build subtree in its
  own producers, and the borrowing scan would never run. A first version passed that mutation for exactly that
  reason.
- `TestScanBorrowThroughHashJoinProbe` and `TestScanBorrowThroughOuterHashJoinProbe` now expect the build side to
  borrow too.

**Measured.** HEAD `f24bff055` against the candidate, a fresh server per arm, interleaved rounds of five runs:

| query | HEAD warm (s) | candidate warm (s) | change |
|---|---|---|---|
| Q10 | ~1.70 | ~1.18 | −30 % |
| Q21 | ~1.18 | ~1.03 | −13 % |

- HEAD's 1.8–2.0 s second run of Q21 disappears; with `GOGC=off` the clones filled the heap.
- Q13 and Q7 are unchanged across three interleaved rounds. A single full pass had suggested otherwise; that was
  drift between passes.

**Not done (ledgered):**

- the cooperative parallel build's producer trees and GatherMerge workers do not borrow, though both copy each row
  into their queue;
- the `BuildFast` builder's join children are `opNodeOperator` wrappers, which the descent does not enter.

## Slice 24 (2026-10-21): the hashed aggregate's int64 group lane — TPC-H Q13; Q18 examined

**Q18 first.** Q18 has the largest absolute time (~4.0 s against PG's 3.39 s). Its inner HashAggregate folds 6M
lineitem rows into ~1.5M `l_orderkey` groups. Two hypotheses were measured:

| hypothesis | profile evidence | result |
|---|---|---|
| hashing the key's string per row | `mapaccess2_faststr` + `aeshashbody` ~13 % | neutral on Q18 |
| per-group allocation | `heapSetTypeSmallHeader` 18 % in an early run; the group-founding closure 23 % cum | slab chunks for the group, key row and states: **no gain**, rounds overlapped and leaned worse; reverted |

The first three runs per server take ~6.3 s and later ones ~4.5 s. That pattern points at heap growth under the
bench's `GOGC=off`, not at either hypothesis. It is left for a dedicated look; the bench does not run with the
server's own GC settings.

**Q13.** Q13's `GROUP BY c_custkey` keys the same way and does move.

**Rule.** For a single grouping set over a single key, `aggregateOp.Open`'s drain files each group under
`datumToInt64Key(value)` in an int64 map. The lane cannot change a group's identity or the output order:

- `datumToInt64Key` is exact since slice 22. For every value it maps, `appendGroupKey`'s canonical key is
  `canonicalNumericKey(v, 0)`.
- The first key the lane cannot represent (NULL, a fraction, any other kind) demotes it. Every int group moves to
  the string map under that canonical key, in founding order.
- After the drain the lane always demotes. The partial/finalize publication and the emit loops therefore see
  exactly the groups and order the string path builds.
- The row-transport finalize (`AggModeFinal` with `PartialEmit`) and grouping sets keep the string path.

**Pin.** `TestAggregateIntGroupLaneMatchesStringPath` runs five aggregates and compares their output, order
included, with the lane disabled (`aggIntLaneDisabled`). The keys mix spellings of one value (2, 2.000,
1.0000000000000000000000), negatives and zero. One query sees a NULL key mid-stream and one a fraction, so the
lane demotes after many int groups exist. Two mutations fail it: deferring the demotion (106–111 rows against 61),
and a wrong canonical key on demotion.

**Measured.** Q13, HEAD `4dba3357b` against the candidate, three interleaved rounds of five runs, in two separate
experiments:

- the warm runs went from ~3.1–3.2 s to ~2.9–3.0 s (~5 %) both times;
- Q18 is unchanged.

**Not done (ledgered):**

- multi-key and grouping-set aggregates still hash a string;
- a single NULL key demotes the whole lane;
- Q18's early-run cost is not yet attributed.

## Slice 25 (2026-10-21): the early qual decodes only its own columns — TPC-H Q12, Q10

**Q18 attribution, from slice 24.** Run without the bench's `GOGC=off GOMEMLIMIT=12GiB` override, so the server
applies its own `GOGC=200`, Q18 swings just as much (4.5–6.3 s across a server's five runs). So the swing is not
the `GOGC=off` heap growth slice 24 suspected. Both clusters run 2 GB `shared_buffers`, and a large parallel scan
reads through a ring in both engines, re-reading lineitem from the OS page cache each run. The swing stays
unattributed (ledgered).

**Cause.** Q10's lineitem qual is `l_returnflag = 'R'`, and about 25 % of rows survive. The early qual (slice 21;
`seqScanOp.Next`'s prefilter) deformed every column up to `l_returnflag`: four numerics, two keys and a date
the qual never reads, for every row. Decode was 32 % of the query. Q12's prefix is the same, ahead of its IN list
and three date compares.

PG's `slot_getsomeattrs` also deforms that prefix. A by-reference value costs it only a pointer store, though,
and goopg converts every value.

**Rule:**

- **The first pass skips.** `prefilterSkipInfo` copies `colInfo` and marks skip on every column in
  `[0, prefilter.MaxCols)` that the qual does not read (`deformScanReadSet`). The prefilter's first pass decodes
  with that copy (`pfInfo`), stepping over the other columns by stored length (`skipAttrLen`, slice 19).
- **Survivors decode again from column 0** with `colInfo`, so a skipped cell never reaches a consumer. The qual's
  own columns are decoded twice for survivors only.
- `pfInfo` is nil when the qual's reads cannot be bounded or nothing would be skipped. `scanSkipDisabled` turns it
  off.
- **`deformScanVisit` admits a value-list `InExpr`**: its operand and items, nothing else, which is slice 21's
  `PlanScanQual` rule. The same walker bounds deform windows elsewhere, so value-list IN quals and targets now
  narrow those too.
- A qual that reads the last column still gets no prefilter: `PlanScanQual` declines it as saving nothing. With
  skipping it would now save a lot (ledgered).

**Pin.** `TestScanPrefilterSkipMatchesFullDecode` runs five quals over a 19-column table that has every type
ahead of the columns read, including a value-list `= ANY`. For each it:

- asserts that `pfInfo` is armed;
- reads the skipped columns in the select list, with tail poison armed;
- requires the result to equal the result with the skip off and with the early path off.

Resuming survivors from `MaxCols` instead of column 0 fails it: the poison fires on a skipped cell.

**Measured.** HEAD `19610b0a5` against the candidate, three interleaved rounds of five runs:

| query | HEAD warm (s) | candidate warm (s) | change |
|---|---|---|---|
| Q12 | ~0.82 | ~0.66 | −20 %; PG 0.47 |
| Q10 | ~1.19 | ~1.11 | −7 % |
| Q6 | — | — | unchanged; its qual columns sit early in the row |

**Not done (ledgered):**

- the last-column prefilter;
- the double decode of a survivor's qual columns;
- Q18's swing.

## Slice 26 (2026-10-21): a memory context reuses the chunks its Reset emptied — TPC-H Q13

**Cause.** In Q13's flat profile after slice 25, two costs stood out:

- `mmgr.(*Context).Reset` cost 4 % flat, called once per page by the orders scan;
- `growChunk` → `typedslicecopy` cost another 4.5 %.

`growChunk` always inserted a new chunk after the head. It never reused the chunks a `Reset` had emptied. So the
seq scan's per-page arena (`sctx`, reset at every block boundary) gained a chunk on every page whose decoded text
overflowed one. Three things followed:

- the chunk list grew for the whole scan, holding its memory until Close;
- each `Reset` walked every chunk;
- each insertion shifted the chunks after the head.

PG's `AllocSetReset` keeps its keeper block and reuses its free lists for the next cycle.

**Rule.**

- Every chunk after the head is empty, because growth always moves the head to the chunk it adds and only `Reset`
  moves it back. So `growChunk` takes the first empty standard-size chunk after the head and swaps it into the
  next slot. The swap moves no live offset.
- An oversized chunk (`n > cs`) is never reused for a small allocation. A second allocation in it could start past
  `cs`, which the `chunkIdx*cs + start` offset encoding cannot express. An oversized allocation still gets a chunk
  of its own.

**Pin.** `TestResetReusesChunks` runs 50 reset cycles. Each cycle overflows three chunks, and every tenth cycle also
makes an oversized allocation. Every offset must resolve to its own bytes, and the chunk list must stay bounded.
Without the reuse it grows from 8 to 156 chunks.

**Measured.** HEAD `493de93b5` against the candidate, three interleaved rounds of five runs:

| query | HEAD warm (s) | candidate warm (s) | change |
|---|---|---|---|
| Q13 | ~2.93 | ~2.70 | −8 % |
| Q17 | ~2.55 | ~2.49 | −2 % |

**Not done (ledgered):** oversized chunks are kept until Release. PG's `AllocSetReset` frees every block but the
keeper.

## Slice 27 (2026-10-21): a same-kind comparison compares in place — TPC-H Q6

**Cause.** Q6 (0.48 s against PG's 0.24 s) is a filtered lineitem scan whose five-comparison qual dominated its
profile:

| function | flat | cumulative |
|---|---|---|
| `evalFastExpr` | 33 % | 56 % |
| `evalBinary` | — | 22 % |
| `compareDatum` | — | 16 % |

`time.Unix` and `promoteCrossKind` showed up as well. Every comparison went `evalBinary` → `compareDatum` →
`promoteCrossKind`, then a per-kind switch. For `date`/`timestamp`, that switch built a `time.Time` from each side
before comparing two instants that are already UnixNano integers.

**Rule.** Inside the compiled BinaryOp arm's existing simple-path gate (`fastBinarySimple`), a comparison of two
non-NULL values of one kind goes through `fastCompareDatum`. Each of its arms is the arm `compareDatum` reaches
for that pair:

| pair | how it compares | why it matches `compareDatum` |
|---|---|---|
| int / int | `Int` | `promoteCrossKind` leaves equal kinds alone |
| time / time, neither a timetz (`Scale == 0`) | `Int` | `Int` is UnixNano; the ±infinity sentinels sit at the int64 extremes and order the same way |
| numeric / numeric on the int64 lane | `numericCmp` | the same call `compareDatum` makes |

Every other pair, and any NULL, takes `evalBinary` as before.

**Pin.** `TestFastCompareDatumMatchesCompareDatum` compares 18 values pairwise. They cover:

- a date against a timestamp at the same instant and one nanosecond later;
- the infinities;
- timetz values whose UTC order differs from their local order;
- numerics at mixed scales;
- the big lane.

Every decided pair must equal `compareDatum`, and every timetz, big-lane or mixed-kind pair must be declined.
Comparing numerics by raw `Int` fails it.

**Measured.** HEAD `7909e3796` against the candidate, three interleaved rounds of five runs:

| query | HEAD warm (s) | candidate warm (s) | change |
|---|---|---|---|
| Q6 | ~0.45 | ~0.31 | −30 %; PG 0.24 |
| Q1 | — | — | within noise; its qual is a single date compare |

**Not done (ledgered):** the recursive evaluation of column and constant leaves (`evalFastExpr` line 434 still
carried the most flat time) remains. PG's `ExecInterpExpr` reads a scan Var and a Const through opcode steps, with
no recursion.

## Slice 28 (2026-10-21): the scan borrow reaches through nested loops — TPC-H Q3

**Cause.** Q3 (0.64 s against PG's 0.21 s) still spent 12 % in `cloneRowOwnedPrefix`. A debug pass on a private
server (temporary print, not committed) showed the orders scan opening without the borrow in all 25 participant
opens.

Its chain is Gather worker → Nested Loop → Parallel Hash Join → orders. The descent stopped at the Nested Loop, a
`joinOp` with `JoinAlgoNestedLoop`. Because its inner side is a parameterised index probe, it runs in
`join_lateral_stream.go`.

**Rule.** A nested loop pulls its next outer row only once the inner side is done with the current one, and keeps
nothing of it afterwards. PG's NestLoop holds the outer slot across the inner rescans the same way. So the descent
continues into its outer side:

- **A streaming nested-loop `joinOp`**, plain (`join_nl_stream.go`) or LATERAL (`join_lateral_stream.go`). The
  descent sets `nlOuterBorrowed`, and `stepOuter` then keeps the current outer row by a shallow copy instead of
  `cloneRowOwned`. Otherwise the clone would only move from the scan into the loop.
- **A `nestedLoopIndexJoinOp`.** It already copies only the outer row's slice.

The inner side keeps nothing of the outer row across outer rows:

- the index probe reads the bound slot at Rescan;
- Memoize keys its cache by a formatted string;
- the lateral CTE cache resets per outer row;
- the sublink result caches key by formatted strings.

A sublink could return an outer column as its cached value. A real-server probe of that case — 30,000 rows
repeating three values across pages, `(SELECT so.s FROM one)` — returned correct groups. The sublink cache stores
values undetached, which is ledgered.

**Pin.** `TestScanBorrowThroughNestedLoopOuter` runs five aggregate queries over a multi-page outer table, each
under a borrowing aggregate and with tail poison armed:

- two over a parameterised nested loop (NLI), one of them a LEFT join;
- one non-equi join, which cannot hash;
- one LATERAL subquery.

For each, the nested loop's outer scan must borrow, and the result must equal the cloning path's. Removing the
nested-loop `joinOp` case fails it.

The unit fixture's scans do not use the per-page arena, so a value read after an arena reset cannot show in a unit
test. Arena lifetime is covered only by the server gates (spotcheck, acceptance, sf025, regress, isolation), and
that is ledgered.

**Measured.** HEAD `169a92964` against the candidate, four interleaved rounds of five runs:

- Q3's warm median went from ~0.56 s to ~0.45 s (−20 %);
- HEAD drifted upward within each server (0.51 → 0.73 s), while the candidate stayed flat.

**Not done (ledgered):**

- merge joins, Sort, Filter and GatherMerge still stop the descent;
- the sublink caches store result Datums undetached;
- there is no arena-backed unit fixture.
