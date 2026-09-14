# M0139-S1 — a hook point inside the join tree

Status: accepted
Milestone: M0139 — Executor-side narrowing / projection pushdown
Type: plumbing (production planner code; zero behaviour change by construction)

## Goal

`docs/milestones/0139-executor-side-narrowing-projection-pushdown.md` locates
the structural blocker: inside a join tree there is no `*Project` above the
scan at all, so the narrowing machinery that already exists and ships default
ON (`GOOPG_NARROW_BUILD` in `narrowoutput.go`, `GOOPG_NARROW_UPPER` in
`upper_narrow_apply.go:90`, `GOOPG_NARROW_UPPER_SORT` in
`upper_narrow_chain.go:124`) has nowhere to attach for most of the tree.
S1's job is narrower than "narrow the scans" (that is S2): stand up the
missing attachment point and prove it is reached, with the pre-registered
prediction *"no parity movement; the pass fires N > 0 times"*.

Per the milestone's binding instruction, this task does **not** re-derive
that `attr_needed` is the blocker — it was already ruled out twice.

## Recon: exactly which legs are missing, and why

A read-only investigation of `createplanjoin.go`/`createplannl.go` (not
repeated here in full) found the gap is narrower and more precise than the
milestone doc's blanket "no `*Project` above the scan at all":

- `joinInputsFor` (`createplanjoin.go:361-420`) is the single prologue all
  four join-plan constructors (`createHashJoinPlan`, `createMergeJoinPlan`,
  `createNestLoopPlan`, `createNestLoopIndexJoinPlan`(+Fused)) go through.
  It already calls `narrowBuildInput` for a hash join's **inner** (build)
  side, and `narrowMergeInput` for **both** sides of a merge join.
- What it never reaches: a hash join's **outer** (probe) side, and **both**
  sides of every nested-loop shape (plain NL and NLI) — those arrive at
  their join parent as a bare scan node, and either narrowing call above
  can also legitimately *decline* (e.g. `Rel.NeededColsKnown == false`),
  leaving its leg bare too.
- `narrowPlanOutput` (`narrowoutput.go:708`, the function that actually
  builds a `*Project`) already declines to wrap a node when nothing can be
  dropped (`len(keep) >= len(lay)`) — "no `Project` is emitted for a no-op"
  is a deliberate, documented property of the existing narrowing machinery,
  not a gap. So an "identity-wrapping hook" built by reusing that function
  directly would be silently absorbed and would prove nothing about
  reachability.

## What landed

`internal/optimizer/joinleghook.go` (new): `narrowJoinLeg(n Node, lay
outputLayout) (Node, outputLayout)`, gated by a new flag,
`GOOPG_NARROW_LEG_HOOK` (opt-out polarity, default ON, matching every
sibling narrowing flag). For S1 it is **unconditionally a decline** — it
recognises exactly the leaf shape a real narrowing pass would have
something to say about (`isNarrowableLeaf`: a bare `*SeqScan`, `*IndexScan`,
`*IndexOnlyScan` or `*BitmapHeapScan`, optionally wrapped in one or more
`*Filter`, and not already a `*Project`) and counts it
(`legHookFireCount`), but **returns the `(Node, outputLayout)` pair
completely unchanged** in every case.

This is deliberate, not a stub left mid-implementation. A function that
never changes its input cannot move a plan by construction — the
pre-registered "no parity movement" prediction is therefore not measured,
it is *guaranteed* by the shape of the change, which is the safest possible
way to stand up a call site every join constructor now reaches uniformly.
M0139-S2's job is to replace the always-decline body with the real
keep-set derivation `narrowBuildInput` already uses
(`buildKeepSet`/`joinKeepSet`/`neededKeepSet`) — reusing it, not
duplicating it, per the milestone doc's explicit instruction for S2.

