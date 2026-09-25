# R47 — Expose grouping candidates to the ordered level; measure (K101 rev 3)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Aggregation-strategy + sort-strategy axes. TPC-H Q4 nearest miss.
Rev 1 REJECTED (11 findings); rev 2 REJECTED (narrow, 8-item
checklist). This revision answers every item AND downgrades the
unprovable claim: after exhaustive PG-source analysis the firing
micro-rule could not be derived from first principles (details
below), so this round lands SAFE PLUMBING + MEASUREMENT and lets
the corpus arbitrate — no predicted flip, no hedge, no invented
rules. K101 retained.*

## 0. Baseline (R46 `de85a10`, canonical data, pinned env)

- TPC-H: `match=1 shapediff=19` (agg 16 / sort 10).
- TPC-DS: `match=1 (Q9) shapediff=69` (out of scope).

Reference scoping (K9-compliant): NO parity-verdict claim on Q4
and no fixture targeting. The fixture (`bench/tpch/plans-pg/Q4.txt`,
stale-serial) is a movement detector only; acceptance is gates +
pins + the five live flips as CHARACTERIZATION (informative, not
gating). Live-parallel shapes stay out via K96/K97. Owner waiver
for fixture re-capture remains requested, not presupposed.

## 1. Step 0 results (all measured; traces removed, tree clean)

- **Provenance:** Q4's semi is a legacy `tryBuildNLI`
  `*NestedLoopIndexJoin`, unstamped (`CostSet=false`; bypasses
  the `createPlanNode` stamp funnel, `createplan.go:44-52`).
  Search builds zero nested-loop paths for Q4 (traced).
- **1141.32 accounting (CLOSED):** `Derive(Filter_wrapper[NLI],
  57066)` = NLI_derive (570.66 — no children arm in
  `legacyDisplayChildren`, `internal/optimizer/plancost.go:204-243`)
  + perRow (570.66, `internal/optimizer/plancost.go:154-160`).
  The render reads the collapsed wrapper
  (`internal/executor/operators_explain.go:421-456`,
  `:483-515`, `:2177-2190`).
  HashAggregate startup 1283.98 = 1141.32 + 0.0025×57066,
  exact.
- **Width flip (448 vs 64):** same 9-col semi Output both GUC
  arms; per-column type widths differ. Open mechanism,
  verdict-neutral; recorded.
- **Rows:** semi selectivity 1.0 acknowledged, not owned.
- **PG flips, re-measured live `:65432` 2026-09-10, archived
  `pg-flips/`:** default→sorted (70122.64),
  sort-off→hashed (69911.66), hashagg-off→sorted,
  no-ORDER-BY→sorted, LIMIT-5→sorted. Deterministic.
- **Micro-rule status (honest): UNIDENTIFIED after exhaustive
  analysis.** `create_ordered_paths` (`planner.c:5308-5360`)
  offers presorted inputs directly and Sort over the cheapest
  input — subject to PG's inner gate (`planner.c:~5355-5365`:
  non-cheapest unsorted inputs are skipped absent
  incremental-sort presorted keys). Slice 2 offers Sort per
  surviving candidate, which is BROADER than PG; declared
  deviation, harmless (extra Sort candidates lose on cost
  unless the cheapest-input rule itself diverges — visible in
  the §3.3 census if so). Both candidates reach `add_path`
  either way. Every dominance model constructed from PG's
  documented rules (add_path startup/total/fuzz/pathkeys in
  all combinations; cheapest_total final; grouping-level
  pruning) predicts hashed at least once, yet PG picks sorted
  in all five flips (including no-ORDER-BY, where no ordered
  level exists).
  Work_mem spot-checks (1MB/4MB/256MB, identical costs and
  choice, observed in-session — no artefact file; the five
  `pg-flips/` captures are the archived evidence) argue
  against spill-driven explanations. The firing rule is
  therefore NOT derivable from the rules as understood — it
  may lie in
  native-vs-sort pathkey comparison semantics, an
  unidentified grouping-level pruning, or `can_hash`-adjacent
  gating. Per the program's honesty norm this is STATED, not
  papered over — and it is WHY this round predicts no flip.

## 2. Change (two slices)

Rationale (PG-cited, rule-free): PG's grouping rel RETAINS
multiple strategies (`add_paths_to_grouping_rel` offers hashed
+ sorted; both survive `add_path` whenever neither dominates)
and `create_ordered_paths` iterates ALL input paths
(`planner.c:5345+` `foreach(lc, input_rel->pathlist)`).
goopg collapses grouping to one node (`createGroupingPaths`,
`groupingpaths.go:89-112`) before ordering sees alternatives —
so whatever PG's firing rule is, goopg cannot execute it. This
round removes that structural inability; NOTHING about
comparison logic changes.

