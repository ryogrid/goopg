# M0146-0034: the parallel post-pass keeps an index scan's ORDER BY

Status: landed 2026-10-02 (banner item 2a, S2).

## Defect

On TPC-DS SF0.25:

    select d_date_sk from date_dim where d_year = 1998 order by d_date_sk limit 1

The serial plan is `Limit -> Index Scan using date_dim_pkey`. The index
delivers the ORDER BY, so the search drops the Sort. The parallel post-pass
(`MaybeAddGather`, run by the postmaster on the finished and possibly
cached plan) then wrapped the scan in a **plain** `Gather`. A plain Gather
interleaves the leader's and the workers' streams, so the "first" row was
whichever stream answered first: 2450926 instead of 2450815, on 1 run in 6
here and 6 of 12 when filed. regress limit.sql showed the same thing with
`ORDER BY unique2 LIMIT 10`.

PG never claims pathkeys for a Gather (`create_gather_path`). It orders a
partial path with Gather Merge (`generate_useful_gather_paths`). goopg's
path model already does this (`makeGatherPath` sets `Pathkeys: nil`), and
in-process planning with statistics chose Gather Merge correctly. Only the
post-pass, which sees neither the statement nor the elided Sort, got it
wrong.

## Change

- **Plan time** (`markIndexOrderRelied`, called at the end of
  `PlanWithSettings`). When the top-level SELECT has an ORDER BY, walk the
  post-pass's own descent (`parallelChildren`, then `drivingScan`) through
  order-preserving nodes. Set `OrderRelied` on the index scan
  (`IndexScan` / `IndexOnlyScan`) that delivers the order. A Sort, hashed
  Aggregate or set operation on the way stops the marking.
  - The flag lives on the plan node, so it survives the plan cache. The
    extended protocol reuses a cached plan without parsing the statement
    again.
- **Post-pass** (`indexOrderReliedOn`, `indexOrderKeys`). For a plain
  target driven by an index scan, the scan's order is relied on when
  either:
  - the scan is `OrderRelied`; or
  - a node between root and target consumes order: a sorted Aggregate,
    WindowAgg or DISTINCT ON.

  Neither may sit under a Sort, hashed Aggregate or set operation, which
  end the check with "not relied on". When the order is relied on, the
  leader builds a **Gather Merge** keyed on the index key columns, with
  their declared direction and NULLS placement, stated over the target's
  output (`partialTarget.orderKeys`). If the keys can't be stated (an
  expression column or a column not visible), or `enable_gathermerge` is
  off, the plan stays serial. A wrong plain Gather is never the fallback.
- A Limit without ORDER BY does not count as relied on, so
  `... WHERE d_year = 1998 AND d_date_sk > 2450000 LIMIT 1` keeps a plain
  Gather, as PG's pathkey-free Gather would.

## Verification

- The repro returns 2450815 in 10/10 simple-protocol runs and 12/12
  extended-protocol runs (cache hits included), and the plan shows
  `Limit -> Gather Merge -> Parallel Index Scan`.
- `TestParallelPostPassKeepsIndexOrder` (executor) covers three plans,
  each first planned serially and then passed through `MaybeAddGather`:
  - the ORDER BY…LIMIT plan becomes a Gather Merge on the index key, never
    a plain Gather;
  - with `enable_gathermerge` off it stays serial;
  - a hashed GROUP BY gets no Gather Merge.

  On the old code the first case is a plain Gather.
  `TestExplainParallelIndexScanLabel` (a bare range scan with no ORDER BY)
  still gets its plain Gather.
- Regress A/B: `limit`, `select_parallel` and `write_parallel` are
  unchanged. Their tables are small enough to stay serial in the runner.
- The fire set changed no TPC-DS plan. Units, spotcheck, sweep 96/96, arm
  (values identical) and ea-ratchet pass.

## Left open (ledgered)

PG keeps this query serial; goopg's size-rule post-pass parallelises it.
That is the pre-existing post-pass-vs-cost divergence, now with the
correct node.
