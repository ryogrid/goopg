# R95 SCOPE — lateral-probe nested-loop worker semantics

R95 follows R94 report commit `ea78ba7`, whose item-4 measurement closed
the ordinary-shape route for Q96: every NL node in Q96's tree is
`Lateral=true` (4/4 TEMP-probed, reverted) — R25's decomposed probe
(`createplannl.go:369`), a parameterized `IndexScan` with `OuterColumnRef`
keys re-opened per outer tuple. R94's walks correctly refuse it. This
round designs that third shape's worker semantics from its execution
contract; it does not widen R94's INNER-only admission.

## Evidence

- Q96's top NL (`time_dim_pkey` probe, `Index Cond: (t_time_sk =
  store_sales.ss_sold_time_sk)`) re-opens its inner per outer row with
  the outer tuple in scope (`openLateral`/`nextLateral`,
  `join_lateral_stream.go:105-140`). All correlation state is
  per-outer-tuple: the lateralBindable slot is repointed per tuple,
  `ctx.OuterRows` is pushed/popped around each right-side call, and the
  per-tuple `innerCTE` is swapped in/out (`join_lateral_stream.go:30-56`).
- Worker contexts already separate exactly this state: `OuterRows` is
  copied by value at fan-out, `CTERowCache` is created empty per worker
  (`parallel_worker_ctx.go`, third rule — sharing it is a fatal map
  throw, not a race). A worker re-opening the probe for its own outer
  rows touches no cross-worker state. The per-call binding hygiene
  (`bindOuter`/`unbindOuter`: `ctx.OuterRows` append/truncate plus
  `CTERowCache` swap, `join_lateral_stream.go:282-320`) runs on
  worker-owned state only.
- The probe is never materialized whole (re-opened per row), so the
  probe itself has no N× memory term — cheaper than R94's ordinary
  case, whose size-gate deferral does not transfer. Composition is
  separate: a merge join below the outer is N× by design (each worker
  sorts/reads the whole merge inner), and a hash below the outer is
  N× until the prebuild-descent gap (below) is closed.
- R60 files parameterized partial NL probes today
  (`inner.CheapestParameterized` loop in `addPartialNestLoopPaths`);
  only the classifier + node twin refuse them.

## Authorized change

Under the existing opt-in gather/upper partial-aggregate controls only:

1. Planner predicate (new, beside `nestedLoopJoinIsPartialCapable`):
   `*Join` with `Algo == JoinAlgoNestedLoop`, `Lateral == true`,
   INNER type only (Q96's comma joins plan as INNER — the search treats
   a clauseless pair as inner, `joinpaths.go:167`, and the R94 probe
   read `type=Inner` on all four NL nodes; CROSS has no R60 producer
   and stays refused), non-nil children, and an inner that is a bare
   parameterized index probe (`*IndexScan`/`*IndexOnlyScan` with probe
   keys, `SAOPKeys == 0` — the `pidx` leaf filter is consulted only at
   the single-range site, `operators_index.go:235-239`, so a SAOP probe
   is refused). No wrappers (the BuildFast `opNodeOperator` bridge
   implements `lateralBindable` unconditionally, `opnode.go:431` — the
   probe must bind via `OuterColumnRef` keys under the `ctx.OuterRows`
   push only, so assert `right.(lateralBindable) == false`), no Memoize
   (covers R60's `getMemoizePath` loop output explicitly), no bitmap.
   General lateral subtrees (lateral aggregates, SRF, CTE-dependent
   inners) return the existing refusal marker. SEMI/ANTI/LEFT/RIGHT/
   FULL refused as scope-minimization.
2. Path classifier: admit a partial `PathNestLoop` whose inner has
   `RequiredOuter != 0` only when the requirement is satisfiable by the
   outer (`req ⊆ outer relids`, the V8 subset test already computed at
   the R60 site — re-check, don't trust), the inner is the filed
   parameterized probe, jointype INNER only (CROSS has no producer —
   review close-out), outer a recognized partial worker shape with
   workers/safety as in R94. Unparameterized inners keep the R94 rule
   unchanged.
3. Node walks (`drivingScan`, `stampParallelScan`, sort-crossing
   sibling; `unstampParallelScan` by matrix pin): descend the outer
   literally under the new predicate. Executor: all three claim
   walkers descend `joinOp.left` literally under the same predicate;
   the probe inner takes no claim state. The probe's per-row re-open
   needs no prebuild (unlike hash/bitmap) — state that explicitly.
4. Outer-subtree composition: Q96's outer is itself hash joins.
   `collectShareableJoins` (`parallel_hash_build.go:273-284`) returns
   without descending on any non-hash `joinOp` — including NL — so
   hashes under the lateral outer are invisible to the leader prebuild
   today (the same gap exists for R94's ordinary-NL composition, via
   `parallelChildren`/`HasShareableHashJoin` claiming what the operator
   walk cannot collect). Close it with a lateral-NL-left descent arm in
   `collectShareableJoins` plus a cross-walk agreement test
   (`parallelChildren` / `HasShareableHashJoin` / `collectShareableJoins`
   / all three claim walks); if that arm proves invasive, fall back to
   refuse-when-outer-contains-hash, stated in the report. Bitmap in the
   outer gets the same reuse-or-refuse treatment via the existing
   `prebuildBitmap` path. No other new prebuild machinery.

No path or node is admitted by kind alone; the three predicates
(path / node / executor) must agree and be unit-pinned together, as
in R94.

## Required proof

- Q96 serial-vs-parallel identity (hand-Gathered lateral probe,
  duplicate-sensitive) at 1/2/4 workers — the round's gate, since Q96
  is the only known consumer.
- Refusal matrix: SEMI/ANTI/LEFT/RIGHT/FULL lateral, CROSS lateral,
  non-probe lateral inner (aggregate/SRF/CTE), SAOP probe, Memoize/
  bitmap probe (incl. the `getMemoizePath` loop output), wrapped probe
  (incl. the `lateralBindable`-bridge case), unsatisfiable
  parameterization, nil claims, unsafe/Gather sources; inner probe
  takes no claim state on all three walks (`pscan`/`pbm`/`pidx`).
  One `subqCache` depth-scoping regression pin (worker-local, but the
  probe re-open churns depth per row).
- Copy-isolation and rows/cost/workers/divisor pins only if item-4-style
  construction is used; if the rendered-child route suffices (as in
  R94), state that with the test that proves the stamped node is the
  EXPLAIN-read instance (the R76/Q22 lesson).
- Q96 natural + opt-in plan/value captures plus Q9/Q41/Q91 controls;
  a plan change is reported, not forced; values must match PG.

## Non-goals and gates

R95 does not alter defaults, PostgreSQL sources, worker sizing, join
order, join/aggregate cost formulas, join-method choice, partial-path
dominance, EXPLAIN text, R94's ordinary-shape rules, or general
lateral-subtree parallelism. It does not force Q96's Gather.

Before code: agent review, then `git commit -n` and push. After code:
focused tests; `go test ./internal/optimizer ./internal/executor
./internal/testutil/estimateaudit`; matching `go vet`; whitespace;
then committed-binary foreground TPC-H values, SF0.25 values,
Q96/Q9/Q41/Q91 captures, and a fresh live-PG shape census. Commit and
push an English report with exact results.
