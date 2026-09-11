# R66 SCOPE — alias → source Sort/Group keys (K11(d)): Sort group-section + Star + Distinct now; G/T chase after a live dump

Follows HEAD `d40ba5e58` (R65 landed ledger + R66 scope-intent, pushed).
Target: the last R65-ordered query-closing rendering round before the (a)
Materialize re-triage. Rendering-bearing set at R65-close: {Q7, Q9, Q10,
Q13, Q16} (5); Q10 is Group-Key FD-trim (planner work, explicitly out).
This round owns Q7/Q9/Q13/Q16's alias-vs-source key lines.

## 0. Baseline (live pair, pinned env)

goopg `r65mine.plans.txt` (estimate-audit -plan-only, :5533 clone,
`work_mem='64MB'`, `max_parallel_workers_per_gather=4`) vs live PG
`r65pg.plans.txt` (PG :65432, same GUCs). Verdicts vs fixtures 6/14/0/2,
identical vs live PG (R65 REPORT §2.5). Residuals (`pp65-new-verbose.txt`):

- Q7: `Group Key: supp_nation, cust_nation, l_year` + `Sort Key:
  supp_nation, cust_nation, l_year` vs PG `Group Key: n1.n_name,
  n2.n_name, (EXTRACT(year FROM lineitem.l_shipdate))` + same Sort Key.
- Q9: `Sort Key: nation, o_year DESC` / `Group Key: nation, o_year` vs
  PG `Sort Key: nation.n_name, (EXTRACT(year FROM orders.o_orderdate))
  DESC` / `Group Key: nation.n_name, EXTRACT(year FROM orders.o_orderdate)`.
- Q13: `Sort Key: count DESC, c_count DESC` / `Group Key: c_count` vs PG
  `Sort Key: (count(*)) DESC, (count(orders.o_orderkey)) DESC` /
  `Group Key: count(orders.o_orderkey)`.
- Q16: `Sort Key: count DESC, p_brand, p_type, p_size` vs PG `Sort Key:
  (count(DISTINCT partsupp.ps_suppkey)) DESC, part.p_brand, part.p_type,
  part.p_size` (Group Key already pairs).

## 1. Probe evidence (throwaway `zz_r66_probe_test.go`, reverted, tree clean)

Nine shapes, `optimizer.Plan` + EXPLAIN (COSTS OFF) on small fixtures.
All facts below are dumped node contents, not readings:

- **A** (single-table, GROUP BY source): Sort keys are
  `ColumnRef{output name, idx, SourceTableIdx:0}`; GroupExprs are source
  (`ColumnRef{table:1}`, `ExtractExpr`). Renders `Sort Key: n_name,
  extract` vs `Group Key: n_name, EXTRACT(...)`.
- **E** (join, GROUP BY source): Group Key renders qualified sources
  (`n1.n_name, EXTRACT(year FROM l.d)`); Sort keys still bare.
- **F/I** (single-table AND join, GROUP BY **alias**): GroupExprs STILL
  resolve to source (table-carrying ColumnRef / ExtractExpr). **An alias
  in GROUP BY is NOT what makes Q7/Q9 render aliases.** The shape that
  does is unreproduced at unit level (boundary-Project candidate per
  K76 — Slice 2 Step 0 names the dump that settles it).
- **G/H** (subquery): reproduce alias Group Keys — GroupExprs[0] =
  `ColumnRef{alias, idx, table:1}`, child `*Project` with alias-named
  output. PG stops the chase at the subquery boundary (`s.supp`), so a
  chase to `Project.Targets[j]` would print one level too deep
  (`r66a.n_name`) — the chase target needs the live dump, not a guess.
- **B** (`count(*)`): Aggs[0] = `{count, Star:true, Arg:nil}` — Star is
  the ONLY decline reason in `expandAggOutputRef`.
- **C** (`count(DISTINCT x)`): Aggs[0] = `{count, Distinct:true,
  Arg:ColumnRef}` — Distinct is the ONLY decline reason.
- **D** (transitive miniature of Q13-key2): outer GroupExprs[0] =
  `ColumnRef{c, idx:1}` → inner Aggs[0] = `count(x)` plain and
  synthesisable; outer name `c` ≠ inner output name `count` — **the
  chase must be positional (Index), never name-based** (R65's name check
  would decline here).
- Struct facts: `ColumnRef{Index, Name, SourceTableIdx}` — sort-key refs
  carry table 0, group refs table N, otherwise identical. `FuncCall`
  HAS `Star` (`plan.go:600`) but NO `Distinct`; the renderer prints
  `Name(args)` with neither handled. PG's paren rule matches goopg's
  existing S18 split: Sort arm always wraps non-Var; Group Key arm wraps
  only under a Sort child (Q7 PG wraps EXTRACT in Group Key over its
  Sort; Q9 PG leaves it bare over its join).

