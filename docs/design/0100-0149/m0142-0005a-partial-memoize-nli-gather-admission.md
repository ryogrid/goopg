# M0142-0005a — Admit the fused NLI+Memoize shape as a Gather-driving kind

Task: `.ralph/fix_plan.md` **M0142-0005a**. Parent: **M0142-0005** (scoping
recon, `m0142-0005-recon-partial-memoize-refused-by-gather-eligibility.md`,
esp. its "Update 2026-09-18" section).

Status: **implemented** — see §"Verification" for the gate evidence.

## What the recon got right, and the one thing it got wrong

The recon's diagnosis was correct end to end: the `NLI + Memoize` candidate
for TPC-DS Q34/Q73 is **generated and correctly costed** as a partial path,
and the sole blocker is Gather eligibility — no Gather path was ever filed
from it. Its correction that "no per-worker Memoize mechanism is needed —
the executor already builds one for free" was also correct.

Its **site list was wrong** about which representation needs the arms. The
update named three siblings to extend:

- `partialPathDrivingKind`'s `PathNestLoop` lateral-probe branch — correct;
- `parallel.go`'s `lateralProbeIsPartialProbe` — wrong layer;
- `parallel_scan.go`'s `lateralProbeJoinPartial` — wrong layer.

The two node/executor twins serve the **decomposed** lateral probe:
`Join{Algo: NestedLoop, Lateral}` / `joinOp`, which is what a **bare**
parameterized index probe emits (R25 decomposition,
`createplannl.go:196-209`). A `*Memoize` can never appear under
`Join.Right`, and a `*memoizeOp` can never appear under `joinOp.right` —
`createPlan` has **no** free-standing `PathMemoize` arm at all (it panics);
the memoized shape emits the fused `*NestedLoopIndexJoin` node, which
carries the cache as the **`InnerMemo` field**, not as a child:

```text
Path:   PathNestLoop(outer=partial, inner=PathMemoize(PathIndexScan))
Node:   NestedLoopIndexJoin{Outer, Inner: *IndexScan, InnerMemo: *Memoize}
Op:     nestedLoopIndexJoinOp{outer, inner: indexScanOp+memoizeOp}
```

So `*Memoize`/`*memoizeOp` cases in the decomposed twins would have been
dead code (attempted, verified unreachable, reverted). The real work is a
**new sibling set for the fused shape**, guard-for-guard across every walk
that already has a `*Join`/`joinOp` arm.

## Site map — what actually landed

### Path level (`internal/optimizer/gatherpaths.go`)

`partialPathDrivingKind`'s `PathNestLoop` arm: the lateral-probe branch
unwraps **one** `PathMemoize` layer (`Children[0]` is always the wrapped
probe — `getMemoizePath`, `joinpathsmemoize.go:292-303`) and runs the
existing bare-probe check on the child: `PathIndexScan` +
`len(IndexClauses) > 0`, `Jointype == JoinInner`, outer/inner required-relid
re-check via `calcNestloopRequiredOuter`. A **whole-inner** `PathMemoize`
stays refused — `getMemoizePath` only wraps a parameterized probe, so a
parameterless memoize means a producer changed (fail closed).

`setOpBranchDrivingKindIsSupported`'s `PathNestLoop` arm got the identical
unwrap — its header claims "mirrors `partialPathDrivingKind`'s PathNestLoop
arm guard-for-guard", which the general arm's widening would otherwise
falsify.

### Node level (`internal/optimizer/parallel.go`)

New exported predicate `NestedLoopIndexJoinIsPartialCapable` — the single
verdict every executor walk re-runs (literal agreement, no twin to drift):
non-nil `Outer`/`Inner`, `JoinTypeInner` only, and `Inner` is exactly the
bare keyed probe `lateralProbeIsPartialProbe` admits (`*IndexScan`/
`*IndexOnlyScan` with `Key`/`Keys`, no SAOP keys, no range bounds — a
bitmap inner is refused by the default arm since a re-probed bitmap has no
claim-set story). `InnerMemo` is irrelevant to the verdict: each worker
builds its own `memoizeOp`/`kvcache` over the shared read-only plan —
matching PG's per-worker `MemoizeState`, whose DSM shuttles only
instrumentation counters (`nodeMemoize.c:1190-1260`).

Sibling arms added, all descending the **Outer literally**:

- `drivingScan` — refuse non-capable, else descend `Outer`.
- `stampParallelScan` — copy-on-write through `Outer` only; the probe is
  never stamped (it is not a partitioned scan).
- `unstampParallelScan` — walks both sides unconditionally, matching its
  enforcement-inverse convention (an unstamped inner un-stamps to itself).
- `HasShareableHashJoin` — a hash below an approved NLI outer is
  leader-prebuilt; non-capable reports nothing.