**Slice 1 — plumbing, zero behavior change.** Expose the
grouping rel's surviving PathAgg candidates (Paths, never
built Nodes) for the ordered stage; the ORDERED stage is
untouched (still wraps today's single winner identically).
Exact mechanics:
- Per-candidate `Agg` spec clone at offer time (today all arms
  share `aggNode`: plain `:331`, hashed `:346`, sorted `:396`;
  only index clones `:419-451`). Winner copy-back
  (`*aggNode = *built`, `:112`) keeps identical pointer
  identity (HAVING filter aliases `agg.node`,
  `planner.go:1732`, `:1746-1748`).
- `stampAggregateInputTarget(agg.node, nil)` (`:1758`) runs
  only on the winner, as today.
- Losers stay unbuilt Paths (no stamps on shared children;
  `maybeAttachMemoize` runs once on the final tree —
  Memoize-node census on Q4 before/after).
- Plan-cache safety: `upper` is per-statement local
  (`fetchUpperRel`, `:64`; same registry reaches the ordered
  stage, `planner.go:1937-1940`).
- Gate: byte-identical corpus A/B both suites + full suites.

**Slice 2 — expose at the ordered level, measure.** A new
ordered-level loop over the grouping survivors calls
`addOrderedPaths` per candidate directly on the ORDERED rel
(NOT `createOrderedPaths` reuse — it takes one finished Node
and `inputNodePathkeys` returns nil through `*Aggregate`,
`upperorderedinput.go:184-186`; no Aggregate node arm is
added — paths already carry Pathkeys: hashed `:335`,
sorted `:390`, `:400`). Existing `setCheapest` +
`getCheapestFractionalPath` elect (cheapest-total final, no
LIMIT → `tupleFraction` 0 — stated). Deviations declared:
M0129-S1 tie-break (`path.go:752-776`, never returns
costsEqual — does not fire for Q4's numbers but governs other
near-ties); `sortPathForBounded` stamps sort-disabled
(`joinpathsmerge.go:490-492`, what makes flip #2
reproducible).
- Gate: byte-guard A/B (sections MAY move — that is the
  measurement), per-query census with ZERO EXTRA flips
  REQUIRED (no previously-matching query newly diverges;
  any newly diverging section fails the round), shape-delta
  reported, standard gates. If Q4 (or anything) flips toward
  PG → stretch achieved, recorded. If nothing flips → the
  measurement (which candidates survive where, from the census
  + unit probes below) must ELIMINATE at least one of the
  three named hypotheses (native-vs-sort pathkey semantics;
  grouping-level pruning; `can_hash`-adjacent gating) so the
  pass always produces the follow-up's starting point.
  Either outcome is a pass; inventing a rule is the only fail.

Explicitly NOT in this round: probe-execution pricing (PG
itself would pick hashed at grouping on pure cost — shown by
the sort-off flip; the override, whatever it is, lives above),
width narrowing, qual-placement + stray `Filter: (true)`
(→ R48, independent, either order), join-method costing,
K100, rescan-discount/Memoize refinements, semi selectivity.

## 3. Success tests (all must hold)

1. Slice-1 gate (§2).
2. Comparator-parity unit pins (KNOWN PG behaviors, not Q4's
   contested pair): fuzz bands (1.01), startup dimension
   (`considerPathStartupCost` gating stated),
   prefix-pathkeys (`dimEqual`/`dimBetter1`/incomparable),
   DisabledNodes trumping, M0129-S1 tie-break direction.
   Plus: no-sort offered iff pathkeys satisfy (unit).
3. Dominance instrumentation (diagnostic, both magnitudes
   rows 57066/13628, widths 448/64): which grouping
   candidates survive `add_path`, which ordered candidates
   survive, who the final pick is — RECORDED, not asserted.
4. Five live flips as characterization (informative): goopg's
   strategy picks reported against PG's; mismatch does NOT
   fail the round (it scopes the follow-up) — but any flip
   toward PG is recorded as stretch.
5. Pins pre-declared: Q4 estimate re-baseline IF its shape
   moves (estimate-audit/EA-ratchet/c13a); exercised seed
   sites are `groupingpaths.go:77` + `upperordered.go:89`
   (others untouched — stated); Memoize-node census Q4
   before/after; inode-verified serving binary (K91).
6. Standard gates: units, suites, TPC-H spotcheck, SF0.5 sweep
   `MISMATCH=0`-class, byte-guard A/B both corpora.
7. `match` count is NOT a criterion (conjunction rule).

## 4. Non-goals / follow-ups (named, not owned)

- R48: semi JoinQual placement + `Filter: (true)` drop.
- The firing micro-rule follow-up (uses §3.3's diagnosis):
  native-vs-sort pathkey semantics, grouping-level pruning
  audit, or `can_hash`-adjacent gating — whichever the
  measurement implicates.
- Q84/Q96 join-method costing; K100; TPC-H fixture
  re-capture (owner); K26 halves; NLI rescan-discount/Memoize
  refinements; semi selectivity cardinality.
- K96/K97 execution program for live-parallel shapes.

## 5. Evidence archive

- `pg-flips/` (five live PG captures, pinned GUCs,
  2026-09-10); `/tmp/pp2/oc-tpch-r46c.txt` (goopg Q4
  before-shape);
  Step-0 trace logs `/tmp/pp2/oc-trace*.log` (binaries
  removed; tree verified clean of `R47TRACE`)
- Servers `:5552`/`:5553`/`:5554` (clean r46); PG `:65432`
  restarted for re-measurement, stopped after (was down on
  arrival).
