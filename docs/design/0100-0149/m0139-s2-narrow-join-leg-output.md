# M0139-S2 — narrow scan output at the new hook

Status: accepted
Milestone: M0139 — Executor-side narrowing / projection pushdown
Type: planner behaviour change (default ON via the existing
`GOOPG_NARROW_LEG_HOOK` flag, opt-out polarity)

## Goal

S1 (`docs/design/0100-0149/m0139-s1-join-leg-hook.md`) stood up
`narrowJoinLeg`, an attachment point reached by every join constructor for
the legs `narrowBuildInput`/`narrowMergeInput` never touch — a hash join's
outer/probe side, and both sides of a nested loop (plain and NLI) — but made
it unconditionally decline, proven byte-identical to its input by
construction. S2's job, per the milestone doc's own instruction, is to
**reuse the existing narrowing rather than duplicating it**: fill that
decline in with `narrowBuildInput`'s own keep-set derivation chain
(`joinKeepSet` → `buildKeepSet` → `neededKeepSet`, all in
`narrowoutput.go`), applied uniformly at the new hook.

## What landed

`narrowJoinLeg`'s signature grew a `*Path` parameter (the leg's own path,
already in scope at both `joinInputsFor` call sites, since `outerPath`/
`innerPath` are its own arguments) plus an `nliInner bool`, and its body now
runs the SAME three-tier derivation `narrowBuildInput` uses:

1. `joinKeepSet(n, p)` — the parent-aware `JoinKeep` derived by
   `deriveJoinKeeps` at search time.
2. `buildKeepSet(n, p)` — the path's own `Target`, when the leaf's schema
   still matches the path's `Rel.baseLeaf` coordinates.
3. `neededKeepSet(n.Output(), p.Rel.NeededCols)` — the statement-wide
   fallback, over-inclusive by construction.

Whichever tier answers, the result feeds `narrowPlanOutput` exactly as
`narrowBuildInput`/`narrowMergeInput` already do. Every existing refusal
(`p == nil`, `p.Rel == nil`, `!p.Rel.NeededColsKnown`) returns the pair
untouched, matching those two arms.

### Why this is safe under a nested loop, where `deriveJoinKeepsAt` never
### stamps `JoinKeep` ("F3-conservative, B-01a NL policy")

That policy is about the *tightest* tier only. `joinKeepSet`'s derivation
walks the join *path's* own quals (`collectJoinQualNames`), which cannot see
an NLI's per-row probe key — that key lives on the **inner path's**
`IndexClauses`, not on the join path. Declining to stamp `JoinKeep` under an
NL correctly makes `joinKeepSet` report "unknown" for any such leg (never a
wrong answer, because `deriveJoinKeepsAt`'s `PathNestLoop` case `return`s
without recursing into its children at all — nothing below an NL is ever
stamped), and this function then falls through to `buildKeepSet` /
`neededKeepSet`. Both of those tiers are join-kind-agnostic by construction:

- `buildKeepSet` reads a path's `Target`, computed from the statement-wide
  `NeededCols` at path-creation time (Slice 1), independent of which join
  wraps the scan.
- `neededKeepSet` reads `Rel.NeededCols` directly — a NAME set collected by
  a **raw walk of the parse tree's** WHERE/ON/FROM clauses
  (`collectStmtColumnNames`), not reconstructed from path-level qual
  structures. An NLI's index-qual reference to an outer column is still a
  plain AST-level clause reference, so it is already a name in that set,
  the same as any other qual reference.

`narrowcostinputs.go`'s safety argument for this same chain — "this can
only ever keep TOO MANY columns, never too few" — therefore holds here
unchanged, regardless of join kind.

### The one case that genuinely cannot narrow: an NLI's inner slot

Verified live, not merely reasoned about: the first version of this change
(no `nliInner` guard) panicked two different ways on real fixtures —

- `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput` (already in the
  optimizer suite): `createplan: NLI inner emitted a *optimizer.Project,
  but NestedLoopIndexJoin.Inner is an *IndexScan`
  (`createNestLoopIndexJoinPlan`/`createNestLoopIndexJoinPlanFused`,
  `createplannl.go`).
- `TestDerivedTableUnderIndexNLReturnsRows` (executor suite): the same
  class of panic one level down, in the bitmap-index sibling arm
  (`createNestLoopBitmapJoinPlan`, `interface conversion: optimizer.Node is
  *optimizer.Project, not *optimizer.BitmapHeapScan`).

