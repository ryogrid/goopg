# R55 SCOPE — Q7 sort gap + Q19 range/IN-in-OR defaults + Q8 tie-break (2026-09-11)

Follows R54-redesign LANDED (`aee14635c`, REPORT-redesign.md): (i)
totals-sourcing re-landed decimal-exact, (ii) OR-join estimator live —
Q7 top join 229626→1468 vs PG 2520 (0.58×), nation-cross 25→2
(PG-exact), winner split→gathered HashAgg by 32.82 (0.02%, both
modes); (iii) Q8 split a 161-cost (0.10%) tie-break over an
estimator-justified seed; Q19 9125→133 (PG anchor 47, approximate).
Two fragilities recorded: Q7's 0.02% winner margin, and the Q8
tie-break owned by neither arm nor estimator.

Two scoping probes ran in parallel (read-only, no code), verdicts
filled 2026-09-11: (A) NO-MECHANISM, (B) WELL-DEFINED-CUT. §1's
MECHANISM-FOUND branch below is retained for the record but marked
WITHDRAWN — it must not be implemented or tested. Nothing here
authorises a constant nudge without a mechanism (R54 REPORT §10:
overfitting warning stands).

## 1. Q7's remaining sort gap — probe A verdict: NO-MECHANISM

Probe A (2026-09-11) compared `costSortRun` (`cost_funcs.go:289-344`)
term-by-term against PG 18.3 `cost_tuplesort` (`costsize.c:1897-1985`
via `cost_sort` `:2144-2165`): comparison cost, quicksort startup,
disk startup, bounded startup, run term, clamp — ALL IDENTICAL. At
1468 rows/width-248 the sort-only total is ~80.9 on both engines
(delta ≈ 0), so the comparator term arithmetically cannot own the
~382 Q7 gap even at 2× misprice. A constant nudge to sort cost is
overfitting — REJECTED, per the R54 warning.

Per §1's own conditional, scope is therefore tie-break calibration
design (see §3), NOT a sort-term change — PLUS one named follow-up
audit the probe surfaced (not yet scoped for implementation): the
Sort+GroupAgg rival's grouping-comparison term (`costAgg` sorted
arm, `cost_funcs.go:383-388`: `cpuOperatorCost × numGroupCols` per
input tuple — PG's `cost_agg` equivalent unaudited), the Gather
Merge 5% IPC term both finalists share, or input-row-count
differences feeding N·log₂N. That audit is R56-candidate; R55 does
not touch `costAgg`.

- R54 end state: gathered-no-split HashAgg 149268.71 vs Sort+GroupAgg
  (pk=3) 149651.11 vs split 149301.53 — three-way tie within 0.3%,
  PG sorts (GroupAggregate→Gather Merge→Sort over a 2520-row join).
- ~~IF MECHANISM-FOUND~~ — WITHDRAWN under the NO-MECHANISM
  verdict (retained for the record only; P1/P2 withdrawn with it):
  would have priced the named term PG-faithfully with the sorted
  arm closing the ~382 gap and hashed/split arms unmoved.
- IF NO-MECHANISM (actual): scope = tie-break calibration design
  (see §3), NOT a sort-term change.

## 2. Q19 range/IN-in-OR defaults — probe B verdict: WELL-DEFINED-CUT

Probe B (2026-09-11): PG prices single-side OR members through the
restriction path (`clause_selectivity_ext`,
`postgres/src/backend/optimizer/path/clausesel.c:684`;
`is_orclause` → `clauselist_selectivity_or` `:359`; inequalities via
`restriction_selectivity` → `scalarineqsel` (`selfuncs.c:588`);
ANY/IN via `scalararraysel` (`selfuncs.c:1824`), non-const/subquery
RHS → 0.5 guess). goopg already owns the same shapes outside OR
arms: `eqSelectivityForColumn` (`selectivity.go:264`, already reused),
`rangeOpSelectivity` (`:307`) + `histogramOpSelectivity` (`:360`),
`inListSelectivity` (`:124`) + `inListElementSelectivity` (`:159`).
Extension point is `orConjunctSelectivity`
(`joinselectivity.go:474`, Eq/Ne-only gate at :482).

- R54 end state: brand-OR join at 133 rows vs PG anchor ~47
  (approximate — different join shape); residual = one measured
  equality (p_brand) × unmeasured range/IN conjuncts at 0.5 default
  each, inside `orConjunctSelectivity`.
- Scope = extend `orConjunctSelectivity` with two arms (probe's
  small, bounded sketch): (1) inequality via `normalizeColumnConstRange`
  → `relPosForSource` + `columnStatsByName` → stats-first
  histogram/MCV core extracted from `rangeOpSelectivity`, bypassing
  its `child`-Node lookup; (2) `*InExpr` with the same attribution
  → per-element stats-first cores → reuse `inListSelectivity`'s
  OR-merge loop with injected stats. Non-const/`Param`/subquery-
  `Plan` ANY elements keep 0.5 (mirrors `scalararraysel` case 3).
