# R40 — complete the LEFT→ANTI transplant (K69 / `outer-link-no-sjinfo`)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`. Status:
DESIGN, pre-review. Completes the sequence K28→K29→K30 left open at R27
§4a: "Completing the ANTI conversion (drop the forcing qual) is the
prerequisite for transplanting it."*

## 1. The problem, instrumented (K69)

Three of TPC-DS Q78's seam declines are `outer-link-no-sjinfo`
(`joinsearchseam.go:477`, `outerLinksHaveSJInfos`). Each of Q78's three
CTE bodies (`ws`, `cs`, `ss`) has the shape

```sql
<channel>_sales LEFT JOIN <channel>_returns
    ON  wr_order_number = ws_order_number AND ws_item_sk = wr_item_sk
  JOIN date_dim ON ws_sold_date_sk = d_date_sk
 WHERE wr_order_number IS NULL AND d_year = 2000
```

Instrumenting `outerLinksHaveSJInfos` directly (temporary trace, not
inference — the standing rule from R37) on all three occurrences gives
the identical mismatch each time:

```
DPTRACE SJITRACE want jt=1 preserved=1 nullable=2 against list=1 entries:
DPTRACE   have jt=6 syn.L=1 syn.R=2
```

`jt=1`/`JoinLeft` is what the PLAN tree carries for this link; `jt=6`/
`JoinAnti` is what `ctx.joinInfoList`'s `SpecialJoinInfo` carries for the
SAME relids (`internal/parser/ast.go:727-734`). This is exactly K30's
deferral, now root-caused to a specific decline class: `reduceOuterJoins`
(the REAL, un-throwaway call at `planner.go:2900`, which feeds
`ctx.joinInfoList` via `deconstructJointreeScopedSJI`) demotes this LEFT
join to ANTI via S9.3 (`reduce_outer_joins.go:210-224`, forced-null
`wr_order_number` + strict ON position). `demotedForPlan`
(`planner.go:2853`) computes the identical verdict on its own throwaway
copy but — by K30's explicit, correct-at-the-time decision — transplants
only the INNER half of `applyDemotion`'s verdicts onto the plan, leaving
the plan-tree link at LEFT. The two consumers disagree on Jointype,
`outerLinksHaveSJInfos` fails closed, and the whole CTE-body join search
declines — not just loses an optimization, but falls to the legacy
planner and cannot converge on PG's plan by any costing work (K27).

## 2. Verified against the oracle: this is a real, currently-unrealized optimization

`web_sales LEFT JOIN web_returns ON … WHERE wr_order_number IS NULL` on
the live PG 18.3 TPC-DS SF0.5 oracle (`:65438`) plans as a
`Merge Anti Join` (or `Hash Anti Join`, join-method dependent) with the
`wr_order_number IS NULL` qual **dropped from the rendered plan
entirely** — not left as a residual filter. Row counts confirmed
identical (323532 both ways) between the LEFT+`IS NULL` form and an
explicit `NOT EXISTS` anti-join rewrite on the oracle, so this is not a
cosmetic PG rendering choice, it is PG's `reduce_outer_joins_pass2`
doing exactly what `applyDemotion`'s S9.3 rule already transliterates —
goopg's analysis is right; only the transplant is incomplete.

## 3. Why a bare Jointype transplant is unsafe (confirms K30, structurally)

`Join.Output()` (`plan.go:1205`) special-cases `JoinTypeSemi`/
`JoinTypeAnti`: it returns `Left.Output()` only — the nullable/inner
side's columns are not in the row at all once a join becomes ANTI.
`joinPublishesInner` (`joinrelsize.go:136-142`) states the same rule for
sizing. If `demotedForPlan` transplanted `parser.JoinAnti` onto the plan
tree today, without also removing `wr_order_number IS NULL` from
`s.Where`, the later WHERE-Filter build (`planner.go:1452`) would try to
resolve `wr_order_number` against a row that no longer carries a
`web_returns` binding for it — at best a resolve failure, at worst (if a
stale binding survives) a read past the anti join's actual output. K30's
one-line reason — "PG also drops the forcing IS NULL qual" — is the
literal fix requirement, not a simplification.

There is a second, independent gap: `mapJoinType`
(`planner.go:6586-6598`) has no case for `parser.JoinAnti`/
`parser.JoinSemi` — both silently fall to `default: return
JoinTypeInner`. Transplanting the parser-level verdict alone, even with
the qual dropped, would still plan an INNER join today. Both gaps must
close together or the fix is inert (Anti in, Inner out) or wrong
(Anti in, Anti out, undropped qual).

