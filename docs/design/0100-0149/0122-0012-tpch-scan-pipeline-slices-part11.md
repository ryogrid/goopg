# M0122-0012 — scan pipeline slices, part 11 (slice 115–)

Split from part 10 (`0122-0012-tpch-scan-pipeline-slices-part10.md`, slices 102–114) at the length limit. Same
rules: each slice names its Target before it is measured (banner item 10, return-first rule), and every slice is
perf-only (plans, values and EXPLAIN output unchanged).

## Slice 115 — C38: the nested loop's outer seq scan decodes only the join filter's columns (Q10 −32.9 %)

Loop #222. The S9 census `routing-20261028e` (at `f645129a8`) is exhausted. A G1 refresh,
`analysis/m0122/m0122-0012/census-20261028/`, brought DS Q10 into the G2 top 10 at #7, with excess 543 ms and the
highest ratio, 4.05. G3 (instructions:u):

- `decodeRowRangeInfo` 21.6 % self, 32 % cumulative;
- `seqScanOp.Next` 13 %.

**Symptom.** Q10's plan is a Nested Loop whose Join Filter `cd_demo_sk = c.c_current_cdemo_sk` compares the outer
Seq Scan on customer\_demographics (1.92 M rows at SF0.25) against a Materialize of about three inner rows.

- goopg decoded all nine outer columns of every row. Three of them are `char(n)`, copied into the arena.
- The scan sits under Sort → GroupAggregate, which stop the borrow chain, so every row was also cloned.
- PG's NestLoop evaluates the join qual with `EEOP_OUTER_FETCHSOME` up to the highest outer attribute the qual
  reads, here attribute 1. Only a pair that passes reaches the projection, which fetches the rest (ExecNestLoop,
  execExpr.c).

**Change.** This is C32's lazy probe deform (`probe_defer.go`) for the streaming nested loop's outer side
(`join_nl_stream.go`).

- `nlJoinStream.armOuterDefer` runs at open. It finds the outer `seqScanOp`, directly or under a projection of bare
  column references, as C32 does, and arms it with the outer columns the predicate reads (`exprReadSet`).
- It declines when:
  - the predicate is nil or its reads cannot be enumerated;
  - the predicate reads no outer column;
  - the join is lateral;
  - EXPLAIN ANALYZE's filter counters are live (they evaluate the probe qual on the pair).
- `seqScanOp.armDefer(keyCols, requireBorrow)` is `armProbeDefer` with the borrow requirement optional. The hash
  join needs the borrow because it holds the row the scan returned. The nested loop instead re-copies the finished
  cells from `scanRow`, so it also arms a scan that clones its rows. That is safe because the stream pulls the next
  outer row only after the inner side is done, so the scan has not advanced and its page is still pinned;
  `finishDeferred` re-takes the content lock.
- `finishOuterRow` completes the row and copies it into `outerRow` exactly as `stepOuter` did, deep-copying
  arena-backed cells unless the outer is borrowed. It runs before every use beyond the predicate:
  - a matched pair, whose outer half of `pair` is then refreshed;
  - a semi/anti emit;
  - a null-extended LEFT/FULL outer row.
- C32's self-disarm (`probeDeferDisarmRows`) applies unchanged, so a loop that matches most outer rows stops
  deferring.

**Measured** (`tools/ds-instr.sh`, 3 runs × 2, a fresh A/B against `f645129a8`):

| query | before | after | change |
|---|---|---|---|
| TPC-DS Q10 (Target) | 10.255 / 10.277 | 6.889 / 6.899 | **−32.9 %** |
| TPC-DS Q35 | 9.092 | 9.087 | ±0 |
| TPC-DS Q78, Q26, Q23 | | | equal |

The prediction was ~20 %; the result meets the 3 % bar. Q35 is flat, so the disarm path costs nothing. Outputs of
Q10, Q35, Q69, Q13 and Q48 are byte-identical, and the sweep's Q10 time is 516 ms against 721 ms.

