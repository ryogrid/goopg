# R54 Step-0 — parallel-admission death-level measurement (scope, 2026-09-10)

*Scope for the R52 §4.2 half-round (TODO.md R54 entry): H-Q5's Gather and
DS-Q84's Gather Merge died under R51-new shapes; both NEW plans are
entirely serial. Question: at which admission gate does parallelism die,
per query — leaf, join-clause walk, or upper/gather admission? No planner
behaviour changes in Step-0: the deliverable is the instrument (a
trace-only consider_parallel line, gate-held, production-inert) plus the
death-level numbers that scope the pricing slice. Design basis: R52
REPORT §4.2; R53 Step-0/SLICE1 recipe (trace → stable-plan check → ledger).*

## 0. Standing facts (all verified this round, in-tree + /tmp/pp2/r52 evidence)

- H-Q5 NEW: `Sort → HashAggregate rows=1`, serial throughout (no Parallel
  anywhere incl. base scans). OLD had Gather + Partial; PG has `Finalize
  GroupAggregate + Gather Merge + Partial GroupAggregate`. Toward half
  (R52 §2): top Hash Cond is PG's exact 2 clauses + one implied-redundant
  synth (`n_nationkey = s_nationkey`); index-driven NLI below (lineitem
  `l_orderkey` bitmap probe, customer `c_nationkey` bitmap probe).
- DS-Q84 NEW: `Limit → Sort → NL`, serial throughout. R50 had
  `Gather Merge (Workers: 1)` matching PG's Gather Merge (R52 §3).
  Toward half: top two levels text-identical to PG modulo Parallel.
- Admission chain (all `internal/optimizer/`): `joinrelConsiderParallel`
  (`considerparallel.go:379` — false if either input is false OR any
  clause fails `isParallelSafeExpr`) feeds `joinrel.ConsiderParallel`
  (`joinsearchlevel.go:652`); consumers are the gather gate
  (`gatherpaths.go:144`), partial-path arms
  (`joinpathsparallel.go:104,238`), the parallel addPath gate
  (`path.go:947`), and ordered paths (`pathindexordered.go:279`).
  Walk input is `buildJoinRelRestrictList` output
  (`joinrestrict.go:357`) taken at `joinsearchlevel.go:589` — the
  join's own clauses plus OJ nullable-side filters, nothing else.
- CONTROL (bounds the suspect space, flag level only): Q9 carries an L4
  NLI join AND a parallel plan (Gather + Parallel Seq Scan on orders).
  Nothing in `joinrelConsiderParallel` vetoes NLI membership or
  parameterisation as such (inputs-AND + clause walk only), so NLI
  membership does not kill the ConsiderParallel *flag*. Below flag
  resolution, parameterisation DOES veto: the partial-hash arms refuse
  `RequiredOuter != 0` (`joinpathsparallel.go` RequiredOuter refusals),
  as do the gather paths (`gatherpaths.go:337,364,381`) and
  `addPartialIndexPath` (`serial.RequiredOuter != 0`,
  `pathindexordered.go:279` gate). So Q9's L4 NLI can coexist with a
  parallel plan while Q5/Q84 die at the path level (S4) with every
  joinrel flag green — the Step-0 trace runs Q9 as the control arm and
  the S4 bits (§2) keep the control honest.

## 1. Suspects, ordered (all falsifiable by the §2 trace)

- **S0 — session/query-wide gate, checked once.** `parallelModeOK`
  (`considerparallel.go:60-61`: `ParallelEnabled()` GUC AND
  `maxParallelWorkersPerGather > 0`) plus table-level
  `parallel_workers` / `computeParallelWorker` zero-worker refusal
  (`:602` area). The Q9 control bounds S0 only if all three arms share
  the session/GUCs — pinned in §3. If S0 is closed once, it stays
  closed for the round.
- **S1 — leaf CP=false propagation.** New leaves under the new shapes:
  Q5's bitmap probes (lineitem, customer); Q84's Index probe (vs PG's
  Index Only — the known no-IOS gap, predates R51). Check
  `relConsiderParallel` leaf coverage (`considerparallel.go:124`) and
  `setBaseRelConsiderParallel` (`:73`): if a bitmap/index leaf reports
  false, every joinrel above it is serial by propagation
  (`:380` first disjunct), and no clause analysis is needed.
- **S2 — clause-walk veto on synth clauses.** `isParallelSafeExpr`
  (`:407`) vetoes `*FuncCall` (non-safe), SubPlan (any form, via
  `scopeVeto`), and `*OuterColumnRef` / `*ExecParamRef`. R51's seam
  output (implied/redundant equalities, doubled conds on Q47/Q57-class
  shapes) is the new clause population the walk never saw pre-R51. A
  single vetoed clause zeroes the joinrel (`:383` loop). The trace
  names the first failing clause per joinrel, so this resolves to a
  clause KIND, not a suspicion.
