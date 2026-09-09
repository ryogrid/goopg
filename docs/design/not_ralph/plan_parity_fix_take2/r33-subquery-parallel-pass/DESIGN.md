# R33 — Recurse the post-cache parallel pass into uncorrelated sublink plans (K44)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Parallelism axis (K37 campaign, slice 2). Unblocked by R32, which made
the gap measurable.*

## 0. How this round was chosen

R32 revealed Q9's real gap: 15 InitPlans, every one serial
`Aggregate -> Seq Scan` where PG plans `Finalize Aggregate ->
Gather -> Partial Aggregate -> Parallel Seq Scan` (15/15/15/15,
`Workers Planned: 3`, verified from the fixtures). No PG query
section contains more than 15 Gather lines (Q9 is the corpus max;
next-highest 8: Q14/Q23/Q88), so 15 is the largest attainable single-query
delta — and the rendered `InitPlan` labels themselves prove
`IsNonCorrelated=true` for all 15 (the `subPlanName` discriminator,
`internal/executor/operators_explain.go:582-585`).

The layer is forced by elimination, not preference. An earlier draft
claimed all-mode search builds gathers inside subqueries
(Q14/23/24/45/58); re-examination with the R32 binary refutes it —
those gathers sit in outer or CTE-body subtrees, and Q23's one
ambiguous instance is a shared top-level-planned CTE body. The
clincher is a fresh all-mode capture with the R32 binary
(`/tmp/pp2/k37-ds-all-r32.plans.txt`): Q9's 15 InitPlans are STILL
serial (66 lines, 0 gathers) with every knob on. Q9's sublinks are
single-relation `Aggregate -> Seq Scan`, and one-relation statements
never enter the path search (`makeRelFromJoinlist` early return;
`gatherpaths.go` header) — no search-knob setting can produce these
15 gathers. The post-cache pass is the only producer that can reach
one-relation nested shapes. (Boundary note: partial-agg paths
default ON since 2026-09-07 and own top-level/CTE parallelism; this
round owns only the nested one-rel shapes the search cannot enter.)
Q9 is the largest single-query Gather delta in the corpus and every
sublink is uncorrelated, which bounds the slice.

## 1. Root cause

The entire post-pass is blind past expression boundaries.
`parallelChildren` (`parallel.go:1121`) follows plan-tree Child links
only and has no expression arm; `parallel.go` mentions no sublink
type at all. Consequently all five walks stop at the same boundary:

- `findPartialSubtree` never sees a subquery Aggregate (no split, no
  Gather target);
- `statementIsParallelSafe` / `subtreeHasUnsafeNode` never sees a
  subquery scan (no per-level safety verdict);
- `subtreeHasGather` never sees a subquery Gather (coexistence rule
  decides on the top tree alone);
- `StripGather` cannot enforce inside subqueries either (symmetric
  note for §2).

Sublink plans (`SubqueryExpr`/`ExistsExpr`/`InExpr`-with-`Plan`/
`ArraySubqueryExpr`, all carrying `Plan Node` + `IsNonCorrelated`,
`plan.go:231-340`; a fifth kind, `MultiAssignSubqRow` (`plan.go:359`,
shared by pointer across `MultiAssignSubqElem`s), is explicitly OUT
of scope — assignment-row sublinks never drive aggregations worth
parallelising, and their hosts are DML (`statementIsParallelSafe`
false, `parallel.go:252`) so the pass refuses them regardless) are planned by `planSelectWithParent` into the
same cached tree the pass rewrites — the pass just never descends to
them.

Executor readiness (no executor change proposed): `gatherOp.Open`
derives workers, arenas, and claim sets from the session `Context`
(`operators_gather.go:191-260`), with no top-level-only input; an
uncorrelated InitPlan executes once under a constant cache key (the
mechanism: `internal/executor/expr.go`, constant key for
non-correlated ~:10599-10605, `nonCorrelatedCacheKey` :18664 — the
round must assert all in-scope types hit it, not assume it), so
`classifySubPlan` degrading an inner Gather to `rescanRebuild` is
safe for the once-execution case. Values risk is therefore confined
to (a) correlated plans wrongly admitted, (b) once-semantics
violated, (c) an unsafe node nested inside an operand-nested sublink
invisible to the per-sublink safety verdict — closed by the pinned
safety walk below plus a test (temp table inside an operand-nested
sublink gains no enclosing Gather).

## 2. Change (planner post-pass only)

In `MaybeAddGather`, when the top-level pass did NOT take a strip
path (`GOOPG_PARALLEL=off`, `MaxWorkers<=0`, serializable, unsafe —
on any of these the whole statement including interiors stays
serial), walk the plan's expressions and for each in-scope sublink
with `Plan != nil` **and `IsNonCorrelated`** recurse the full pass
over a copy-on-write graft: copy the host sublink expr, recurse on
its Plan, and rebuild every ancestor expr (via `exprChildSlots`)
and ancestor plan node (a `StripGather`-structured rebuilder) up to
the root — never writing through `exprSlot.plan` into the cached
tree, and never writing through plan nodes either (copy-on-write at
both levels). A seen-set keyed by sublink Plan pointer covers shared
bodies (shared-or-cloned; key by Plan pointer so repeats collapse —
`NodeSubplans` precedent, `walk_export.go:308`). The rebuilder must
cover at least the walker-modelled set (next paragraph) and fail
closed — leave the host serial — on rebuilder-unmodelled node types. Correlated sublinks are excluded
outright: they execute per outer row, possibly inside workers, where
a Gather multiplies workers by rows. Recursion gives each InitPlan
its own `statementIsParallelSafe` verdict and its own partial-agg
tournament sizing, mirroring PG planning each query level
independently in `subquery_planner`. No search, executor, or
costing-function change.

