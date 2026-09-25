# R34 results — parse-time coercion of unknown literals

Implements `DESIGN.md` v2. One behaviour change, in
`internal/optimizer/planner.go`: `resolveExpr`'s `*parser.CastExpr` arm
resolves `cast('<string literal>' as T)` to a `TypedStringLit` when T is
in the DATE/TIME family, instead of emitting a runtime `CastExpr`.
Transliterates `parse_coerce.c:232-250`.

## 1. The mechanism is fixed (isolated verification)

`date_dim.d_date`, actual matching rows 15, PG estimates 14:

| predicate form | before | **after** | PG |
|---|---|---|---|
| bare literals | 13 | 13 | 14 |
| `cast(..) AND '2000-09-02'` | 12233 | **13** | 14 |
| `cast(..) AND cast(..)` | 8116 | **13** | 14 |
| `cast(..) AND (cast(..) + INTERVAL)` | 8116 | 12121 | 14 |

Cast bounds now reach the histogram. Row 4 is unchanged in kind and was
predicted to be (DESIGN §4): folding one bound is not enough.

## 2. Corpus effect — and it is not an improvement in the numbers

18 TPC-DS plans changed (Q5 Q12 Q16 Q20 Q21 Q32 Q36 Q37 Q40 Q70 Q77 Q80
Q82 Q86 Q92 Q94 Q95 Q98; Q36/Q70/Q86 are the known nondeterministic
dsqgen artefacts, so ~15 are real).

**Every one of them uses the interval form**, so their `date_dim`
estimate moves from one wrong value to another:

| query | before | after | PG |
|---|---|---|---|
| Q5 | 8116 | **12121** | 14 |
| Q77 | 8116 | **12124** | 14 |
| Q80 | 8116 | **11883** | 14 |
| Q98 | 8116 | **12315** | 14 |

That is **numerically further from PG** (580x wrong becomes 866x
wrong), because the lower bound now consults the histogram while the
upper bound still takes the default, and the two combine worse than two
defaults did. This is stated rather than glossed: the round improves
the mechanism and, on this corpus, degrades the number until K46/K47
land.

It is shipped anyway, for reasons that are about the goal's terms
rather than the metric: PG has no cast node here at all, so keeping one
is a divergence in the planning logic itself; the change is a strict
prerequisite for the interval work; and it causes no correctness or
runtime harm (§3). Restoring the old number would mean re-introducing a
node PG does not build, to make a wrong estimate coincidentally less
wrong.

## 3. Parity: no movement, as predicted

| | TPC-DS base | TPC-DS R34 | TPC-H base | TPC-H R34 |
|---|---|---|---|---|
| match | 0 | 0 | 1 | 1 |
| join-order | 95 | 95 | 20 | 20 |
| every other category | — | identical | — | identical |

Not a single category moved on either corpus. DESIGN §8 predicted
exactly this.

## 4. The finding that outranks the change

**This is the third input-correctness fix in a row that moved no
parity category, and the second to move nothing at all:**

| round | input corrected | magnitude | parity effect |
|---|---|---|---|
| R30 | column correlation | 0.07 -> 1.0 (was ~14x wrong) | scan-type −1, join-method +2 |
| R31b | fact-table `relpages` | 15% inflated | qual-placement −1 |
| **R34** | **range cardinality** | **580x wrong on 15 queries** | **none** |

`join-order` has sat at exactly 95/99 (TPC-DS) and 20/22 (TPC-H)
through all three. A 580x cardinality error on 15 queries did not
perturb it by one.

**Conclusion: goopg's join-order divergence is not estimate-driven.**
K26 has been carried since the start of this workstream on the implicit
premise that better cardinalities would move join order. Three
controlled experiments now contradict it. The next join-order round
should target the SEARCH — enumeration order, `add_path` dominance,
which candidates are generated at all (the `planner_verify_both_candidates_generated`
lesson) — and should not spend further effort on estimate inputs
expecting join order to follow.

## 5. Gates

- `go test ./internal/optimizer/ ./internal/executor/ -count=1` — ok.
- `RALPH_PRECOMMIT_SCOPE=units` — 44 packages ok, zero FAIL.
- TPC-H values digest — **byte-identical** to the r9 reference.
- TPC-DS SF0.5 sweep — `PASS=95 (57 ck-verified) MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4`; status-delta
  `verdict-changes=none`, total runtime **-5.0%**.

## 6. Scope kept narrow, deliberately

Only DATE/TIME targets fold. Numeric and integer targets are excluded
even though `evalTypedStringLit` accepts them, because upstream's own
comment on the code being transliterated warns that a type's INPUT
function differs from its CONVERSION function — `int4`'s typinput
rejects `'1.2'` where float-to-int rounds — so folding those would
change which behaviour a query gets. Widening the set needs its own
oracle cases.

## 7. Successors (unchanged from DESIGN §5)

K46 `estimate_expression_value` (folds STABLE for estimation only —
`date_in` is `provolatile='s'`, so no immutable-only fold can ever
touch a date), K47 type-aware histogram comparison (a folded
`date + interval` renders as a timestamp and cannot byte-match a date
histogram — measured 204 vs PG's 14), K48 pre-existing folder defects.
