# R93 REPORT — `PathNestLoop` did not establish an executable partial NLI

R93 followed scope commit `de19a9494`. It made no production planner,
executor, costing, renderer, default, or plan-behavior change.

## Result

The Q96 final-relset partial source is not an executable partial NLI source.
The temporary, copy-isolated construction path successfully copied the shared
`PathPrebuilt` leaves before entering `createPlanNode`; its DP trace then
reported that the built source was `*optimizer.Join` and had no driving scan.
It was not `*optimizer.NestedLoopIndexJoin`.

This distinguishes two facts that the R92 evidence alone did not:

- `PathNestLoop` identifies a search path family. It does not prove which
  executor node `createNestLoopPlan` will emit.
- The Q96 candidate's inner was not parameterized in the way that selects the
  index-driven NLI node. It built an ordinary nested-loop `*Join`, whose
  current partial worker semantics are deliberately unmodelled. Its outer
  cannot simply be claimed by workers: the ordinary join operator has a
  different execution protocol from `nestedLoopIndexJoinOp`, and no focused
  serial-versus-parallel identity proof exists for it.

The observed trace line was:

```
DPTRACE upper gate=agg-source verdict=refused gate=shape type=*optimizer.Join subtree=no-driving-scan
```

The first attempted change briefly admitted `PathNestLoop` to the generic
Gather path reader based on the path kind. Q96 then reached
`createPlan: PathGather over a subtree with no driving scan`, proving that
path-kind admission and node-side executability were not equivalent. That
change was removed before final validation.

## Isolation result

The temporary copier made a recursive private copy of every `PathPrebuilt`
leaf before ordinary construction, avoiding `createPlanNode`'s in-place
`PlanCost` stamp. The isolated build completed; it was the node-kind/driving
scan test, rather than a shared-state mutation or construction panic, that
refused the candidate. The copier, temporary trace, and all walker changes
were removed because the selected source did not meet the NLI premise.

## Final validation and artifact state

After removal, the production diff is empty. The following foreground gates
passed:

```
go test ./internal/optimizer ./internal/executor
go vet ./internal/optimizer ./internal/executor
git diff --check
```

Q96 was run against the private SF0.25 clone at `127.0.0.1:5560` under
`GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`, and
`GOOPG_PGSHAPED_DP_TRACE=1`. The final no-change plan remained serial, and
the private service was stopped after every arm.

## Consequence

Do not widen generic Gather admission, executor claim walkers, or upper
partial-aggregate materialization from `PathNestLoop` alone. A future round
must either prove a particular partial candidate builds a
`NestedLoopIndexJoin`, or separately design ordinary nested-loop worker
semantics, its outer/inner identity rules, and exact partial cost/row
coordinates. Neither route may force Q96's Gather.
