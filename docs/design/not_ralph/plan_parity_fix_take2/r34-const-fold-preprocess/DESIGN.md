# R34 — parse-time coercion of unknown literals (K45 root cause)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Estimate axis. Settles K45's attribution, which R32/R33 left open.*

**Revision history.** v1 attributed this to a missing
`eval_const_expressions` on the planning path and proposed wiring
`FoldConstants` into `preprocess_expression`. Agent review returned
**REJECT** with three findings I verified and accepted; v2 is a
different fix in a different layer. v1's errors are kept in §6 rather
than deleted, because two of them were mine and are worth not
repeating.

## 1. The measurement

TPC-DS Q5's date bound on the fresh PG-faithful clone vs PG 18.3.
Actual matching rows: 15. Full ladder in `evidence-estimates.md`.

| predicate form on `date_dim.d_date` | goopg | PG |
|---|---|---|
| `between '2000-08-19' AND '2000-09-02'` (bare) | 13 | 14 |
| `between DATE '..' AND DATE '..'` (typed literal) | 13 | 14 |
| `between cast(..) AND '2000-09-02'` | 12233 | 14 |
| `between cast(..) AND cast(..)` | 8116 | 14 |
| `between cast(..) AND (cast(..) + INTERVAL '14 d')` (Q5) | 8116 | 14 |

TPC-H `lineitem.l_shipdate`: bare literal 5,916,028 vs PG 5,918,116
(0.04%); `cast(..)` or `date - interval` gives 2,000,418, which is
`6,001,215 / 3` exactly — the DEFAULT 0.3333 range selectivity. The
estimator is not degrading, it is not engaging.

## 2. Root cause

