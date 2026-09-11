# R72 SCOPE — election program Step-0: where sorted loses for Q4 (2026-09-12)

Ordered by R71 DIAGNOSIS §5.1 (election/firing-rule program is the
Q4-closing path) and the R67 queue (join-order costing owns Q5's
surroundings; priced levers exhausted: NLI landed R69, width BLOCKED
R70 on minimize_datum, Q4-rows dead R71). Measurement ONLY (R68
precedent): locate the lost election on current numbers, then scope
the slice. No planner/executor/costing change.

## 0. Re-triage (why this, why now)

- Q4 is the unique DIVERGING ≤2-category query
  (`[aggregation-strategy,sort-strategy]`, neither join-order —
  R71 SCOPE, review-verified; MATCH queries trivially carry fewer).
  R71 proved rows can't elect it
  (crossover probe: no election change at 3439–57066) and PG's own
  0.23 needs no transplant (goopg would compute 0.78 — different
  stats, correct behavior). What remains is the election itself.
- The structural gap is named but unmeasured at HEAD: K12 (upper
  planner doesn't model ordering requirements — WindowAgg orders
  internally, so the requirement never reaches the planner) and K24
  (join rel's PartialPathlist/Pathkeys die in `planJoinlistSearch`;
  R21 threaded `searchRelOf` for partial paths, not pathkeys). R47
   slice-2 landed an ordered loop that did NOT flip Q4 (hashed+Sort
   1426.78 beat no-sort sorted 6077.69 at R47-time rows — STALE,
   re-measured in Step-0; R69 repriced everything underneath since).
- #6/R61-#4/(b)-as-was stay deferred (grounds unchanged:
   `pp69bpost.txt` still shows Q4 at 2 cats, Q3 aggregation-flagged
   in #6's territory, and R69's moves confined to Q5/Q7/Q9/Q18+Q2 —
   spot-checked, not asserted);
  projection-pushdown/minimize_datum stay queued programs (R71 §0);
  (a) stays queued behind join-order (R67, unchanged).

## 1. Step-0 protocol

Question: at HEAD, for Q4, (a) which grouping candidates exist with
what prices (hashed? sorted-over-Sort? who wins, by how much?), (b)
which ordered-level candidates exist with what prices (hashed+Sort
vs no-sort sorted — R47's loop, re-measured), and (c) what rule
elects differently in PG (`create_grouping_paths` pathkey handling +
cheapest-path-satisfying-ordering — read, cite lines, do not carry
R47's reading)?

Method (all FOREGROUND): temp env-gated candidate logging at the
grouping producer (`addGroupingPaths`-adjacent) and the ordered loop
(`upperorderedgrouping.go`, R47's site) — prices + winner per
contest, nothing else; clean-HEAD binary + private TPC-H clone
(`:55xx`, inode-verified, serving probe); hand-written Q4
(`Queries()[4]`) via psql EXPLAIN, twice byte-identical; Q6/Q13/Q22
trace-on/off byte-identity control (production-inertness proof,
R68 precedent). PG oracle: live `:65432` Q4 (already on file
`r69bpg.pg.plans.txt` — re-capture iff stale) + `create_grouping_
paths` pathkey cite from `./postgres` (read-only).

Exit: named losing election(s) with candidate prices + PG's rule
cited + one scoped slice. Sizing/admission/parameterisation are NOT
re-litigated (R68 settled the DP frame for joinrels; grouping/upper
rels are a different frame — state which frame each number belongs
to, the K52 discipline).

## 2. Slice menu (decided AFTER Step-0; pre-declared options)

- (a) Thread pathkeys to the grouping/ordered stages (K24 line, R21
  `searchRelOf` precedent: carry on the tag, not through fifteen
  signatures) so a sorted aggregate can enter contests it should win.
- (b) Ordered-loop comparator fix (if the loop exists, prices right,
  but elects wrong — mis-comparison, not missing candidates).
- (c) Nothing (elections already correct at HEAD → re-baseline the
  R47 numbers and close; Q4's flags then belong elsewhere — name
  where, do not drop; (c) is exempt from the movement bar by
  definition).
Exactly one of (a–c), no hybrids. Whichever slice: falsifiable bar
(named Q4 lines move toward PG's text), ZERO EXTRA flips, values +
sweep bind at implementation (this Step-0: trace-only). Partial-agg
elections and Gather/GatherMerge interplay are out of the menu:
Q4 is serial (no Gather, no partial split in its plans) and its loop
declines on any PathFinalizeAgg — the candidate census is
self-covering (any surprise candidate gets logged), so the omission
is recorded reasoning, not a silent assumption.

## 3. Predictions (measurement round)

- P0: the losing election(s) named with candidate prices + PG rule
  cited + one scoped slice (a–c). Any of the three missing → STOP.
- P1: ZERO plan moves (temp instrument + inertness gate — the proof,
  not an assertion).
- P2: values/sweep untouched by construction (nothing shipped);
  suites green (temp compiles cleanly in and out).

## 4. Sibling audit

- Grouping producer + ordered loop + any trace channel touched move
  together (writer ↔ test ↔ parser discipline, R53 §4 precedent).
  `createplanroot.go` boundary-walk coverage (R11: every kind between
  root and searched subtree) constrains where new nodes may appear —
  Step-0 adds none.
- Executor sorted-agg gating (`operators_join_agg.go:2222`,
  Mode==Simple) bounds which shapes are even electable (R45/K97
  lesson: do not elect unexecutable shapes) — the slice must check
  electability, not just price.
- NLI staleness comment, R63-#1/#2, K58/K59, R51 items 2–3, R52 §4.2,
  R54 follow-ups: carried, untouched.

## 5. Gates (Step-0 measurement, all FOREGROUND)

1. `go test ./internal/optimizer/ ./internal/executor/
   ./internal/testutil/estimateaudit/` green (no `-count=1`);
   `go vet` clean.
2. Trace-on/off byte-identical plans on the Step-0 corpus (Q4 ×2 +
   Q6/Q13/Q22 control).
3. `STEP0.md`: elections + prices + PG rule + one scoped slice.
4. Agent review (APPROVE* to proceed; REJECT re-scopes).
5. Commit (explicit pathspec: STEP0 + TODO — SCOPE herewith) with
   `-n` + push. No code change in this round by design; spotcheck
   iff `:65433` free, else the standing deferral rationale carries.

## 6. Ledger (carried)

Election-program follow-ups sequence from the slice; #6/R61-#4/(b)
watches; projection-pushdown/minimize_datum queued programs; slice
(b) BLOCKED standing; R51 items 2–3; R52 §4.2; R54 follow-ups.
