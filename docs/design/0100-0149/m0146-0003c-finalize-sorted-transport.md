# M0146-0003c: a sorted transport-final folds same-key state runs without a group map

Slice of M0146-0003 (row-emitting PartialAgg), adopting M0141-S5: the
GatherMerge-fed Finalize-Sorted merge-combine. S3/S4 (row emission +
state serialization) landed as M0146-0003b (`104c2c90b`); S6 (planner
producer + EXPLAIN arm) remains open — no producer emits `PartialEmit`
yet, sorted or hashed.

## PG behaviour

PG's parallel grouping stack is

    Finalize GroupAggregate -> Gather Merge -> Sort -> Partial HashAggregate

`gather_grouping_paths` (planner.c:7704-7724) prices this against the
Gather/Partial-HASH variant. The top node is a GroupAggregate in
AGG_SORTED mode: its input is globally ordered by group key (each
worker's partial emits sorted rows; the Sort keeps that order; Gather
Merge interleaves the worker streams preserving it), so `nodeAgg.c`'s
sorted retrieve folds the transition states of a same-key run — which
may span workers — into ONE live state, finalizes it when the key
changes, and emits. There is no group map: on a merge-ordered stream a
key cannot recur once its run ends, and the live state is exactly one
group.

## goopg before

M0146-0003b's transport-final (`absorbPartialStateRow`) absorbed every
serialized-state row into a `groups`/`order` map and emitted after the
drain. The combination is result-correct — `combineAggRuntime` is
associative and commutative on the decomposable whitelist — but the
node shape is a hash combine: it does not benefit from the order
GatherMerge guarantees, and it cannot express "one live group" —
PostgreSQL's Finalize GroupAggregate operator contract.

## Change

`Strategy == AggStrategySorted` on a `Mode == AggModeFinal &&
PartialEmit` node now routes to `openSortedPartialTransport`
(internal/executor/operators_join_agg.go), the row-transport twin of
`openSorted` (the existing AGG_SORTED arm for `AggModeSimple`):

- Each incoming row is decoded by `decodePartialStateRow`, the shared
  validator both transport arms now use — same width/kind/frame checks,
  so the two consumers can never disagree about the wire shape.
- Same-key runs (boundary test via `sameGroupKey` on the datumKey
  vector — the exact same grouping decision the hash absorb makes)
  fold each deserialized state into the live group through
  `combineAggRuntime`.
- A key change runs the order belt, then finalizes and emits the
  completed group through the shared `finalizeGroup` tail — so hash
  absorb, sorted fold, and accumulator transport can never diverge on
  finishAgg, GROUPING masks, or passthrough handling.

The order belt: before discarding a finished group, the fold compares
the NEW key against it column-wise via `compareDatum`; a key below the
just-emitted group means the stream is not merge-ordered — a plan
construction error. It must fail loudly (XX000): without the check a
key recurring after a higher key produces TWO output rows for one
group, a silently wrong result with no user-visible explanation. The
check is once per group, so the belt is free relative to the
deserialization it guards.

Truth table after this slice:

| Mode    | PartialEmit | Strategy | behaviour |
|---------|-------------|----------|-----------|
| Partial | false       | *        | publish to shared accumulator, emit zero rows |
| Partial | true        | *        | hash internally, emit sorted [keys|pt|states] rows |
| Final   | false       | *        | drain Gather, read accumulator |
| Final   | true        | Hashed   | absorb state rows into group map (0003b) |
| Final   | true        | Sorted   | THIS SLICE: streaming fold, requires non-descending keys |
| Simple  | —           | Sorted   | openSorted (M0134-0001 S8) |

## What this does NOT do (deferred inside M0146-0003)

- **S6 producer + EXPLAIN**: no planner path sets `PartialEmit` on
  either strategy; both nodes remain directly-constructed only. PG's
  `Output:` line shows the serialized `internal` columns; goopg's
  EXPLAIN arm is unwired.
- **Grouping sets** stay refused on the transport arms (already
  outside the split whitelist).
- **Partial-GroupAggregate** (PG's AGG_SORTED partial — sorted input
  into the partial itself, no Sort node under the merge): the partial
  arm still hashes internally and sorts its output; only the finalize
  gained a sorted mode. This is the `Partial GroupAggregate` ->
  `Gather Merge` pairing PG also emits; reachable once the producer
  costs it (S6).

## Tests

`internal/executor/parallel_agg_transport_test.go`:

- `TestPartialEmitSortedIdentity` — `Finalize(PartialEmit,Sorted) ->
  GatherMerge -> Sort -> Partial(PartialEmit)` on the pq_agg fixture
  vs. serial, workers 1/2/4: grouped, mixed-family, float, sparse,
  two-column key cases; positional comparison (sorted output is the
  contract — ORDER BY grp's serial result defines the merge order).
  Ungrouped over a plain Gather folds every worker's state row into
  one group.
- `TestPartialEmitSortedRejectsUnsorted` — direct aggregateOp over a
  rowsOp carrying an out-of-order state stream: the belt errors
  ("not ordered by group key"); the sorted prefix of the same stream
  emits one row per key run.

## Gates

units PASS; executor+optimizer packages PASS; `-race` on both
identity tests PASS; tpch-spotcheck PASS (Q12=2, Q13=33);
tpcds-sf025 sweep PASS under FORCE=1 (96/96 verdicts, 99/99 plan
shapes — run during the nightly batch, timings void, verdicts real);
tpch-acceptance-arm PASS under FORCE=1 (24/24 value-identical);
tpcds-fireset PASS (fires=none both scales — zero plan moves, the
strategy bit has no producer); canonical TPC-H capture identical to
104c2c90b.