`selectivity.go:717 isConstExpr` admits `*IntegerConst`, `*StringConst`,
`*NumericConst`, `*BooleanConst`, `*TypedStringLit` — **not
`*CastExpr`**. goopg's parse analysis leaves `cast('2000-08-19' as
date)` as a `CastExpr` wrapping a `StringConst`, so the histogram is
never consulted.

PG never has this node. `parse_coerce.c:232-250`: when the input is
`UNKNOWNOID` and a `Const`, coercion applies the target type's typinput
**at parse time** and yields a typed `Const`. So PG's planner sees
`'2000-08-19'::date` as a constant, and its EXPLAIN prints exactly
that.

**This is a parse-analysis gap, not a planner one.** v1 sent it to
`preprocess_expression`; that was the wrong layer.

## 3. Change

In `resolveExpr`'s `*CastExpr` arm: when the operand is an untyped
string literal, produce a **`TypedStringLit{Type, Value}`** instead of a
`CastExpr`. This is `stringTypeDatum` transliterated, and goopg already
constructs exactly this node for the `DATE 'x'` spelling
(planner.go:8245, :14939).

Measured: the `DATE 'x'` form already estimates 13, identical to a bare
literal (§1 row 2). **No estimator change is needed** — the fold target
is a node `isConstExpr` and `formatExprConstant` already accept, and
`formatExprConstant` renders `TypedStringLit.Value` byte-equal to what
ANALYZE stamped.

### Why this is result-safe

It produces the node the `DATE 'x'` spelling already produces, for an
expression PG defines as parse-time-equivalent. It adds no evaluation
of a stable function at plan time and no new arithmetic. Contrast v1,
which proposed *evaluating* casts in the optimizer — see §6.2.

## 4. Scope, measured before implementation

| form | today | after this change | PG |
|---|---|---|---|
| `cast(..) AND cast(..)` | 8116 | **13** (fixed) | 14 |
| `cast(..) AND (cast(..) + INTERVAL)` (Q5) | 8116 | **12121** (still wrong) | 14 |
| `DATE '..' AND TIMESTAMP '..'` | **204** | 204 | 14 |

Folding one bound is not enough: a single unfolded bound loses the
histogram. So this round fixes the **cast-only** queries and not the
interval ones.

TPC-DS: 22 queries use `cast(`, 19 use `INTERVAL`, union 25. The 6
cast-only queries are Q18, Q49, Q54, Q61, Q75, Q90 — **but their casts
are `cast(x as decimal(N,M))` over COLUMN references in target lists,
not over literals in predicates**, so this change does not move their
scan estimates either. Corpus yield on the two benchmark suites is
therefore expected to be **small or zero**, and the round is honest
about that up front.

It is still worth landing: it removes a real PG divergence at the layer
PG puts it, it is a strict prerequisite for the interval work, and the
probe ladder (§1) is a direct, non-corpus verification that it worked.

## 5. Successor work (filed, not bundled)

- **K46 — `estimate_expression_value`.** The PG-faithful home for the
  interval case is `clauses.c:2379-2407`, which folds **stable as well
  as immutable** functions *for estimation only*, so the folded Const
  never enters the plan and cannot change results. This is a
  selectivity-path call, not a preprocess rewrite. Required because
  `date_in`/`timestamp_in`/`timestamptz_in` are `provolatile='s'`
  (verified against the live oracle) — any immutable-only guard folds
  nothing for dates.
- **K47 — type-aware histogram comparison.** `formatExprConstant`
  matches BYTE-EQUAL on rendered strings, so a folded `date + interval`
  (a TIMESTAMP rendering `'2000-09-02 00:00:00'`) cannot match a date
  histogram stamped `'2000-09-02'`. Measured: 204 vs PG's 14. K46 alone
  will not fix the interval queries without this.
- **K48 — pre-existing folder defects**, live today via
  `foldPlanConstants` (planner.go:2136) and NOT introduced here:
  numeric arithmetic routed through `float64`
  (`foldconst.go:562-586`) so `1.10 + 2.20` folds to `3.3` where PG
  gives `3.30`; no int4-width overflow check (`:513-555`) so
  `2000000000 + 2000000000` folds instead of raising `22003`;
  string ordering by byte-wise Go `<` (`:640-645`) ignoring collation.
  Each is result-affecting and wants `pg-oracle-diff` cases.

## 6. What v1 got wrong (kept deliberately)

1. **"`FoldConstants` is dead code."** False, and my own error: I
   grepped for callers while excluding `foldconst.go`, which is where
   the wrapper lives. `foldPlanConstants` runs at **planner.go:2136**,
   on every `planSelect`. The true statement is narrower and is not a
   licence to rewire anything: goopg folds **after the plan is chosen**,
   so path search and selectivity never see folded quals — only the
   *timing* differs from PG, not the set of expressions folded.
2. **The proposed guard was self-defeating.** v1 said to fold "when the
   target type's input conversion is immutable". `date_in`,
   `timestamp_in`, `timestamptz_in` are all STABLE, so a faithful
   implementation would have folded nothing for the entire target
   corpus — and implementing it anyway would have diverged from PG.
3. **Wrong layer.** PG's Q5 constant comes from the parser
   (`stringTypeDatum`), not from `eval_const_expressions`.
4. Minor: v1 attributed planner.c's "don't want to do this before
   eval_const_expressions" comment to `canonicalize_qual`; it belongs to
   `make_ands_implicit` (:1346). The *ordering* claim was right.

## 7. Gates

Suites; TPC-H values digest byte-identical; SF0.5 sweep all-zero;
parity both corpora on the fresh clone, pinned seed, both sides
re-ANALYZEd. Plus the §1 probe ladder re-run as direct verification,
since the corpus gates are not expected to move.

Note on gate power (review finding): the values digest is a weak
detector for folding semantics, because the folder already runs on
every plan today, so the digest encodes any existing fold bug as the
baseline. That is an argument about K48, which this round does not
touch; this round's change introduces no arithmetic and no stable-
function evaluation.

## 8. Prediction

- The §1 ladder's `cast(..) AND cast(..)` row moves 8116 -> ~13.
- Corpus parity: **no movement predicted** on either suite, for the
  reason in §4. Reported as such; a round whose honest yield is a
  removed divergence plus a prerequisite is still a round.