## 2. The cut — Slice 1 (this implementation round, renderer-only)

Three arms in `operators_explain.go` beside R65's, no planner/executor/
costing line touched:

- **Arm S — Sort group-section keys render `GroupExprs[idx]`.**
  When a Sort key is a ColumnRef with `0 <= idx < len(GroupExprs)` of
  the child agg from `childAggregateThroughFilters` (explicit lower
  bound: a negative Index must decline, never index), AND
  `agg.Output()[idx].Name == col.Name` (R65's same-level coordinate
  guard, kept — Sort keys carry the output name per probe A; probe D's
  positional-only rule belongs to the cross-level Slice-2 chase, not
  here), render `GroupExprs[idx]` through the Sort arm's existing
  pipeline (S18 wrap included: ExtractExpr → `(EXTRACT(...))`, exactly
  PG's Sort-Key form). Written order, NOT `GroupKeyOrder` (ORDER BY
  binds to grouping positions, not print order). Further fail-closed:
  nil, `GroupingSets != nil`, or GroupExprs[idx] containing a sublink /
  OuterColumnRef (numbering risk per R65's Filter-half note). Where
  GroupExprs is alias-named (Q7/Q9/Q13-outer) the text is unchanged by
  construction — Slice 2's input, not Slice 1's failure.
- **Arm Star — synthesise `count(*)`.** `expandAggOutputRef` admits
  `call.Star && call.Arg == nil`, synthesising
  `FuncCall{Name, Star:true}`; the FuncCall render arm gains an additive
  `Star && len(Args) == 0 → "(*)"` case (premise, stated: no valid-SQL
  scalar Star FuncCall reaches EXPLAIN — Star arrives only via aggregate
  calls like `planner.go:8658` or this synthesis — so the new case fires
  solely on aggregate-derived objects). Sort-Key wrap yields PG's
  `(count(*))`; HAVING position stays bare per R65's established
  general qual-position rule (no corpus Star-HAVING oracle case exists;
  phrased as the rule, not as oracle fact).
- **Arm Distinct — synthesise `count(DISTINCT x)`.** Additive
  `Distinct bool` on `optimizer.FuncCall` (zero value false: every
  existing construction site keeps today's behaviour; the compiler, not
  grep, scopes the change per the grep-oversize memory) + render arm
  prints `name(DISTINCT args)`. Admit only R65-synthesisable calls (no
  Filter/OrderBy/WithinGroup, non-nil Arg). Synthesised calls are
  render-only objects, never stored back — R65's precedent, pinned by
  the same never-executes reasoning (constructor lives in the renderer;
  nothing outside `formatExprQual` receives them).

Slice 1 deliberately does NOT touch the Group Key arm (S18 pair moves
only via the Sort arm reading the same `GroupExprs[i]` object the Group
Key arm renders — no manufactured inconsistency), the correlated-SubPlan
`.col1` remainder, or Q10's FD-trim.

## 3. Falsifiable predictions (Slice 1)

| # | Claim | Mechanism (§1–2) | Verdict on miss |
|---|---|---|---|
| P0 | Q16 sheds `rendering` (all four Sort keys PG-text); Q13 key1 fixed (`(count(*)) DESC`, flag stays via key2 + Group Key) | Arms S + Distinct (Q16), Star (Q13) | surviving text gap on Q16 → STOP, re-audit (third gap) |
| P1 | rendering set {Q7,Q9,Q10,Q13,Q16} → {Q7,Q9,Q10,Q13}; pp 6/14/0/2 unmoved; ZERO new categories; ZERO EXTRA flips | fail-closed arms | any previously-matching query newly diverges, or any category rises → STOP |
| P2 | values 24/24 MATCH pre/post binary, all 22 TPC-H | renderer-only cut | ANY values move → STOP (render path shares state with exec) |
| P3 | explain A/B: ONLY Sort Key lines plus Star/Distinct HAVING-Filter lines move; Group Key/node/child lines byte-identical | Group Key arm untouched; shared `expandAggOutputRef` also serves the Aggregate HAVING-Filter arm, so e.g. `HAVING count(*) > 1` newly expands (PG-faithful) | any Group/node/child move, or a Filter move outside the pre-flight list → STOP |
| P4 | DS SF0.25 sweep PASS=96 SKIP=3 (2026-09-11 oracle) | renderer cannot move row counts | any mismatch → STOP |
| P5 | Q6 stays MATCH (nearest-match tripwire) | no agg-Sort on Q6 | Q6 moves → STOP |

Re-audit rule (R59–R65): any P-miss triggers DPTRACE A/B against the
pre-round binary, not celebration. Match count is NOT a criterion
(ROADMAP conjunction rule — no Slice-1 query can close).

## 4. Sibling audit

- `BareVarKeysUnchanged` pin: scan child → `childAggregateThroughFilters`
  nil → Arm S unreachable; keeps passing unmodified. R65's 4 pins keep
  passing unmodified (Star/Distinct were declines; the pins never
  construct them).
- `ExistsExpr`/`InExpr`/Array arms, `subPlanName`, correlated-SubPlan
  bare parens: untouched.
- InitPlan numbering: Arms S/Star/Distinct never introduce sublinks
  (sublink-bearing GroupExprs decline) — pin by test with a sublink in
  GROUP BY rendering unchanged.
- `TestExplainSortKeyPassThroughUnchanged` is Arm-S-REACHABLE (Sort over
  Aggregate, group-section key) and passes by single-table bareness
  coincidence, not unreachability — the REPORT must state the
  coincidence; the multi-table Q16-shape pin covers the divergence.
- Executor `FuncCall` eval + `planExprContentKey` (`exprwalk.go:714-721`
  keys `fn:Name/Star/Variadic`, NOT Distinct): survey call sites for the
  new `Distinct` field and state the content-key disposition explicitly
  in the REPORT (unreachable today — planner never sets it, synthesised
  objects never stored/keyed — but the compiler will never flag a
  conflation there, so the survey is the guard).

## 5. Gates (Slice-1 implementation, all FOREGROUND)

1. `go test ./internal/executor/ ./internal/optimizer/` green (new pins
   incl. Q16-shape, Q13-`count(*)`-shape, sublink-in-GROUP-BY-unchanged,
   alias-GroupExprs-no-op (Q7-shape: group-section key over alias
   GroupExprs renders byte-identical — guards Slice 1's zero-move on
   Q7/Q9/Q13-outer), Star/Distinct render pins; no `-count=1`);
   `go vet` clean.
2. `scripts/tpch-spotcheck.sh` PASS.
3. TPC-H values A/B vs pre-round binary, same clone+seed: 24/24 MATCH.
4. `tpch-runner -explain` A/B, normalised: Sort-Key + pre-flighted
   Star/Distinct HAVING-Filter moves only (P3). Pre-flight (before the
   cut): enumerate Star/Distinct HAVING-Filter lines in both corpora;
   each move adjudicated, anything outside the list trips P3.
5. pp vs fixtures AND vs fresh live-PG capture: 6/14/0/2, Q16 flag gone (P0–P1).
6. DS SF0.25 sweep foreground, private lane: PASS=96 SKIP=3 (P4).
7. `make plan-gate` triage: structural mode keeps key text — expect a
   Q16 verdict move; discharge by re-pin or R65-precedent opt-out with
   pre/post-identical rationale. No silent red.
8. `SLICE1-REPORT.md`, then review, then commit (explicit pathspec,
   `commit -n`) + push.

## 6. Slice 2 — Step 0 spec (NOT designed; measurement first)

**Question.** What stands between the outer agg's GroupExprs and source
on Q7/Q9 (E/I prove GROUP-BY-alias resolves through, so the culprit is
a shape those fixtures lack — K76's boundary Project is the candidate,
unmeasured), and what qualification does Q13-outer's inner-arg carry?

**Protocol (one of, cheapest first).** (i) Env-forced search-path unit
fixture reproducing alias GroupExprs (shapes E/I engage the default
path; force the search the way the corpus does and dump GroupExprs +
child chain + child Output schemas). (ii) Failing that, a temp
DPTRACE-style print (reverted before commit) of outer GroupExprs +
child chain + child outputs for Q7/Q9/Q13 on the TPC-H clone.

**Exit.** Per-query chase target named (`Project.Targets[j]` /
join-output / inner-Aggs[k]) with the PG one-level-stop rule, or a
principled decline with the reason. STEP0.md must name the search-path
knob the corpus engages (or record the negative if none separates E/I
fixtures from corpus shapes). No planner/executor change in Step
0. Deliverables: `STEP0.md` + the arms' scope. Family G and family T
land only on top of it.

## 7. Ledgered follow-ups (not this round)

Unchanged queue: (a) Materialize re-triage (R65-ordered precondition
met by R65 itself — Slice 1 doesn't touch it), #6 large-group stats,
R61 #4 CTE tagging, (b) Q4 cost-model framing, R63-#1/#2, R61 #5,
R62-#1, P0-04 suffix, correlated-SubPlan `.col1`, general ANY,
Q8 tie-break, §3 calibration ordering (still after the costAgg audit
per R55). Q10 FD-trim stays planner work.

Evidence live-only: `/tmp/pp2/r65/{r65mine.plans.txt,r65pg.plans.txt,
pp65-new-verbose.txt}` (repo-external; re-capture per R65 SCOPE §0
protocol if stale).