## 4. The fix

Three coordinated changes, each independently gated:

### 4a. `demotedForPlan` transplants ANTI, and reports which tables

`demotedForPlan` (`reduce_outer_joins.go:89`) currently returns only the
rewritten `parser.FromExpr`. Extend it to also return the set of
right-side table names it demoted to ANTI in this item (empty for the
common case). The ANTI branch of the transplant loop — currently `if
got != parser.JoinInner { continue }` — gains a second arm:

```go
if got == parser.JoinAnti {
    out.Joins[i].Type = parser.JoinAnti
    if antiTables == nil {
        antiTables = make(map[string]bool)
    }
    antiTables[rangeVarPrimaryName(out.Joins[i].Right)] = true
    continue
}
if got != parser.JoinInner {
    continue
}
out.Joins[i].Type = parser.JoinInner
```

INNER's existing transplant is untouched — this is additive, not a
rewrite of the safe case R27 already shipped.

### 4b. `mapJoinType` learns ANTI

```go
case parser.JoinAnti:
    return JoinTypeAnti
```

`parser.JoinSemi` is NOT added: `applyDemotion` never produces a Semi
verdict (grep-confirmed, no `JoinSemi` assignment anywhere in
`reduce_outer_joins.go`), so mapping it would be speculative code with
no caller — exactly the class of unverified addition the goal's "no
arbitrary changes" instruction rules out. Left as a pre-existing,
unrelated gap (default-to-Inner is silently wrong for a Semi verdict
too, but nothing produces one today).

### 4c. Drop the forcing qual from WHERE before it becomes a Filter

