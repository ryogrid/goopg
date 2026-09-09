# R44 — `date + interval` is never folded, so its selectivity is 1.0 (K83)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Status: DESIGN **rev 2** — reviewed; four corrections adopted, two rev-1
claims REFUTED (§3, §5b). First round aimed at ESTIMATE parity rather than
shape parity.*

## 1. The measurement

Two forms of the same TPC-H Q14 restriction, measured on the live goopg
TPC-H cluster at HEAD:

| filter | goopg `lineitem` estimate |
|---|---|
| `l_shipdate >= DATE '1995-09-01' AND l_shipdate < DATE '1995-10-01'` | **rows=78,680** |
| `l_shipdate >= DATE '1995-09-01' AND l_shipdate < DATE '1995-09-01' + INTERVAL '1 month'` | **rows=938,645** |

938,645 is exactly the parallel-divided full table: **selectivity 1.0 —
the second conjunct is contributing nothing.** An **11.9× estimate error**
caused purely by the unfolded `date + interval`.

TPC-H Q14 uses the second form, which is why its `Parallel Seq Scan on
lineitem` reads `rows=938,645` against PG's `rows=18,444`.

## 2. Why PG does not have this problem

PG folds the expression in `preprocess_expression` →
`eval_const_expressions`, **before** the qual reaches the estimator. Its
plan therefore renders the already-folded constant:

```
PG     Filter: (… AND l_shipdate < '1995-10-01 00:00:00'::timestamp without time zone)
goopg  Filter: (… AND l_shipdate < ('1995-09-01'::date + '1 month'::interval))
```

The fold is licensed because `date + interval` is `date_pl_interval`,
`provolatile = 'i'` — **verified on the live 18.3 oracle**, not assumed.

## 3. Why goopg does not fold it — corrected by review (rev 2)

**Rev 1 said arithmetic folding was absent. That was WRONG.**
`tryFoldBinaryOp` does fold arithmetic, concatenation and comparison, via
`toLiteralValue` → `evalLiteralBinary` → `evalArith`
(`foldconst.go:467,484,513`). The AND/OR arms rev 1 saw are the
*short-circuit* cases, not the whole function.

The real gap is a **type-domain** gap, not a missing operator:
`toLiteralValue` accepts `*IntegerConst`, `*StringConst`, `*NumericConst`,
`*BooleanConst` — and **not** `*TypedStringLit` or `*IntervalLit`, which
are exactly what `DATE '…'` and `INTERVAL '…'` resolve to. `FoldConstants`
lists both under "literals: return unchanged" (`foldconst.go:27-29`), so
they are deliberately excluded. So the fix is not "add an arithmetic arm";
it is **teach the folder the temporal domain**, which is a materially
bigger change because it needs a real evaluator (§5a).

The second reason stands and is confirmed: `foldPlanConstants` has exactly
ONE production call site, `planner.go:2197`, at the END of `planSelect`,
after plan selection — so the estimator never sees a folded qual.

**But rev 1's "inert" framing is also wrong, in the converse direction,
and this changes the round's shape.** Because arithmetic folding *already
works* for int/numeric, **moving the fold earlier changes estimates
immediately**, on every query with constant arithmetic in a qual — with or
without the temporal arm. TPC-H **Q6 is exactly such a query**
(`l_discount BETWEEN 0.05 - 0.01 AND 0.05 + 0.01`): today those `BinaryOp`s
fail `isConstExpr` (`selectivity.go:717`) and fall to
`defaultIneqSelectivity`; after the move they would fold and be estimated
properly.

So this is **two independently measurable steps**, and the design must land
them as such:

- **step A — move the fold before the estimator.** Changes estimates on
  queries with constant arithmetic. Q6 is in scope.
- **step B — add the temporal domain** (`TypedStringLit`/`IntervalLit`).
  Changes the 7 `date + interval` sites.

Landing them together would make it impossible to attribute any movement.

## 4. Scale

goopg leaves `date + interval` unfolded in **7 filter positions** across
TPC-H:

