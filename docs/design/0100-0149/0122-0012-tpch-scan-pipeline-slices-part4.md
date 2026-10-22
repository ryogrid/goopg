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

## Slice 38 (2026-10-22): a CTE scan returns one slot — TPC-DS Q81

**Target, named up front:** TPC-DS Q81, ~7.8 s on an idle host (10–12 s under the nightly's load) against
PG's 3.8 s.

**Cause.** PG spends almost all of Q81 (4.6 s) in a correlated SubPlan over the `customer_total_return` CTE.
It makes 6 893 calls, each scanning the 6 893-row CTE for `ctr1.ctr_state = ctr2.ctr_state` at ~0.58 ms per
call. goopg runs the same plan, single-threaded. Its profile has 21 % in `SlotFromRow` called from
`cteScanOp.Next`: a fresh `MaterializedSlot` was heap-allocated for every row of every rescan. PG's CteScan
stores every tuple into its single `ss_ScanTupleSlot` (nodeCtescan.c `CteScanNext`).

**Rule.** `cteScanOp` and its sibling `materializedCTEScanOp` keep one `MaterializedSlot` and re-point it at
each cached row. The rows are the CTE cache's own and outlive the call. Only the slot is reused, under the same
per-Next contract that the stack-aliased slots of `projectOp` and `indexScanOp` follow.

**Pin.** `TestCTEScanReusesOneSlot` covers both CTE scan kinds:

- the same slot comes back for every row;
- it carries the cached row itself;
- the values come back in order, followed by EOF.

Restoring `SlotFromRow` fails it.

**Measured.** Warm, two interleaved rounds of three:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q81 | 9.87–11.84 | 7.48–8.17 |

The gain is ~30 %, more than the 21 % profile share, because GC pressure falls with the allocations. The sf025
sweep's 99-query sum went from 168.2 s to 159.3 s, with Q81 at 7.5 s in the sweep. Plans are unchanged and the
values match (61 checksum-verified).

**Left.** The SubPlan's comparison still runs through the interpreted evaluator, because the compiled path does
not take the `OuterColumnRef` operand. Each call:

- lowercases both type names (`declaredBpcharTypmod`, `strings.ToLower`);
- sniffs the string values for pg\_lsn / UUID / row shapes (`textShapeAmbiguous`).

The sniffing is ledgered since slice 30, and the per-call lowercasing now has a row of its own.

## Slice 39 (2026-10-22): a Gather under an aggregate ships only the columns the aggregate reads — TPC-DS Q23

**Target, named up front:** TPC-DS Q23, ~5.2 s warm at HEAD against PG's 2.0 s. It had the largest
goopg/PG ratio of the remaining SF0.25 gaps.

**Cause.** 11.6 % of Q23's profile was `cloneRowOwned`, called from `MaterializeForTransfer` in the Gather
workers' send path. Every worker row is deep-copied before it crosses the queue, so the leader never reads
memory that a worker's arena recycles. Q23's Gathers sit under simple HashAggregates:

- `frequent_ss_items` groups by `substr(i_item_desc, 1, 30)`, `i_item_sk` and `d_date`;
- `best_ss_customer`'s InitPlan sums `ss_quantity * ss_sales_price` per `c_customer_sk`.

Their worker rows are whole store\_sales ⋈ date\_dim ⋈ item / customer join rows, around 50 columns, of which
the aggregate reads three. PG's workers send the projected tuple, which holds only the targetlist the parent
needs. goopg's Gather sits below that projection, so it shipped everything.

**Rule.**

- **`buildCompleteChild` builds the Gather itself.** This is slice 35's consumer-complete chain: a plain or
  partial Aggregate, whose arms are the only readers of its child's rows. It builds the Gather through the
  shared `newGatherNode` and stamps `gatherOp.liveCols` from `consumerLiveColumns(consumers)`.
- **Workers send masked rows.** They call `transferRowForQueueLive`, which goes through `cloneRowOwnedLive`:
  only live cells are detached from the producer arena; dead cells are NULL, or the deform poison when that is
  armed.
- **`consumerLiveColumns` only asks which cells a consumer may read.** So unlike `deformScanVisit` it walks
  through function calls and operators of any kind. It uses `optimizer.WalkExprHostScope`, which fails closed on
  unknown kinds, and returns nil (every column) on anything that can read the row as a whole or outside its
  cells:
  - a whole-row or negative `ColumnRef`;
  - `MergeWholeRowRef`;
  - a ctid or tableoid read;
  - a sublink.
- **Every other Gather keeps every column.** A Finalize aggregate reads its child's state columns without an
  arm, so it is not consumer-complete. The leader's own rows pass up unmasked.

**Pins.**

- `TestConsumerLiveColumns`: a function call's arguments are live; no consumer gives an empty, non-nil mask; a
  sublink, a whole-row reference or a ctid read gives nil.
- `TestGatherUnderAggregateShipsLiveColumns`: a simple aggregate over a Gather gives exactly the arm columns,
  and a Finalize aggregate gives nil. Dropping the stamp fails it.
- `TestCloneRowOwnedLive`: live cells are copied and dead cells are NULL.
- The executor package also passes with the deform poison armed, which poisons every dead cell.

**Measured.** Warm, four interleaved rounds of three, two of them in reversed order:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q23, mean | 5.10 (range 3.98–6.49) | 4.49 (range 3.88–4.86) |

That is ~−12 %. The `frequent_ss_items` CTE alone moved only from ~1.25 to ~1.2 s, so most of the gain is in
`best_ss_customer`'s Gather, whose join rows are wider. The sf025 sweep's 99-query sum went from 159.3 s to
155.0 s, with Q23 at 3.66 s in the sweep. TPC-H is flat (15.05 / 15.06 s; it has no simple aggregate over a
Gather).

**Left.**

- **GatherMerge and the leader's own rows are unmasked.**
- **The workers still evaluate and deform columns nobody reads.** The mask only stops them from being copied.
  PG's workers project before sending; goopg's plans put the projection above the Gather. This is recorded in
  the deferral ledger.

## Slice 40 (2026-10-22): the legacy Filter evaluates a compiled predicate — TPC-DS Q81, Q1

**Target, named up front:** TPC-DS Q81, ~7.4 s warm at HEAD against PG's 3.8 s.

**Cause.** 97 % of Q81's CPU sat in `filterOp.nextPerRow` → `evalExprSlot`. It is the correlated SubPlan's
`ctr1.ctr_state = ctr2.ctr_state` over the CTE scan, evaluated about 47 M times. The interpreted evaluator
re-derives every static fact of the comparison on each call:

- both operands' declared bpchar widths (`declaredBpcharTypmod`, which lowercases the type name with
  `strings.ToLower` each time);
