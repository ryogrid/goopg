# R118 SCOPE — Q96 lower Hash Join cardinality and schema producer audit

R117 proved that the forced-form 7.68 margin is inherited by the selected
upper Hash Join and that the first differing selected/root-lineage legacy Join
ledger is the lower Hash Join: hdem-first is 688465 rows / width 476, while
store-first is 688081 rows / width 1104.  It deliberately did not establish
which producer wrote either field.  The forced orders have different direct
right inputs, so no equality premise may be assumed.

R118 is a measurement-only producer audit.  It must identify, for each R111
lower selected Hash Join, the exact `estimateJoin` call and the exact
schema/layout construction that reaches its final node.  It may not modify
production cardinality, required-column, schema, Datum, cost, statistics,
Gather, search, or executor behavior.

## Required evidence

### Per-statement producer-to-final lifecycle

Producer calls run during planning, while R117's final occurrence census ran
during EXPLAIN rendering. R118 must bridge those phases with an explicit,
bounded lifecycle; a process-global registry, reused pointer value, or a
render-only token cannot do so.

Only when the outer parsed statement is EXPLAIN and the default-off diagnostic
is enabled, `PlanWithSettings(*parser.ExplainStmt)` creates one
statement-local sidecar and explicitly passes it into the recursive
`PlanWithSettings(explainInner, ...)` call. That inner call is where lower
joins are constructed. The sidecar returns with the inner plan and is then
frozen/attached when `planStmtWithSettings` constructs `&Explain{Child: inner}`.
Nested or non-wrapper statements receive no sidecar. An error or panic in the
recursive inner call must clean the sidecar without publishing a partial
report. Focused tests must exercise this EXPLAIN-wrapper/inner-plan hand-off.

Each instrumented Join construction receives a monotonically assigned
deterministic construction ID. Its record holds immutable scalar snapshots
plus an internal, planning-lifetime association to that concrete Join. Every
clone, rewrite, or replacement site must carry a documented successor edge
from construction ID to construction ID, or be instrumented as source-proven
irrelevant to the final lower joins.

After all planning rewrites and immediately before the planner returns the
`optimizer.Explain` plan, one final-tree census uses a canonical traversal, or
an explicitly equivalence-tested traversal, covering every renderer-visible
child/wrapper including Gather, GatherMerge, and shared occurrences. It assigns
stable pre-order final-occurrence ordinals and resolves every final Join to
exactly one construction/successor lineage. It freezes a self-contained
immutable report keyed by final ordinal and attaches that report to the
EXPLAIN plan. The renderer reads only the frozen report; it does no producer
lookup. Planning pointers and mutable maps are discarded before return. A
final Join with zero or multiple lineages is a recorded stop state, not a
best-effort match. Ordinary statements, non-EXPLAIN planning, and
diagnostic-off EXPLAIN allocate no state.

The implementation must prove reset/cleanup on planner error and panic, and
concurrent EXPLAIN calls must use disjoint sidecars and render tokens. Focused
tests must show deterministic construction/final IDs across repeated plans and
must show clone/rewrite successor handling is observed or explicitly rejected.
The frozen report must retain no Node pointer or mutable planner state. Tests
must prove that frozen final ordinal/occurrence census agrees with both TEXT
and JSON renderer-visible occurrence output.

Before source work, inventory these producers and all post-construction
mutators/recomputations:

1. `EstimateRows(*Join)` / `estimateJoin` in `cardinality.go`, including
   left/right estimated rows, join type/algo, equi-pair identity, every
   selectivity factor and bound/fallback, residual selectivity, and final
   saturated integer output.
2. The selected explicit join's construction route, including legacy direct
   constructors in `planner.go` and path-backed construction through
   `createplanjoin.go`; establish which route each forced lower join uses.
3. `joinInputs.publishedSchema` / `publishedLayout` and the eventual Join or
   Project schema writer, including required-column narrowing/projection and
   every later schema replacement. Each lineage row must include ordered output
   position, origin/expression identity, source-table identity, type OID/name,
   typmod and collation where applicable, nullability/width inputs, and final
   computed `TupleWidth`. Source-table sets and names alone are insufficient.
   Every writer/replacement/copy must feed one attributable lineage or be
   source-proven irrelevant.
4. The final plan occurrence census used by R117.  Mapping must be
   collision-free and must explicitly report zero, multiple, or unobserved
   mappings.  No claim about a producer is valid without a one-to-one mapping
   from producer record to final lower Join occurrence.

The temporary default-off diagnostic must be observational: no fresh estimate
or schema derivation, no retained mutable planner node after finalization, no
planner/executor read whose presence changes behavior, and no recursive
double-count. Its
OFF path must allocate no trace payload.  It must use a unique per-render
token and deterministic source-set/column order.  Trace field names must make
the distinction between observed final field and source-level inference
unambiguous.

## Controls and stop rules

Repeat R111 on the private common-input cluster with 64MB work_mem,
`join_collapse_limit=1`, `from_collapse_limit=1`, `GOOPG_GATHER_PATHS=top`,
and `GOOPG_PARTIAL_AGG_PATHS=on`: both forced forms, TEXT and JSON,
OFFx2/ONx2 byte checksums, trace logs/tokens, and values `266`.  Retain paths
and checksums in the report.  Verify disabled silence, direct and path-backed
constructor controls, duplicate mapping suppression, source-column ordering,
overflow/non-finite selectivity handling, and concurrent render isolation.

Direct and path-backed constructor coverage is a source-census and focused-test
gate: record which route reaches each R111 lower Join and separately exercise
the alternate route. Every `estimateJoin` record must identify the concrete
Join construction/route and every arithmetic input/output actually used,
including rounding, saturation, and non-finite branch; a test must reproduce
the recorded final rows from that equation.

Stop and report without a corrective change if a selected lower Join has zero
or multiple producer mapping, a required source is unreachable, its final
schema was mutated without a single attributable writer, or the observed
producer equation does not reproduce the final rows/width.  Remove all
temporary source before the report.  A successor may scope a production change
only after this evidence identifies one source-level divergence and validates
the corresponding PG18.3 comparison; R118 itself authorizes none.