Walker coverage the graft needs (review 2026-09-09): `WalkPlanExprs`
(`walk_export.go:10`, impl `unnest.go:1035`) hosts Q9's `Project`
targets, but has no `*Result` arm (the S6 min/max InitPlan hangs in
`Result.Targets`, `plan.go:1557`), its `IndexScan` arm skips `Cond`,
and there is no `IndexOnlyScan` arm — an unlisted node skips its
entire subtree. The round adds Result/IndexCond/IndexOnlyScan arms
mirroring `NodeSubplans` (`walk_export.go:91-95,136-138,139-152`),
plus a test pinning `NodeSubplans`-vs-`WalkPlanExprs` host coverage.
Sublinks nested inside `InExpr.Operand/List/Args` or sublink `Args`
stay out of PLACEMENT discovery (the outer sublink itself is still
visited) — but NOT out of the safety scan: without this, an
uncorrelated sublink S admitted to a Gather could hide `y =
(SELECT ... FROM temp_t)` in an `InExpr` operand, invisible to S's
expression-blind per-sublink verdict (`subtreeHasUnsafeNode` via
`parallelChildren`, `internal/optimizer/parallel.go:264-305` and
`:1121`) yet executed inside S's workers. The safety
descent is therefore pinned to the complete enumerator —
`exprChildSlots`-driven `walkExprRefs` with the `scopeDescend`
policy (`internal/optimizer/exprwalk.go:288-295`, covering
`Operand/List/Args`+Plan at `:201-252` — `MultiAssignSubqRow` at
`:248-252` is out of scope per §1) — which `walkExprTree`
(`internal/optimizer/unnest.go:1287-1324`, no sublink-type arm)
cannot supply.

Hardening named, not smuggled: the per-sublink
`statementIsParallelSafe` verdict inherits expression-blindness, so
a temp table / `LockRows` inside a NESTED sub-sublink is invisible —
the safety walk (not placement) descends into nested sublink Plans
via the pinned `walkExprRefs` enumerator above. And the extended-protocol call site
(`internal/postmaster/dispatch_extended.go:160-168`) never reads isolation, so this
round adds gathers under that weaker check — a separate fix, named
here so the round isn't blamed later.

## 3. Success tests (all must hold)

1. Default-mode Q9: all 15 InitPlans become `Finalize Aggregate ->
   Gather -> Partial Aggregate -> Parallel Seq Scan` (PG's shape;
   worker counts need not match PG yet — costing is K45/follow-up
   territory, placement is this round; the decider is the now-default-
   on partial-agg tournament).
2. Values: SF0.5 sweep `MISMATCH=0`-class unchanged (Q9 checksum
   must still verify — parallelism must not change results).
3. Byte-guard: full-corpus A/B both corpora; only intended sections
   move.
4. Once-semantics probe: `EXPLAIN ANALYZE` Q9 shows per-InitPlan
   `rebuilds==1, misses==1` on the SubPlan line, exactly 15
   `Workers Launched: N>0` lines, inner `loops==1`
   (`recordGatherLaunched` overwrites, so "launched exactly once" is
   proxied, not counted directly).
5. Negative probe: a correlated-subquery query gains NO inner Gather
   — with a vacuity guard (the inner scan must clear the size gate,
   or the probe proves nothing).
6. `match` count is NOT a criterion (conjunction rule).

## 4. Non-goals / sequencing

- K43 (partial Append): independent producer+executor work.
- K45 (date-range inflation): estimate side; worker-count fidelity
  waits for it.
- K26 (join-order): untouched.
- Search-side (`all`-mode) behaviour: unchanged by this round. The
  StripGather-inside-subquery symmetry question is ANSWERED no for
  correlated/unsafe-qual sublinks, three layers (review 2026-09-09):
  (1) nested scopes clear `ParallelStatementOK`
  (`planner.go:14795-14805`) and the partial-agg upper refuses
  without it (`partialaggupper.go:192`); (2) base-rel admission needs
  `rel.ConsiderParallel` (gate `gatherpaths.go:144`, setter
  `considerparallel.go:78-82`, quals vetting `:259-264`), which fails
  sublink quals and outer refs; (3) joinrel admission vets clauses
  with `isParallelSafeExpr` (`considerparallel.go:379-389`), which
  fails `OuterColumnRef`/`ExecParamRef`. The uncorrelated-safe
  multi-rel all-mode case is NOT excluded by these layers and stays
  as-today (pre-existing behaviour, correctly out of scope).
  CTE bodies are top-level-planned, out of scope for the question.
  Record probes for later: all-mode + correlated join-cond subquery
  asserting no inner Gather, plus the uncorrelated-safe all-mode
  case.
- JSON EXPLAIN: still out (R32 scope note carries over).

## 5. Evidence archive

- `/tmp/pp2/k37-ds-r32.plans.txt` (Q9: 66 lines, 15 serial
  InitPlans — the before-shape)
- `/tmp/pp2/k37-ds-all-r32.plans.txt` (R32-binary all-mode: Q9 still
  0 gathers — the C-19h one-rel clincher; outer/CTE-body account of
  the old Q14/23/24/45/58 claim)
- `/tmp/pp2/k37-ds-pg.plans.txt` (Q9 reference shape)

*Convention (review 2026-09-09): bare `file.go:line` cites in this
doc mean `internal/optimizer/` unless prefixed; `internal/executor/`
and `internal/postmaster/` cites carry prefixes. Recorded so future
rounds stop re-litigating it.*
