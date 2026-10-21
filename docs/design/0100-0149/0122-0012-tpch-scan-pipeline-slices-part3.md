# M0122-0012 — TPC-H scan pipeline slices, part 3

Continues [0122-0012-tpch-scan-pipeline-slices.md](0122-0012-tpch-scan-pipeline-slices.md) (slices 17–24), which
continues [0122-0012-vector-filter-pipeline-recon.md](0122-0012-vector-filter-pipeline-recon.md) (the decision
record and slices 2–16). Split out on 2026-10-21 under the 800-line design-doc limit; slice numbering is unchanged.

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

## Slice 29 (2026-10-21): filtered scans walk past unread columns — TPC-H Q21

**Census.** A per-query pass after slice 28 ranks the largest gaps as Q13 (2.81 / 1.50 s), Q18 (4.00 / 3.39), Q10
(1.09 / 0.62) and Q21 (0.96 / 0.53).

Q11 read 0.34 s in that pass. Interleaved runs of the slice 26, 27 and 28 builds all measure it at ~0.19 s warm,
so the 0.34 s is a whole-suite artifact, not a regression.

**Cause.** Q21 is decode-bound: `decodeRowRangeInfo` is 37 % cumulative. Its lineitem scan carries a filter
(`l_receiptdate > l_commitdate`) and sits directly under a narrowing Project. Slice 19's walk-past did not apply,
for two reasons:

- `markProjectScanReads` required the Project's plan child to be a `SeqScan`, but a filtered scan's plan child is
  `Filter{SeqScan}`. The Filter is absorbed into the scan operator.
- Open declined every scan that had a qual or the SSI predicates. An absorbed Filter always sets `ssiGistPred` and
  `ssiGinPred` to its predicate, but those hooks read the row only once Open resolves an index OID for them, and
  that resolution happens after the skip flags were computed. So no filtered scan ever skipped.

**Rule:**

- `markProjectScanReads` also accepts a `Filter` directly over the `SeqScan`.
- `seqScanOp.skipReadSet` is the consumer read set, plus the qual's own columns (`deformScanReadSet`) for a scan
  with an absorbed qual. The late qual reads the finished row, so its columns must stay decoded. An unbounded qual
  declines.
- The skip flags are applied after the GiST/GIN SSI hooks resolve, and decline only when a hook's index OID
  resolved (`gistSSIIdxOID`, `ginSSIIdxOID`).
- The prefilter's `pfInfo` copies `colInfo` afterwards, so it inherits the skips.

**Pin.** `TestScanSkipMatchesFullDecode` gains three filtered queries:

- an early qual;
- a qual on the last column, which gets no prefilter, so the late path;
- a two-column date and float qual.

Every query must now arm at least one skip after Open. That assertion exists because the first version of this
slice passed vacuously, with no skip armed, before the SSI ordering was found. With tail poison armed, the results
must equal the full decode. Leaving the qual's columns out of the read set trips the poison.

**Measured.** HEAD `d857ea0b9` against the candidate, three interleaved rounds of five runs:

| query | HEAD warm (s) | candidate warm (s) | change |
|---|---|---|---|
| Q21 | ~0.95 | ~0.86 | −9 % |
| Q10 | — | — | unchanged; its filtered scans are not directly under a Project |

**Not done (ledgered):** the read set still comes only from a Project directly over the scan. A scan feeding a join
decodes its whole deform window: the set-valued twin of the deform bound through joins.

## Slice 30 (2026-10-21): a plain-string comparison compares in place — TPC-H Q10

**Cause.** Q10's `l_returnflag = 'R'::bpchar` qual is a string comparison, so it misses `fastBinarySimple` and takes
`evalFastBinaryApply` on every row (21 % of the profile):

1. the ambiguous-text-shape check;
2. a pg\_lsn sniff on both operands (`looksLikePgLSN`);
3. the bpchar normalisation;
4. `evalBinary` → `compareDatum`, whose string arm sniffs again for pg\_lsn shapes, UUID spellings and row/array
   literals before it reaches `strings.Compare`.