The first gate sweep flagged Q23 3 s → 7 s and moved the total +13.2 % (Q78 818 → 3670 ms, Q26 119 → 936 ms). That
was host contention, not this change:

- Q78, Q26 and Q23 run identical instructions on both binaries;
- the next sweep came back at −15.0 %, with Q78 at 818 ms and Q26 at 183 ms.

Gates:

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99 (twice), with plans 99/99 same;
- fire-set: PASS, no fires;
- RegressSuite: counts equal to the C37 run except the `plpgsql` flapper;
- isolation suite: PASS (`-timeout 30m`, 633 s).

**Test.** `TestNLOuterDeferMatchesEager` runs 12 queries with deferral on and off and requires identical rows, in
order. The outer table has the predicate column first and last, NULLs, numeric, `char(n)` and toasted text. The
queries cover:

- inner, left, right, full, semi and anti nested loops;
- a predicate on a late column;
- a projection between the loop and its scan;
- a cross join;
- emits under a sort and under an aggregate.

A hook counts arms: 5 of the 12 arm, the others by design not (the small table is the outer, or there is no
predicate). Dropping the matched-pair finish fails the test.

## Slice 116 — C43: index, index-only and bitmap scans evaluate their filter compiled (Q14 −1.9 %, below the bar)

Loop #225. Two leads in DS Q14's heap fetches were dismissed first:

- pruning already runs once per new heap block; `PagePruneXIDSet`'s 2 % is the first touch of each heap page, which
  PG pays too;
- consecutive same-block TIDs are rare for these probes, so pin reuse would save little.

**Symptom.** `indexScanOp.Next` evaluated its per-row `Cond` with the interpreted `evalExpr`, at 7.08 % of Q14. The
date\_dim probes filter on `d_year = 2001 AND d_moy = 11` and `d_week_seq = (InitPlan)`. The seq scan's absorbed
qual and the join residual (`joinPredicateMatch`, slice 45) already run compiled. The index-only and bitmap heap
scans had the same interpreted call. PG evaluates a scan qual through ExecQual over the ExprState that ExecInitQual
compiles (execExpr.c).