- **S3 — upper/gather admission above a healthy joinrel.** Q5's serial
  `HashAggregate rows=1` vs PG's Finalize + Gather Merge + Partial:
  even with ConsiderParallel=true at the top joinrel, the partial-agg
  upper stand-ins (`partialaggpaths.go:316`,
  `partialsortpaths.go:218-220`, `partialaggupper.go:293`) and the
  gather gate (`gatherpaths.go:144`) must admit. If the trace shows
  CP=true reaching the top joinrel on Q5, the death is S3, and the
  pricing slice becomes an upper-rel round, not a joinrel round.
- **S4 — path-level veto below green flags.** Even with every joinrel
  flag true, partial/gather candidacy can die per path: `RequiredOuter
  != 0` refusals (`joinpathsparallel.go`, `gatherpaths.go:337,364,381`,
  `addPartialIndexPath`), `addPartialPath`'s `ParallelSafe` refusal
  (`path.go:947`), zero-worker twins (`pathindexordered.go:279` gate —
  re-pinned this round: btree-only, CP, unparameterised, session
  parallelModeOK). The §2 gather-considered bit exists so S4 reads as
  "generated but lost / never generated", not as a flag verdict.

## 2. Instrument recipe (R53 recipe, trace-only)

Per-joinrel line emitted at the build site (`joinsearchlevel.go`, after
`:652`): relset, ConsiderParallel, rel1.CP, rel2.CP, clause count, first
failing clause index + fmt-kind (empty when admitted). Plus a base-rel
line in `setBaseRelConsiderParallel` (`considerparallel.go:73`):
relset, leaf kind, CP verdict. Plus, per top-level rel: a
gather-considered bit (did `gatherpaths.go:144` pass its gate) with the
partial-pathlist length at decision time, and one upper-gate line naming
which of `partialaggpaths.go:316` / `partialsortpaths.go:218-220` /
`partialaggupper.go:293` / `gatherpaths.go:144` admitted or refused —
without these, "generated but lost on cost" is indistinguishable from
"never generated" and S3 localises only "above the top joinrel". Gated
unit test in the style of `pathtrace_test.go` /
`joinsearchtrace_test.go` (admit + veto + leaf verdict pinned).
Env-gated (`GOOPG_PGSHAPED_DP_TRACE=1` family), zero planner readers —
plan-identity check (Q5/Q84/Q9 byte-identical pre/post instrument)
proves trace-only, as in SLICE1 §5.

## 3. Measurement (foreground, capped, isolated — standing rules)

Binary from `./cmd/goopg/`; capped scratch server (`GOOPG_CG_UNIT`,
`scripts/goopg-test-run.sh`) on a private data clone :553x; hand-written
Q5 / Q84 / Q9(control) via psql EXPLAIN, twice each
(PLAN-IDENTICAL stability bar from R53 Step-0 §0). All three arms run
under identical GUCs in one session profile (record
`SHOW max_parallel_workers_per_gather` in the evidence — otherwise the
Q9 control does not bound the S0 confound). Evidence tmp-only
(`/tmp/pp2/r54/`, kept): plans ×2, trace logs, GUC record, PG oracle
reads (read-only :65432/:65438 for the partition PG parallelises, never
restarted). For each query: the lowest joinrel (or upper gate) where
CP first reads false, with the responsible input or clause named.

## 4. Falsifiability / exit criteria

- Step-0 CLOSES when each query has a named death gate: S0 reads once
  from the GUC record; S1 names the leaf kind + `relConsiderParallel`
  line; S2 names the clause kind + veto arm; S3 names the upper gate
  via the §2 upper-gate line (CP=true at the top joinrel alone only
  localises above it — gate-naming needs the line); S4 reads from the
  gather-considered bit + partial-pathlist length.
- The pricing slice that follows is conditioned on the verdict: S0 →
  session/table-config slice; S1 → leaf-admission slice; S2 →
  clause-form slice (seam output vs walk); S3 → upper-rel slice; S4 →
  path-level slice. If admission is clean everywhere and parallel
  paths are generated but lose on cost, Step-0 states that WITH the
  candidacy numbers — the pricing itself is the follow-up round's.
- NOT in Step-0: sizing, enumeration/phases, merge/NL/hash arms,
  partial-path / gather pricing numbers, the footprint-model slice
  (SLICE1 §6 candidate), R51 items 2–3, any planner behaviour change.

## 5. Gates

New gate-held test + full `internal/optimizer` + `testutil/estimateaudit`
suites green (never `-count=1`); plan-identity Q5/Q84/Q9 pre/post
instrument; `scripts/tpch-spotcheck.sh` where runnable (SKIPs in a
data-less worktree per SLICE1 §5 — then the plan-identity check
substitutes, documented). Values gates (digest 24/24, SF0.5) bind the
implementation slice, not Step-0 (no behaviour change).