**Rule.** After the ambiguous-shape branch, a comparison of two `KindString` operands that are both
`plainCompareString` compares in place: bpchar normalisation, then `strings.Compare`. A plain string is one no sniff
can match:

| sniff | needs | plain string excludes it by |
|---|---|---|
| `looksLikePgLSN` | a `/` | containing no `/` |
| `isValidUUIDStr` | exactly 32, 36 or 38 bytes | being shorter than 32 bytes |
| row / array comparison | a leading `(` or `{` | not starting with either |

Trimming trailing blanks cannot make a plain string unplain. Every branch the old path took is therefore skipped,
and the result equals `evalBinary`'s.

**Pin.** `TestExprSiblingParityStringCompare` compares the compiled and interpreted twins:

- 16 values, among them blank-padded, pg\_lsn-, UUID-, row- and array-shaped strings;
- text, bpchar(3) and untyped operands, plus literals opposite a bpchar;
- all six comparison operators.

Two mutations fail it: skipping the bpchar normalisation (140 divergences), and dropping the guard (120 untyped
divergences). Typed operands never reach the guard: the ambiguous-text-shape branch takes those shapes first.

**Measured.** HEAD `c53226b14` against the candidate, three interleaved rounds of five runs:

| query | HEAD warm (s) | candidate warm (s) | change |
|---|---|---|---|
| Q10 | ~1.04 | ~0.95 | −9 % |
| Q12 | — | — | unchanged; its strings go through the IN-list arm |

**Not done (ledgered).** For untyped operands goopg decides by sniffing the value, where PG decides by the
operand's type (`texteq`, `bpchareq`). Two equal UUIDs spelled differently compare equal as untyped strings in
goopg.

## Measured, not landed (2026-10-21, after slice 30)

Two candidate levers were measured and dropped this round.

