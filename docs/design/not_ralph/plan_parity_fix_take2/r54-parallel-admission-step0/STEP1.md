# R54 Step-1 — S4 path-level veto attribution (scope, 2026-09-10)

*Scope for the R54 follow-up (TODO.md R54 entry; REPORT.md §3): Step-0
exonerated S0/S1/S2 — every base leaf `cp=1`, every join admit
`failkind=none` — and showed 100% `no-partials` at the gather gate for
Q5/Q9/Q84 plus Q5's `gate=subtree` and Q84's silent sort route. Step-1
names the veto INSIDE the producers: which of the ~10 early returns in
`addPartialHashJoinPath` fires per joinrel+orientation, which base rels
never get a partial path and why, which merge-partial veto fires for
Q84, and which `subtree` disjunct refuses Q5. No planner behaviour
changes in Step-1: the deliverable is the veto-attribution instrument
(trace-only, gate-held, production-inert) plus the per-query veto table
that scopes the Step-2 fix slice. Design basis: REPORT.md §§2–4.*

## 0. Standing facts (Step-0 verdicts + code inventory this round)

- Step-0 counts (re-verified by review): Q5 30+30 join admits, Q9 29+29,
  Q84 25+25 — all `failkind=none`; every `cpgather`
  `partials=0 verdict=no-partials`; zero `gate=sort` lines; upper lines
  4×`split` (Q9×2, Q1 probe+`.1`) + 2×`refused gate=subtree` (Q5×2).
- Hash-partial veto chain (`joinpathsparallel.go:80-150`, in order):
  V0 `gatherPathsMode==off` → V1 `!partialHashJoinTypeOK(jt)` → V2
  `s/parallelModeOK/joinrel/ConsiderParallel` → V3 nil outer/inner or
  empty keys → V4 `outer.PartialPathlist` EMPTY (propagation death: L1
  starves everything above) → V5 outer head nil/`Workers<=0`/
  `!ParallelSafe` → V6 `!partialPathShapeIsGatherable(o)` → V7
  `cheapestParallelSafeTotalInner==nil` → V8a `RequiredOuter!=0`
  (either side) → V8b `calcNonNestloopRequiredOuter!=0` → V9 admitted
  (filed). V8 is split (not lumped) because H3 demands each veto name a
  different fix; V2/V5 keep their sub-conditions with `detail`
  disambiguating (nil-safety vs the live predicate).
- Base-partial skips (`considerparallel.go:543 addBaseRelPartialPaths`,
  called once at `relfromjoinlist.go:708`): B1 `!parallelModeOK` or
  `<2 joinrels` → B2 `!ConsiderParallel` → B3 leaf not `*SeqScan`
  (`leafBaseScan(...).(*SeqScan)` fails: bitmap/index/subtree leaves get
  NOTHING — *"an index or bitmap leaf is the legacy rule-based planner's
  choice standing in for the relation"*) → B4 `workers<=0`
  (`computeParallelWorkerForRel`: sub-`min_parallel_table_scan_size`
  tables earn zero).
- Q5's NEW plan leaves (verified): orders seq, lineitem BITMAP, customer
  BITMAP, supplier/nation/region seq — BUT Step-0 S1 recorded all 6 base
  leaves `cp=1 leaf=seq`, and S1's `leaf=` (`traceLeafKind`) and B3's
  `leafBaseScan(...).(*SeqScan)` read the SAME `rel.baseLeaf` in adjacent
  calls (`relfromjoinlist.go:707-708`, nothing between). All-seq at S1
  therefore entails B3 fires ZERO times for Q5: the final-plan bitmap
  choice is the legacy rule-based planner's pick made LATER (index paths
  are added at :709, AFTER base partials at :708), not the `baseLeaf`
  kind at production time. H1 below is rewritten accordingly — the base
  census still adjudicates it either way, but the prediction is now that
  all six leaves file base partials (workers permitting) and the killer
  sits higher (V4+ orientation/propagation or H4).