| expression | occurrences |
|---|---|
| `('1994-01-01'::date + '1 year'::interval)` | 4 |
| `('1993-07-01'::date + '3 month'::interval)` | 1 |
| `('1993-10-01'::date + '3 month'::interval)` | 1 |
| `('1995-09-01'::date + '1 month'::interval)` | 1 |

Every one is a range restriction on a large fact table, so each is an
estimate error of the same class as Q14's 11.9×. TPC-DS uses the same
`date_dim`-driven idiom heavily and should be re-measured once TPC-H is
clean.

Per query (git-tracked `r2-instrument` captures, since PG folds at
preprocess the PG side shows **0** occurrences of this form anywhere):

**Q4, Q5, Q6, Q10, Q12, Q14, Q20** — 7 queries, 7 occurrences.

**Q6 is on that list, and Q6 is one of goopg's only two MATCHes.** That is
the round's single biggest risk and it is not hypothetical: folding will
change Q6's restriction estimate, and Q6 currently *under*-estimates
(goopg 2,412 vs PG 28,092 — 11.6× the other way, measured). A shape change
there costs a match the workstream already has. Q6 must be checked FIRST
after implementing, before any corpus-wide reading.

Measured goopg-vs-PG fact-table scan estimates on the affected set:

| query | goopg | PG | note |
|---|---|---|---|
| Q4 | 385,423 | 14,974 | only the foldable conjunct applied |
| Q12 | 1,500,000 | 7,006 | — |
| Q14 | 938,645 | 18,444 | selectivity 1.0 |
| Q6 | 2,412 | 28,092 | **under**-estimates; currently a MATCH |

(Q1's 2,000,418 vs 1,479,529 is a different cause — Q1 carries no
`date + interval` — and is out of scope.)

## 5. The fix — site corrected by review

**Step A: run the fold on the RESOLVED `Expr`, at/after `resolveExpr` —
NOT beside `canonicalizeQual`.** Rev 1 proposed the latter; review showed
it would cover **WHERE restrictions only**. ON-clause join quals reach the
estimator by a different route (`planJoinPredicate` → `chainOnQual` →
`joinsearchseam.go`), bypassing that site entirely. Four reasons the
resolved side is correct:

1. every estimator entry point is typed on resolved `Expr`
   (`clauseSelectivity(expr Expr, child Node)`, `conjunctionSelectivity`,
   `isConstExpr`);
2. `resolveExpr` is the **single choke point every qual passes** — WHERE,
   ON, HAVING, USING — so ON-clause quals are covered for free;
3. the volatility/type lookup needs resolved types; on `parser.Expr` it
   would mean re-running analysis, a layering violation;
4. §10.3's deparse worry dissolves: `resolveExpr` builds a *fresh* tree, so
   the parse tree the view/rule deparsers share is untouched by
   construction.

The existing late `foldPlanConstants` call stays for target lists.

**Step B: teach the folder the temporal domain** — accept
`*TypedStringLit`/`*IntervalLit` in `toLiteralValue`, gated on the
operator's function being IMMUTABLE.

## 5a. Three pieces the design assumed existed and do NOT (review)

1. **There is no volatility route.** `catalog.IsStrictProc` is backed by a
   generated `pgProcIsStrictByOID` with **no volatility index**, and there
   is no `IsImmutableProc`. `catalog.BuiltinProc.Volatile` covers only the
   three pg_dump fixture entries (`date_pl_interval` is not among them),
   and `catalog.Routines.Volatile` is user-defined functions only.
   `internal/initdb/pg_proc_seed_data.go` *does* carry `Volatile:` per row
   — but the optimizer **cannot import initdb** (initdb → executor →
   optimizer cycle; the "version constants must live in a leaf config pkg"
   precedent applies verbatim). **Must add:** generate
   `pgProcVolatileByOID` from the same `pg_proc.dat` source (absent ⇒
   `'i'`, per `BKI_DEFAULT(i)`) and expose
   `catalog.IsImmutableProc(oid)`. The operator→proc hop already exists
   via `LookupOperatorForNode(...).Code`.