`planFromClause` accumulates `antiTables` across all `s.FromExprs` items
(4a's per-item set, unioned) and stashes it on the `resolveContext` it
returns, in a new field:

```go
// antiForcedNullTables: the table names demotedForPlan (4a) converted
// LEFT->ANTI in this statement. The WHERE-clause IS NULL conjunct that
// forced each conversion must not reach resolveExpr — the ANTI join's
// Output() (plan.go:1205) no longer carries that table's columns, and
// PG's own reduce_outer_joins drops the identical conjunct for the
// identical reason. Nil in every context that is not a top-level FROM
// clause, same convention as joinlist/joinInfoList. R40/K69.
antiForcedNullTables map[string]bool
```

Before the join-arm WHERE code (`planner.go:1357` on) calls
`canonicalizeQual(s.Where)`, a new `stripForcingNullQuals` (mirrors
`collectForcedNullWalk`'s top-level-AND-only walk, so it stays exactly
in step with the analysis that decided to strip in the first place) is
applied when `ctx.antiForcedNullTables` is non-empty:

```go
effectiveWhere := s.Where
if len(ctx.antiForcedNullTables) > 0 {
    effectiveWhere = stripForcingNullQuals(s.Where, ctx.antiForcedNullTables,
        buildTableMap(s.FromExprs, cat), cat)
}
```

`stripForcingNullQuals` walks top-level AND only (identical descent
rule to `collectForcedNullWalk`, so it can only drop a conjunct that
function already certified as the forcing one — no new classification
logic, just the mirror-image action) and elides any `IsNullExpr`
(non-negated) whose operand resolves to a table in the set. `s.Where`
itself is never mutated (same discipline `canonicalizeQual` already
uses one line below it — the parse tree is shared with view/rule
deparsers). Every remaining use of `s.Where` in this arm (the
`exprHasAggregate` validity check, `s.Where.Pos()` for node
positioning) is unaffected and stays on the original; only the value
fed to `canonicalizeQual`/`resolveExpr` changes.

`effectiveWhere` can become `nil` (a WHERE clause that was ONLY the
forcing `IS NULL` test) — the join-arm currently builds
`Filter{Predicate: pred}` unconditionally at `planner.go:1452`; this
gains the same `pred == nil → no Filter node` guard the single-relation
arm already has for its `restriction_is_always_true` case, rather than
inventing a new empty-predicate convention.

### 4d. Narrow the per-item schema when a chained join follows an ANTI join
(added after adversarial review — see §7a; this is not optional polish,
it is the correctness condition for Q78's own repro shape)

`planFromItem`'s per-item join-chaining loop (`planner.go:3085-3349`)
builds each `Join` with an **unconditional** `schema: mergedSchema`
(`jn := &Join{...}` at `planner.go:3211`) and, at the bottom of the
loop, unconditionally carries `leftCtx = mergedCtx` into the next
iteration (`planner.go:3346-3347`). Neither special-cases Semi/Anti.
`Join.Output()` (`plan.go:1205`) narrows dynamically at read time, but
nothing here narrows the STORED `.schema` field or the resolution
context threaded to a later join in the SAME chain — exactly Q78's
shape, `<channel>_sales LEFT JOIN(->ANTI) <channel>_returns ...
JOIN date_dim ON ...`, where `date_dim` is a second join in the same
`item.Joins` chain, chained onto the just-demoted ANTI join.
Left as-is, `date_dim`'s `ColumnRef.Index` values are computed against
a schema that still budgets space for `web_returns`'s columns, which
the ANTI join's actual runtime `Output()` does not produce — an offset
corrupted by `len(web_returns.Output())`, silently or as an
out-of-range panic. This is not a new pattern to invent: `unnest.go`'s
own Semi/Anti construction (`unnest.go:3315`) already stores
`schema: append(Schema(nil), outerChild.Output()...)` — left-only, at
construction, not deferred to `Output()` — for exactly this reason, and
`joinlayout.go`'s `reresolveJoinByName` documents a PREVIOUSLY SHIPPED
regression from this identical bug class (a stale merged Semi-join
schema leaking a dropped side's column index into an upstream operator,
Q21 NOT-EXISTS, silently returning 0 rows instead of ~411).

Fix, mirroring `unnest.go`'s convention: at `jn`'s construction
(`planner.go:3211`), narrow `jn.schema` to left-only for
`JoinTypeAnti`/`JoinTypeSemi`; and at the loop-carry (`planner.go:
3346-3347`), do NOT adopt `mergedCtx` for the next iteration's
`leftCtx` when this join was Anti/Semi — keep the PRE-join `leftCtx`
unchanged (its schema/bindings already equal what `jn.Output()` will
produce, since Semi/Anti publish exactly the left input unchanged):

```go
leftNode = jn
if joinType != JoinTypeSemi && joinType != JoinTypeAnti {
    leftCtx = mergedCtx
}
// else: leftCtx stays exactly what it was before this join — jn's
// Output() is Left.Output() (plan.go:1205), i.e. unchanged from what
// leftCtx already described, so nothing needs updating. Also apply the
// same left-only narrowing to jn.schema itself at construction, per
// unnest.go's precedent, so any reader that touches `.schema` directly
// (rather than through `.Output()`) sees the same narrow answer.
```

Traced end-to-end for Q78's shape: iteration 1 (Anti, `web_returns`)
leaves `leftCtx` at `web_sales`-only; iteration 2 (`date_dim`, INNER)
computes `rightBinding.offset`/`rightCtx`/`mergedSchema` against that
narrow `leftCtx`, so `date_dim`'s columns land at the CORRECT physical
offset (immediately after `web_sales`'s own columns, with no gap for
the now-invisible `web_returns`); the final `leftCtx = mergedCtx` at
the end of iteration 2 (an INNER join, untouched by this guard) then
correctly excludes `web_returns` from the returned bindings, matching
what the plan tree actually produces at runtime.

This closes the gap the review identified: without it, 4a-4c alone
would be reachable-and-wrong for the target query rather than inert.

## 5. What does NOT change

- `reduceOuterJoins`'s real call (`planner.go:2900`) and
  `ctx.joinInfoList` construction: already correct (this is what
  exposed the mismatch in the first place).
- The RIGHT/FULL demotion arms, the S9.4 flip, `collectForcedNullWalk`'s
  AND-only/no-OR descent rule: unchanged, out of scope.
- `outerLinksHaveSJInfos` itself: once the two consumers agree on
  Jointype the guard should simply stop firing for this class — it is
  not weakened or bypassed.
- Join ORDER or METHOD choice by fiat: this round changes which join
  TYPE is eligible to enter the search, not which plan the search picks
  — an admitted problem still costs every candidate PG-faithfully.

## 6. Gates

Suites green (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`);
new unit tests, written FRESH — review found no existing `demotedForPlan`
unit test to extend (`TestReduceOuterJoinsLeftToAnti*` pins
`applyDemotion`/`reduceOuterJoins`, the analysis side, not the
transplant): `demotedForPlan` transplants ANTI + reports the table name;
`stripForcingNullQuals` drops only the matching top-level `IS NULL`
conjunct and leaves siblings (`d_year = 2000`) and any OR-nested
`IS NULL` untouched; a `leftjoin_search_admission_test.go`-style
executor VALUES regression in TWO shapes, not one — (i) the 2-relation
`A LEFT JOIN B ON <strict> ... WHERE B.x IS NULL` case, and (ii),
**required by review, not optional**, a 3-relation `A LEFT JOIN B ON
<strict> ... JOIN C ON ... WHERE B.x IS NULL` case with a `C` column
referenced downstream — this is the shape that exercises §4d's
schema-narrowing fix and is exactly Q78's own repro; (i) alone would
pass even if §4d were skipped or wrong, since the bug it guards against
only surfaces with a chained join after the ANTI one. Both pin the SAME
rows as today's (pre-fix) LEFT+filter plan (the correctness gate K30
asked for before this was attempted). Then: TPC-H values digest
byte-identical, TPC-DS SF0.5 sweep `PASS=95 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0`, full parity re-measurement both corpora +
`shape-delta.sh`, decline census re-run (expect Q78's 3
`outer-link-no-sjinfo` → 0, per K27 predicting eligibility without a
guaranteed match). Report states the before/after decline count,
node-shape delta (LEFT JOIN + Filter → Anti Join, PG-shaped), and
match-count movement (or its absence) exactly as R27 §4a's report did —
becoming eligible is not becoming matched, and the report will say so
plainly rather than imply otherwise.

## 7. Open risk to flag for review

`rangeVarPrimaryName(out.Joins[i].Right)` (4a) identifies the demoted
table by name for the WHERE-strip; if a CTE body ever demoted TWO
different joins to ANTI on WHERE clauses that both test the same alias
under different qualification (unlikely, unseen in the corpus, not
constructed as a test), the table-name-keyed set could over-strip. Flag
for the reviewer: is the existing `collectForcedNullTableNames`
table-name granularity (not per-conjunct) an acceptable simplification
here too, given `applyDemotion` already uses it for the DECISION side
and this round only mirrors it for the ACTION side?

## 8. Review record

Adversarial subagent review (general-purpose, full HEAD re-derivation,
not trust-the-doc): verified 15 separate factual claims against the
real source line-by-line, including the crux wiring claim (§4c: is the
`ctx` `planFromClause` returns literally the same object the later
WHERE arm reads? — traced `node, ctx, err = planFromClause(...)` at the
single call site and confirmed yes, no re-plumbing needed) and the crux
ordering claim (§4c: is `demotedForPlan`/`reduceOuterJoins` called with
the SAME raw, uncanonicalized `s.Where` that `stripForcingNullQuals`
would walk? — confirmed yes, `canonicalizeQual` runs later). It also
independently found and quoted PG's own `prepjointree.c:3083-3088`
comment ("the IS NULL clause then becomes redundant... must be
removed"), corroborating §2's oracle claim from the PG source itself,
not just from observed EXPLAIN output.

**Correction applied by review, and the reason it matters:** the
design's original text (4a-4c only) analyzed the WHERE-qual hazard
(§3) but not a second, independent hazard in the same target shape —
`planFromItem`'s per-item schema-carry does not narrow for Semi/Anti
when a further join in the SAME `FromExpr` chains onto it, which is
exactly Q78's own repro (anti join to `web_returns`, then an inner join
to `date_dim` in the same CTE body). Un-narrowed, `date_dim`'s column
offsets would be computed against a schema still budgeting space for
`web_returns`'s now-invisible columns — the review traced this to a
PREVIOUSLY SHIPPED bug of the identical class (`joinlayout.go`'s
`reresolveJoinByName` doc comment: a stale merged Semi-join schema
silently corrupted Q21's NOT-EXISTS to 0 rows instead of ~411), and to
an existing, proven-safe counter-pattern already in the codebase
(`unnest.go:3315` stores a left-only `schema` at Semi/Anti construction
rather than deferring to `Output()`). §4d, added after review, closes
this by mirroring that same counter-pattern plus gating the loop-carry
`leftCtx = mergedCtx` at `planner.go:3346-3347` on join type — traced
end-to-end for Q78's exact shape in §4d to confirm the offsets land
correctly. §6's test plan was corrected in the same pass: no existing
`demotedForPlan` unit test exists to extend (the design's original
claim was wrong), and a 3-relation VALUES regression is now required,
not optional, because a 2-relation-only test would stay green even if
§4d were dropped or wrong.

Verdict after correction: the reviewer's objection was to a real,
load-bearing gap, not a style nit — proceeding to implement 4a-4c
without 4d would have been reachable-and-wrong for the design's own
target query. With §4d incorporated, the design is sound to implement.