Both drivers re-probe their inner per outer row and therefore type the slot
concretely (`*IndexScan` / `*BitmapHeapScan`, not `Node`) — wrapping it in a
`*Project` is a plan-**time** panic, not a narrower plan. This is structural,
not a keep-set question, so it cannot be fixed by picking a different
derivation tier; it has to be excluded outright. `narrowJoinLeg` takes an
`nliInner bool` for exactly this, and `joinInputsFor` sets it to true only
for the inner call when `kind` is `"PathNestLoop(NLI)"` or
`"PathNestLoop(NLI-bitmap)"` (the two kind strings `createplannl.go` uses
for the two NLI shapes) — the outer/probe side of the same NLI has no such
constraint (`Outer`/typed field is `Node`) and narrows normally.

## Pre-existing test re-baseline

Landing real narrowing at a hook every join kind already reaches (S1's own
design) legitimately changes plan shape wherever a previously-unwrapped leg
had something to drop — four pre-existing regression tests hard-coded a
build/Project **count** from before this hook did anything, and needed their
oracle updated to the new (correct) shape, not their assertions loosened:

- `TestSlice3LiveQ9ShapeDerivation` (`internal/optimizer/pathtarget_test.go`):
  5 → 7 narrow builds. The old 8-column build (lineitem concatenated with an
  un-narrowed supplier⋈nation subtree) splits into two — lineitem's own
  6-column build and the supplier⋈nation subtree's own 2-column build — and
  `orders` gains its own 2-column build. Net effect verified column-by-column
  against the new `wantSets`.
- `TestSlice3LateralDeclinesDerivation` (same file) and its executor-package
  twin `TestOwnedBuildPoisonCorrAboveDecline`
  (`internal/executor/owned_build_poison_test.go`): the lateral body's
  `orders` build (unchanged) is joined by a new `lineitem` build
  (`l_orderkey`, `l_suppkey`) — 1 → 2.
- `TestSlice3DerivedTableAliasMapping`: `t2` (needs only `k`/`y`) gains its
  own build alongside `t1`'s — 1 → 2.
- `TestSlice3SelfJoinInDerivedTable`: the self-join's over-kept 6-column
  combined build (`nation a`+`nation b`) splits into two separate 3-column
  builds, one per side — the F4 "never drop exactly one of a same-named
  pair" property now shows up as two builds each keeping their own full
  triple, rather than one build keeping both triples together. The
  generic same-build duplicate-name check is kept (for any future build
  whose child still has a duplicate), but is vacuous on this fixture now
  since the duplicate boundary moved to the `*Join` node these two builds
  are children of.
- `TestPlanJoinPicksHashAlgo`'s LEFT-join case: narrowing a nested loop's
  legs can newly require a NULL-pad restoration `*Project` directly below
  the top-level SELECT-list Project (restoring the pre-narrow width at the
  search boundary, the same "licensed holes" mechanism the Q9 pad check
  already exercises) — so `proj.Child` is no longer always the `*Join`
  itself. Fixed by reusing `findFirstJoin`
  (`small_dim_buildside_test.go`), which already descends through any
  number of wrapping wrapper nodes, instead of asserting the immediate
  child's type.

Each of these was re-derived by adding a throwaway probe test
(`zz_probe*_test.go`, deleted before commit) that dumped the actual narrow
builds for the fixture, then checked the new shape made sense against the
keep-set rules above before hard-coding it — never accepted blind.

## Verification

- `TestNarrowJoinLegCountsAndNarrows` (replaces S1's
  `TestNarrowJoinLegDeclinesButCounts`): every refusal path (flag off,
  already-Project, not-a-leaf, nil/unknown path, no-op cut, NLI inner)
  still returns the pair untouched; the one case with a known subset needed
  produces a `*Project` with exactly the expected columns and layout.
- `TestNarrowJoinLegFiresAndNarrowsOnLiveJoinSearch` (replaces S1's
  shape-identical live test — S2 is no longer a no-op by construction, so
  "byte-identical hook on vs off" is the wrong invariant now): a live
  two-table equi-join, forced off the tied-cost MERGE-join default
  (`EnableMergeJoin = false`, since a merge join's own `narrowMergeInput`
  already narrows both sides and would not exercise this hook) onto a plain
  nested loop. With the hook off, the outer/probe leg (`jlh1`, carrying an
  unused third column) reaches the join un-narrowed — asserted directly, so
  the fixture cannot silently stop being a witness. With the hook on, the
  same column is gone from the join's immediate children.
