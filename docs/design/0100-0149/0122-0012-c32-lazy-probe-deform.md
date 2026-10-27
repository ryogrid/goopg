# M0122-0012 C32 — lazy probe-side deform: a seq scan under a hash-join probe decodes non-key columns only for matching rows

Status: design (2026-10-28, loop #212). Area: `internal/executor` (`seqScanOp`, `joinOp` hash probe). Perf-only:
plans, values and EXPLAIN ANALYZE row counts must be unchanged. Parent: M0122-0012 (banner item 10). This is a
design-sized lever, so it qualifies through its first slice-sized step (rule G4). The Target is written in the
M0122-0012 entry.

## Symptom

TPC-H Q17 (G2 #3 in census-20261027b; 1040 ms over release PG) runs:

    Aggregate
      -> Hash Join  (Hash Cond: lineitem.l_partkey = part.p_partkey;
                     Join Filter: lineitem.l_quantity < (SubPlan 1))
           -> Seq Scan on lineitem          (rows = 5 998 835)
           -> Hash -> Gather -> Parallel Seq Scan on part   (182 rows)

Only 5 471 of the ~6.0 M lineitem rows find a match.

The scan decodes its whole read set for every row before the join sees it: `l_partkey`, `l_quantity` and
`l_extendedprice`, three NUMERICs (`readCols`, slice 19). Q17's instructions:u profile at `a8367e529`
(`tmp/g3-208/h17i.data`):

- `decodeRowRangeInfo` 12.7 %;
- `NumericInt64FromStoredPayload` 9.7 %;
- the two legacy-format probes 3.7 %;
- `skipAttrLen` 2.0 %.

Two of the three decoded numerics, `l_quantity` and `l_extendedprice`, are needed by only 0.09 % of the rows.

## PG mechanism

PG deforms lazily.

- A seq scan returns a buffer-heap slot with `tts_nvalid = 0`.
- `ExecHashJoin` (nodeHashjoin.c, the `HJ_NEED_NEW_OUTER` state) evaluates the outer hash keys through
  `ExecHashGetHashValue`. The key expressions' `EEOP_OUTER_FETCHSOME` step calls `slot_getsomeattrs` up to the
  highest key attribute only, here `l_partkey`, attribute 2.
- Only in `HJ_SCAN_BUCKET`, for a tuple whose hash matched, do the join qual and the projection fetch further
  attributes. `slot_getsomeattrs_int` and `slot_deform_heap_tuple` (execTuples.c) resume from the attribute where
  the last deform stopped.

goopg's own two-phase deform for absorbed scan quals (`prefilterSet` in `seqScanOp.Next`) already follows that
discipline inside one operator. C32 extends it across the scan → hash-probe boundary.

## Design

**Negotiation, at `joinOp` Open, before the probe child's first `Next`.** The join arms deferral only when all of
the following hold:

- the probe child is a `*seqScanOp`, with no operator in between;
- the join is a hash join whose probe keys are plain column references into that scan;
- the scan has a read set (`readCols != nil`);
- the scan has no gist/gin SSI predicate;
- the scan has no ctid consumer;
- the plan is not running inside a parallel worker (that path gets its own later slice).

The join then calls `scan.deferNonKey(keyCols)`, where `keyCols` is the set of columns its probe keys read.

**Scan side.** With deferral armed, `seqScanOp.Next` changes as follows:

- It decodes key columns ∪ absorbed-qual columns in the first pass, with the same `pfInfo` skip table the
  prefilter uses.
- It records the tuple's `(data, bitmap, storedNatts)` and sets `pending = true`.
- It returns the row without decoding the deferred columns.
- The page stays pinned until the scan advances (the existing borrow contract), so the tuple bytes stay valid,
  exactly as a PG slot's buffer pin keeps them.
- `finishDeferred()` decodes the remaining read-set columns from column 0, through `pfInfo` = nil. It then runs
  the detoast step (`DetoastRowBound`) the eager path runs, and clears `pending`.
- The next `Next()` discards an unfinished row: `pending` resets.

**Join side.** The join calls `finishDeferred()` exactly where a probe row is first used beyond its keys:

- before `joinPredicateMatchSlot` for the first match of the row;
- before `outerOnlyEmit` for an unmatched row of a probe-preserving join (LEFT/FULL with the probe as the
  preserved side, and anti joins that emit it);
- before any null-aware NOT IN shortcut that emits the probe row.

A row whose bucket is empty and is not emitted is never finished. That saving is the whole point.

**Invariant, enforced.** While `pending` is set, any read of a deferred column must panic under the existing
`GOOPG_POISON_DEFORM`-style debug flag (`poisonDeformTail`). The deferred cells are poisoned at return in debug
builds, so a missed `finishDeferred` site fails loudly in tests instead of reading the previous row's values.

## Equivalence and tests

- `TestLazyProbeDeformMatchesEager` runs each query with deferral forced on and off and compares the rows, in
  order, over:
  - inner, left (probe preserved), right (build preserved), full, semi, anti and null-aware anti joins;
  - NULL keys;
  - toasted non-key columns;
  - a scan with an absorbed qual on a deferred column;
  - multi-key joins;
  - empty build.
- The debug-poison run of the regress suite and the TPC-H acceptance arm must stay green.
- EXPLAIN ANALYZE: the scan's actual rows are unchanged, because every row is still returned.

## Slice plan

1. **The first slice is the Target slice.** `seqScanOp` direct-child negotiation, the scan deferral, and the join
   finish sites for every join type. Target: TPC-H SF1 Q17 instructions:u, predicted ~9 %.
2. Probe under a Project that only narrows (`buildCompleteChild` chains), and the parallel-worker probe.
3. Index-scan and bitmap-heap-scan probes, which use the same decode helpers.

## Risks

- **A missed finish site would read stale cells.** The debug poison and the forced on/off equivalence test cover
  it.
- **Per-row overhead for a join that matches every row.** The two-pass decode re-walks attribute lengths up to the
  key columns. A join whose estimated probe selectivity is high may skip arming; the first slice arms
  unconditionally and measures Q17 plus the sf025 sweep's instruction-insensitive totals.
