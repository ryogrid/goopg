# R44 step A — fold quals where PG folds them (K83/K85/K89)

*2026-09-10. Step A only; step B (the temporal domain) is NOT in this
change and is scoped in §5. All gates green.*

## 1. Result

| | before | after |
|---|---|---|
| **TPC-DS `join-method`** | 71 | **69** |
| **TPC-DS `parameterisation`** | 39 | **37** |
| **TPC-DS `aggregation-strategy`** | 84 | **82** |
| TPC-DS join-order / scan-type / sort-strategy / parallelism / qual-placement / rendering | 95 / 73 / 83 / 88 / 12 / 32 | unchanged |
| TPC-DS plans changed | — | **31** |
| TPC-DS total runtime | 811 s | **771 s** (−4.9 %) |
| TPC-DS match | 0/99 | 0/99 |
| TPC-H everything | — | **byte-identical** |

**Net −6 parity categories on TPC-DS**, three queries substantially faster
(Q38 12 s → 2 s, Q87 11 s → 2 s, Q99 5 s → 1 s), and no answer changed
anywhere.

The match count did not move, as `DESIGN.md` §7 predicted.

## 2. What landed

`foldQualConstants` — a single-expression entry point applying the existing
`FoldConstants` to a resolved qual **at the point PG's
`preprocess_expression` runs `eval_const_expressions`**, i.e. before the
clause reaches the estimator. Wired at the three qual sites:

- the WHERE arm's single-relation chooser,
- the WHERE arm's join path,
- `planJoinPredicate`'s ON clause — **required**, because ON-clause quals
  reach the estimator via `chainOnQual`/`joinsearchseam` and bypass the
  WHERE site entirely (K86). Without it the round would be WHERE-only.

It converts a folding evaluation error into a `*PlanError` through the same
`foldEvalPanic` mechanism `foldPlanConstants` uses. The late
`foldPlanConstants` call is untouched — it also folds target lists and
projections, which is out of scope here.

## 3. A correction I have to make (K89)

**I reported "step A changed nothing" after measuring TPC-H alone. That was
wrong.** TPC-H plans really are byte-identical — but TPC-DS moves **31
plans** and −6 categories. I generalised from one corpus to both.

This also settles the review's K85 in the review's favour. The design's rev
1 called step A "inert without the temporal arm"; review said the converse
was false because plain numeric arithmetic already folds. TPC-H made that
look wrong; TPC-DS shows it was right. **Two corpora, two different
answers — measure both before characterising a change.**

The visible mechanism is the one review named: a qual like
`l_discount BETWEEN 0.05 - 0.01 AND 0.05 + 0.01` fails `isConstExpr`
before the fold and takes `defaultIneqSelectivity`; after it, the folded
literal is estimated properly. TPC-H Q6 has exactly that shape and did not
move; many TPC-DS queries have it and did.

## 4. K88 confirmed in the wild: the fold is float64, not PG `numeric`

The rendered Q6 filter now reads

```
(l_discount >= 0.04) AND (l_discount <= 0.060000000000000005)
```

`0.05 + 0.01` folds through `strconv.ParseFloat` → float64 → `FormatFloat`,
so it produces `0.060000000000000005` where PG produces `0.06`. This is a
**pre-existing** defect of `evalArith` (it already rendered this way via
the late `foldPlanConstants`), but step A now feeds that string to the
*estimator* as well as to EXPLAIN.

It is answer-safe here only because the perturbation *loosens* an upper
bound. A fold that tightened one would drop rows. **Filed as its own
defect: `evalArith`'s numeric path must use exact `numeric`, not float64.**
It is also a live `rendering`-category divergence from PG.

## 5. What step B still needs — unchanged and non-trivial

Step B (fold `date + interval`, the 11.9× Q14 error K83 measured) is NOT in
this change, and the design's §5a blockers all stand:

1. **No volatility route.** No `provolatile` index is reachable from the
   optimizer; `initdb`'s seed data has one but cannot be imported (cycle).
2. **No optimizer-side temporal evaluator.** Confirmed while implementing:
   the leaf `internal/utils/adt/datetime` package has formatting,
   normalisation and validation but **no date+interval arithmetic** — that
   lives in `executor/expr.go`'s `addDateTimeInt`, and the executor imports
   the optimizer in ~91 files so the dependency cannot be reversed. Step B
   needs that arithmetic extracted into the leaf package, shared by both.
   Month arithmetic is answer-changing if it diverges (K88).
3. **The folded literal's spelling is still undecided** — timestamp (PG
   text parity, but fails `numericValue`'s date arm so `bucketFraction`
   returns a flat 0.5) versus date (histogram parity, diverges from PG's
   rendered `::timestamp`).

## 6. Gates

| gate | result |
|---|---|
| optimizer + executor suites | green |
| **TPC-DS SF0.5 sweep** | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0** |
| TPC-DS verdicts + row counts vs baseline | identical for all 99 |
| TPC-DS plan shape | 31 changed, −6 parity categories, −4.9 % runtime |
| TPC-H values, 27 query files | byte-identical (A/B against a pre-change binary) |
| TPC-H plan text | byte-identical |

The TPC-H A/B was run against a purpose-built pre-change binary rather than
a stored baseline, because the previous session's `/tmp` captures were
cleared — see §7.

## 7. Operational note

`/tmp/pp2` (private bench clone, all captures) did not survive the session
boundary. The git-tracked captures under `r0-baseline/` and
`r2-instrument/` did, and were used as the PG reference. **Anything needed
across sessions must live in the repo**, not `/tmp`.

One format trap worth recording: the sweep writes sections as
`===== Qn =====` while `pg-plan-parity-diff.py` expects `=== Qn`. Feeding
it the sweep file directly yields `queries=0 match=0` — which reads exactly
like a clean run rather than a parse failure. Convert first.