2. **There is no optimizer-side temporal evaluator, and the executor's
   cannot be reused.** `internal/executor` imports `internal/optimizer` in
   ~91 files, so the import can never go the other way. `date_pl_interval`
   semantics (`evalIntervalLit`, `addDateTimeInt`) live in
   `executor/expr.go`. R44 must therefore either host the month/day/micro
   arithmetic in a **leaf package both can import**, or duplicate it and
   pin the duplication with a test. Duplicating silently is the repo's
   known "sibling code paths must stay in sync" failure mode.
3. **The folded literal's SPELLING is an unresolved trade-off that decides
   whether §7's number is even reachable.** `date + interval` types as
   `timestamp`, so a PG-faithful fold yields `'1995-10-01 00:00:00'`. But
   the column is `date`, and `numericValue`'s `"date"` arm
   (`selectivity.go:512-517`) accepts **only** `"2006-01-02"`. A timestamp
   spelling fails to parse, so `bucketFraction` returns a flat **0.5** and
   the estimate lands within half a bucket rather than on target — bucket
   *selection* still works because ISO-8601 sorts lexically. Two options,
   and the design must pick one:
   - fold to `timestamp` (PG text parity) **and widen `numericValue`'s
     date arm** to accept timestamp spellings; or
   - fold to a `date`-spelled literal (histogram parity) and accept
     divergence from PG's rendered `::timestamp` text.

## 5b. Folding CAN change answers — rev 1's claim REFUTED

Rev 1's §6 implied folding is answer-safe. It is not, and there is a live
demonstration at HEAD: `evalArith`'s numeric path is **float64-based**
(`strconv.ParseFloat` + `FormatFloat(…, -1, 64)`), so `0.05 + 0.01` folds
to `0.060000000000000005`, not `0.06`. Q6 survives only because that
perturbation *loosens* an upper bound; a fold that tightened one would drop
rows. Any optimizer-side temporal arithmetic that diverges from the
executor's `evalIntervalLit` has the same exposure.

This is an answer-changing surface, not merely an estimate surface, and it
is why the TPC-H values digest and the sweep are non-negotiable gates here
— and why the numeric fold's PG-fidelity (PG uses exact `numeric`, not
float64) should be filed as its own defect.

## 6. What must NOT change

- **STABLE and VOLATILE functions must not fold.** PG's
  `eval_const_expressions` folds immutable functions outright; stable ones
  it folds only in a plan-time context that goopg does not model here.
  R34 already hit this: `date_in`/`timestamp_in`/`timestamptz_in` are all
  `provolatile='s'`, and an immutable-only guard folded *nothing* for its
  target corpus. This round's operands are different (`date_pl_interval`
  is immutable), but the guard must still be volatility-driven, and R34's
  finding is why it must be *checked* rather than assumed for each
  operator.
- Division-by-zero and overflow behaviour: `foldPlanConstants` already
  converts a folding evaluation error into a `*PlanError` via
  `foldEvalPanic`, matching PG raising at plan time. The new arm must
  route through the same mechanism, not panic.

## 7. Prediction, recorded before implementing (METHODOLOGY §2)

- TPC-H Q14's `lineitem` estimate should improve markedly from 938,645.
  **Rev 1 predicted ~78,680; rev 2 retracts that number**: unless
  §5a.3's date-arm widening lands too, the timestamp-spelled literal fails
  `numericValue` and `bucketFraction` returns a flat 0.5, so the estimate
  lands within half a histogram bucket rather than on target.
- **This will change plan SHAPES**, because 7 restrictions across the
  corpus stop being no-ops and the joins above them get real cardinalities.
  Direction is expected to be toward PG (PG plans from the folded form),
  but that is a prediction to measure, not assume.
- **Match count: not predicted to move by itself.** Q14 would still differ
  on `parallelism` (R43). Q6 and Q13 currently match and MUST NOT
  regress — they are the only two matches goopg has, so they are this
  round's most sensitive pins.
