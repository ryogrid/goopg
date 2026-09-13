# R112 DESIGN — PG cost-family representation inventory

## 1. Question and boundary

R108 established a narrow negative result: substituting PostgreSQL's packed
`HashJoinTuple` geometry for the Hash Join spill *price* did not move the
measured plans, because its relevant candidates had one PG batch. It also
established an important constraint: PostgreSQL does not have one universal
"Datum size" usable in every cost formula. Its Hash-table batch selection,
spill page traffic, sort input volume, index tuples, and memoize entries use
different representations and overheads.

R112 is therefore a Step-0 inventory, not a whole-cost size substitution. It
must identify every Goopg planner cost family which presently derives a
byte-volume, row-width, page count, or in-memory capacity from Goopg's
`[]Datum`-oriented geometry, identify PG18.3's exact counterpart, and classify
whether a family-specific comparison could change a plan. No production cost,
executor capacity, row estimate, or planner default is authorized in this
round.

This scope depends on R108's DONE report and review. It may not reopen R108's
Hash Join spill price or use executor memory safety as a reason to copy Goopg
representation into a PG-parity planner formula.

## 2. Required inventory

The report must contain one row for each reached family below, with: (a) the
Goopg caller and the exact byte/width inputs it consumes, (b) the PG18.3
source function and representation, (c) whether the inputs are already
PG-faithful emitted widths, executor geometry, or neither, (d) the reachable
TPC-H/TPC-DS candidate/query census, and (e) a disposition of `candidate`,
`already-faithful`, `no-PG-analogue`, or `unreached`.

| family | Goopg entry point | PG18.3 authority to inspect | initial constraint |
|---|---|---|---|
| sequential / parallel sequential scan | `costSeqscan`, `costParallelSeqscan` | `cost_seqscan` | PG prices relation pages, not a synthetic Datum row; prove the source of Goopg's page count before considering any change. |
| index / bitmap scan | `costIndexScanCore`, bitmap cost callers | `cost_index`, `cost_bitmap_heap_scan` | Index tuple layout and heap-page fetch estimation are separate; do not substitute an output Datum width for either. |
| sort, merge-input sort, WindowAgg, SetOp | `costSortRun`, `costWindow`, `costSetOp` | `cost_tuplesort`, `relation_byte_size` | Goopg currently reaches its disk arm through `hashsize.EntryBytes(ncols, avgVarBytes)`; PG uses `MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader)`. This is a candidate only after width/provenance and reachability are measured. |
| Hash Join | `hashJoinCost` | `initial_cost_hashjoin`, `final_cost_hashjoin`, `ExecChooseHashTableSize`, `page_size` | R108 already separates packed HashJoinTuple batch geometry from heap-header spill pages. Record it as `already-faithful experiment / no R112 code`. |
| HashAggregate / grouping | `costAgg`, `hashAggEntrySize`, `hashAggSetLimits`, grouping path producers | `cost_agg`, `hash_agg_entry_size`, `hash_agg_set_limits`, `relation_byte_size` | Goopg already has a HashAggregate spill-price arm. Inventory its entry, partition, and I/O representations against PG and establish an executor correspondence before any family-specific change. |
| Materialize / rescan | materialize path and rescan callers, if present | `cost_material`, `cost_rescan`, `relation_byte_size` | Classify only reachable Goopg paths; executor buffering and planner price must remain distinct. |
| Memoize | `costMemoizeRescan` | `cost_memoize_rescan`, `ExecEstimateCacheEntryOverheadBytes` | PG includes relation bytes, cache-entry overhead, and parameter expression widths. A `ncols` proxy is not a universal Datum size. |
| upper / Gather / Append paths | relevant upper-path producers | `cost_gather`, `cost_gather_merge`, `cost_append`, `cost_merge_append` | Determine whether any width input controls an election rather than merely EXPLAIN output. |

The inventory is complete only after compiler/search evidence shows whether
additional width-to-cost readers exist. A text search is a starting point, not
proof of a cost family.

## 3. Measurement protocol

Use the committed R108 binary/source as the baseline and no uncommitted
production code. Obtain live PG18.3 source/cost references from the read-only
`postgres/` tree. For each candidate family:

1. add no planner behaviour; use only existing trace channels or a temporary,
   reverted diagnostic if existing channels cannot establish reachability;
2. capture a fixed-seed TPC-H and TPC-DS SF0.25 candidate census, recording
   call count, width inputs, memory/batch decision where applicable, and
   whether a competing path exists; and
3. run an OFF/OFF A/A EXPLAIN control before treating a plan difference as a
   family signal.

The report must distinguish a no-op because the candidate was not reached,
because it was already PG-faithful, and because a PG formula has no executable
Goopg counterpart. It must name the exact next family, if any, rather than
authorizing a cross-family size rewrite.

## 4. Gates and stop rules

This documentation/inventory round requires source citations, an explicit
family table, and reproducible census commands/results. It does not modify
production code. If an instrument would need to alter a planner cost, change
an executor allocation, use a universal PG Datum size, or compare against a
stale PG fixture, stop and write a follow-on scope instead.

Any later candidate implementation requires its own Design Doc, agent review,
`git commit -n`, and push before source changes. Its verification must include
focused tests, a live-PG structural census with session-pinned GUCs, TPC-H
value digest, and foreground TPC-DS SF0.25 values sweep.
