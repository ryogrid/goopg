# R92 SCOPE — attribute Q96 upper partial-aggregate source admission

R92 follows R91 report commit `42b5b6bf1`. It is measurement-only. It changes
no production planner, executor, cost, renderer, default, or plan behavior.

## Evidence and question

PG18.3 Q96 uses a partial aggregate above a partial
`store_sales -> household_demographics -> store` prefix. R91 now gives Goopg
that relation order, but its natural plan remains serial. The R91 trace is
specific: `upper.groupagg.plain` is offered and `agg-upper` refuses with
`gate=subtree subtree=no-driving-scan`.

This is not absence of a partial path. The same trace records an accepted
`join.nestloop.partial` path for the final relset (24 rows, total 16367.34),
and `cpgather` reports the final relset admitted. `createGroupingPaths`
currently passes its rendered serial child to `addPartialAggSplitPath`; that
child has no parallel driving scan. `searchedRelOf(child)` already exposes the
same search rel and its `PartialPathlist`.

The missing fact is whether a node built from that partial path is a valid
single-copy worker subtree at the upper boundary. Earlier R21 Gather-unwrapping
notes do not establish this, because R91's refusal is on the serial child
before that route produces a split candidate.

## Authorized work

After review and commit/push, add only default-off trace diagnostics. For the
serial child and every final-relset partial candidate, record path kind, rows,
cost, `ParallelWorkers`, `ParallelSafe`, `ParallelAware`, `RequiredOuter`, and
whether the root has an unresolved outer binding. Classify worker eligibility
only when workers are positive, the root is unparameterized/bound for the
worker context, construction succeeds, and the existing exact guards report
no unsafe node, no Gather, and a driving scan. A scan alone is not evidence of
an independently runnable worker subtree.

Any path-to-node inspection must use a copy-only, isolated and recoverable
attempt. It must recover a construction panic and report it as a distinct
barrier, without letting trace-on alter normal query success. The throwaway
tree must not be published, cached, or attached to the live plan, and the
diagnostic may not mutate its source path, rel, or shared node. It may not
append paths, alter costs, enable a Gather, or affect trace-disabled EXPLAIN.
Remove all diagnostic code before R92 completes.

## Non-goals and gates

R92 does not enable `GOOPG_GATHER_PATHS`, force Q96, change join or aggregate
costing, alter partial-path dominance, revive a Gather unwrap, weaken any
driving-scan/double-Gather guard, or implement partial-node wiring. A behavior
change needs a separate scope after this measurement.

Pin trace-disabled no-op and serial/partial classifications in focused tests.
Capture Q96 trace A/A, Q9/Q41/Q91/Q96 trace on/off identity, and Q96 values.
Run optimizer test/vet and whitespace gates before and after diagnostic removal.
The report must name either an exact safe construction, an exact barrier, or a
non-runnable partial source; it must not claim parity from a forced control.
