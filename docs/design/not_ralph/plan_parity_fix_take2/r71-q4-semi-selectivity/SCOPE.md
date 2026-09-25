# R71 SCOPE rev 2 — Q4 theory diagnosis: rows vs election rule (NO implementation) (2026-09-11)

Rev 1 REJECTED on review (correctly): its P0 (corrected rows flip Q4
to MATCH) is arithmetically impossible under the landed cost model,
and its Gate-0 missed where the number lives. This revision
re-scopes the round to what can actually be proven — a diagnosis with
a crossover probe — and explicitly takes implementation OFF the table
until the probe shows an election change. K22 guardrail held by the
review, not by optimism.

## Live pair (evidence — all numbers below come from these files)

- goopg (`/tmp/pp2/r69/r69bpost.plans.txt` Q4): `Sort → HashAggregate
  (Group o_orderpriority) → Nested Loop Semi Join rows=57066` over
  `Seq Scan orders rows=57066` + `Index Scan lineitem rows=5`.
- PG (`/tmp/pp2/r69/r69bpg.pg.plans.txt` Q4, serial, 64MB):
  `GroupAggregate (Group orders.o_orderpriority) → Sort →
  Nested Loop Semi Join rows=13490` over `Seq Scan orders rows=58222`
  + same probe. 13490/58222 = 0.2317 vs goopg's 57066/57066 = 1.0;
  scans tied 0.98.
- Q4 is the unique ≤2-category query on both refs
  (`[aggregation-strategy,sort-strategy]`, neither join-order).

## 0. What rev 1 got wrong (kept on record so nobody re-proposes it)

- `costAgg` prices SORTED and HASHED CPU identically
  (`cost_funcs.go:383-400`); the deficit is the sort run itself
  (`costSortRun`, `cost_funcs.go:289-344` — computed 925 startup /
  959 total at 13490 rows vs hash-proc ≈ 67). It narrows with rows
  but never crosses — at *no* row count (even n=5: 0.058 > 0).
  R47 proved PG itself picks hashed at grouping on pure cost
  (sort-off flip) with the firing micro-rule UNIDENTIFIED (DESIGN.md).
  Corrected rows reprice; they cannot re-elect. The viable theory of
  Q4 is the election/firing rule, not rows.