- Serial NL/NLI have NO partial twin (`joinpaths.go:367+`:
  `addNestLoopPath`/`addNLIPaths` with no partial arm) — a join whose
  only viable shape is NL can never propagate a partial upward, however
  green its flags. Q5's spine is NL-heavy; Q84's top is NL-only.
- Merge-partial veto chains (both sites emit; vocabulary M0–M12).
  Wrapper `addPartialMergeJoinPath` (`joinpathsparallel.go:233-271`,
  sort_inner_and_outer site via `joinpathsmerge.go:274`): M0 mode-off →
  M1 flag → M2 nil outer/inner or empty mergeClauses → M3 empty outer
  partials → M4 outer head nil/`Workers<=0`/`!ParallelSafe` → M5
  inner-nil. Inner `tryPartialMergeJoinPath` (:304-329): M6 nil o/i →
  M7 outer-sort-DECLINE → M8 inner-sort-decline → M9 `RequiredOuter` →
  M10 `calcNonNestloop` → M11 `!partialPathShapeIsGatherable(o)` → M12
  admitted. Unsorted-outer site `matchUnsortedOuterMergePartial`
  (`joinpathsmergeouter.go:273`) loops the WHOLE partial pathlist (one
  line per outer candidate, site=`mergeu`, same M6–M12 vocabulary;
  M3u = empty pathlist, loop never entered). No-sort gate rationale:
  no per-worker sort model (`partialPathDrivingKind`/`drivingScan`
  have no Sort arm). Base partials are unordered seq scans, so the
  sort-site wrapper can only win when the outer is ordered by
  construction — Q84's route needs veto-level confirmation, plus the
  open R50-origin question (path Gather Merge vs post-pass) answered by
  which site, if any, fires.
- Q5 `subtree` gate (`partialaggupper.go:266`) ORs three preconditions
  (`subtreeHasUnsafeNode || subtreeHasGather || drivingScan==nil`);
  REPORT §2 attributes no-driving-scan by elimination (node-kind level
  verified by review). Review correction: NOT a "missing NL arm" —
  `drivingScan`'s `case *Join` catches NL; the nil comes from the
  capability predicates refusing `Algo!=Hash/Merge`.

## 1. Suspects, ordered (all falsifiable by the §2 trace)

- **H1 — B3 bitmap-leaf starvation (DOWNGRADED from prime: already
  contradicted by Step-0 S1).** All-seq at S1 over the same `baseLeaf`
  field B3 reads predicts B3 fires zero times for Q5 — the final-plan
  bitmap scans are a later legacy-planner pick, not the production-time
  leaf kind. The base-partial census still adjudicates it directly
  (either B3 lines appear, reopening the S1/B3 mechanism question, or —
  predicted — all six rels file and the killer is higher: V4+
  orientation/propagation or H4).
- **H2 — B4 small-table sizing (nation 25 rows, region 1 row).**
  Expect workers=0 skips; PG-faithful (PG also builds no partial scan
  below `min_parallel_table_scan_size`), so a confirmation, not a fix
  target — but it quantifies how many V4 deaths are "correct".
- **H3 — V6/V7/V8a/V8b mid-chain vetoes.** Shape refusals (V6), no
  parallel-safe complete inner (V7), parameterisation either-side (V8a)
  vs derived non-NL requirement (V8b). Each names a different Step-2
  fix; the trace distinguishes them rather than lumping "no partial".
- **H4 — NL has no partial arm.** If Q5/Q84's serial winners at key
  levels are NL-only shapes, even a fully-fed V-chain files nothing for
  those orientations — the fix slice is a new producer (or a post-pass
  fallback), not a veto relaxation. NL orientations emit NO `pveto`
  line (no producer exists to refuse), so H4 is harvested out-of-band:
  per-level serial-winner shape from the final plan joined against the
  `pveto` per-orientation census (an orientation with hash/merge `pveto`
  lines lost at a veto; one with no lines at all and an NL winner is
  H4). Count orientations where the serial winner is NL vs hash/merge.