- Mechanical input-alignment note (probe): `eqSelectivityForColumn`
  is already stats-first (why the R54 equality reuse was trivial);
  `rangeOpSelectivity` and the range/LIKE arms of
  `inListElementSelectivity` are `child`-Node-indexed
  (`columnStatsForChild`) but the join search has no child Node —
  only `searchCtx.relInfos` + `SourceTableIdx`. Fix = extract
  stats-first cores (`histogramOpSelectivity` already takes
  `(op, bounds, literal, typeName)`).
- Out of scope (hard shape, ledgered): general ANY —
  subquery/`Plan` RHS, non-const elements, `NotEqualAny`/`AllOp`
  variants, where goopg's `InExpr` diverges most from PG's
  Const-array deconstruction. Q19's actual `p_container` shape is
  const-IN-list (easy side) — the cut covers it.
- Prediction = Q19 brand-OR join moves DOWN from 133 and lands
  within 2× of the ~47 anchor (24–94), WITHOUT touching the
  measured-equality path (Q7 pins stay: nation-cross = 2, top =
  1468); must-hold = `TestOrJoinSelectivityMeasuredArms` and
  `TestCalcJoinrelSizeOrJoinClauseMeasured` green unchanged. A
  landing above 94 or at/above 133 FAILS the prediction (no
  "toward" credit for mere downward movement). Bar-retirement
  note (review R55): 26 sits 2 rows above the 24 floor, and the
  ledgered general-ANY follow-up pushes the same direction
  (down) — once general-ANY lands, sub-24 is the EXPECTED
  outcome and this 24–94 bar is retired, not violated; a future
  round must not misread an out-of-bar-low result as a
  regression.

## 3. Q8 sorted-vs-split tie-break (follows §1)

- R54 end state: split wins by 161.33 (0.10%) — partial 158323.05 +
  1086.01 vs gathered 159570.39 vs PG-shape 159709.16; no mispriced
  arm, owner = tie-break.
- Tie-break calibration is LEDGERED, not authorised — and ORDERED
  after the R56-candidate `costAgg` audit: the audit's
  grouping-comparison term (`cost_funcs.go:383-388`) moves the
  GroupAgg arm, not the sort arm, so a calibration built on current
  margins could be invalidated by the audit landing first. Any
  proposal must (a) name the PG-faithful mechanism, (b) show Q7's
  three-way tie resolving the same direction, (c) hold Q5-split-
  wins and all fix-seed §5 must-holds (see §4). A constant that
  fixes Q8 while scattering Q7 is overfitting — reject.

## 4. Must-holds (all assignments)

- The fix-seed doc's §5 (`r54-parallel-admission-step0/FIX-SEED.md`)
  incl. structural Q19 MATCH (pin mode strips rows) +
  Q5-split-wins non-vacuous; TPC-H digest 24/24 MATCH; TPC-DS SF0.5
  sweep PASS=95 all-zero.
- `scripts/pg-plan-parity-diff.py` on BOTH corpora vs live PG
  capture; unparsed = 0 required.
- Planner-only changes per `cmd/plan-snapshot/main.go:36-42`
  decision tree: estimate-only changes keep row counts invariant;
  any tournament change re-runs the fix-binary sweep + spotcheck
  (Q12=2, Q13=34 canonical; Q12=0/Q13=2 = known failure signature).

## 5. Predictions (pre-stated, falsifiable)

- P1 (§1-found) — WITHDRAWN under NO-MECHANISM (kept for the
  record): Q7 sorted arm −~380, hashed/split arms ±0.
- P2 (§1-found) — WITHDRAWN under NO-MECHANISM (kept for the
  record): Q8 PG-shape arm moves by the same term in the same
  direction; split arm does not.
- P3 (§2-cut, live): Q19 brand-OR join moves DOWN from 133 and lands
  within 2× of the ~47 anchor (24–94); Q7 joins bit-exact (1468
  top, 2 nation-cross). Landing above 94 or at/above 133 FAILS.
- P4 (all, live): values gates byte-identical (TPC-H 24/24, DS 95/0).

## 6. Status

- Verdicts filled 2026-09-11: §1 NO-MECHANISM (tie-break design +
  R56-candidate `cost_agg` audit, no `costSort`/`costAgg` change in
  R55); §2 WELL-DEFINED-CUT (inequality + const-IN-list land,
  general ANY ledgered).
- R55 implementation scope is therefore §2 ONLY; §1 contributes no
  code change (§3 tie-break stays ledgered pending the R56 audit).
- Next: agent scope review → reflect → commit + push.