- whether both are character strings (`exprIsCharacterString`).

A SubPlan's inner tree is built by the legacy `buildNode`, whose `filterOp` always interpreted. Only the slab
path's OpFilter ran the compiled evaluator, whose node carries those facts in its payload.

**Rule.** `newFilterOp` compiles the predicate once into an `exprTreeSlab`. `nextPerRow` evaluates it with
`evalFastExpr`, exactly as the slab path's `filterOpNext` does. Kinds the compiler does not know fall back to
`evalExprSlot` inside the slab, so results stay the interpreter's. A hand-built `filterOp` with no compiled
slab keeps the interpreted path.

**Found and fixed: the batch filter disagreed with both per-row twins.** The pin below drives `filterOp` with
its batch path on and off. With batching on, `evalFilterBatch` computed `evalBinaryBatch` over the raw values
and skipped two rules both per-row twins apply first:

- **bpchar semantics:** a `char(n)` operand, or a bare literal opposite one, compares with trailing blanks
  stripped (`comparisonOperandsAsBpchar`). So text `'ab'` = `'ab  '::char(4)` came out false.
- **the pg_lsn-shape comparison.**

`evalFilterBatch` now re-evaluates through the interpreter exactly the rows those rules could touch:

- either operand is declared bpchar;
- a bare literal faces a string;
- a value has the pg\_lsn shape;
- two character strings have a sniffed shape.

Non-string rows keep the batch result. Plain SQL did not reach the bug in this pin's probes: a filter directly
over a scan is absorbed into the scan, and `t = b` returned PG's {1}. The batch path serves only a
batch-eligible Filter above a non-scan child.

**Pin.** `TestFilterOpCompiledPredicateMatchesInterpreted` runs every case with the batch path on and off and
requires exactly the rows `evalExprSlot` keeps. It covers:

- `=` `<` `>` `<>` over int, text and `char(4)` columns and bare literals;
- padded and unpadded values;
- the sniffed shapes: row, array, pg\_lsn and uuid;
- NULLs.

Restoring the old `evalFilterBatch` fails it on `t = b (batch=true)`.

**Measured.**

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q81, warm, two interleaved rounds | 7.27–8.95 | 5.38–5.47, one 7.12 |

In the sf025 sweep, the 99-query sum went from 155.0 s to 147.2 s. Q1, another correlated subquery over a CTE,
went from 17.8 to 11.2 s (PG 13.3 s). TPC-H is flat (14.81 vs 14.65 s, `GOGC=100`).

**Left.** `textShapeAmbiguous` still sniffs the values on every call, in the compiled arm too. That is the
slice 30 ledger row: the operand types are not carried everywhere.

## Slice 41 (2026-10-22): an exclusive lower bound descends past its group — TPC-DS Q72

**Target, named up front:** TPC-DS Q72, ~8.5 s warm at HEAD against PG's 3.4 s.

**Cause.** 55 % of Q72's CPU was `leafScanStart`, the binary search inside a leaf. Counting showed 28 M leaf
searches for 0.93 M cursors, about 30 leaves per cursor; PG reads about 4 pages per index search.

The source was the skip-scan walk's past-the-group jump (slice 33): `NewScanCursor(gPivot, nil, excl)`.
`descendToLeaf` searches with `compare`, which reads a bound naming only a prefix as minus infinity beyond it.
So an exclusive bound landed at the FIRST leaf of its equal-prefix group. The cursor then binary-searched every
leaf of that group (about 70 for one inventory date) only to skip all of their entries.

PG's `_bt_first` handles a `>` bound with `nextkey = true` (nbtsearch.c):

- `_bt_binsrch` picks the first item past every key the bound names;
- `_bt_moveright` moves right while the high key is at or below the bound.

The descent therefore lands after the group.

**Rule.**

- **`descendToLowerBound(lo, loExclusive)` is the entry point.** It is `descendToLeaf` for an inclusive bound,
  and `descendMode(..., nextKey=true)` for an exclusive one.
- **The nextkey descent uses `compareHigh` on both tests.** `findChildBlockDirect` takes the first item with
  `compareHigh(item, lo) > 0`, and the right-link check moves right on `keyAtOrPastHighKey`, i.e. while
  `compareHigh(highkey, lo) <= 0`.
- **The scans use it.** `NewScanCursor` and the eager `rangeScanPosLeaf` descend through it. Their per-leaf
  "nothing here reaches lo" skip is `lowerBoundPastPage`, the same test, so a leaf the descent could not avoid
  costs one high-key comparison instead of a binary search.
- **No result changes.** `scanLeafItems` already treats an exclusive lower bound as "skip the whole equal-key
  group" (`compareHigh(key, lo) <= 0`), so the descent only reaches the same first entry sooner. For the blob
  format `compareHigh` is `compare`, so its descent is unchanged.

**Pin.** `TestExclusivePrefixBoundDescendsPastTheGroup` builds a 2-column tree whose a=2 group spans many
leaves. It checks four things:

- the nextkey descent lands on the leaf of the first a=3 entry, or its left neighbour;
- the inclusive descent lands elsewhere;
- the cursor and the eager scan return exactly the entries past the group;
- the cursor reads at most one leaf before the first of them.

Disabling the nextkey descent fails it. The nbtree package passes under `-race` (98 s).

**Measured.** Warm, two interleaved rounds of three:

| query | HEAD (s) | candidate (s) | PG (s) |
|---|---|---|---|
| TPC-DS Q72 | 8.38–10.42 | 2.31–2.60 | 3.4 |

That is −71 %, and goopg is now ahead of PG on Q72. Q72, Q94, Q95 and Q16 output is byte-identical to
`:65438`. In the sf025 sweep, the 99-query sum went from 147.2 s to 142.0 s, with Q72 at 3.1 s. TPC-H is flat
(15.11 vs 15.21 s, `GOGC=100`).

**Left.** A later leaf that the walk does read is still binary-searched against the bound, where `_bt_readpage`
starts at the first item on any page after the first. goopg also still descends twice per prefix group in the
weekly-dates case, as PG does without skip-support hits. Both are recorded in the deferral ledger.

## Slice 42 (2026-10-22): one identity-key memo per search, and an indexed sameClass — TPC-DS Q64