**Change.** `compiledCond` (`compiled_cond.go`) builds `plan.Cond` once per operator into an `exprTreeSlab`, without
constant folding (the plan is fixed for the operator's life), and evaluates it with `evalFastExpr` over a reused
`MaterializedSlot`. `evalFastExpr` delegates every kind it does not compile back to `evalExprSlot`, so the result is
the interpreter's. The index scan, index-only scan and bitmap heap scan all use it. `compiledCondDisabled` is the
test knob.

**Measured** (`tools/ds-instr.sh`, 3 runs × 2, a fresh A/B against `07f9d5c6a`):

| query | before | after | change |
|---|---|---|---|
| TPC-DS Q14 (Target) | 116.985 / 116.965 | 114.717 / 114.720 | **−1.94 %** |
| TPC-DS Q7 | 6.014 | 5.644 | −6.2 % |
| TPC-DS Q88 | 18.432 | 18.236 | −1.1 % |

This misses the 3 % bar (predicted ~5 %): the compiled filter still costs about two thirds of the interpreted one
on these short predicates. It is landed below the bar, as C21 was, because it is a faithful port with no plan or
value change, and it is recorded on the Below-bar list. It is the second consecutive miss after C42, so it triggers
the one-task yield.

The sweep's total moved +13.7 % (Q88 527 → 3158 ms, Q7 234 → 1677 ms) with fewer instructions on both binaries:
host contention, as with slice 115.

Gates:

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99, with plans 99/99 same;
- fire-set: PASS, no fires;
- RegressSuite: counts equal to the C38 run except the `plpgsql` flapper;
- isolation suite: PASS (`-timeout 30m`).

**Test.** `TestCompiledCondMatchesInterpreter` runs nine queries with the compiled filter on and off and requires
identical rows, in order. They use nested-loop index probes, bare index scans and bitmap heap scans, with filters
covering:

- AND and OR;
- NULLs;
- `char(n)` and numeric columns;
- an uncorrelated sublink;
- a function call;
- a lateral outer reference.

A filter that divides by zero must raise the same error both ways.

## Slice 117 — C45: the int lane reads a bare-column probe key in place (Q17 −3.85 %)

Loop #229/#230. G3 on TPC-H Q17 at `a42524dd1`, attributed by innermost operator (`tmp/g3-229/h17*`):

- `seqScanOp.Next` 35 % (first touches, checksums);
- `decodeScanRowRangeInfo` 26 %;
- `nextLazy` 10 %;
- **`joinOp.evalHashKeyDatumSlot` 6.0 %** (evalFastExpr 3.2, self 2.0, `MaterializedSlot.Get` 0.7).

**Symptom.** Q17's hash probe key is the bare column `l_partkey`. Yet each of the 6 M lineitem probe rows evaluated
it through the general compiled evaluator: `evalFastExpr` dispatch, the merged-key `VirtualSlot` over
{probe row, null other side}, the slot's `Get`, and a 48-byte `Datum` return. PG special-cases exactly this shape:
a single-Var ExprState runs ExecJustOuterVar / ExecJustInnerVar (execExprInterp.c), which is what
ExecHashGetHashValue's keys hit for a plain column key.

**Change.** `joinOp.probeIntKeyDatum` replaces the int lane's `evalHashKeyDatumSlot` call in `nextLazy`. On the
first probe row whose two side widths are known (`lazyLW`, `lazyRW` both > 0), it decides once per operator whether
the single probe key is an `ExprColumnRef` landing on the probe side of the merged key layout:

- `BuildLeft` (probe = right side, layout `[lazyLW nulls][probe]`): index in `[lazyLW, lazyLW+lazyRW)` maps to probe
  column `idx-lazyLW`;
- otherwise (probe = left side, layout `[probe][nulls]`): index in `[0, lazyLW)` is the probe column.

When it does, the cell is read from the probe `MaterializedSlot`'s row directly (`MaterializedSlot.Get` is the
same plain index, so the value is identical). A trimmed key (`probeKeyTrim`), an expression key, a key landing on
the null side, a slot that is not a `MaterializedSlot` or an out-of-range column take `evalHashKeyDatumSlot` as
before — the out-of-range case keeps the evaluator's XX000 error path. `probeKeyColDisabled` is the A/B and test
knob; `probeKeyColArmed` counts arming operators for the test. The string lane and the multi-key composite lane
are unchanged.

**Measured** (`analysis/m0122/m0122-0012/tools/h-instr.sh`, 3 runs × 2 alternating rounds, fresh server per run,
base binary at `a42524dd1`):

| query | before | after | change |
|---|---|---|---|
| TPC-H Q17 (Target) | 22.586 / 22.616 | 21.737 / 21.720 | **−3.85 %** |

This meets the 3 % bar (predicted ~4 %). Miss counter reset to 0.

Gates:

- units (executor 49.9 s, all PASS);
- spotcheck: Q12=2, Q13=35;
- acceptance: 24 MATCH;
- sf025 PASS=99 (61 ck-verified), no verdict changes;
- fire-set: out of scope (executor-only change, `scripts/fireset-scope.sh`).