**Read set through joins (slice 29's ledgered follow-up).** A throwaway debug build printed each scan's deform
window and read set at Open:

| query | scan | what it showed |
|---|---|---|
| Q13 | orders | already has a read set (since slice 29) |
| Q10 | lineitem | already has a read set (since slice 29) |
| Q10 | customer | already has a read set (since slice 29) |
| Q10 | orders | none (its parent is the Parallel Hash build), but its early date qual rejects ~96 % of rows before the full decode, so only survivors pay |

A set-valued twin of the deform-bound walk would mean rewriting a ~30-call-site signature, with coordinate
remapping at every join, for little on these queries. It stays ledgered, not scheduled.

**Inline leaf fetch in the compiled BinaryOp arm.** `evalFastLeaf` read a column or constant child without
recursing. Three interleaved rounds of five runs against HEAD `5fcdbb6e0`:

| query | HEAD warm (s) | candidate warm (s) |
|---|---|---|
| Q6 | ~0.32 | ~0.35 (slower) |
| Q10 | — | neutral |
| Q1 | — | neutral |

The helper repeats `evalFastExpr`'s slot type switch and still returns a 48-byte Datum, so it saves nothing and adds
a call layer. It was reverted.

Removing the recursion needs PG's shape: a flat step program over a result register, not a helper on the same
recursive interface (ledger slice 27 row).

## Slice 31 (2026-10-21): numeric B-tree keys compare in place — TPC-H Q3

**Cause.** Q3's per-orders-row probe of lineitem's `l_orderkey` index (HammerDB declares the key `numeric`) spent
15 % in `comparePGIndexTuples` and 10.5 % in `PGCompareNumeric`, with `mallocgc` at 16.8 % of the query.
`decodeNumericParts` copied the NBASE digit array of both keys into a fresh `[]int16`, so every key comparison of
every descent made two heap allocations.

**Rule.** `numericParts` keeps `raw`, a view of the stored little-endian digit pairs, and `dig(i)` reads digit i in
place. `cmpNumericVar` and `cmpNumericAbs` use `ndig()`/`dig()`. The comparison is still upstream's `cmp_numerics` /
`cmp_var_common`, digit for digit; only the copy is gone.

**Pin.** `TestPGCompareNumericMatchesExactOrder` compares 407 values pairwise against exact `big.Rat` comparison:

- random signs;
- up to 12 integer digits and scales 0–12;
- NBASE boundary values such as 9999, 10000 and 0.9999.

Reading the digits big-endian fails it. The existing hand-picked total-order test did not catch that mutation:
for small digit values a big-endian read stays order-preserving.

**Measured.** HEAD `836273d83` against the candidate, three interleaved rounds of five runs:

| query | HEAD warm (s) | candidate warm (s) | change |
|---|---|---|---|
| Q3 | ~0.46 | ~0.44 | ~4 %; the first round was ~0.52 against ~0.44 |
| Q21 | — | — | ~2 % |
| Q18 | — | — | within its run-to-run swing |

## Slice 32 (2026-10-21): pathkey usefulness compares identity keys once — TPC-DS Q64

**Cause.** Away from TPC-H, a per-query gap census of the TPC-DS SF0.25 corpus against `:65438` put Q64 at
36–44 s against PG's 2.0 s. Nearly all of it was planning, not execution. In the CPU profile, 94 % of samples
sat under `exprEqual` → `exprIdentityKey` → `fmt.Sprintf`, called from `pathkeyUsefulness.sameClass`.
`sameClass` scans every equivalence-class member against two expressions per call, and `exprEqual` formats
both identity keys from scratch on every comparison. Q64's 18-relation join search asks the question for
every candidate path's pathkeys.

**Rule.** `pathkeyUsefulness` compares identity keys it has already computed:

- `classMember` carries its `exprIdentityKey`, computed once when `newClassMember` collects it.
- `keyOf` memoises the key of any other compared expression (pathkey, merge and query keys) by node identity,
  for the rel's lifetime.
- `exprEqualKeyed` is `exprEqual`'s own decision from precomputed keys. Nil equals only nil, a node equals
  itself, and otherwise both keys must exist and match.

`sameClass`, `equivalentWithin`, `rightMergeDirection`, `usefulForMerging` and `sortableEarlyMember` use it.
Which paths are useful does not change; only the repeated formatting is gone.

**Pin.** `TestPathkeyUsefulnessKeyedMatchesExprEqual` checks every keyed answer against plain `exprEqual` over
equal, distinct, nil and literal (unkeyed) members. Comparing by pointer only, without keys, fails 8 of its
checks.

**Measured.** Q64 went from 36–44 s to ~3.0 s warm on a private SF0.25 copy (PG 2.0 s). Plans did not move:

- the sf025 sweep reported plan shapes identical, 99/99;
- the TPC-DS fire-set gate found no changed query;
- the TPC-H acceptance arm matched all 24 labels on values.

An A/B of Q72 (30 s against PG 3.4 s) was attempted but was unreadable: host load was 13–23 with 69 % iowait,
and the same build swung 3× from run to run. It is left for re-measurement.

**Left.** The other `exprEqual` callers still format both keys per comparison: `pathKeyEqual` in
`joinpathsmerge.go`/`orderedappend.go`, plus 29 call sites in the package. Q64's remaining ~1 s over PG has not
been profiled yet.

## Slice 33 (2026-10-21): index key comparison walks attributes; the skip-scan walk re-descends on finaltup — TPC-DS Q72

**Cause.** After slice 32, TPC-DS Q72 was the largest SF0.25 gap: ~29 s warm, against PG's 3.4 s. The plan
matches PG's. Its inner `inventory_pkey` (inv\_date\_sk, inv\_item\_sk, inv\_warehouse\_sk) index scan is a
skip scan probed on `inv_item_sk` (PG: 2185 loops, 898 676 index searches). 88 % of the CPU was in
`scanLeafItems`, which had two costs:

- **Every key comparison deformed both tuples.** `comparePGIndexTuples` deformed all shared attributes of both
  operands into a zeroed 384-byte scratch frame before comparing the first one.
- **`compareHigh` compared twice for a prefix bound.** When the bound named only a prefix, it re-compared a heap
  copy of the entry restamped to the bound's attribute count: an allocation and a second deform for every
  entry of a prefix group.
- **The skip-scan leaf walk read whole leaves it could not use.** `btreeSkipEnum.walkLeaf` compared every
  remaining entry of a leaf. This happened both before the group's probe and after passing it, and it only
  re-descended after a leaf turned out to hold a single group. PG re-descends earlier.
  `_bt_advance_array_keys` checks the page's finaltup and starts a new primitive scan as soon as no tuple left
  on the page can satisfy the advanced array keys (nbtutils.c, "Proactively check finaltup").

**Rule.**

- **`pgIndexTupleWalk` reads one attribute at a time.** `comparePGIndexTupleAttrs` walks both tuples together
  and returns at the first attribute that differs, as `_bt_compare` does with `index_getattr`.
  `deformPGIndexTupleInto` is now the same walker run to `natts`, so the two cannot drift apart.
- **`compareHigh` answers the prefix-bound case in the same pass.** Equal on every attribute the shorter bound
  stores means inside the bound (0). A bound that stores no attribute is still minus infinity (1). The truncated
  copy (`truncatedPGIndexTuple`) is gone.
- **`ScanCursor.LeafLastKey` returns the walked leaf's last data item.** This is the finaltup test, using the
  tighter last item instead of the high key. `walkLeaf` checks it once per group state per leaf:
  - when the entry is still before the probe and so is the last item, it descends to the probe;
  - when the probe is passed and the last item is still inside the group, it descends past the group
    (exclusive pivot).

  Neither jump skips a row the walk would have emitted: the walk would skip every entry between the jump point
  and the target too.

**Pins.**

- `TestComparePGIndexTuplesWalkMatchesDeformFirst` reproduces the deform-everything-first algorithm and the
  two-pass `compareHigh`. It checks them against the new code over 26 000 pairs: three attributes (int4,
  NULLS FIRST text, DESC int4), random NULLs, and pivots storing 0–3 attributes. It fails each of three
  mutations:
  - the 0-attribute bound read as a prefix;
  - the prefix case answered as `compare`;
  - a walk that does not advance its offset.
- `TestSkipScanLeafWalk` (seq-scan oracle over the dense, sparse and long-run shapes) exercised both new jumps:
  46 past-group and 40 to-probe jumps, counted with a temporary counter.

**Measured.** Private SF0.25 copy, warm:

| query | HEAD (s) | candidate (s) | PG (s) |
|---|---|---|---|
| Q72 | 28.1–31.3 | 8.2–10.7 | 3.4 |

- The comparison change alone took Q72 to 17–20 s; the finaltup re-descent took it the rest of the way.
- In the sf025 sweep, Q72 went from 29.7 s to 10.8 s, Q81 from 11.1 s to 7.8 s, and the sum of the 99 query
  times from 203 s to 182 s.
- Q35 and Q1 rose in that sweep but A/B as noise. Both builds drift from ~3.4 s to 5–8 s on Q35 as the server
  ages, and Q1 swings 21–32 s on HEAD.
- TPC-H warm totals sit within run-to-run noise (15.4–16.1 s, two interleaved rounds); TPC-H has no skip scan.
- Q72, Q94, Q95 and Q16 output is byte-identical to `:65438`.

**Left.** Q72 is still ~2.4× PG. What remains is mostly the leaf binary search (`leafScanStart`) on cold
leaves. goopg descends twice per prefix group (past the group, then to the next group's probe), where PG with
skip support often descends once. The cursor also binary-searches every later leaf against its lower bound.
Both are recorded in the deferral ledger.

## Slice 34 (2026-10-21): a plain hash aggregate does not sort its output — TPC-DS Q23, TPC-H Q18/Q13

**Cause.** After slice 33, TPC-DS Q23 was the largest SF0.25 ratio: ~8.7 s warm against PG's 2.0 s. Its
`frequent_ss_items` CTE alone took ~3.5 s against PG's 0.40 s. It hash-aggregates 414 575 rows into 403 882
groups. `aggregateOp.Open` then sorted the finished groups by their GROUP BY columns with `sort.SliceStable`
over `compareDatumTyped`, which cost ~2.1 s.

M0097-0117 added that sort when goopg had only hash aggregation and the regress expected output came from PG's
sort-based GroupAggregate. Today the planner chooses Hashed or Sorted as PG does, and the plans match PG's. A
HashAggregate therefore sits only where PG has one, and PG's `agg_retrieve_hash_table` (nodeAgg.c) returns
groups in hash-table order. PG promises no order there; wherever one is needed, the planner puts a Sort above
the aggregate.

**Rule.** The output pre-sort is skipped for `AggStrategyHashed` without grouping sets. Groups come out in the
order they were first seen. Sorted aggregates and every grouping-sets shape keep the per-set/rollup ordering
code unchanged.

**Pin.** No new unit test: what changed is the absence of an order nobody may rely on. The evidence is that
every order-sensitive consumer still passes:

- `TestPort_RegressSuite` and `TestPort_Isolation`;
- the sf025 sweep, whose `ck` is an ORDERED sha256 over result rows: 61 ck-verified, CKMISMATCH=0;
- the TPC-H acceptance arm: 24 labels match on values;
- the `internal/executor` package, including `TestAggSortedHashParity`.

**Measured.** Warm, interleaved HEAD/candidate:

| query | HEAD (s) | candidate (s) | PG (s) |
|---|---|---|---|
| TPC-DS Q23 | 7.96–9.30 | 4.47–6.46 | 2.0 |
| TPC-H Q18 | 4.16–4.18 | 3.58–3.60 | — |
| TPC-H Q13 | 2.40–2.43 | 2.16–2.19 | — |
| TPC-H total (21 queries) | 16.64–16.87 | 15.82–15.99 | — |

**Left.** goopg's hash aggregate emits first-seen order, not PG's hash-table order. The two agree only on the
set of rows. Where the input arrives through a Gather, the first-seen order also varies from run to run. PG's
order is fixed by its hash values. Both are recorded in the deferral ledger. The Sorted-strategy final and
partial modes still re-sort input that already arrives in group order.

## Slice 35 (2026-10-21): a projection under an aggregate evaluates only what the aggregate reads — TPC-H Q3/Q5/Q13, TPC-DS Q14

**Cause.** TPC-DS Q14 runs ~17.5 s warm against PG's 8.5 s. Its time sits in index probes: `store_sales_pkey`
took 4.2 s for 16 173 loops returning 688 034 rows, where PG takes ~0.63 s. The probe deformed every heap
column, with no narrowing at all.

Above each probe the planner puts a **re-expansion Project**. It maps the join's narrowed output back to the
coordinates of the aggregate's arms, so it names every join column: 74 or 85 targets. Two things followed:

- **The deform walk counted every target as read.** It folds every Project target, so all join columns counted
  as consumers and every probe below deformed in full.
- **`projectOp` evaluated all of them.** It did so on every row, and the GroupAggregate's Sort then stored
  ~4 kB per row (83 MB for 21 135 rows).

PG never builds dead targets: a node's targetlist holds only what its parent needs.

**Rule.**

- **A consumer-complete parent builds the projection itself.** A plain or partial Aggregate qualifies, because
  its arms (`aggConsumerExprs`) are the only readers of its child's rows. Down a chain of Sorts and
  IncrementalSorts that stays true once their keys join the consumers. `buildCompleteChild` walks that chain and
  builds the Project through `buildProjectNode` with a live set (`projectLiveSet`).
- **`projectOp` evaluates only the live targets.** A dead target's cell is set once at Open, to NULL (or to the
  deform poison when that is armed). Only live targets fold into the child's bound (`deformProjectBelow`).
- **A dead target is skipped only when that is safe.** It must be a column reference or a plain constant.
  Anything else (a cast, a call, an operator) stays live. A consumer the visitor cannot enumerate, or a
  reference outside the list, keeps every target.
- **Every other parent keeps every target live.** That includes a Finalize aggregate, which reads its child's
  state columns without an arm. It also includes the root: the walk's root bound (None) is not a consumer
  inventory, since a root Sort folds only its keys while the client reads every column. The slab twin
  (`buildRec`/OpProject) has no consumer-complete parent, so it is unchanged.
- **Lateral probe keys now fold into the outer's bound.** Narrowing exposed a gap in `deformJoinBounds`: a
  LATERAL join's inner probe reads its keys from the outer row (`OuterColumnRef{Level: 1}`). When the probe has
  absorbed the clause, no join Predicate repeats it, and nothing folded those keys into the outer's bound.
  `deformLateralOuterRefs` folds them, plus any level-1 reference in the probe's Cond, through
  `WalkExprHostScope`; it fails closed to full on a sublink or an expression kind it cannot enumerate. A
  full-width projection above the outer had always masked this gap.

**Pins.**

- `TestProjectLiveSetUnderAggregate` covers six things:
  - Aggregate → Sort → re-expansion Project evaluates exactly the arm and key targets, and the scan bound is
    their fold (14 of 16);
  - a partial aggregate behaves the same;
  - a Final aggregate keeps every target;
  - a root Sort keeps every target and the scan stays full;
  - an operator target stays live even though it is dead.

  Removing Sort from the chain fails it, and so does treating a Final aggregate as complete.
- `TestDeformJoinBoundsFoldsLateralProbeKeys`: dropping the lateral fold gives left bound 0 instead of 2, and a
  sublink in the Cond goes full.
- `TestLateralProbeKeepsOuterKeyDeformed` is an SQL value check, run with poison armed.
- The whole `internal/executor` package also passes with the deform poison armed globally.

**Measured.** Warm, interleaved HEAD/candidate:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-H Q3 | 0.42–0.44 | 0.37–0.38 |
| TPC-H Q5 | 0.44–0.45 | 0.39–0.41 |
| TPC-H Q13 | 2.21–2.24 | 2.02–2.03 |
| TPC-H total | 16.03–16.27 | 15.47–16.10 |
| TPC-DS Q14 | 17.47–17.65 | 16.87–17.21 |
| probe micro (serial) | ~0.592 | ~0.525 |

The sf025 sweep's 99-query sum went from 173.9 s to 169.5 s.

**Left.**

- **Q14 moves only ~2.5 %.** The deform bound is a prefix. The arm coordinates interleave the tables, so the
  live targets' refs still reach column 20 of 34 on catalog\_sales and web\_sales, and column 12 of 23 on one
  store\_sales probe. Two store\_sales probes in statement 2 stay full.
- **A real fix needs one of two designs:** a set-valued read set for index-scan heap fetches threaded through
  joins, or a planner that remaps the aggregate's arms onto the narrowed join output so the re-expansion
  Project disappears, as in PG. Both are recorded in the deferral ledger.
- **Parallel is slower than serial here.** The probe micro runs ~0.75 s with 3 workers against ~0.59 s serially.
  PG's Gather overhead is smaller (0.23 s parallel, 0.18 s serial).

## Slice 36 (2026-10-22): a re-opened index probe keeps its decode buffer — TPC-H Q5/Q7/Q3, TPC-DS Q14

**Cause.** The probe shape under Q14 is a parallel nested loop driving `item_pkey` probes; a standalone copy
is `tmp/q14-micro.sql`. On that shape goopg spent ~2.5 core-seconds per run against PG's ~0.9. 22 % of the CPU
was `runtime.mallocgc`, and 60 % of that came from `acquireRow` inside `indexScanOp.Next`.

`lateralJoinStream` closes and re-opens the inner probe for every outer row. `indexScanOp.Close` dropped
`scanRow`, so every probe allocated a fresh full-width row: 22 Datums, ~1 kB of pointerful memory. That cost
4.1 s of a 30 s profile.

Comparing against the serial run was misleading. With no parallel workers, both goopg and PG choose a hash join
with item, so "parallel slower than serial" was a plan difference, not Gather overhead.

**Rule.** `indexScanOp.Close` scrubs the deform poison but keeps the buffer, and so does its sibling
`bitmapHeapScanOp.Close`. The returned slot already aliases `scanRow` until the next `Next`. A re-Open followed
by `Next` is no different, so reuse across Close adds no aliasing the scan did not already have across rows. PG
likewise keeps a scan node's slot for the node's lifetime: `ExecReScanIndexScan` does not free
`ss_ScanTupleSlot`. Memoize copies the rows it caches (`operators_memoize.go`), so its cache is unaffected.

**Pin.** `TestIndexScanKeepsScanRowAcrossReopen` checks two things:

- after Close, the same buffer is still held;
- re-opening the probe decodes into that buffer and returns the probe's own values.

Restoring the old `o.scanRow = nil` fails it.

**Measured.** Warm. TPC-H ran under `GOGC=100`, two interleaved rounds:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| probe micro (parallel) | 0.71–0.90 | 0.50–0.56 |
| TPC-DS Q14 | 16.82–17.09 | 15.84–16.15 |
| TPC-H Q5 | 0.53–0.66 | 0.40 |
| TPC-H Q7 | 0.70–0.85 | 0.59–0.61 |
| TPC-H Q3 | 0.41–0.42 | 0.36–0.37 |
| TPC-H total | 16.16–16.37 | 15.79–15.95 |

The sf025 sweep's 99-query sum went from 169.5 s (slice 35) to 168.6 s.

The usual `tmp/tpch-gap.sh` harness runs with `GOGC=off`, so its collections fire at GOMEMLIMIT. Under it, Q2
showed 0.40 → 0.72 s on the candidate in both rounds. Under `GOGC=100` and in isolation, Q2 is unchanged
(0.33–0.38 s and ~0.48 s): the collection was simply landing in Q2 instead of elsewhere.

**Left.**

- **`indexOnlyScanOp` still allocates a fresh output row per row** (`projectKeyVals` / `projectHeapRow`).
- **`lateralJoinStream` clones every emitted pair.** That is `cloneRow(m.pair)`, 2.2 s on the micro.
- **The probe shape is still ~2× PG's CPU.** Both items above are recorded in the deferral ledger.

## Slice 37 (2026-10-22): a borrowed nested loop emits its pair buffer — the Q14 probe shape

**Cause.** After slice 36, the nested loop's emit was the next allocation on the probe micro
(`tmp/q14-micro.sql`). Both streaming loops, `lateralJoinStream.stepInner` and `nlJoinStream`'s inner step,
built every joined pair in a reused buffer and then emitted `cloneRow(m.pair)`, a fresh row per output tuple.
That clone cost 2.2 s of a 30 s profile.

PG's NestLoop returns its projected result slot, which is valid until the next call; it makes no copy.

**Rule.** `markScanBorrowChain` already descends through a nested-loop `joinOp` from a borrowing root:

- a borrowing aggregate;
- a hash join's build side;
- a Gather worker tree.

Each such root copies whatever it keeps before pulling again. On that descent it now also sets
`nlEmitBorrowed`, so both streams emit `m.pair` itself. The consumer is done with the row before the next call
rewrites the buffer, which is the same contract `nlOuterBorrowed` relies on for the outer row since slice 28.
Off the borrow chain both streams still clone.

**Pin.** `TestScanBorrowThroughNestedLoopOuter` now also asserts that every streaming nested loop on the
borrowed chain has `nlEmitBorrowed` set. Leaving the flag unset fails it. Its existing comparison still checks
the results against the cloning path (`scanBorrowDisabled`) with the deform poison armed, over five plain,
non-equi and LATERAL shapes.

**Measured.** Warm, interleaved:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| probe micro (parallel) | 0.50–0.63 | 0.40–0.41 |
| TPC-DS Q14 | 15.83–16.42 | 15.82–16.40 |
| TPC-H total (`GOGC=100`, fresh HEAD build) | 15.37–15.82 | 15.36–15.38 |

The sf025 sweep's 99-query sum is 168.2 s.

**Erratum for slice 36.** TPC-H Q5 and Q7 are bimodal from run to run on the same binary. Across two rounds,
a freshly built HEAD gave Q5 0.63 and 0.39 s, and Q7 0.62 and 0.58 s. So slice 36's Q5 "−30 %" and Q7 "−20 %"
rows overstate it. Its robust results are the probe micro (−35 %) and Q14 (−5.5 %, consistent over two rounds).
Treat any single-query TPC-H delta below ~0.2 s as noise unless it repeats across ≥ 3 interleaved rounds.

**Left.** `indexOnlyScanOp` still allocates its output row per row. The Q14 statement-level gap (16.0 s against
PG's 8.5 s) now sits in the wide prefix deform of the fact-table probes (slice 35, ledgered) and in the descent
itself.