**Target, named up front:** TPC-DS Q64. A fresh warm gap census against `:65438` put it at 4.4 s against PG's
0.36 s, a ratio of 12, the largest left. A plain EXPLAIN took 2.9–4.6 s, so it is still planning.

**Cause.** In the planning profile, `pathkeyUsefulness.leadingMembersOf` took 50 %. Within it:

- **`sameClass` scanned the members.** It walked every equivalence-class member twice per call (30 %), once for
  each operand.
- **Identity keys were built again for every join rel.** `appendExprIdentityKey`, building keys, took 26 %.
  Slice 32's memo lived on each rel's `pathkeyUsefulness`, so each of Q64's hundreds of join rels re-formatted
  the keys of the same clause and pathkey expressions.

**Rule.**

- **One memo for the whole search.** `searchCtx.exprKeys` is created once, and every rel's `pathkeyUsefulness`
  shares it as its `keys`. Members are built with `u.newClassMember`, so their keys come from that memo too. The
  search runs in one goroutine. The in-place `ColumnRef` rewrites in the optimizer act on fresh clones or in
  plan creation, not on the nodes the search keys.
- **`sameClass` is two lookups.** It is `ecClassOf(a) == ecClassOf(b)`. `ecClassOf` reads two lazily built
  indexes:
  - `ecByKey`: the class of the first member in `ecMembers` order with each identity key;
  - `ecByPtr`: the class of the first member that is each node.

  An expression with a key equals exactly the members carrying that key, since a member that is the same node
  carries the same key. So its first match is `ecByKey[key]`. A keyless expression equals only itself, so its
  first match is `ecByPtr[e]`. This preserves the scan's "first match wins" answer.

**Pin.** `TestSameClassIndexMatchesScan` compares `ecClassOf` and `sameClass` against the old scan over:

- keyed and literal members;
- a key duplicated across two classes;
- a keyless sublink, which matches by node only;
- a nil member and nil probes.

Making the index keep the LAST duplicate fails it.

**Measured.** Warm, two interleaved rounds:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q64, plain EXPLAIN | 2.5–4.2 | 1.7–1.8 |
| TPC-DS Q64, full run | 3.2–3.8 | 2.3–2.5 |

The full run is ~−32 %. In the sf025 sweep, Q64 went from 4.36 to 1.85 s. Plans did not move: the sweep's plan
shapes are identical, and the fire-set gate found no changed query on SF1. TPC-H is identical (14.84 s, the
same on both builds).

**Left.** PG's pathkeys are canonical and compare by EquivalenceClass pointer. goopg still keys expressions by
identity strings, through map lookups on interface keys (`runtime.ifaceeq` was ~10 %). That is the slice 32
ledger row, with a progress row added for this slice.

## Slice 43 (2026-10-22): an unknown literal is coerced once, not per row — TPC-DS Q83, Q58

**Target, named up front:** TPC-DS Q83 (1.34 s in the sf025 sweep against PG's 0.10 s, a ratio of 13) and Q58
(0.92 s against 0.087 s). A plain EXPLAIN of each takes ~10 ms, so the time is execution.

**Cause.** EXPLAIN ANALYZE put Q83's time in three scans of `date_dim` (73 049 rows) filtered by
`d_date IN ('2001-07-13', '2001-09-10', '2001-11-16')`: ~300–600 ms each, where PG takes 6.5 ms. Alone,
`count(*) … WHERE d_date IN (…)` took 590 ms and `d_date = '2001-07-13'` 204 ms; an integer IN list over the
same table took 17 ms. 92 % of the profile was `compareEq → compareDatum → promoteCrossKind →
tryParseStringAs → parseCopyTimestamp → time.Parse`, half of it in `time.newParseError` from the layouts that
fail before the one that fits.

goopg types a bare literal `unknown` and coerces it at comparison time (root-0019), so every row re-parsed the
literal text. PG coerces it once, while the query is parsed: `oper()` resolves the unknown operand to the other
operand's type and `coerce_type` runs that type's input function on the literal (`parse_oper.c`,
`parse_coerce.c`; `transformAExprIn` does the same for an IN list).

**Rule.** Both memos rest on one identity. `compareDatum(a, lit)` begins with `promoteCrossKind(a, lit)`, which
for a non-string `a` and a string `lit` is `(a, tryParseStringAs(a.Kind, lit))`, and leaves a pair of equal
kinds alone. So `compareDatum(a, tryParseStringAs(a.Kind, lit))` equals `compareDatum(a, lit)` for every `a`.
Each memo is used only when the opposite operand's runtime kind is the kind the literal was coerced to; every
other row takes the old path. A literal that does not parse is not memoised, so its row still fails or falls
back exactly as before.

- **A compiled comparison** (`unknownLitCoercion`, at build time). The memo is built when the operator is a
  comparison and exactly one side is a bare literal. It also needs:
  - no character-string pair;
  - no bpchar-typed side;
  - a literal that does not look like a pg_lsn.

  These are the preamble branches of `evalFastBinaryApply` that could see the literal as a string. The other
  side's declared type (`optimizer.ExprResultType`) picks the kind (`unknownLitTargetKind`: int, numeric,
  date/timestamp, interval). The coerced literal rides in `ExprNode.constVal`, with its kind in `payload[17]`.
  `coerceUnknownLit` substitutes it in the per-row arm before the `fastBinarySimple` test, so the pair then
  takes slice 27's in-place `fastCompareDatum`. It also substitutes at the top of `evalFastBinaryApply`, for the
  batch evaluator. The slab is per operator instance and the memo is immutable after the build, so workers
  share nothing mutable.