**Test.** `TestProbeKeyColMatchesEvaluator` runs twelve hash joins with the in-place read on and off and requires
identical rows, in order. Keys are int4, int8 and NUMERIC (HammerDB's TPC-H key type) with NULLs on both sides;
the joins are inner, left, right (the other input builds), full, semi, anti and NOT IN, plus a residual, an
expression key (must not arm) and a projection between the join and its probe scan. Eight of the twelve arm. An
off-by-one column mapping fails it.

## Slice 118 — C46: the aggregate finalize path reads state and call through pointers (Q18 −4.44 %)

Loop #231. G3 on TPC-H Q18 at `923750127` by instructions:u (`tmp/g3-231/h18i.data`):

- `runtime.mapaccess1_fast64` 11.1 %, 60 % of it on the instruction after the group control-word load. That is stall
  skid on the first touch of each new group's slot (~1.5 M groups in the `l_orderkey` hash aggregate), which PG's
  TupleHashTable pays too;
- decodeRowRangeInfo 7.1 %, NumericInt64FromStoredPayload 7.0 %, seqScanOp.Next 7.0 %, checksums 4.5 %;
- the aggregate ~19 %.

**Symptom.** Mapped against `go tool objdump`, most of the finalize path's samples are 64-byte-stride `movups` copy
loops: `finalizeGroup` 3.69 % (83 % copies), `finishAgg` 1.18 % (90 %), `finishBuiltinAgg` 1.01 % (88 %),
`foldIntLaneIntoNumeric` 1.03 % (95 %). `aggRuntime` is 704 bytes and `optimizer.AggregateCall` 320. For every
finalized group, `finalizeGroup` copied the call in three `for i, call := range o.plan.Aggs` loops, passed state and
call by value to `finishAgg`, which passed both by value again to `finishBuiltinAgg`, and the sum/avg arms copied
the state into and out of `foldIntLaneIntoNumeric`. That is ~3 KB of copying per group. PG's finalize_aggregates /
finalize_aggregate (nodeAgg.c) work on AggStatePerAgg / AggStatePerGroup pointers.

`applyAgg`'s hot lines are different: the 48-byte Datum return of `aggEvalList` (C13 class), not struct copies.

**Change.**

- `finalizeGroup` walks `o.plan.Aggs` by index with `call := &o.plan.Aggs[i]`.
- `finishAgg(st *aggRuntime, call *optimizer.AggregateCall)` and `finishBuiltinAgg` take pointers. Neither writes
  `st` or `call`; the rare ordered-set branch still hands `finishWithinGroupAgg` copies.
- `foldIntLaneIntoNumeric(st *aggRuntime) (numericSum Datum, sum int64)` returns the folded pair instead of a
  modified copy of the state. The sum and avg arms read those locals. The group's state stays exactly as the
  transitions built it, as it did when finishAgg worked on a copy.
- Callers: the window operator passes `&running, &call`; the parallel-combine test helper passes its pointer.

**Measured** (`analysis/m0122/m0122-0012/tools/h-instr.sh` and `ds-instr.sh`, 3 runs × 2 alternating rounds,
fresh server per run, base binary at `923750127`):

| query | before | after | change |
|---|---|---|---|
| TPC-H Q18 (Target) | 41.142 / 41.112 | 39.309 / 39.292 | **−4.44 %** |
| TPC-DS Q67 | 15.959 / 15.956 | 15.654 / 15.648 | −1.9 % |

This meets the 3 % bar (predicted ~5 %). The sf025 sweep read Q67 at 4.3 s against 1.3 s in the previous sweep.
That was host variance: warm wall time on the same host measured base 2.03 / 2.94 s against C46 1.95 / 1.78 s.

Gates:

- units;
- spotcheck: Q12=2, Q13=35;
- acceptance: 24 MATCH;
- sf025 PASS=99 (61 ck-verified), plans 99/99 same;
- fire-set: out of scope (executor-only).

**Test.** `TestFinishAggLeavesStateUntouched` (agg\_mixed\_lane\_test.go) finalizes sum/avg/count/max states twice,
including the mixed int/numeric lane, and requires the same value both times and a state `reflect.DeepEqual` to
the one the transitions built. Writing the fold back into the state fails it.

## Slice 119 — C48: the seq scan fetches its heap tuple in place (Q10 −2.4 % instructions, below the bar; −11 % wall)

Loop #235. C47 (the all-visible scan skip) is blocked on P0-E15, so G3 went to the top-10 queries not profiled
recently, at `44fbe565b` (`tmp/g3-235/`).

- **DS Q72:** nbtree descents (isPostingRaw 10 %, compareFixedPrefix 10 %, readItemID 8 %, findChildBlockDirect 8 %)
  — PG-equivalent work after C18/C36/C37.
- **DS Q10:** the scan. `seqScanOp.Next` 23.4 %, `PageGetHeapTupleNoCopy` 12.2 %, `parseHeapTupleAlias` 4.4 %.
- **The Q39 sizing of the same mechanism** came out at 0.5–1 % (18.03 Ginstr/q over a few million tuples), so Q10,
  which scans per outer row, is the target.

**Symptom.** In `PageGetHeapTupleNoCopy`, 90 % of the samples sit on `return parseHeapTupleAlias(...)`. Half of
them land on one `movups` store: the ~80-byte HeapTuple is assembled with 2- and 4-byte stores, returned by value
through two non-inlined calls, and reloaded 16 bytes at a time, which is a store-forwarding stall. The call line in
`seqScanOp.Next` carries another 29 % of that function's share. PG fills the scan descriptor's HeapTupleData in
place (heapgettup\_pagemode: `tuple->t_data = PageGetItem(...)`, `t_len`), with no struct copy.

**Change.** `storage.PageGetHeapTupleNoCopyInto(p, slot, *HeapTuple)` and `parseHeapTupleAliasInto` fill the
caller's tuple field by field. Every field is written, so a reused tuple carries nothing over; on error the tuple
is untouched. `PageGetHeapTupleNoCopy` and `parseHeapTupleAlias` are now thin wrappers, so one implementation
serves both forms. `seqScanOp.Next` passes a stack tuple. Escape analysis confirms `t does not escape`, so there is
no allocation.

**Measured** (`tools/ds-instr.sh` 3 runs × 2 alternating, and `tools/ds-warm.sh` warm wall, base at `44fbe565b`,
idle host, load 0.5–0.9):

| query | metric | before | after | change |
|---|---|---|---|---|
| TPC-DS Q10 (Target) | Ginstr/q | 6.683 / 6.715 | 6.540 / 6.541 | **−2.37 %** |
| TPC-DS Q10 | warm wall s/q | 0.299 / 0.289 | 0.263 / 0.261 | −10.9 % |

This misses the 3 % bar on the pre-named metric. The sampled share was mostly the stall, which instructions:u
barely count, while wall time (−11 %) and user CPU (−10 %) see it. It is landed below the bar, as C21 and C43 were:
a faithful port with no plan or value change. It goes on the Below-bar list, and the miss counter is now 1.

**Lesson for G5:** when the samples behind a share sit on one store or load right after a call return
(store-forwarding) or a first touch, the instruction metric undercounts. Name wall time on an idle host as the
Target metric instead.

Gates:

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99, with plans 99/99 same.

**Test.** `TestPageGetHeapTupleNoCopyIntoReusesTuple` (storage) reuses one tuple across alternating NULL-bearing and
NULL-free tuples. Each fetch must equal the by-value fetch, and a failed fetch must leave the tuple untouched.
Leaving the bitmap unwritten fails it.

## Slice 120 — C49: the seq scan's visibility check takes the header and snapshot by pointer (Q10 wall −2.8 % median, below the bar)

Loop #236. DS Q10 re-profiled on the C48 binary (`tmp/g3-236/q10.data`). `seqScanOp.Next` is 22.6 %, and 24.6 % of
that is on the call line `transam.TupleVisibleSubxact(tuple.Header, o.ctx.Snap, …)`, about 5.6 % of Q10. Every tuple
passed the header and the ~80-byte `Snapshot` (two slices and a clog pointer) by value, and the Snapshot query
methods (`SeesCommittedXID`, `SeesCommittedXIDHinted`, `HasInProgress`, …) had value receivers.
HeapTupleSatisfiesVisibility (heapam\_visibility.c) takes the HeapTuple and the Snapshot by pointer.

`parseHeapTupleAliasInto`'s header composite store, 52 % of its 10 %, is C48's own site on the same query. The
Below-bar rule makes it ineligible here.

**Change.**

- `transam.TupleVisibleSubxactPtr(*HeapTupleHeader, *Snapshot, …)` is now the implementation.
  `TupleVisibleSubxact` wraps it, so its other 15 callers are unchanged.
- `seesCommittedXIDWithSubxactsPtr` is the same split for `SeesCommittedXIDWithSubxacts`.
- The six read-only Snapshot query methods take `*Snapshot`. None writes the receiver; the only calls are
  `sort.Search` reads.
- `seqScanOp.Next` calls the pointer form with `&tuple.Header, &o.ctx.Snap`.

**Measured** (`tools/ds-warm.sh` warm wall, 6 alternating rounds, base at `12f21f440`, load 1.1–2.0; and
`tools/ds-instr.sh`):

| metric | before | after | change |
|---|---|---|---|
| Q10 warm wall, median of 6 (Target) | 0.265 s | 0.2575 s | **−2.83 %** |
| Q10 warm wall, mean of 6 | 0.2657 s | 0.2572 s | −3.2 % |
| Q10 instructions:u | 6.538 G | 6.458 G | −1.2 % |

Judged by the median, as C44 was, this misses the 3 % bar. The per-round differences run from −1.5 % to −5.5 %,
and the mean would pass. It is landed below the bar: a faithful port with no plan or value change, and an
instruction count that also falls. This is the second consecutive miss (C48, C49), so it triggers the one-task
yield.

Gates:

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99, with plans 99/99 same.

No new test: the visibility logic is unchanged and still reached through the existing callers' tests, the units
suite and every scan gate. The pointer form is the body the by-value wrapper calls.

## Slice 121 — C50: a searched CASE consumes its WHEN comparison as a bool (Q59 instructions −2.6 %, wall ±0, below the bar)

Loop #239, the first slice of C13 (the recursive 48-byte Datum return) after the fresh census `census-20261028b`.
DS Q59 at HEAD (`tmp/g3-239/q59.data`):

- `evalFastExpr` 21.6 %, `applyAgg` 7.0 %, `evalFastCase` 6.4 %, `aggEvalList` 3.7 %.
- In `evalFastCase`, 35 % is on the WHEN evaluation and 19 % on `return evalFastExpr(…, ci.els, …)` for the constant
  `ELSE NULL`.
- Six of Q59's seven `sum(CASE WHEN d_day_name = '<day>' THEN ss_sales_price ELSE NULL END)` per row fail their WHEN,
  and each pays a bool Datum returned through two calls plus a NULL Datum returned through another.

**Change.**

- `fastComparisonBool` holds evalFastExpr's two in-place comparison branches: the non-string `fastCompareDatum`
  branch (slice 75) and the plain-string branch (slice 89). It returns `(result, true)`, or `(_, false)` when the
  general path must run.
- evalFastExpr's comparison arm wraps the result in a bool Datum. `evalFastCase`'s searched WHEN consumes it as a
  bool, as PG's EEOP\_JUMP\_IF\_NOT\_TRUE reads the comparison step's result in place (execExprInterp.c).
- One helper serves both callers, so they cannot disagree. NULL operands never take it.
- `evalFastCaseResult` returns a constant THEN/ELSE arm (`ExprConstVal`, `ExprNullConst`) the way evalFastExpr's
  arms do, without re-entering the evaluator (EEOP\_CONST).

**Measured** (base at `3419876c9`, idle host, load 0.5–0.8):

| metric | before | after | change |
|---|---|---|---|
| Q59 warm wall, median of 6 (Target) | 0.818 s | 0.816 s | **−0.24 %** |
| Q59 instructions:u | 11.075 G | 10.788 G | −2.6 % |

Wall time does not move: the removed instructions were cheap, well-predicted calls, and the cost that remains is
the per-row work around them. This misses the 3 % bar by far. It is landed below the bar, like C43: a faithful port
with an instruction gain and no plan or value change. Miss counter 1.

**Lesson for G5:** a share made of call and return overhead, rather than a stall, shows up in instructions but not
in wall time. Before naming a wall Target, check the share's instruction count with `perf annotate` or a quick A/B.

Gates:

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99, with plans 99/99 same.

**Test.** `TestCaseCompiledMatchesInterpreted` gains two cases: an int comparison (the non-string branch), `<>` and
`<` on the char(9) column, NULL operands falling back, and constant THEN/ELSE arms. Inverting the bool in the new
path produces 11 mismatches against the interpreter.
