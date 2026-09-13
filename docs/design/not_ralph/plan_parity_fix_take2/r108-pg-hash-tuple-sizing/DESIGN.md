# R108 DESIGN — opt-in PG packed-tuple Hash Join spill-cost comparison

R111 (`9e94e594f`) establishes the first valid common-input Q96 oracle after
the varchar repair. On that input, the two immutable R101 forced forms remain
value-correct but PG18.3 prices hdem-first lower by 175.91 while Goopg prices
store-first lower by 7.68. Existing Goopg Hash Join spill costing instead
uses the executor's `map[K][]Row` footprint (`48 * columns + payload` and
map slots). That is necessary for safe execution, but it is not PostgreSQL's
planner representation. This experiment isolates the resulting question:
would pricing spill geometry with PG18's packed `HashJoinTuple` widths move
the relevant plan choices, and would such a planner geometry remain distinct
from Goopg executor capacity?

## Authorized implementation

Add an explicitly opt-in, default-off planner experiment controlled only by
`GOOPG_PG_HASH_TUPLE_SPILL_COST=1`. With the variable absent or not exactly
`1`, every `hashJoinCost` result must be byte-for-byte cost-identical to the
current Goopg map-geometry path. The experiment may change only the Hash Join
batch I/O *cost* decision:

1. obtain PG18 non-parallel packed geometry from the existing pure
   `pgHashGeometry(innerRows, innerWidth, workMem)` helper, including its
   `numBatches`. This is `HashJoinTuple + MinimalTupleHeader +
   MAXALIGN(width)` geometry only; and
2. if that geometry is valid and has more than one batch, charge PG's
   `final_cost_hashjoin` batch I/O with a separate planner-private PG
   `page_size` helper, `ceil(rows * (MAXALIGN(width) +
   MAXALIGN(SizeofHeapTupleHeader)) / BLCKSZ)`, for both inputs: one inner
   write at startup, then one inner read and outer write/read at run.

`innerWidth` is already the PG-style emitted width (`Path.OutputWidth` or its
relation-width fallback). Add and thread the corresponding `outerWidth` so
both page counts are from emitted PG widths, never `NCols`, `AvgVarBytes`, or
the executor `Datum` size. Invalid/unknown width or geometry must fail closed
to the existing Goopg map-geometry spill rule. This is a comparison switch,
not a new default cost model.

The executor is an explicit non-target: do not modify `hashsize.Choose`,
`joinOp.buildGeometry`, map construction, actual batch files, work-memory
limits, or spill behavior. The normal map geometry remains the sole source of
execution capacity. A plan which the experiment treats as in-memory may still
spill at execution, and the report must record that fact rather than hide it.

## Required proof and measurement

Before runtime comparison, focused optimizer tests must prove:

1. the switch-off cost is exactly the prior map-geometry cost, including
   spill/no-spill boundaries;
2. the switch-on path uses packed `HashJoinTuple` PG `numBatches` only for the
   batch decision and the separate heap-header `page_size` formula for both
   I/O page counts. Include `MAXALIGN`/header-boundary vectors, independently
   varied outer/inner widths, a case where PG fits but the Goopg map spills,
   and the reverse-safe fallback on invalid PG geometry;
3. a narrow output path's width changes only the opt-in planner I/O price,
   never executor `hashsize.Choose` geometry; and
4. serial and partial Hash Join candidate plumbing carries both emitted widths
   without changing non-Hash paths or EXPLAIN width rendering.

Add trace-only measurement fields when the existing shaped-DP trace is
enabled: join identity, Goopg entry bytes/buckets/batches/spill decision, PG
emitted widths/tuple geometry/batches/spill decision, and the selected spill
cost currency. They must have no planner effect beyond the named opt-in
switch. Tests must cover that absent switch and invalid geometry cannot select
the PG currency.

After focused tests, run the optimizer, executor, and estimate-audit package
suites, vet, and `git diff --check`. On R111's unchanged four-table data,
capture switch-off versus switch-on Q96 forced forms twice, their values, and
natural Q96; record each per-join geometry and executor spill reality. Then
run the unchanged TPC-H digest, TPC-DS SF0.25 sweep, and a fresh PG18.3 plan
census for both modes. The experiment may not be promoted, even if it improves
some shapes; report all plan/value deltas and decide only whether the evidence
supports keeping cost geometry distinct from executor capacity.

## Boundaries and process

This design authorizes no default change, selectivity adjustment, join-method
disablement, query/data/reference-PostgreSQL mutation, executor sizing change,
or claim that packed PostgreSQL Datum size is Goopg's actual memory usage.
If R111's values fail, trace cannot show both geometries, executor spill facts
are unavailable, or the experiment changes behavior with the switch off, stop
and report rather than generalize it. Obtain agent review, correct this design
if needed, and `git commit -n` plus push it before editing production code.