- `drivingScanCrossesSort` — follows `Outer` so a Sort between join and
  driving scan is still seen (unreachable via `findPartialSubtree` today —
  `*NestedLoopIndexJoin` is a `terminatesPartial` member — kept
  guard-for-guard so the two can never disagree).

### Executor level (`internal/executor/`)

All descend `x.outer` only, guarded by
`optimizer.NestedLoopIndexJoinIsPartialCapable(x.plan)`:

- `parallel_scan.go`: `attachParallelScan`, `attachParallelBitmapScan`,
  `attachParallelIndexScan` — the outer's scan/bitmap/index takes the
  claim; the re-probed inner takes none.
- `parallel_hash_build.go`: `collectShareableJoins` — hashes under the
  approved outer are collected for leader prebuild; the probe is never
  descended.
- `operators_gather.go`: `collectBitmapScans` — a driving bitmap under
  the approved outer is found for leader prebuild/page-claiming.

The comment in `parallel_scan.go`'s `lateralProbeJoinPartial` was updated
to record that `memoizeOp` never reaches that walk (it lives only as
`nestedLoopIndexJoinOp.inner`).

## Semantics

PG's `try_partial_nestloop_path` (`joinpath.c`) files a partial nestloop
whose inner is the cheapest parameterized path — explicitly including the
`get_memoize_path` variant (`joinpath.c:2194-2199`). The worker semantics
this admits:

1. Only the outer is partitioned (claim state attaches to the driving
   scan under `Outer`).
2. No claim state ever reaches the inner probe — it is not a partitioned
   relation; each worker binds its own outer row (`BindOuter`) and
   re-opens/rescans the probe.
3. Each worker's Memoize cache is private by construction — no shared
   cache, no DSM analogue needed.
4. Non-capable shapes fail closed at every layer (planner refuses the
   partial path; executor walks refuse attachment), so an unexpected
   wrapper can never silently produce duplicated or dropped rows.

## Verification

- `GOOPG_PGSHAPED_DP_TRACE=1` on Q34 (private :5533 clone): accepted
  `producer=gather` DPPATH lines now appear, including
  `relids={0,1,2,3}` `rows=136` `total=17477.699…` — matching the emitted
  `Gather cost=1182.47..17477.70 rows=136` — plus accepted
  `producer=join.nestloop.partial` lines for the driving relsets.
- TPC-DS SF0.25 sweep: **PASS=96 MISMATCH=0 CKMISMATCH=0 TIMEOUT=0**
  (stamped). 60/99 plans flipped vs the previous capture — the newly
  admissible partial NLI contesting everywhere; **Q34 and Q73 both flip to
  the PG reference shape** (`Gather > Nested Loop > … > Memoize > Index
  Scan`, `bench/tpcds/plans-pg/Q34.txt`/`Q73.txt`). Q66 kept its
  `Gather > Append` pure-arm shape (the branch-arm unwrap now also admits
  a memoized NLI partial inside a SetOp branch).
- New tests: `internal/optimizer/partial_nli_memoize_test.go`,
  `internal/executor/parallel_nli_memoize_test.go` — bare+memoized
  admission, the refusal matrix (non-INNER jointypes, bitmap/SAOP/unkeyed/
  ranged inner, nil plan/state), outer-only claim attachment on all three
  walks, shared-hash and bitmap collection through the outer.
- `go test -race` on the new tests: green.
- Gates: build+vet clean; `RALPH_PRECOMMIT_SCOPE=units` all-ok;
  `scripts/tpch-spotcheck.sh` PASS Q12=2/Q13=34 (stamped);
  acceptance-arm `PGSHAPED=1` VERDICT **PASS 24/24 MATCH** on values vs
  `/tmp/arm-0006c3-staged.txt` (stamped).
- `make plan-gate`: 14/22 diverged — **baseline drift, not this change**.
  The live :65433 binary (built 09-19 03:11) predates the staged work, so
  the diff measures HEAD-vs-`m0137-0005-rebaseline-20260915` (~100
  commits) — the same 14/22 the prior loop recorded. Spot-verified the
  divergent NL shapes are PG-faithful anyway: Q12's
  `Gather > NL > Parallel Seq Scan + Index Scan` is exactly PG 18.3's own
  Q12 plan; the bare-probe flips ride the pre-existing decomposed
  `Join{Lateral}` path this change does not touch.

## Recorded blocker (separate task)

`make race-gate` is red at HEAD: a pre-existing data race between
execution-time lazy SubPlan builds (`acquireSubPlanOp`/`expr.go`
`Build`→`maybeInstrument` reading the package-global `instrumentScope`)
and the build-time scope handoffs (`buildUnderNilScope`/
`buildUnderFreshScope` under `instrumentScopeMu`). Reproduced at the base
commit before this change; unrelated to the NLI arms (the new tests are
race-clean). Already filed as **M-NIGHTLY-instrumentscope-race-fix** with
`TestExplainAnalyzeSubPlanScopeObservation` as its prescribed probe —
left to that task rather than folded in here.
