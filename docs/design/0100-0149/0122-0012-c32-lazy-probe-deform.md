# M0122-0012 C32 — lazy probe-side deform: a seq scan under a hash-join probe decodes non-key columns only for matching rows

Status: first slice landed 2026-10-28 (loop #213; see "Landed" at the end). Designed in loop #212. Area: `internal/executor` (`seqScanOp`, `joinOp` hash probe). Perf-only:
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

## Landed (slice 1, loop #213)

The implementation (`probe_defer.go`, `seqScanOp.Next`, `joinOp.nextLazy`) differs from the design above in three
ways, all found while measuring.

1. **The first pass stops after the last key column; it does not skip over the rest of the row.** The design's
   skip-table form gave TPC-H Q17 −9.2 %, but its finish re-decoded the whole row from column 0. On joins where
   most probe rows match, every row then paid for two passes: TPC-DS Q59 +13.9 % instructions, Q26 +4.9 %,
   Q7 +2.8 %.

   The landed form follows PG's own `slot_getsomeattrs` resume at `tts_nvalid`:
   - The first pass decodes `[0, probeDeferFrom)`, where `probeDeferFrom` is the last key column + 1, and saves
     the byte offset where it stopped.
   - Read non-key columns *before* the stop are skipped (`probeDeferInfo`), because HammerDB's lineitem puts
     `l_partkey` after `l_quantity` and `l_extendedprice`.
   - Cells past the stop are set to `NullDatum`.
   - `finishDeferred` resumes at the stop from the saved offset. When it skipped early columns, it re-decodes from
     column 0 instead.

2. **Self-disarm.** After `probeDeferDisarmRows` (4096) deferred rows, a scan whose join finished at least half of
   them stops deferring for the rest of the scan. The remaining per-row costs of deferral, the NULL fill and the
   page lock re-taken per finished row, would otherwise tax joins that use most rows. Q59 ends at +0.3 %.

3. **Through a narrowing projection.** TPC-H Q17's probe is Hash Join → Project → Seq Scan; the Project does not
   print in EXPLAIN. `armProbeDefer` accepts a Project whose targets are all bare column references. The finish
   re-copies the Project's output cells from the finished scan row.

Arming, in `joinOp.armProbeDefer` after the probe side opens:

- a hash join that is not batched (`nbatch <= 1`) and not Parallel Hash;
- every probe key a bare column reference;
- the scan borrowing its rows (only then is the returned row the one the join holds);
- none of: an absorbed qual, the GiST/GIN SSI hooks, enum columns, the aclitem renders, extra ctid columns, or
  the debug deform poison.

Every `routeProbeRow` site finishes the row first, so a batch file never receives a pending row.

Finish sites: before the residual for a hash match (inner/outer and semi/anti), before every emit of the probe
row (unmatched fill, anti, null-aware NOT IN with an empty build), and before batch routing. The only exits that
never finish a deferred row are a hash miss on a join that does not preserve the probe, and a NULL key under
NOT IN; PG never fetches past the key for those rows either.

**Measured** (instructions:u, 5 runs × 2 for the Target, 2 runs for the others; fresh A/B against `2349ef9b0`):

| query | before | after | change |
|---|---|---|---|
| TPC-H Q17 (Target) | 27.281 / 27.291 / 27.310 | 24.960 | **−8.6 %** |
| TPC-DS Q8 | 5.202 | 3.491 | −32.9 % |
| TPC-DS Q26 | 4.325 | 3.416 | −21.0 % |
| TPC-DS Q7 | 7.558 | 6.220 | −17.7 % |
| TPC-DS Q59 | 11.628 | 11.660 | +0.3 % (disarmed) |

Gates: units; spotcheck; acceptance 24 MATCH (Q17 MATCH); sf025 PASS=99 with plans 99/99 same; fire-set PASS
(no fires); RegressSuite counts equal to the slice-111 run except the plpgsql flapper, with `join`, `join_hash`,
`aggregates` and `subselect` byte-identical; isolation suite PASS.

Test: `TestLazyProbeDeformMatchesEager` runs 17 queries with deferral on and off and requires identical rows, in
order. It covers:

- inner, left, semi, anti and NOT IN joins;
- a residual on a deferred column;
- multi-column keys;
- a narrowing projection;
- an empty build;
- a key placed after other read columns;
- a toasted non-key column;
- 10 000 all-matching rows, which crosses the disarm threshold mid-scan.

A test hook counts arms, so the deferred path is proven to run (12 of the 17 queries arm; the others decline by
design).

**Left (ledgered):**

- probes under a Parallel Hash join or a batched join;
- index-scan and bitmap-heap-scan probes;
- keys that are expressions;
- scans with an absorbed qual;
- RIGHT/FULL joins whose probe scan is not borrow-armed.

PG's deform is lazy for every consumer; goopg's is lazy only at this one boundary.