- **H5 — Q84 merge-route veto.** Which M-veto fires per level per
  site (M3/M3u no-partial-outer? M7/M8 sort-decline? M11 shape?), and
  did ANY M12-admitted fire in R50's shape? Bounds whether Gather Merge
  is recoverable via paths at all.
- **H6 — Q5 subtree disjunct.** Refine `gate=subtree` detail to
  `unsafe | gathered | no-driving-scan` (predict: no-driving-scan via
  the NL capability refusal). Decides Step-2: NestedLoop descent
  soundness vs refusal-correct-look-elsewhere.

## 2. Instrument recipe (trace-only, same pattern as Step-0)

- New tag `pveto`, one line per producer call that does NOT file:
  `DPTRACE pveto site=<base|hash|merge|mergeu> rel=<joinrel relset>
  dir=<outer relset>+<inner relset> veto=<V0..V9|B1..B4|M0..M12>
  detail=<per-veto-mandated>`. `dir=-` for `site=base` (no orientation).
  `detail` contract per veto (not "as applicable"): B→leaf-kind+workers,
  V2/V5→which sub-condition, V→jointype, M→site+ordering-state, so H1/H2
  read named fields. One line per admitted file too (`veto=admitted`,
  at ALL sites incl. both merge sites) so absence of lines is
  distinguishable from absence of calls. Hash site covers both
  orientations the `addPathsToJoinrel` loop tries; merge sites tagged
  separately (`merge` vs `mergeu`) to answer H5's which-site half.
  Query/run identity comes from the per-file harvest (§3: one log per
  query×run), not from the line.
- `partialaggupper.go:266` area: extend the `gate=subtree` detail to
  name the firing disjunct (`subtree=unsafe|gathered|no-driving-scan`)
  — three cheap boolean re-evaluations on the refusal path only.
- Same gates as Step-0: nil-safe recorder on `searchTrace`
  (`dpTraceEnabled` first, single stderr write); `enumtrace.go`
  discard `pveto` without Malformed++; unit pins (veto-name pure test,
  one capture test per site incl. admitted, subtree-detail test;
  existence-before-verdict throughout); full optimizer +
  estimateaudit suites green, no `-count=1`.
- Reuse Step-0's `upper`/`cpadmit`/`cpgather` lines unchanged for
  cross-check (per-orientation V9-admitted must reconcile with
  `DPPATH ... verdict=accepted` producers and the `no-partials`
  counts).

## 3. Measurement protocol (Step-0 repeat + veto harvest)

Same capped clones (`/tmp/pp2/clone-tpch` :5534, `/tmp/pp2/clone-ds05`
:5533), same GUCs (`work_mem=64MB`, `mpwg=4`), same arms
(Q5/Q9/Q1 + Q84), same gates: baseline-vs-instrumented EXPLAIN
byte-identity ×2 runs each (plan-cache cap: ≤2 traced runs per query
text per server lifetime — REPORT §1), then `grep DPTRACE` harvest:
per-query veto histogram (site × veto), base-partial census (which
rels filed, leaf kind + workers), merge-site activity, subtree
disjunct for Q5.

## 4. Exit criteria (what Step-1 owes Step-2)

A per-query veto table: for Q5, Q84 (and Q9/Q1 controls), each dead
route named to one veto (V0–V9 / B1–B4 / M-veto / subtree disjunct)
with counts, e.g. "Q5 L1: 2×B3 (lineitem, customer bitmap leaves);
L2–L5: N×V4 on bitmap-descended outers; winning orientations M×H4
(NL-only)". Plus the H5 answer (merge sites silent/firing) and the H6
disjunct confirmation. Step-2 then prices exactly one fix per named
veto — NOT in Step-1: any new producer, any veto relaxation, pricing,
sizing, footprint model.

## 5. Sibling-path audit (same loop as Step-0)

`partialPathDrivingKind` (path view, `gatherpaths.go:387,404` arms) ↔
`drivingScan` (node view) must stay in step — any NL/capability reading
Step-1 produces applies to both. `createplanjoin.go:614,744` executor
twins of the two partial producers: a veto renamed here must still
match the shape the executor can run.