- Estimate parity is the actual target here, and the parity differ's N1
  normalisation **cannot see it** (estimates are pushed to a side column).
  So this round's success is NOT measurable by the match count, and must
  be judged by direct estimate comparison against PG per query.

## 8. Gates

Standard bar plus one addition: because the metric this round targets is
invisible to the differ, the report must include a **per-query goopg-vs-PG
row-estimate table** for the affected restrictions, not just category
counts.

**Q6 is a named gate item, captured before/after, not left to the corpus
sweep.** It is one of only two matches goopg has, it carries BOTH a
`date + interval` site (step B) and `0.05 ± 0.01` constant arithmetic
(step A), so it is in the blast radius of each half independently. Its
plan and values must be pinned explicitly.

Steps A and B are landed and measured SEPARATELY (§3), or movement cannot
be attributed.

Suites + `RALPH_PRECOMMIT_SCOPE=units`; TPC-H values digest byte-identical;
TPC-H plan structure diffed and every change explained; TPC-DS SF0.5 sweep
`PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`; parity re-measured on
both corpora; `shape-delta.sh`. Held uncommitted until the sweep is clean.

## 9. Why this round exists at all

Five rounds this session closed shape categories without moving the match
count, and the blockers kept terminating in estimate-side work (K65 →
`minimize_datum`, K68 → B-06 CTE stats, and now Q14's selectivity). The
goal asks for plans identical "as a result of the same statistics and the
same cost computation" — a shape-only match does not satisfy that. This is
the first round to attack the estimate side directly, and an 11.9× error
from a missing constant fold is the cheapest such defect found so far.

## 10. Review record

Adversarial subagent review, full HEAD re-derivation. **Verified:**
`foldPlanConstants`' single call site at `planner.go:2197` after
estimation; the `foldEvalPanic` → `*PlanError` mechanism; `canonicalizeQual`'s
single call site and its deparser warning; `date + interval` typing as
timestamp; and that nothing downstream depends on the unfolded text
*provided the fold runs on the resolved tree*.

**Refuted or corrected — four changes, all adopted:**

1. §3.1's "arithmetic folding is absent" — **wrong**; it exists via
   `toLiteralValue`/`evalArith`. The gap is the temporal *type domain*.
2. §3.2's "inert" framing — the **converse is false**: moving the fold
   earlier changes estimates on its own, Q6 included. Split into steps A
   and B.
3. §5.2's site — beside `canonicalizeQual` would be **WHERE-only**;
   ON-clause quals bypass it. Move to the resolved `Expr` at `resolveExpr`.
4. Three assumed-existing pieces do not exist (§5a): no `provolatile`
   index, no optimizer-side temporal evaluator (executor is un-importable),
   and an unresolved folded-literal spelling trade-off that gates §7's
   number.

Plus §5b: folding **can** change answers — `0.05 + 0.01` →
`0.060000000000000005` under the float64 numeric path, demonstrable at
HEAD.

Review also flagged R34's `TypedStringLit` coercion in `resolveExpr`'s
`CastExpr` arm as the shipped precedent to mirror for the node type and
site — which is the same conclusion §5's step A reaches independently.

## 11. Original open questions (answered by the review above)

1. Is `canonicalizeQual`'s call site really before every consumer that
   estimates? The WHERE arm is one entry; ON-clause quals reach the
   estimator through `planFromItem`/`chainOnQual`. If those bypass the
   proposed fold, the round fixes WHERE restrictions only and the design
   should say so rather than over-claim.
2. Should the fold operate on `parser.Expr` (pre-resolution, beside
   `canonicalizeQual`) or on the resolved `Expr` (post-`resolveExpr`,
   pre-search)? The former matches PG's staging; the latter has types
   available for the catalog volatility lookup. Reviewer: which does
   goopg's structure actually support without a layering violation?
3. Does anything downstream rely on the UNFOLDED text — view/rule
   deparsing in particular, which `canonicalizeQual`'s own comment warns
   "must keep rendering the query as written"?
