# M0146-0005 slice 23: `M0146-0005v` — btree skip scan (PG18 `_bt_skiparray`)

Implementation slice. Filed at slice 22: Q82's `join-method` depth=7
record — PG probes `inventory_pkey` on the non-leading `inv_item_sk`
column by enumerating the 209 distinct `inv_date_sk` values and running
one bounded descent each; goopg admitted only gapless leading-prefix
index keys, so the `{item,inventory}` join could never file the probe
and elected a Parallel Hash Join.

## What landed

**Path/plan metadata.** `Path.IndexSkipPrefix` + `IndexScan.SkipPrefix`:
`Keys[i]` binds `Index.Columns[SkipPrefix+i]`; the first `SkipPrefix`
key columns are procedurally bound, not clause-bound. Legal only with
`Keys` (never `Key` — a one-column skip probe must NOT bind column 0),
and never with SAOP/range shape — the executor reports a mixed shape
rather than guessing (fail-closed).

**Planner admission** (`pathparamindex.go`):
- `addOneParameterizedSkipPath` runs after the ordinary prefix arm, so
  both candidates exist per outer relset — PG generates all usable
  index paths and lets costing elect (`match_clauses_to_index`,
  indxpath.c).
- `pickIndexSkipRun`: btree, ≥2 columns, first bound column past column
  0, contiguous equality run, skipped columns ordinary ASC (expression
  indexes and DESC skips declined this slice).
- `indexSkipNullSafe`: every unbound index column must be NOT NULL
  unless the index stores NULL-keyed entries — a skipped prefix value
  absent from the index would silently drop rows otherwise.
- `indexSkipClauses` / shifted `indexCol` positions
  (`SkipPrefix+i`), parameterization + target metadata carried.

**Costing** (`costindex.go`): PG's `num_sa_scans` machinery
(genericcostestimate selfuncs.c:7086-7100 + btcostestimate
:7391-7577, :7715-7719, :7780):
- `skipScanDescents`: product starts at 1; each skipped leading column
  multiplies by ndistinct (+1 when no inequality restricts it); a
  default ndistinct estimate or a cumulative product past
  `index->pages` REVERTS that column — the path stays admitted but the
  bound set loses the run's quals, so `boundSelectivity` splits from
  heap-side `selectivity` and the index side prices the whole index.
- `btreeIndexAMCostPages`: numSAScans clamped to
  `max(1, ceil(pages/3))`; `numIndexTuples = rint(numIndexTuples /
  num_sa_scans)` (math.RoundToEven) before page sizing; tuple CPU
  multiplied back by numSAScans; descent charged once per descent.
  The division applies to SAOP multi-descents too — same upstream
  line — which moved `TestSAOPNumSAScansCost`'s clamp-case fixture.

**Plan creation** (`createplanindex.go`, `createplannl.go`): validates
`IndexSkipPrefix` bounds and shifted clause positions
(`c.indexCol == SkipPrefix+i`), rejects unparameterized skip paths
(this slice only produces parameterized ones), forces skip probes into
`Keys` in both decomposed and fused NLI builders.

**Executor** (`operators_index.go`): `rescanSkip` evaluates bound
suffix exprs once per rescan; `skipNextGroup` lazily enumerates
distinct skipped-prefix values via a cursor and opens one bounded
probe per group — no materialized skip list, so LIMIT/semi/anti
consumers stop early. Tuple format decodes datums
(`pgIndexTupleKeyDatums`) and re-encodes truncated pivots (exclusive
lo = "first entry past this prefix"); blob format walks
`decodeIndexKeyColumn` segment boundaries and byte-increments the
prefix. NULL suffix probe → empty scan (same early-out as
`lookupKeys`). SSI/kill-list lifecycle integrated; `Rescan`,
`openPrep`, `Close` reset skip state; skip dispatch precedes SAOP
dispatch so a mixed shape is refused, not silently re-interpreted.

