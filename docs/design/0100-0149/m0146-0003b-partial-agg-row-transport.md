# M0146-0003b: a partial aggregate's transition state crosses the Gather inside a row

Slice of M0146-0003 (row-emitting PartialAgg), adopting the filed
M0141-S3/S4 chain: partial-state row emission + aggregate-state
serialization. The GatherMerge-fed Finalize-Sorted merge-combine (S5) and
the planner producer + EXPLAIN arm (S6) remain open; no producer emits the
new flag yet.

## PG behaviour

`create_partial_grouping_paths` / `gather_grouping_paths`
(postgres/src/backend/optimizer/plan/planner.c:7704-7724) split a
parallelizable aggregate into

    Finalize Aggregate -> Gather [Merge] -> [Sort ->] Partial Aggregate

where the Partial node emits REAL rows: the group key columns plus one
`internal`-typed transition-state column per aggregate. The state crosses
the worker boundary inside the row — for serialisable aggregates
(`aggserialfn`/`aggdeserialfn`, nodeAgg.c) it is flattened to bytes;
pointer-bearing states (numeric `N`, exact integer Sx/Sxx) are what the
serialization exists FOR. A row cannot carry pointers, so the transport
is bytes, not the state object.

## goopg before

The existing split is the zero-row shared-accumulator model: a
`AggModePartial` node drains its child, publishes per-group `aggRuntime`
states into the `aggPartialAccum` side channel, and emits NOTHING; the
`AggModeFinal` node drains the Gather, then reads the accumulator and
combines. Because workers and leader share the accumulator object, the
transition state never becomes row content — which means no row-moving
node can sit between the pair. PG's `Sort -> Partial HashAggregate`
under a `Gather Merge`, and hence the `Finalize GroupAggregate ->
Gather Merge` stack that M0146-0003 targets, is inexpressible: a Sort or
a GatherMerge moves rows, and a Partial that emits no rows has nothing
to move.

## Change

A new `Aggregate.PartialEmit` flag (internal/optimizer/plan.go) switches
a Partial/Finalize pair onto row transport. It must be set on BOTH nodes
of the pair; a PartialEmit partial under a plain finalize (or the
reverse) is an internal-error construction.

- Partial arm (`emitPartialStateRows`,
  internal/executor/operators_join_agg.go): after the drain, emit one
  row per group — `[group key values | passthrough values | one
  serialized transition state per aggregate]` — sorted by the group
  key columns. The state column is a `KindBytes` Datum produced by
  `serializeAggRuntime`, goopg's in-process analogue of `aggserialfn`.
- Transport-final arm (`absorbPartialStateRow` on the drain loop):
  validate the row's width and column kinds, key the group map from the
  transported key values (detached from the row, same retention
  boundary as the input drain), `deserializeAggRuntime` each state
  column, and fold it in through the SAME `combineAggRuntime` rules the
  shared-accumulator transport uses — so the two transports can never
  disagree about what a combination means. Finalization then reuses the
  shared emit tail verbatim.

## Serialization surface (agg_state_serial.go)

Name-keyed fixed-layout encoding bounded by the SAME whitelist
`combineAggRuntime`/`aggregateIsDecomposable` serves: count; sum/avg
(hasValue, floatSpecial, int sum, count, `encodeDatum` of numericSum);
min/max/any_value (hasValue + `encodeDatum` of the extreme — reusing the
spill.go Datum codec); bool_and/every/bool_or; bit_*; the
variance/stddev family (floatSx/floatM2 bits, exact intSx/intSxx and
numericSx/numericSxx via signed-magnitude big.Int/Rat arms); the
regression/covariance/correlation family (the five regr sums + N).

Fail-closed in both directions:

- `aggStateFieldBelt` refuses to serialize ANY state carrying surface
  outside the whitelist — DISTINCT sets, string/array accumulators,
  WITHIN GROUP state, user-aggregate state. A silently truncated state
  is a silently wrong final answer, and there is no user-visible error
  that would explain it.
- `deserializeAggRuntime` treats truncation and trailing bytes as
  internal errors: writer and reader live inside one plan, so a length
  mismatch means they disagree about the layout — a bug, not data.
- Unknown aggregate names error on both sides; the planner's
  decomposable whitelist is what makes that unreachable by
  construction.

NaN/+Inf/-Inf round-trip through `floatSpecial`; empty states through
`hasValue`; exact integer and numeric variance state through the
big.Int/big.Rat arms (a state transport that approximated would betray
PG's numeric-var_pop contract).

## What this does NOT do (deferred, in scope of M0146-0003)

- **S5 merge-combine**: the transport-final absorbs rows unordered and
  hash-combines. PG's shape is GatherMerge + a Finalize that
  merge-combines same-key runs across workers (streaming, no group map
  growth beyond the current key). The result is identical — combine is
  associative/commutative on this whitelist — but the node shape and
  memory profile are not PG's. Resume: a `StrategySorted`-armed
  transport-final consuming GatherMerge order.
- **S6 producer + EXPLAIN**: no planner path sets `PartialEmit`; the
  node pair is reachable only in directly-constructed plans (the
  identity test). `EXPLAIN` rendering of serialized-state rows
  (`Output:` shows `internal` columns in PG) is unwired.
- **Grouping sets**: transport-final refuses `GroupingSets` outright
  (grouping sets were already outside the split whitelist).

## Tests

`internal/executor/parallel_agg_transport_test.go`:

- `TestAggRuntimeSerialization` — round-trip per supported family:
  count, int/numeric sum, avg incl. NaN marker, min, empty max,
  bool_and/bool_or, bit_and, any_value, int/numeric var_pop, float
  stddev incl. NaN, regr_slope.
- `TestAggRuntimeSerializationRefusals` — the belt: DISTINCT,
  string_agg, array_agg, WITHIN GROUP, user state each error rather
  than transport.
- `TestPartialEmitIdentity` — direct-plan splice
  `Finalize(PartialEmit) -> Gather|GatherMerge -> Partial(PartialEmit)
  -> pq_agg scan`, workers 1/2/4, vs. the serial single-aggregate
  baseline: ungrouped, grouped, mixed-family (count+avg+var_pop+min),
  float, sparse-group cases; row-multiset identical.
- `TestPartialEmitPairingErrors` — width/kind mismatches and
  non-PartialEmit pairing fail closed.

## Gates

units PASS; tpch-spotcheck PASS (Q12=2, Q13=33); tpcds-sf025 sweep
PASS (96/96, 99/99 plan shapes identical to 9fcb9c952);
tpch-acceptance-arm PASS (24/24 value-identical); tpcds-fireset PASS
(fires=none at both scales — zero plan moves, the flag has no
producer); -race on the new identity test PASS.
