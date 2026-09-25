# M0142-0011 — disambiguate the `EstimateRows(*Join)` recompute gap M0142-0004b found

Status: accepted (recon closed 2026-09-15, no code change; fix filed as M0142-0012)

## Task

`.ralph/fix_plan.md`'s M0142-0011 line, filed by M0142-0004b: a generic
`*Join{Algo: JoinAlgoNestedLoop}` node beneath a SEMI join's outer input
(Q33/Q56/Q60's CTE branches) estimates `rows=1` via `estimateJoin`'s fresh
recursive `EstimateRows`, while the SAME node's own already-costed
`PlanCost.PlanRows` (what `EXPLAIN` actually prints for it) is `32` — two
disagreeing numbers for one subtree. Two candidate mechanisms were left
undisambiguated:

- **Mechanism A** — `estimateJoin`'s measured-selectivity branch is gated on
  `j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge`, excluding
  `JoinAlgoNestedLoop` for no PG-faithful reason (`calc_joinrel_size_estimate`
  sizes a joinrel once, independent of the winning Path's algorithm).
- **Mechanism B** — `EstimateRows(*Join)` never consults the node's own
  already-costed `PlanCost.PlanRows` the way `legacyDisplayCostOf`'s callers
  already do elsewhere.

The task's own resume point: re-instrument to determine (1) whether Mechanism
A alone fixes the witness, (2) whether B is reachable/needed independently,
(3) whether either fix is DP-search-safe. Do not land either fix blind.

## Method — instrument before theorising

Re-derived the standalone witness first (Q33's `ss` CTE body, same repro as
M0142-0004b) against the git-tracked SF0.25 cluster (private binary
`tmp/goopg-m0142-0011-bin`, port 65437 — no server was live on that port at
loop start; the shared nightly `tmp/goopg-bench-bin` path was avoided).
`EXPLAIN` reproduces the witness exactly (`HashAggregate rows=1` above `Hash
Semi Join rows=1` above a `Gather rows=99` subtree whose own `Nested Loop`
nodes print `rows=32`).

**Tested Mechanism A directly first** (cheaper than pure tracing, since the
candidate fix is one line and trivially revertible): broadened
`estimateJoin`'s gate from `j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge`
to unconditional entry (the `measured` flag inside the branch already falls
through to the untouched fallback when `joinEquiPairs` finds nothing, so this
should be a no-op everywhere Mechanism A does not apply). Rebuilt, re-ran the
same `EXPLAIN` — **the plan was byte-identical, `rows=1` unchanged.**
Mechanism A alone does not fix the witness.

Re-added a temporary `GOOPG_M01420011_TRACE`-gated trace in `estimateJoin`
(printed `j.Type`, `j.Algo`, `%T` of `j.Left`/`j.Right`, `l`, `r`,
`j.Predicate`, and the `joinEquiPairs`/`superkeyJoinEstimate` outputs;
reverted in full afterward — `git diff`/`git status` on `cardinality.go` and
every other file empty after the loop). Re-ran the standalone `ss` body with
the broadened gate still in place, to see whether Mechanism A even REACHES
the nodes that collapse:

```
estimateJoin type=0 algo=1 left=Project right=Project l=719876 r=30   (Parallel Hash Join store_sales x date_dim)
  pairs=1 fired=false boundProven=true measured=true
estimateJoin type=0 algo=0 left=*optimizer.Join right=*optimizer.IndexScan l=282 r=1  predicate=<nil>
  pairs=0 fired=false boundProven=false measured=false
estimateJoin type=0 algo=0 left=*optimizer.Join right=*optimizer.IndexScan l=1   r=1  predicate=<nil>
  pairs=0 fired=false boundProven=false measured=false
estimateJoin type=5 algo=1 left=Project right=Project l=1 r=1733   (the Hash Semi Join against item_1)
```

The two `Nested Loop -> Index Scan` nodes (`customer_address_pkey`,
`item_pkey`) are generic `*optimizer.Join` nodes (not `*NestedLoopIndexJoin`
— confirmed by dynamic type, since `estimateNLIndexJoin` is a SEPARATE
`EstimateRows` case that never printed) with **`Predicate == nil` and
`pairs == 0` regardless of the Algo gate.** Mechanism A cannot fix these
nodes no matter how the gate is written: `joinEquiPairs` has nothing to find
in `HashKeys`/`Predicate`/`LeftKey+RightKey` because none of them are
populated on this node shape.

## Root cause found: neither A nor B — R25's NLI decomposition orphaned cardinality estimation entirely

Reading `createplannl.go` (not tracing further — the question became "where
does this join's equi-key actually live", a static-code question) resolved
it. `createNestLoopIndexJoinPlan` (`internal/optimizer/createplannl.go:208`)
is the arm that builds a parameterized index-probe nested loop. Its own
comment names the mechanism directly:

> R25 (plan-parity-fix-take2): the fused `NestedLoopIndexJoin` is gone — PG
> has no such node. The decomposed shape is a lateral `Join` over the
> parameterized probe: `NestLoop` with an inner `IndexScan` whose keys are PG
> nestloop params (`OuterColumnRef`, level 1), driven per outer row by the
> lateral stream ... (`createplannl.go:353-361`)

The unmemoized NLI shape is built as (`createplannl.go:355-364`):

```go
j := &Join{
    Type:      jtNLI,
    Algo:      JoinAlgoNestedLoop,
    Lateral:   true,           // BindLateralOuter contract, plan.go:1150-1154
    Left:      in.outer,
    Right:     is,             // *IndexScan; is.Key/is.Keys hold the probe key
    Predicate: in.joinPredicate("PathNestLoop(NLI)", nil, p.Residual),
    ...
}
```

The join's actual equi-key lives on `is.Key`/`is.Keys` (the `*IndexScan`
child's own bound probe expression, an `OuterColumnRef` at level 1) —
`Predicate` carries only the LEFTOVER residual, which is nil for a fully
key-bound probe like this one. This is the exact same "key transplanted off
of `Predicate`" shape M0142-0006 already found and fixed for
`*NestedLoopIndexJoin.Predicate` — but M0142-0006's fix (`nliSemiMatchFraction`)
only patched `estimateNLIndexJoin`, the estimator for the OLD fused
`*NestedLoopIndexJoin` type. **The generic `estimateJoin` used for the NEW
decomposed `Join{Lateral: true}` shape was never taught the equivalent: it has
no `Lateral` arm, so `joinEquiPairs` — which only reads
`HashKeys`/`Predicate`/`LeftKey+RightKey` — always finds zero pairs on this
shape, `measured` is always `false`, and every unmemoized index-probe nested
loop in both corpora falls to the crude `l * r * 0.005` (capped at
`max(l,r)`) fallback, treating a ~1-row-per-outer-probe equality lookup as an
unmeasurable equijoin.**

`createNestLoopIndexJoinPlanFused` (`createplannl.go:265`, the sibling arm
taken when the inner is Memoize-wrapped) is the ONLY remaining producer of
the OLD `*NestedLoopIndexJoin` type — its own comment says so directly
("the memoized shape keeps the fused node and driver ... the decomposed Join
below cannot carry a Memoize child"). So `estimateNLIndexJoin` (which already
has the correct logic: return the outer's row count unchanged for INNER,
`nliSemiMatchFraction` for SEMI/ANTI — M0142-0006's fix) is reachable **only**
on the Memoize-wrapped minority of index-probe nested loops. Every unmemoized
one — the majority, since M0142-0005 (B6) independently established that
goopg's executor rarely gets a Memoize on the NL probe path — silently
reverts to the crude fallback.

This is neither Mechanism A nor Mechanism B: it is a THIRD, more precise
mechanism (call it **Mechanism C**), and it is a strictly better answer than
either candidate — Mechanism A cannot reach this node shape at all (no
`Predicate`/`HashKeys` exist to broaden the gate onto), and Mechanism B would
only paper over it post-search (reading the child's already-correct
`PlanCost.PlanRows`) rather than fixing what `EstimateRows` itself computes
for this shape during search, which is what every OTHER caller of
`EstimateRows` on a not-yet-costed candidate needs.

**Disambiguation answers, per the task's own three questions:**

1. Mechanism A alone does **not** fix the witness (confirmed empirically:
   broadening the gate produced a byte-identical plan; `pairs=0` at the
   affected nodes regardless of the gate).
2. Mechanism B is not the precise fix either — it would work around the
   symptom (post-search) rather than correct the estimator, and does not
   generalize to every `EstimateRows` call site the way fixing the
   `Lateral`-shape gap does.
3. Mechanism C's fix operates purely on structural fields already present on
   the node at construction time (`j.Lateral`, `is.Key`/`is.Keys`) — the same
   inputs `estimateNLIndexJoin` already reads for the fused type — so it is
   DP-search-safe by the same argument that makes `estimateNLIndexJoin`
   itself DP-search-safe today: no `PlanCost` consultation, no ordering
   dependency on when the node was costed.

## Follow-up

Filed as **M0142-0012** (`.ralph/fix_plan.md`, under this same M0142
milestone): teach cardinality estimation the decomposed NLI shape. Concrete
resume point — either (a) add a case ahead of the generic `*Join` dispatch in
`EstimateRows` (`cardinality.go:95-96`) that recognizes
`j.Lateral && isBoundIndexProbe(j.Right)` and routes to a generalized version
of `estimateNLIndexJoin`'s logic (adapted to read `j.Left`/`j.Right` instead
of `j.Outer`/`j.Inner`), or (b) fold the same logic directly into
`estimateJoin` as an early branch before the generic equi-pair path. Likely
blast radius is LARGE — this shape is the common (unmemoized) case for every
parameterized index-probe nested loop in both corpora, so M0142-0011's own
"run the full floor-measurement suite before landing" instruction applies
with extra weight; size it as its own task rather than folding into this
recon.

## Floor measurements

No production code changed this loop — Mechanism A's candidate one-line
change and the `GOOPG_M01420011_TRACE` instrumentation were both added and
fully reverted (`git status`/`git diff` on `internal/optimizer/cardinality.go`
and the rest of the tree empty; `go build ./...` clean post-revert;
`go test ./internal/optimizer/...` back to green, including
`TestFallbackCapFiresForNonHashAlgoDespiteStats`, which pins the OLD gated
behavior Mechanism A would have changed — left untouched since Mechanism A is
not being landed here). Plan-parity/`ea-ratchet` floors are unchanged from
M0142-0010's pin (TPC-H `match=8/22`, TPC-DS `match=2/99`, ea-ratchet 112
findings). Private sf025 server (`tmp/goopg-m0142-0011-bin`, port 65437)
stopped and the binary removed before commit; the git-tracked SF0.25 data
directory itself is untouched (read-only `EXPLAIN`s only).