**EXPLAIN** (`operators_explain.go`): skip-aware `formatIndexCond`
renders `Index.Columns[SkipPrefix+i] = key` — the bound column name,
not the skipped leading column and never the procedural values.

**Sibling-path audits.** `deformIndexLeafBound` unions all index
columns (position-agnostic, safe); `deformNLIOuterKeyExprs` appends
`Keys` wholesale (safe); `subquery_parallel.go`'s `c := *x` copy
carries the field; `unnest.go`'s `harvestKey` gained the
`SkipPrefix+i` offset. `lateralProbeIsPartialProbe` stays at
`len(Keys) > 0`: a skip probe under a partial NL is a per-worker
rescan — exactly PG's `Gather > NL > Index Scan` skip shape — and
refusing it is a path/node twin mismatch that panics at
`gatherChildPlan` (measured: Q16/Q72 on :65437's stats).

**Executor-capability bounds** (`pathparamindex.go`). PG files every
usable skip path and lets cost decide — PG's executor then makes that
election affordable. goopg's per-descent and per-heap-tuple costs are
~10-30x PG's, so a skip election that is marginal under goopg's model
can be catastrophic under its executor, and the model's own numbers
are the only available guard:

- `maxSkipProbeRows = 600`: a probe estimated to return more than
  ~600 rows per rescan is a bulk scan in disguise — every row is a
  heap fetch + visibility check. It is reachable through ANY outer
  rel that supplies the bound column, so the bound is on the probe's
  own row estimate, not on the parameterisation.
- `maxSkipProbeLifetimeRows = 5e8`: `rows × loopCountFor(req)` — the
  modelled lifetime output over the probe's nested-loop life — catches
  the thin-probe/huge-modelled-outer signature (the same loop count
  PG's `get_loop_count` produces).

Declining is fail-closed: the rel keeps its ordinary parameterized
and restriction paths and the join search elects among them. Every
verified skip election (Q16/Q37/Q82/Q94 at both scales, Q72 at
SF0.25) sits well under both bounds; the one election past them is
the Q72 SF1 pathology below.

## Measured

`m0146-0005v.plans.txt` / `-pg.plans.txt`: Q82 now elects
`Nested Loop -> Index Scan using inventory_pkey
Index Cond: (inv_item_sk = item.i_item_sk)` — PG's shape minus the
parallel/IOS/upper-rel substrate (D3-partialpath family, owner
elsewhere). Correct rows: Q82 = 0 rows = oracle; positive probe
`item_sk IN (4,8,12)` = 1570 rows = PG's 1570.

Fire-set census delta (fireset/, baseline HEAD vs candidate):
- SF0.25: `jointree-search` 23 -> 21 (Q82 and Q37 leave the class for
  D3-partialpath — join shape now right, remaining diff is the
  partial-path substrate); `join-method` 45 -> 43,
  `join-order` 73 -> 72, `qual-placement` 26 -> 25.
- SF1: `join-method` 53 -> 50, `parameterisation` 44 -> 43,
  `aggregation-strategy` 39 -> 37; `D3-partialpath` 29 -> 26.
  Q72's plan reverted to baseline (bound declined its skip probe), so
  it left the SF1 fire set entirely; `D1-sublink` 7 -> 8,
  `D4-upperrel` 30 -> 32, `sort-strategy` 58 -> 59 and
  `qual-placement` 20 -> 22 moved the other way.
- moved plans all execute PASS on both arms at both scales:
  `FIRE-SET-TIMEOUTS: introduced=none unchanged=none missing=none`.

SF0.25 self-diff (plans-022905 -> plans-033255): 5 changed
{Q16 Q37 Q72 Q82 Q94}, all into-Nested-Loop — each is a skip-scan
election matching PG's shape (Q16 probes `cr_order_number` on
`catalog_returns_pkey` and `cs_order_number` on `catalog_sales_pkey`;
Q37/Q72/Q82 probe `inv_item_sk`). Q72 runtime 0s->168s: the parity
shape IS the per-outer-row probe PG also picks — slow because goopg's
probe machinery is slower, not because the shape is wrong. Report-only
channel; recorded, not a gate failure.

**Q72 at SF1 — the fire-set timeout and the work bound.** The same
skip election at SF1 produced a plan that timed out (>600s). PG's SF1
Q72 elects a different order: `date_dim d2` joins the
`catalog_sales`-arm first via Parallel Hash Join on `d_week_seq`, so
`inventory_pkey` is reached with BOTH leading columns bound — a
1-row/execution prefix probe costing 5.98. goopg's baseline elected
the same order. The candidate instead elected `NL(cs-arm ->
inventory-skip) -> NL(inv -> d2)` — the model priced it ~8% cheaper
(60.5k vs 69.1k) because the ~209-descent probe costed 109.84 under
`loopCountFor({catalog_sales})` ≈ 1.44M-row amortization while the
NL's actual outer ran ~9.5k rows. Measured directly on a private
clone: ~980ms per skip rescan (~418 descents + ~941 heap fetches)
× ~9.5k rescans ≈ 2.6h. PG's own model also files this path and
rejects it — the divergence is ~8k of accumulated join-cost drift in
the surrounding nodes (its prefix-order subtree prices ~52k where
goopg prices ~60k), the `join-order`/`costing` family this burn-down
tracks separately. Within the slice the fix is the
`maxSkipProbeRows`/`maxSkipProbeLifetimeRows` admission bounds above:
the 709-row probe is declined under every outer relset, the search
re-elects the d2-first order (verified: plan byte-identical to
baseline, executes in ~5s), and SF1 parity IMPROVES — goopg's plan
once again matches the order PG elects. At SF0.25 the 527-row probe
stays admitted, preserving the PG-matching election (Q72 executes
168s — the parity shape, slow but inside the window).

## Incidents inside the slice

- First `lateralProbeIsPartialProbe` draft refused `SkipPrefix > 0`
  "conservatively" — wrong direction: the path twin
  (`partialPathDrivingKind`'s parameterized-IndexScan arm) already
  admitted the probe, so Gather-over-NL-inner election produced a
  `PathGather` whose node-twin `drivingScan` refused →
  `createplannl.go` panic `PathGather over a subtree with no driving
  scan` on Q16/Q72. Reverted; `partial_lateral_test.go` pins the skip
  probe's admission.
- `rescanSkip`'s blob arm encoded suffix segments with
  `encodeBTreeKeyForColumn` directly — tripped
  `TestIndexProbeKeyIsTheOnlyScanSideEncoder` (the funnel exists so a
  tuple-format tree never gets a concatenated blob). Routed through
  `indexProbeKey`, whose blob arm IS the concatenation.
- `TestParameterizedIndexPathsBindCompositeIndexFromTwoOuterRels`
  expected `{supplier}` unusable — that was exactly the premise skip
  scan removes; updated to expect the {1,2,3} parameterization set.

## Deferred (ledgered)

- Unparameterized skip scans (`col = const` on a non-leading column):
  `createIndexScanPlan` rejects them this slice — PG's
  `match_clauses_to_index` files them for restriction/bitmap paths too.
- Skip over DESC skipped columns and expression indexes: declined at
  admission.
- Blob-format NULL-keyed-index admission: `indexSkipNullSafe` uses
  catalog NOT NULL; the null-keyed-entry catalog flag path is plumbed
  but untested end-to-end (no such index exists in TPC-DS).
- Bitmap/IOS skip variants: skip paths are parameterized IndexScan
  only.

## Gates

See `gates.txt`. Units, tpch-spotcheck, tpcds-sf025 sweep (96 PASS,
0 ERROR — after the lateral-probe revert), tpch-acceptance-arm
(24/24 MATCH), tpcds-fireset-gate (census delta above; fires PASS).