- The 57066 lives in `EstimateRows` (legacy NLI display arm), not
  `calcJoinrelSize` (K52 lesson, re-learned the hard way): prime
  suspect `estimateNLIndexJoin` (`cardinality.go:239-241`, returns
  outer rows). And PG's 0.23 has no identified mechanism either:
  `eqjoinselSemiCore` returns `1.0−nullfrac` for PK–FK shapes
  (`cardinality.go:833`, reached via `semiPairMatchFraction` at
  `:818`) and `joinResidualSelectivity` SKIPS inner-only quals
  (`:1063-1064` — Q4's `l_commitdate<l_receiptdate` never enters).
  "Transplant PG's rule" presumes a rule nobody has read yet.
- Factual corrections: TPC-DS Q77/Q78 are DECLINED (outer-over-derived
  / outer-spine; R41 was built, measured and REVERTED — never
  admitted), reaching only the legacy driver; R47's "PG 3439" is a
  parallel-plan number vs HEAD-live serial 13490 (drift, not
  contradiction); Q4 differs also in widths (448 vs 16/22), costs and
  probe rows — shape-only pp is verdict-neutral on those, but "single
  cause" is withdrawn; sorted candidates don't exist for special-aggs
  without presorted keys (`groupingpaths.go:405-411`; Q4's
  `count(*)` unaffected); Q21-anti blast radius is a hypothesis
  (opposite estimator direction), covered empirically if ever
  reached; SEMI in CTE bodies unaudited (R61 #4 adjacent).

## 1. The diagnosis (all of this round)

Three questions, in order, each with a pre-registered exit:

1. **Crossover what-if probe.** Temp env-gated row override at the
   legacy NLI estimate site (prime suspect `estimateNLIndexJoin`,
   `cardinality.go:239-241` — siting per R47 Step-0
   (`r47-q4-upper-rel/DESIGN.md:27-30`: legacy `tryBuildNLI`, no
   PlanCost stamp, search builds zero NL paths for Q4) — the probe
   CONFIRMS the site first: if Q4's EXPLAIN rows don't move to the
   forced value, the site is wrong and finding the real one IS the
   result) forcing Q4's semi to PG's 13490, plus ~30000 (slope) and
   3439 (archived parallel-oracle value, R47 pg-flips provenance):
   does EITHER the grouping election (HashAggregate vs
   GroupAggregate-over-Sort) or the ordered-loop election (hashed+Sort
   vs no-sort sorted) change? Victim predicate (required — without
   it the table is confounded): fire only for an NLI whose outer
   subtree contains `orders` and inner contains `lineitem`, and
   assert firings are single-victim+value (log a marker per firing;
   sort -u must show one line — >1 DISTINCT victims → STOP).
   SCOPE rev-3 amendment (review-required): N idempotent firings
   satisfy the predicate; the count (estimation passes) is recorded,
   not gated. Q4-only runs with the knob unset must byte-match HEAD
   (control); sibling paths are unreached by env-gating + predicate,
   not by assertion alone.
   - Grouping flips → rows theory lives beyond B1's arithmetic;
     scope the implementation (fix site + elections) as follow-up.
   - Only the ordered loop flips → follow-up is a rows fix for the
     ORDER BY level alone (to be scoped in the follow-up round).
   - NO change (predicted by B1) → rows theory dead; follow-up is
     the election/firing-rule program (R47's unidentified
     micro-rule), NOT a selectivity fix.
   Any of the three recorded passes this round (site-wrong counts
   as a recorded probe sub-outcome, not a fourth outcome);
   inventing anything else fails it. Temp code reverted before
   commit; byte-identity gate (probe-off plans identical to HEAD)
   proves the knob is inert when unset.
2. **Oracle read.** PG's actual rule for EXISTS-with-inner-quals
   semi selectivity (`clausesel.c` — read, cite lines, do not
   conclude): which inputs produce 0.2317 here, and is it even an
   eqjoinsel_semi shape? If PG's mechanism needs inputs goopg lacks
   (MCVs? inner-uniqueness proofs?), that — not a formula — is the
   filed gap.
3. **R47 confrontation.** Reconcile (1)+(2) with R47 DESIGN's proof
   (PG picks hashed at grouping on pure cost) and its open firing
   rule: name what would have to change for Q4's Group Key line to
   read `GroupAggregate`, given B1's arithmetic forbids a pure-cost
   answer.

## 2. Predictions (diagnosis round — falsifiable without a flip)

- P0: the crossover table (rows → grouping election + ordered
  election, elected shapes named) PLUS the oracle rule cited with
  lines PLUS the R47 reconciliation paragraph. Any of the three
  missing → STOP, the round is incomplete.
- P1: NO implementation, NO plan moves (temp knob byte-identical
  with it unset — the gate, not an assertion). A moved plan under
  the probe-off control fails the round.
- P2: values/sweep untouched by construction (nothing shipped);
  suites green (temp code compiles cleanly in and out).

## 3. Sibling audit (narrow: diagnosis touches nothing)

- The temp knob lives at ONE estimate site (no shared helper);
  unreachability rests on env-gating + the victim predicate + Q4-only
  runs + the probe-off byte-identity control — not on assertion
  alone. Enumerate the site's callers by grep anyway (a shared helper
  would void the predicate argument).
- Q21-anti/DS shapes: unreached by env-gating + predicate (same
  evidentiary standard as above).
- NLI staleness comment, R63-#1/#2, K58/K59: carried, untouched.

## 4. Gates (diagnosis, all FOREGROUND)

0. Temp probe + crossover table + oracle cite + R47 reconciliation
   (`DIAGNOSIS.md`, this round's deliverable).
1. `go test` suites green; `go vet` clean; temp reverted with
   grep-proof (`R71DBG` zero hits) + byte-identity re-verified
   post-revert.
2. Agent review (APPROVE* to close; the follow-up round — rows-fix
   or election-program — gets its own scope THEN).
3. Commit (explicit pathspec: DIAGNOSIS + TODO — SCOPE herewith)
   with `-n` + push. No planner/executor/costing change ships.

## 5. Ledger (carried)

Projection-pushdown program (queued, needs design bundle);
minimize_datum dependency; slice (b) BLOCKED standing; R51 items
2–3; R52 §4.2; R54 follow-ups; #6/R61-#4 watches (still deferred);
R63-#1/#2; Q4-MATCH prediction WITHDRAWN (rev-1 P0 dead by B1).
