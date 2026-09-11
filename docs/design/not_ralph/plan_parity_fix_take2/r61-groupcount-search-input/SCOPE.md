# R61 SCOPE — group-count input rows: size the grouping rel from the search (2026-09-11)

Follows R60 LANDED (`r60-partial-nestloop-producer/REPORT.md`): partial-NL
producer live, pp 5/15/0/2, DS sweep PASS=94 + Q72 TIMEOUT. R61 triage listed
(a) Materialize producer, (b) Q4 agg/sort strategy, (c) Q11 artefact. Probe
round first scoped (c): Q11's `HashAggregate rows=1` (PG: 10667) is NOT a
fixture artefact — it is a live estimator defect, fully chained, and the
bounded cut is one input-rows sourcing fix. (a)/(b) stay queued behind it:
Q11/Q5 carry the same rows=1 disease and this cut moves both.

Relid map (Q11): partsupp ⨝ supplier ⨝ nation, `n_name='GERMANY'`,
`GROUP BY ps_partkey HAVING sum(...) > (subselect × 0.0001)`.

## 1. Probe G verdicts (live instrumented runs, SF1 clone :5551, seed 20260905)

### (i) The group estimator is FINE — its input is 1

Temporary `GOOPG_R61DBG` instrumentation on `estimateNumGroups`
(reverted, tree clean): `resolveBaseColumn` succeeds —
`nd=201356 rawRows=800000 relnil=false oid=16412 att=1`
(ps_partkey via pg_stats, unfiltered partsupp 800000). The `rows=1` comes
from `inputRows=1`: `sizeGroupingRelFromAgg`
(`groupingpaths.go:139`) recomputes `EstimateRows(Project→*Join*)`
instead of using the search joinrel rows (32000).

### (ii) The 1, fully chained

`EstimateRows(*Join*{algo=NL})` has NO nestloop arm in `estimateJoin`
(`cardinality.go:~610`) → `0.005` fallback on `L=*NLI*(1)` ×
`R=*IndexScan*(80)`: `1×80×0.005=0.4` → floor 1. The NLI's 1 is itself
`estimateNLIndexJoin` returning outer-rows-only (=1 for nation⨝supplier
vs search 400 — its "1 row per call site" comment is stale since the
M0127-P5.9 keyed-scan fix made the partsupp probe 80). Then the closing
clamp `min(201356, 1)` → 1 group. Every link measured, none hypothesised.

### (iii) PG oracle: 32000 groups × HAVING 1/3 = 10667, derived exactly

Read-only source + live :65432 reference: `estimate_num_groups`
(selfuncs.c:3449) uses search joinrel rows (32000); partsupp is
unfiltered at base (800000=tuples) so no Yao discount → 203361 → clamp
32000. `cost_agg` (costsize.c) then applies HAVING
`clauselist_selectivity`: 32000×1/3=10667 exact. Verified live:
Q11-without-HAVING → PG rows=32000; with HAVING → 10667.

### (iv) The STAMP IS 1 — stamp-preferring fix REFUTED

Decisive mini-probe: `legacyDisplayCostOf(child).PlanRows` printed at
`estimateNumGroups` time = **1** (float64), not 32000. The display-cost
stamp is downstream of the same defect (its Project arm also yields 1),
so "prefer the stamp when recompute fails" (the partialaggpaths.go:248
precedent) would change nothing here. The fix must repair the INPUT the
recompute consumes, not prefer a second consumer of the same poison.
This is what selects the §2 cut over the stamp cut.

### (v) The search already has the right number two lines below

`createGroupingPaths` (`groupingpaths.go:88-90`, R54 REDESIGN rev 2)
already overrides `seed.Rows` from `searchedJoinInputRelOf(child)` with
the identical fail-closed gate (`sr != nil && sr.Rows > 0`). The join
seed is 32000 today (display shows the 32000 join rows); only the GROUP
COUNT (`sizeGroupingRelFromAgg`, line 65, called before the seed) still
eats the recomputed 1. The cut extends R54's own sourcing to the rel it
missed — same accessor, same gate, no new machinery.

## 2. The cut

