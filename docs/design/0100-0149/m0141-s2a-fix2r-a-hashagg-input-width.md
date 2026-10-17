# M0141-S2a-fix2r-a — the hashed aggregate's spill arm prices PG's `input_width`

Status: done 2026-10-10 (4bf11399d). Parent: M0141-S2a-fix2r.

## Problem

At SF1, TPC-DS Q4 and Q11 diverged from PG in their CTE `year_total`.

- **PG** runs a serial `HashAggregate` for every arm.
- **goopg** ran the store arm (and Q4's catalog arm) as
  `GroupAggregate ← Gather Merge ← Sort`.
- The group counts agree: about 2.63M for the store arm.

## PG behaviour

`cost_agg`'s AGG_HASHED spill tail (costsize.c:2783-2840) sizes everything
from `input_width`, which is the input path's `pathtarget->width`. The same
value feeds two places:

- **`hash_agg_entry_size`** (nodeAgg.c:1701) sizes one entry as:
  - `TupleHashEntrySize()`, which is 16 bytes;
  - `MAXALIGN(SizeofMinimalTupleHeader + width)`;
  - one `AggStatePerGroupData` per transition;
  - the transition space.
- **The pages term** is `relation_byte_size(input_tuples, input_width) / BLCKSZ`.

`input_width` is the sum of `set_rel_width`'s per-Var widths. Each Var is
sized by `get_attavgwidth` (the ANALYZE stawidth) when that is positive,
and by `get_typavgwidth` otherwise.

For Q4's store arm the input is 213 bytes per row:

- entry: 2.63M groups × about 400 bytes ≈ 1.05 GB;
- hash_mem: 512 MB `work_mem` × `hash_mem_multiplier` 2 = 1 GB;
- result: the table fits, so PG charges no spill.

## Cause

M0141-S2a-fix2r priced both terms in `hashsize.EntryBytes(ncols, avgVar)`
(48 × ncols + 24 + avgVar). That is goopg's executor-row footprint. fix2r
argued it was "the same currency as the sorted rival". But PG prices the
rival in `input_width` too, and the executor footprint is about 5× PG's
width.

For Q4's store arm, goopg's entry came to 1256 bytes, so the hash table was
3.3 GB. The arm spilled, and the parallel sorted path won.

## Change

- **`costAgg`'s width input.** The two inputs (`inNcols`, `inAvgVarBytes`)
  become one `inWidth`, PG's `input_width`. `inWidth == 0` means "no width"
  (this is what `addDistinctPaths` passes) and still declines. Any known
  width fires the arm, which keeps fix2r's guard widening.
- **`aggInputPGWidth`** (groupingpaths.go) sums the aggregate's kept input
  columns (`agg.InputTarget`) the way `set_rel_width` does:
  - the stawidth from the searched input rel's per-column map
    (`ColVarBytes`, filled from `ColumnStats.AvgWidth`) when it is positive,
    truncated to an integer as pg_statistic stores it;
  - otherwise `typeWidth`, which is `get_typavgwidth`.
- **Callers.** Every grouping and partial-aggregate caller passes this
  width.
- **Unique-ify.** `create_unique_path`'s hashed arm reads
  `pathWidth(subpath)`, as PG reads `subpath->pathtarget->width`.
- **Pages term.** It uses `pgRelationByteSize`, the same function the PG
  sort currency uses.
- **`hashAggEntrySize`** adds `TupleHashEntrySize()` (16 bytes). Its own
  comment had recorded this term as missing.

### Why stawidth and not the type width alone

The first attempt used `tupleWidth` (type widths only). It moved Q4's
catalog arm but not the store arm. The plan schema's `bpchar` and
`numeric` columns carry no typmod (`Type.Args` is empty), so each one
prices `varlenaDefaultWidth` (32). The store arm's 12 columns came to 356
bytes, and 2.63M × 408 bytes overflowed hash_mem by 0.05%.

With stawidth the arm is 112 to 129 bytes and fits, as in PG.

## Verification

- **Tests.**
  - `TestCostAggHashedSpillsOnPGWidthNotExecutorWidth`: Q4's store-arm
    numbers, in PG's width and PG's hash_mem, must charge no spill.
  - `TestAggInputPGWidthReadsStawidth` checks three things:
    - a positive stawidth is used and truncated;
    - a zero stawidth falls back to the type width;
    - columns that are not read are ignored.
  - `TestHashAggEntrySizeAndLimits` is re-pinned for TupleHashEntrySize.
  - The costAgg fixtures move to the one-width signature.
- **TPC-DS fire set.**
  - SF0.25: no query fires.
  - SF1: Q4 and Q11 fire. Every `year_total` arm is now a serial
    `HashAggregate`, as in PG.
  - Both queries lose `aggregation-strategy` and `parallelism`. SF1
    CATEGORIES-EXCL-MATCH: aggregation-strategy 10 → 8, parallelism
    35 → 33. Matches are unchanged (56/42).
  - Fire-set execution returns identical values for Q4 and Q11.
- **TPC-H.** Plans are byte-identical to m150, and the acceptance arm
  matches on values (24/24).
- **Other gates.** Units, spotcheck, SF0.25 sweep (PASS=99, plan shapes 99
  same) and ea-ratchet (current 1) all pass.
- **Regress A/B** (32 cases).
  - `aggregates` loses 22 diff lines: `group by g%10000` over
    `agg_data_20k` is now PG's `HashAggregate`.
  - `join` shows only its known int8_tbl ordering flap.

## Not covered (filed / ledgered)

- **Bpchar stawidth.** goopg's ANALYZE stawidth for `bpchar` leaves out the
  blank padding. For `c_first_name char(20)`, goopg records 6 bytes; PG
  records 21. goopg's widths are therefore smaller than PG's, which can
  only make goopg's hash table fit where PG's spills. Filed as
  M0141-S2a-fix2r-b.
- **Lost typmods.** The plan schema's types carry no typmod, so
  `get_typavgwidth`'s fallback prices `char(n)`, `varchar(n)` and
  `numeric(p,s)` at the 32-byte default. This affects every width without
  stats, not only this arm. Filed as M0141-S2a-fix2r-c.
- **Partial-aggregate width.** The partial-aggregate producers'
  `aggInputPGWidth` reads an unnarrowed child (`InputTargetKnown` is
  false), so their spill arm prices the full join row (369 bytes for Q4's
  store arm). PG's partial path reads the narrowed scan/join target.
  Ledgered.
- **Transition space.** `transitionSpace` is still 0, because goopg has no
  `aggtransspace`. This under-charges; it is unchanged.

## Correction (2026-10-10, M0141-S2a-fix2r-c)

The "typmod-less bpchar, 356 bytes" finding above was an artifact. The
private SF1 clone was started twice with the debug binary. The first start
scanned the heap (662 bytes, typmods present) and wrote the M0114 JSON
catalog cache. The second start registered tables from that cache, which
drops typmods. The planner's schema keeps typmods; the cache is now
retired. The stawidth-based width this task landed is unaffected.