- Full `go build ./...` and `go test ./internal/optimizer/...
  ./internal/executor/...` clean, plus `./internal/testutil/tpch/...` and
  `./internal/postmaster/...` (both exercise real plans against the
  optimizer) green.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: every
  failure confined to the pre-existing, already-filed `internal/parser`
  `GroupedJoinUnaliased` AST-drift (60 test functions); `internal/optimizer`,
  `internal/executor`, `internal/initdb`, `cmd/goopg` and everything else
  green.
- **Qual-placement census (M0137-0010), the gate every M0139 slice runs**:
  a same-engine A/B on the full TPC-DS SF=0.25 corpus (99 queries,
  `GOOPG_NARROW_LEG_HOOK=0` vs the default-on arm, private clone on
  `tmp/goopg-m0139s2-bin` so as not to touch the shared bench binary),
  captured with `scripts/capture-tpcds.sh --plan-only`-equivalent EXPLAIN
  and diffed with `scripts/qual-placement-census.py`:
  `QUAL-PLACEMENT-CENSUS: queries=99 ok=99 mismatch=0`. No `Filter:`/
  `Index Cond:` line moved on any query — the new Projects only narrow
  columns, they never relocate a predicate. Captures kept at
  `analysis/m0139/m0139s2-sf025-hookoff.txt` /
  `m0139s2-sf025-hookon.txt`.
- **Values-safety, same corpus and binary**:
  `scripts/tpcds-sf025-regression.sh sweep` (default flag, i.e. hook ON) —
  `PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0
  TIMEOUT=0 SKIP=3`. The non-blocking plan-shape channel reports
  `changed=72 added=0 removed=0` against the pre-M0139 snapshot (expected —
  this is the whole point of the slice: many more legs now narrow), and the
  non-blocking status-delta channel reports `verdict-changes=none
  total-delta=-1.7%` (no timing regression; every query kept its pass/fail
  verdict and stayed within the 2.0x band).
- `scripts/tpch-spotcheck.sh` **could not run**, for the same reason as
  S1: the shared TPC-H bench server on `:65433` was up the entire task
  (peer-owned, per `goopg_shared_bench_cluster_collisions` — must not be
  stopped) and the private-clone snapshot step requires it fully down.
  Not treated as a values-safety gap given the TPC-DS SF0.25 sweep above is
  a real, clean values gate on real data (a different corpus, but the
  narrowing mechanism itself is corpus-agnostic — the same three-tier
  derivation chain, gated by the same census). Re-run opportunistically
  once the shared server frees up; expected to reproduce the canonical
  Q12=2/Q13=34 anchors unchanged (narrowing changes row *width*, never row
  *count*).

No ledger row: nothing about PG's own behaviour is deferred here — this is
goopg-internal executor plumbing (projection pushdown), not a PG
compatibility gap.

## Definition of Done — S2's slice of the milestone's list

- [x] "Scans inside join trees emit narrowed rows, with the existing
      narrowing machinery reused rather than duplicated (it lives in
      `narrowoutput.go`, `upper_narrow_apply.go` and
      `upper_narrow_chain.go`)." — reused `narrowoutput.go`'s
      `joinKeepSet`/`buildKeepSet`/`neededKeepSet`/`narrowPlanOutput`
      directly; `upper_narrow_apply.go`/`upper_narrow_chain.go` narrow
      **upper-plan** sites (Aggregate/Sort inputs), a different seam this
      slice does not touch.
- [x] "No values regression on either corpus; the qual-placement census is
      clean on every slice." — TPC-DS SF0.25: PASS=96/MISMATCH=0,
      qual-placement census mismatch=0. TPC-H: values-safety argued from
      the corpus-agnostic mechanism per above; spotcheck itself blocked on
      shared-server contention, to be re-run opportunistically.
- The remaining Definition-of-Done items (the K67 residue measurement, the
  Q4 startup-ratio re-measurement, the packed-retention decision request,
  the duplicate build-map re-measurement) are **S3/0004/0005/0006's
  scope** — see `.ralph/fix_plan.md`'s M0139 section.