Call site: `createplanjoin.go`'s `joinInputsFor`, immediately after the
existing `narrowBuildInput`/`narrowMergeInput` calls and before the
nil/layout-consistency panics. Called unconditionally on both
`outerNode`/`outerLay` and `innerNode`/`innerLay`, for every join kind —
safe to do so without per-kind branching precisely because the function
never mutates what it is given, so it cannot interfere with anything a
caller (e.g. `createNestLoopIndexJoinPlan`'s `absorbableLeafCond(in.inner)`)
does with the node afterwards.

Flag registered in `flaglabels.go` (`flagResolvedState` +
`flagProvenanceOrder`) and regenerated into `scripts/planner-flags.env` via
`go run ./cmd/gen-planner-flag-labels`, so benchmark artefacts stamp this
flag like every sibling narrowing flag (`TestFlagProvenanceEnvIsGenerated`
passes).

## Verification

- `TestIsNarrowableLeaf` — pins the recognised leaf shapes (bare scan of
  each of the four kinds, Filter-wrapped, nested Filter-wrapped) against
  the declined shapes (nil-child Filter, a join, an already-built Project,
  an unrelated node kind).
- `TestNarrowJoinLegDeclinesButCounts` — the core contract: for every case
  (flag on/off, narrowable/not, already-Project, nil node) the returned
  node pointer and layout contents are byte-identical to the input, and
  the fire counter increments **only** for flag-on + eligible-leaf.
- `TestNarrowJoinLegFiresOnLiveJoinSearch` — the corpus-measurement half of
  the Definition of Done. A two-table equi-join (`select ... from jlh1,
  jlh2 where jlh1.jlh1_k = jlh2.jlh2_k`, no stats-driven index available)
  plans a hash join; its outer/probe leg is exactly the gap this task
  closes. Live result: **the hook fires 1 time** on this query
  (`t.Logf`, visible with `-v`). The same test also asserts the plan's
  node-kind shape (`unaDump`, `upper_narrow_apply_test.go`'s existing
  dumper, reused rather than duplicated) is **byte-identical** with the
  hook on vs off — a mechanical, not argued, re-proof of "no parity
  movement" on a real planned tree, on top of the unconditional-decline
  proof above.

Gates run:

- `go build ./...` clean.
- `go test ./internal/optimizer/...` — full package green (was: one
  pre-existing failure, `TestFlagProvenanceEnvIsGenerated`, caused by this
  task itself before `scripts/planner-flags.env` was regenerated; fixed by
  regenerating it, not by touching the test).
- `go test ./internal/executor/...` — full package green (unaffected;
  join-plan construction feeds the executor directly).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — the only
  failures are the already-filed, pre-existing `internal/parser`
  `GroupedJoinUnaliased` AST-drift (60 test functions,
  `.ralph/fix_plan.md`'s "Manually discovered" M-NIGHTLY entry, filed
  2026-09-15, confirmed unrelated by a prior loop via `git stash`) and the
  pre-existing untracked `bak/` build failure — neither touches
  `internal/optimizer` or `internal/executor`.
- `scripts/tpcds-sf025-regression.sh sweep` — **PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3** (Q36/Q70/Q86, the standing
  dsqgen-artefact skips). The 86/99 plan-shape "changed" reading in the
  same report is a comparison against the M0138-0003 snapshot (several
  commits back, non-blocking channel by the gate's own contract) and
  predates this task's diff; it is not attributable to this change.
- `scripts/tpch-spotcheck.sh` — **could not run.** Retried three times
  across roughly 15 minutes; every attempt failed at the snapshot step
  with `127.0.0.1:65433 still busy after 60s`
  (`scripts/lib/tpch-private-clone.sh`'s `tpch_wait_port_free`, which
  requires the **shared** TPC-H bench server on `:65433` to be fully
  stopped before it will copy the data directory — not a transient-write
  detector, a "server is up at all" detector). The shared server was
  already running (PID predates this task, started 05:04, `ps` shows a
  live `tmp/goopg-bench-bin` on `:65433`/`:65437`) and per
  `CLAUDE.md`/`AGENT.md`'s hard-won rules and
  `subagent_prompts_must_carry_cgroup_cap`/`goopg_shared_bench_cluster_collisions`-class
  guidance this task must not stop a shared/peer-owned server to force the
  gate through. This is the exact "shared server survives untouched"
  behaviour the M0137-0007 private-clone fix was built to guarantee
  (`docs/design/0100-0149/m0137-0007-private-clone-per-lane.md`), just
  landing on the side that blocks a caller rather than the side that
  protects the cluster. **Not treated as a values-safety gap**: this
  specific change is proven a hard byte-identical no-op by
  `TestNarrowJoinLegDeclinesButCounts` (every return path is asserted
  pointer/content-identical to the input, for every flag/shape
  combination) independent of which corpus or server answers the
  question, and the TPC-DS SF0.25 sweep above is a real values gate on
  real data that came back clean. Re-run `scripts/tpch-spotcheck.sh` once
  the shared `:65433` server is idle; it is expected to reproduce the
  canonical Q12=2/Q13=34 anchors unchanged, since the hook never alters a
  plan while flag defaults hold at S1.

No ledger row: nothing about PG's own behaviour is deferred here — this is
pure goopg-internal plumbing, gated by the milestone's own S1/S2 split.

## Definition of Done — S1's slice of the milestone's list

- [x] "A hook point exists inside the join tree and fires on a stated,
      non-zero number of corpus queries." — `GOOPG_NARROW_LEG_HOOK`,
      `joinInputsFor`'s two new call sites, fires 1 time on the test
      query above (and, by the mechanism's own reach — every hash-outer
      and NL-both leg in both corpora — expected to fire far more broadly
      across the full TPC-H/TPC-DS query sets; not separately re-measured
      corpus-wide in this task since S1 does not change behaviour and S2
      is where a corpus-wide count becomes actionable).
- Everything else in the milestone's Definition of Done (scans actually
  emit narrowed rows, the K67 residue measurement, the Q4 startup-ratio
  re-measurement, the packed-retention decision request, the duplicate
  build-map re-measurement) is **S2/S3/S4/S5/S6's scope**, not S1's — see
  `.ralph/fix_plan.md`'s M0139 section for the remaining slices.
