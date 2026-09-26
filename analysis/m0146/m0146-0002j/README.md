# M0146-0002j — parameterized-probe partial nested loop under ANTI

Implementation of the ANTI arm of M0146-0002a's finding: the producer
refused `jt=ANTI` at `joinpathsnli.go`'s V1-nl-inner gate, so TPC-H Q21's
`Nested Loop Anti Join` could never live inside the Gather — it sat above
it probing l3's index for each of 39277 gathered rows.

## Change

Four sites widened to ANTI, in the same commit per R94's
together-or-not-at-all rule:

- Producer gate `addPartialNestLoopPaths` (`joinpathsnli.go`): {INNER,SEMI}
  → {INNER,SEMI,ANTI}. This admits ANTI for BOTH inner families the loop
  files — the prepended unparameterized member (ordinary whole-inner
  partial NL, whose downstream `{I,L,S,A}` arm M0145-0010 already
  verified) and the parameterized probes.
- `partialProbeNestLoopJointype` (gatherpaths.go): {I,S} → {I,S,A}.
- `partialProbeNestLoopJoinType` (parallel.go): {I,S} → {I,S,A}.
- Executor `lateralProbeJoinPartial` (parallel_scan.go): {I,S} → {I,S,A}.

LEFT stays refused everywhere (worker-local in principle but
unverified for the probe shape, no measured consumer); bitmap probes
stay refused; fused `NestedLoopIndexJoin` keeps its own verified
{I,L,S,A} set.

## Evidence

- `q21-after.txt` — Q21 canonical plan post-change: `Nested Loop Anti
  Join` INSIDE `Gather` probing `idx_lineitem_orderkey_fkidx` on l3 per
  worker — PG's placement (`q21-pg.txt` shows the same `Gather → NL Anti
  → … → Index Scan l3` spine). Cost 182221 vs pre-change 263100, vs PG
  269864; query executes in ~2.3 s with the canonical 412 rows.
- `tpch-diff-before.txt` / `tpch-diff.txt` — canonical captures. Q21:
  `[join-order, parallelism, qual-placement]` → `[join-order,
  join-method, parallelism, qual-placement]` — the anti probe moved
  inside the gather (the targeted arm) while the outer tree reelected a
  different join order: goopg joins (l1↔supplier) inside the gather then
  probes orders above it, where PG builds a `Parallel Hash Join` over all
  three inside the gather. The residual is join-order on the outer tree
  plus the method labels it carries — attributed to the D1-sublink class
  (the EXISTS→semi/pull-up stage), which already owned Q21's class
  before this change; it is not a new divergence family.
- `tpch-class.txt` — D1-sublink={Q21}; D3-partialpath set unchanged
  (Q1 Q4 Q5 Q8 Q12 Q15a Q19 Q20 Q22).
- Fire-set gate (`tmp/m0146-0002j-fireset/`): zero fires on TPC-DS
  SF0.25 and SF1 — no TPC-DS plan moved.

## Verification

- `TestPartialPathDrivingKindNestLoopProbe` (optimizer): {I,S,A} admit;
  LEFT/RIGHT/FULL and unsatisfiable `RequiredOuter` refused.
- `TestPartialNLFilingInnerOnly`: producer files {I,S,A}, refuses LEFT.
- `TestLateralProbeJoinIsPartialCapable`, `TestPartialLateralWalkAgreement`,
  `TestPartialPathDrivingKindLateralProbe`, `TestPartialPathDrivingKindMemoizedNLI`:
  re-pinned to {I,S,A} with LEFT refused.
- `TestParallelLateralWalkerRefusals` (executor): SEMI+ANTI probes attach
  on all three walks; LEFT/CROSS refused.
- `TestParallelLateralAntiProbeIdentity` (executor, `-race`, workers
  1/2/4): serial-vs-parallel row identity for the decomposed
  `Join{Lateral,Anti}` over `SeqScan + IndexScan` — the fixture's
  `NOT EXISTS` elects the decomposed probe under
  `SetIndexProbeCostMultiplier("1")`.
- Gates: `units` PASS; `tpch-spotcheck` PASS (Q12=2, Q13=33);
  `tpcds-sf025` sweep PASS=96/0/0, plan shapes 99/99 unchanged;
  `tpch-acceptance-arm` PASS (24/24 value-identical);
  `tpcds-fireset` PASS (fires=none at both scales); pgbench smoke via
  pre-commit hook.