- **A literal IN list** (`inListConst.coercedItems`). The statement already caches a literal list's items per
  Context. They are now also coerced, once per operand kind, after the same bpchar normalisation the loop
  applies. The plain-equality loop then compares a coerced item with `compareDatum`, which is what `compareEq`
  calls for those kinds:
  - numeric, through its own arm;
  - date/timestamp/interval, through the cross-kind fallback.

  An integer operand is not served, because `compareEq` compares an integer to a string as text. That is a
  separate wrong-result defect, P0-E10 below; serving it here would have made the cached and uncached lists
  disagree.

The interpreted twin (`evalExprSlot`'s BinaryOp arm) still coerces per row. It returns the same values, only
slower.

**Pins.**

- `TestUnknownLiteralComparisonMatchesPerRowCoercion` compares the compiled per-row and batch evaluators with
  the interpreted twin. It covers:
  - five typed columns, 17 literals, six operators and both operand orders, on every row;
  - rows whose runtime kind is not the declared one: a string `'2001-07-13 00:00:00'` in a date column, which
    equals the date but sorts after the text `'2001-07-13'`.

  Dropping the runtime kind test fails it.
- `TestUnknownLiteralInListMatchesPerRowCoercion` compares a cached list (with a Context) against per-row
  evaluation (without one), for IN and NOT IN with NULL, unparseable and typed items mixed in. Admitting
  `KindInt` to `coercedItems` fails it on `5 IN ('05')`.

**Measured.**

| query | before | after |
|---|---|---|
| `count(*) FROM date_dim WHERE d_date IN (3 literals)` | 590 ms | 18.6 ms |
| `… WHERE d_date = '2001-07-13'` | 204 ms | 11 ms |
| TPC-DS Q83, warm, two interleaved rounds | 1.59–1.64 s | 0.23–0.25 s |
| TPC-DS Q58 | 1.22–1.39 s | 0.60–0.61 s |
| TPC-DS Q16 | 1.09–1.31 s | 0.92–0.93 s |
| TPC-DS Q32 | 0.11 s | 0.04 s |

In the sf025 sweep, Q83 went from 1.34 to 0.21 s, Q58 from 0.92 to 0.45 s, and the sweep sum from 142.4 to
139.6 s. The sweep's larger Q22/Q66/Q7/Q56 moves did not repeat in an A/B (equal on both builds), and Q4 is
bimodal on both. TPC-H values are identical (acceptance 24 MATCH).

**Left.**

- Q58's remaining 0.45 s is join row cloning (`cloneRowOwnedPrefix`), a separate target.
- PG coerces in the analyzer, and goopg still carries the literal as a string through the plan. Moving the
  coercion into the analyzer would also serve the interpreted twin, index-probe keys and every other consumer.
  Recorded in the deferral ledger.
- **P0-E10, filed (S2):** `compareEq`, used by IN lists, compares an integer to an unknown literal as text, so
  `d_year IN ('02000', ' 2001')` matches nothing where PG matches 731 rows. The `=` form is correct. The defect
  is pre-existing at `62b168220`. Repro: `analysis/p0e10/`.

## Slice 44 (2026-10-22): the sorted aggregate and the incremental sort stream — TPC-DS Q35

**Target, named up front:** TPC-DS Q35, 3.4–3.7 s warm against PG's 0.35 s, the largest ratio left after
slice 43. A plain EXPLAIN takes ~10 ms, so the time is execution.

**Cause.** The plan is `Limit 100 → GroupAggregate → Incremental Sort (presorted ca_state) → Nested Loop`. The
nested loop's outer side is a Gather Merge over `customer_address` sorted by state; its inner side is a
Materialize of 328 rows. PG's EXPLAIN ANALYZE reads 20 300 outer rows and stops once the Limit has its 100
groups (6.66 M join-filter pairs). goopg read all 50 000 outer rows (16.4 M pairs, ~4.4 s). Two operators drained
their whole input before returning a row:

- **`incrementalSortOp.Open`** collected and sorted every presorted-prefix group of the child before `Next`
  returned the first. PG's `ExecIncrementalSort` collects one group, sorts it, emits it, and reads on only when
  it is exhausted.
- **The AGG_SORTED path** (`aggregateOp.openSorted`, M0134-0001 S8) finalised every group into `o.rows` in
  `Open`. PG's `agg_retrieve_direct` returns each group as the first row of the next one closes it. Making
  only the sort stream moved nothing: the aggregate above it still read to EOF (its first row came at 4.39 s).

**Rule.**

- **Incremental sort.** `Open` opens the child and reads nothing. `Next` calls `readGroup` when the current group
  is exhausted:
  - it collects rows while `sortPrefixEqual` holds;
  - it keeps the first row of the following group (`next`/`nextKV`) as a one-row lookahead;
  - it sorts the group with the unchanged `sortGroup` (stable, every key).

  Group boundaries and per-group order are the drained version's, so the output is identical.
- **Sorted aggregate.** `Open` sets `o.sorted`, and `Next` calls `nextSorted`. It reads the child until a row's
  key parts differ from the current group's, finalises the current group, starts the new group from that row and
  absorbs it, then returns the finalised group. EOF finalises the last group. That is the drained loop's exact
  sequence of `evalGroupExprs` / `applyAgg` / `finalizeGroup` calls, so shared user-aggregate state, passthrough
  columns and finalisation are unchanged. Only when the consumer sees each group differs.

Both operators re-open cleanly (a rescan resets the cursor and lookahead). An error raised by a group past the
Limit no longer reaches the client, as in PG.

**Pins.** `TestSortedAggregateStreamsGroups` and `TestIncrementalSortStreamsGroups` count the child's pulls after
each output row:

- `Open` pulls nothing;
- each group costs its own rows plus the one row that closes it;
- the last group ends at EOF;
- the aggregate test runs twice to cover a re-open.

Restoring either operator's draining version fails its test.

**Measured.** Warm, two interleaved rounds of three runs:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q35 | 3.35–3.96 (one 5.6) | 1.49–1.65 (one 3.6) |

EXPLAIN ANALYZE now shows the Incremental Sort returning 101 rows, as in PG, over 17 444 outer rows (PG:
20 300; the parallel split differs) and 5.7 M filtered pairs. In the sf025 sweep, Q35 went from 3.50 to 1.66 s
and the sweep sum from 139.6 to 136.5 s. The sweep's Q88/Q42 +2 s rows did not repeat in an A/B (equal on both
builds), the same outlier pattern slice 43 recorded for Q22/Q66. TPC-H values are identical (acceptance 24
MATCH), and the sweep's plan shapes are identical.

**Left.**

- Q35's remaining gap: the 5.7 M join-filter pairs cost ~270 ns each against PG's ~90 ns. That is the nested
  loop over a Materialize, a next target.
- PG's incremental sort also has a full-sort mode, which batches at least 32 tuples (`DEFAULT_MIN_GROUP_SIZE`)
  before looking for a boundary, and a prefix mode for large groups. It also passes a LIMIT bound into its
  tuplesorts and reports `Full-sort Groups` / `Pre-sorted Groups` in EXPLAIN ANALYZE. goopg sorts each prefix
  group as one batch and prints neither line. The output order is the same; the ANALYZE text is not. Recorded
  in the deferral ledger.
