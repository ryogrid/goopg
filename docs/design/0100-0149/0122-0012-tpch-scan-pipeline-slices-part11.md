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
