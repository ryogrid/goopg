# M0146-0002i — parameterized-probe partial nested loop under SEMI

Implementation of the SEMI arm of M0146-0002a's finding: a parameterized
index-probe SEMI path was filed cheaper than the Parallel Hash sibling but
could not drive a Gather (`partialPathDrivingKind` admitted parameterized
inners only under `JoinInner`), starving the pathlist head and falling back
to a serial-above-Gather plan.

## Change

One shared jointype set {INNER, SEMI} is now read by the three probe gates:

- `partialPathDrivingKind`'s parameterized-inner arm reads
  `partialProbeNestLoopJointype` (`gatherpaths.go`).
- `lateralProbeJoinIsPartialCapable` reads `partialProbeNestLoopJoinType`
  (`parallel.go`) — the `optimizer.JoinType`-domain twin.
- Executor `lateralProbeJoinPartial` (`parallel_scan.go`) carries the same
  set.

ANTI stays refused everywhere pending M0146-0002j's producer widening; LEFT
and the bitmap-probe family stay refused; the fused
`NestedLoopIndexJoin` family keeps its own verified {INNER,LEFT,SEMI,ANTI}
set (M0145-0010).

## Evidence

- `q4-after.txt` — Q4 canonical plan post-change: `Nested Loop Semi Join`
  inside `Gather` over `Parallel Seq Scan orders` with the per-worker
  `Index Scan` probe on `idx_lineitem_orderkey_fkidx` — PG's exact spine
  (cost 76255 vs PG 70092; pre-change 172846 serial-above-Gather).
- `tpch-diff-before.txt` / `tpch-diff.txt` — canonical captures. Q4:
  `[join-order, aggregation-strategy, sort-strategy, parallelism]` →
  `[sort-strategy, parallelism]` — two categories cleared. Aggregate
  TPC-H: join-order 14→13, aggregation-strategy 6→5; residual
  sort-strategy/parallelism is the `Gather Merge` + `Partial GroupAggregate`
  upper (M0146-0003/0025).
- `tpch-class.txt` — per-stage divergence class report.
- Fire-set gate (`tmp/m0146-0002i-fireset/`): zero fires on TPC-DS SF0.25
  and SF1 — no TPC-DS plan moved.

## Verification

- `TestPartialPathDrivingKindNestLoopProbe` (optimizer): SEMI probe
  classifies to the outer's driving kind; LEFT/ANTI/RIGHT/FULL and
  unsatisfiable `RequiredOuter` still refused.
- `TestLateralProbeJoinIsPartialCapable`, `TestPartialLateralWalkAgreement`,
  `TestPartialPathDrivingKindLateralProbe`, `TestPartialPathDrivingKindMemoizedNLI`:
  re-pinned to {INNER, SEMI} with LEFT/ANTI as the refused set.
- `TestParallelLateralWalkerRefusals` (executor): SEMI lateral probe
  attaches on all three walks; ANTI/LEFT/CROSS refused.
- `TestParallelLateralSemiProbeIdentity` (executor, `-race`, workers
  1/2/4): serial-vs-parallel row identity for the decomposed
  `Join{Lateral,Semi}` over `SeqScan + IndexScan`.
- Gates: `units` PASS; `tpch-spotcheck` PASS (Q12=2, Q13=33);
  `tpcds-sf025` sweep PASS=96/0/0, plan shapes 99/99 unchanged;
  `tpch-acceptance-arm` PASS (24/24 value-identical);
  `tpcds-fireset` PASS (fires=none at both scales); pgbench smoke via
  pre-commit hook.
