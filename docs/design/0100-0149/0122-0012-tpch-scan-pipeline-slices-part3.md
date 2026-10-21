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
