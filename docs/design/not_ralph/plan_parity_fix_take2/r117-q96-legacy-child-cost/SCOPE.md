# R117 SCOPE — Q96 legacy child-total attribution

R116 established that the 7.68 forced-form margin is inherited child total:
the selected top Hash Join and LATERAL join have equal self terms. R117 is a
measurement-only audit of the lower explicit Hash Join's scan child display
costs, intermediate cardinality, and per-row legacy self term.

Before source work, inventory the producers of the lower join's two input rows
and widths: base relation provenance, predicates, join-selectivity/cardinality
calls, and every subsequent mutator or recomputation. The report must identify
the first divergence and distinguish an observed trace from source inference.

The temporary, default-off `GOOPG_Q96_LEGACY_CHILD_TRACE=1` diagnostic must
extend the collision-free R116 construction-to-selected-join identity. For
each final occurrence, it must record its mapping cardinality (and an explicit
unmapped falsifier), every display-cost child position and concrete type,
whether the child's `PlanCost` is stamped or legacy-derived/fallback, and its
rows, width, startup, and total. It must also record all
`DeriveLegacyDisplayCost` inputs and nonrecursive arithmetic: the CPU tuple
constant, output rows, per-row term, child-total nesting, startup maximum, and
result. It must not add recursive totals.

The diagnostic must make no semantic, planning, or execution reads and retain
the normal disabled path. Generate flag provenance; test disabled silence,
duplicate structurally identical lower-join mapping, trace arithmetic, and
invalid/overflow inputs if it recomputes arithmetic. Reproduce R116's full
R111 controls: both forced forms with 64MB work_mem and collapse settings,
Goopg controls, OFFx2 and ONx2 text/JSON plans, values of 266, checksums, and
trace paths. Trace-on plans must be exact A/A matches to trace-off plans.

Remove diagnostics before the report. If selected lower-join mapping is not
one-to-one, a producer cannot be reached, or nested accounting cannot
reconcile the root 7.68, report that falsifier and stop. No Datum, production
cost, selectivity, Gather, executor, or search change is authorized.
