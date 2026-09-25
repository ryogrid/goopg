# R116 SCOPE — Q96 legacy forced-join decision attribution

## Question

R111 established that PG18.3 prices the common-input Q96 `hdem-first` form
175.91 lower, while Goopg prices `store-first` 7.68 lower. R115 established
that both parenthesized forms bypass Goopg's HashJoin Path/cost seam; a
same-server comma-join control reaches it, while Q96 does not. This slice asks
which inputs and branch decisions in the actual legacy/prebuilt join builder
produce the two Goopg forced-form costs.

## Authorized measurement

Before code, inventory every direct `chooseInnerJoinAlgo` caller (currently
the legacy explicit-join construction and cross-join promotion) and every
post-construction mutator that can change algorithm, BuildLeft, or cost,
including `rewriteJoinsToNLI` and small-dimension/BuildLeft overrides. Also
inventory the legacy PlanCost stamping path: each node self/child contribution
and its caller. The report must name the precise Q96-reached call/mutation
sequence; an unobserved candidate may not be called causal.

Add one exact-value, default-off temporary diagnostic switch,
`GOOPG_Q96_LEGACY_JOIN_TRACE=1`. At each legacy explicit-join construction it
must record a collision-free per-statement occurrence ID; left/right source
sets; parsed join type; pre-decision child estimated rows and displayed costs;
hash-key eligibility; `chooseInnerJoinAlgo` input/result; final algorithm and
BuildLeft; and the emitted node's estimate/cost. Every inventoried later
mutation and decline must emit a record with the same identity or an explicit
successor identity. The diagnostic must separately record legacy PlanCost
stamping terms and reconcile a root exactly once: parent totals may include
children, so the report must state the nesting rule and never sum recursive
totals as though they were disjoint.

Construction-to-selected-tree mapping must use a trace-local observational
sidecar, not structural matching. The sidecar identity is assigned once at
construction, carried only as metadata through each clone/rewrite, reset per
statement, and never read by planning or execution. The final node walk emits
the retained identity and an occurrence cardinality verdict. Focused tests
must include duplicate structurally identical joins and prove an exact,
one-to-one mapping (or an explicit unmapped/merged verdict), disabled silence,
and no semantic read of the sidecar.

The switch must not affect estimates, node construction, algorithm choice,
BuildLeft, PlanCost, plan-cache fingerprinting, execution, or output. The
diagnostic must be removed before the report; the final source diff must be
documentation only.

Run on the private R111 common-input clusters, with the immutable R101 forms,
64 MB work memory, collapse limits one, and existing Goopg controls. Capture
OFF x2 and ON x2 text/JSON plans, exact value evidence (266), and the PG
plans/statistics already required by R111. Record artifact paths, checksums,
and all trace records. The report must reconcile each Goopg forced-form root
to its selected legacy join records, identify the first differing legacy
input/decision, and distinguish an observed fact from a source-level
inference. If the trace cannot map every selected join, report that falsifier
and stop.

## Non-goals

No Datum-size experiment, planner cost tuning, statistics mutation, Gather
forcing, path-search modification, executor change, or production flag is
authorized. A correction suggested by the attribution requires a separate
reviewed scope and all corpus gates.
