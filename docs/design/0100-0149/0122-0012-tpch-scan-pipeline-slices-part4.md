# M0122-0012 — scan pipeline slices, part 4 (TPC-DS executor gaps)

Continues [0122-0012-tpch-scan-pipeline-slices-part3.md](0122-0012-tpch-scan-pipeline-slices-part3.md) (slices
25–33). Split out on 2026-10-22 under the 800-line design-doc limit; slice numbering is unchanged. From slice 32
on, the levers come from a per-query gap census of the TPC-DS SF0.25 corpus against the `:65438` reference,
taken on a private corpus copy; TPC-H is re-measured for every slice.

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
