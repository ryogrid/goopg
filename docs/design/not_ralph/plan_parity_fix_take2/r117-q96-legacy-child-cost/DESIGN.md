# R117 design: Q96 lower legacy child-cost attribution

## Boundary

R116 measured an equal-self-cost top Hash Join and an equal-self-cost LATERAL
join, with the root 7.68 difference inherited through the top join's display
children. R117 measures only the preceding, lower explicit Hash Join in the
two R111 forced forms. It neither changes candidate pricing nor proposes a
cost or cardinality correction.

The production facts to attribute are deliberately separate:

* `DeriveLegacyDisplayCost` gets each child through
  `legacyDisplayChildren`, then reads an existing `PlanCost` when present or
  calls `EstimateRows` and derives a legacy display cost otherwise.
* `EstimateRows(*Join)` calls `estimateJoin`, which recursively obtains input
  rows and applies the hash/merge equality-pair selectivity path. Scan rows
  arise from `seqScanRows`; scan and join widths arise from each node's output
  schema through `TupleWidth`.

The trace must report these as provenance, not treat a display total as a
candidate cost or infer that a row estimate decided the plan.

## Temporary diagnostic

`GOOPG_Q96_LEGACY_CHILD_TRACE=1` is parsed once at package initialization and
is false by default. Its only effect is append-only server logging. It owns a
trace-local, per-statement observational sidecar created and passed within one
`PlanWithSettings` invocation, never a package-global mutable current sidecar.
Explicit construction assigns an increasing identity once, keyed by the
original `*Join` pointer. Each clone or rewrite site, rather than construction,
must register its original-to-successor pointer mapping under the same identity.
The sidecar is cleaned up on every normal and panic return; collision is a
falsifier rather than a merge, and final traversal reports both identity and
occurrence count. The sidecar is never stored in a
`Node`, `PlanCost`, schema, or planner state, and neither planner nor executor
reads it. A selected occurrence exists only when one final lower Hash Join
maps to one construction identity; zero or multiple matches are recorded.

For each selected lower join, the trace emits:

1. construction identity, form-local ordinal, left/right relation/source-set
   identities, join type/algo/build side, output rows and width;
2. left and right input rows and widths, with whether their cost is stamped,
   legacy-derived, or empty fallback;
3. each child ordinal and concrete node type returned by
   `legacyDisplayChildren`, plus rows, width, startup, total, and cost source;
4. the nonrecursive display arithmetic: `cpuTupleCost`, output rows,
   per-row term, sum of child totals, maximum child startup, and the produced
   startup and total; and
5. the `estimateJoin` input/output cardinality observation and branch facts
   needed to identify the first differing source. Any subsequent mutation or
   recomputation discovered in the construction-to-final walk is logged by
   site and before/after rows or widths.

No trace helper calls planner, executor, catalog, statistics, or selectivity
logic. Hooks observe values already computed during the normal display
derivation; they must not invoke fresh `EstimateRows` or
`DeriveLegacyDisplayCost` calls. The diagnostic never stores a new plan cost,
mutates a node, or recursively accumulates totals. Tests cover the
formatter/ledger with absent and extreme inputs, and prove that the sidecar has
no semantic reads and the disabled path allocates nothing and leaves the tree
and costs unchanged. A race/concurrent-statement test proves there is no
identity or record cross-talk. The generated planner-flag provenance file
includes the switch.

## Pre-code producer census

Before adding hooks, the implementation notes and final report must contain a
source call-site table with these concrete entry points and their observed
role for each lower-join side:

| location | value it can produce or alter | required trace/report field |
| --- | --- | --- |
| `planner.go: planFromItem` explicit `Join` construction | left/right node identity, schema and algorithm-time `EstimateRows` reads | construction identity, side node/type, construction rows and widths |
| `cardinality.go: EstimateRows` | node dispatcher, including any carrier/fallback caller | producer kind and input/output rows |
| `cardinality.go: seqScanRows` and `filterSelectivity` | base scan cardinality and predicate reduction | table/stat source, predicate branch, before/after rows |
| `cardinality.go: estimateJoin` | lower-join left/right rows, equality-pair/selectivity branch and output rows | left/right rows, branch, selectivity facts, output rows |
| `plancost.go: legacyDisplayCostOf`, `legacyDisplayChildren`, and `DeriveLegacyDisplayCost` | stamped versus legacy fallback, child list, output width, and display arithmetic | child ordinal/type/source, rows/width/startup/total, ledger terms |
| every clone/copy/rebind/final-plan rewrite reachable after construction (starting with `fillJoinHashKeys` and rebind/remap sites found by the census) | pointer successor, schema/key or row/width recomputation | source site, predecessor/successor identity, before/after rows and widths |

The census must enumerate every caller and every clone/copy/rebind writer
found for these paths, and either instrument each reachable writer or prove it
cannot affect the selected lineage. It must explicitly state whether the
selected lower join has a stamped `PlanCost` or falls back to legacy derivation,
and name the first observed divergence. A source-only possibility remains
labelled inference unless the trace observes it.

## Verification and stop rules

Use the immutable R101 hdem-first and store-first forms on the R111 private
cluster with `work_mem=64MB`, `join_collapse_limit=1`,
`from_collapse_limit=1`, `GOOPG_GATHER_PATHS=top`,
`GOOPG_PARTIAL_AGG_PATHS=on`, `GOGC=off`, and `GOMEMLIMIT=12GiB`. Capture
OFFx2 and ONx2 text and JSON explains, result values, hashes, and trace paths.
Every value must be 266; each trace-on text/JSON plan must match its trace-off
counterpart byte-for-byte. Include a duplicate-structure unit test proving
identity rather than shape maps the selected occurrence.

If the final selected lower join is not one-to-one, an input's producer cannot
be traced, or its nested display ledger does not reconcile the 7.68 root
margin, report that fact and stop. Remove every temporary source change before
committing the report. No Datum, cost, selectivity, statistics, Gather,
executor, or join-search change is in scope.
