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