ONE sourcing change in `sizeGroupingRelFromAgg`
(`groupingpaths.go:134-145`): derive `inputRows` from
`searchedJoinInputRelOf(aggNode.Child)` when it fires
(`sr != nil && sr.Rows > 0`, the R54 gate verbatim), else keep
`EstimateRows(aggNode.Child)`. Everything downstream —
`estimateNumGroups`, the Yao/restriction term, the closing clamp —
runs UNCHANGED on the corrected input.

Deliberate non-mirrors (not drift): no Yao change (#2 below); no HAVING
selectivity change (#3); no `estimateNLIndexJoin` stale-comment fix
(ledgered — its outer-only value stays wrong but becomes unreachable on
this path once the input is stamped); no display-code change (row
NUMBERS move, no stamper logic changes); no executor change.

## 3. Falsifiable predictions

| # | Claim | Mechanism (§1) | Verdict on miss |
|---|-------|----------------|-----------------|
| P0 | `searchedJoinInputRelOf(aggNode.Child)` fires for Q11 (non-nil, Rows=32000) | child chain Project→tagged `*Join*` search root (`markSearchedTree` carriers include `*Join`; §1.v seed gate fires on the same child today — identical argument, so P0 fires iff the seed gate fires). Project/Sort are pass-through; Filter/Limit/Aggregate/Distinct/SetOp stop the walk | FAIL (nil/0) → STOP, re-audit; the cut is a no-op and any movement is drift |
| P1 | Q11 groups 1→~80 | input 32000 through the UNCHANGED Yao term (`cardinality.go:1314-1322`): 201356×(1−((800000−80)/800000)^(800000/201356)) ≈ 201356×0.000397 ≈ 80; closing clamp min(80,32000) | outside [40,160] → re-audit (Yao hand-derivation wrong or `relFilteredRows` moved) |
| P2 | Q11 top: HashAggregate rows 1→~80; Filter(rows) = 80×sel, sel predicted 1/3 (`rangeOpSelectivity`→`defaultIneqSel` on `sum > InitPlan`; verify-or-ledger) | §1.ii chain with corrected input; HAVING stays `Filter`-above-`Aggregate` (`planner.go:1731-1732`) | groups move but Filter sel ≠ 1/3 → ledger #3, still PASS (sel change is its own scope) |
| P3 | values md5 MATCH on all TPC-H queries; Q5 groups move the same direction (same disease, `groups inputtotal=278542.01, rows=1` measured) | row counts only feed cost/display, never qual evaluation | ANY values mismatch → STOP |
| P4 | pp stays 5/15/0/2 modulo the predicted rows/cost NUMBER moves on Q5/Q11 (node-type shapes identical); DS SF0.5 sweep: PASS=94 + Q72 TIMEOUT alone, verdicts unchanged | planner-only sourcing fix; executor untouched | new MISMATCH/CKMISMATCH/TIMEOUT → explain-or-stop |

WATCH (either outcome consistent with P1–P3): Q4/Q10 agg rows (same
`sizeGroupingRelFromAgg` call site, different join shapes — movement
there is expected and parity-direction-checked at REPORT time, not
gated here).

Re-audit rule (carried from R59/R60): any P-miss triggers DPTRACE A/B
against the pre-R61 binary (non-groupcount lines bit-identical), not
celebration.

## 4. Sibling audit + executor inventory (why the cut is planner-only)

- **Callers: TWO production sites, one shared function.** The cut lives
  INSIDE `sizeGroupingRelFromAgg`, so both callers inherit the same
  sourcing: `createGroupingPaths` (`groupingpaths.go:65`) and
  `createPartialGroupingPaths` (`partialaggpaths.go:304`, via
  `addPartialAggSplitPath` receiving the same aggNode — verify pointer
  identity at implementation). This is REQUIRED, not incidental: the
  partial caller's `grouped.Rows` (`finalGroups`) prices the split
  candidates' per-group terms against the serial rel's group count, so
  the two upper rels must agree. Post-cut both read ~80 on Q11 (both
  legacy together if untagged — consistent either way); one firing
  while the other does not is a FAIL mode gate 3 adjudicates. The C-15
  comment at `partialaggpaths.go:298-304` ("a blind `Rows` of 1 is
  harmless here") is superseded by consistency-is-the-justification and
  gets a one-line update in the cut (second file touched, comment
  only). The override self-gates on the R54 accessor + `Rows > 0`; no
  other caller of `estimateNumGroups` (set-op dedup, grouping-sets,
  CTE synthesis — see §6) takes a searched input, so no other call
  site needs the gate.
- **Tagged-`*Filter*`-root caveat**: `*Filter` is a `markSearchedTree`
  carrier, but the walk stops at a non-root `*Filter` → legacy nil
  (fail-closed, and the seed gate nils equally — both sides stay
  consistent; ledgered, not gated).
- **`createPlan` untouched**: only winners build; row numbers are not
  shape.
- **Executor deliberately OUT**: row estimates never reach execution;
  values gate P3 proves it.
- **No GUC, no knob, no display-code change.** EXPLAIN row NUMBERS on
  Q5/Q11 move by P1/P2 (that IS the fix); no stamper question arises.

## 5. Gates (implementation round)

1. `go test ./internal/optimizer/` green (no `-count=1`); `go vet` clean.
2. Q11: groups ≈80 per P1 (bin `goopg-r61`, clone :5551 recipe), values
   md5 MATCH vs pre-R61; Q5 values MATCH. Also assert Q11 `seed.Rows`
   stays 32000 (R54 gate unmoved) and record Q5's groups head value
   (P3 predicts it moves; pin the number in REPORT).
3. DPTRACE A/B on Q11 (and Q5): only groupcount-input lines move; heads
   per P1/P2, WATCH adjudicated with numbers.
4. `scripts/pg-plan-parity-diff.py`: match≥5, unparsed=0 (R60: 5/0);
   Q5/Q11 number-moves recorded as predicted, not regressed.
5. DS SF0.5 sweep foreground, fresh build: PASS=94 (same set) + Q72
   TIMEOUT alone per P4.
6. REPORT.md with the A/B numbers, then review, then commit + push.

## 6. Ledgered follow-ups (not this round)

- **#2 Yao-through-parameterized-probe** (80 vs PG 32000): `relFilteredRows`
  returns the per-probe `IndexScan` 80 for partsupp instead of base-rel
  800000, so Yao crushes 201356→80. Needs parameterized-probe detection
  (`IndexScan{Key,…}`/`OuterColumnRef`, `plan.go:765,452`) to lift the
  restriction scope to the base rel. Full PG 32000 (→10667 with #3)
  requires this.
- **#3 HAVING selectivity confirm**: measure `rangeOpSelectivity` on
  `sum > InitPlan`; predicted 1/3 (=PG `cost_agg`), verify-or-ledger.
- **M1-display** (separate cosmetic round): single-table GROUP BY
  EXPLAIN=200 vs search=201356 via narrowed-IndexOnlyScan + missing
  `*IndexOnlyScan` arm in `resolveBaseColumn`. Display-only, shared-family
  blast radius — its own scope.
- **CTE stats synthesis out of scope**: `cte_stats_synthesis.go:92-93`
  uses the same `EstimateRows(agg.Child)` + `estimateNumGroups` recompute
  for CTE-body column stats. CTE-body join search is rare and the consumer
  is width/ndistinct synthesis, not display rows — explicitly not widened
  here.
- **`estimateNLIndexJoin` staleness**: "1 row per call site" premise
  dead since M0127-P5.9; unreachable on the R61 path post-cut, but the
  comment + outer-only return mislead every future reader. Comment fix
  bundled with #2 (which touches the same arms), not here.
- Queued triage unchanged: (a) Materialize producer, (b) Q4 agg/sort
  strategy — re-triage after R61 lands, since Q4/Q5/Q11 numbers all move.

Evidence tmp-only `/tmp/pp2/` (`r61probe-tpch` SF1 clone,
`r61dbg-start.log` G-verdict lines, `r61-q11-explain.sql`,
`r61-q5-explain.sql`); PG reference :65432 (`PGPASSWORD=tpch -U tpch`).
