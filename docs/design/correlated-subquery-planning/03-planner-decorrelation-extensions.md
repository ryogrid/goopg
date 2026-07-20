# 03 — Planner Decorrelation Extensions

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| part of | [correlated-subquery-planning](README.md) design bundle |
| depends on | [01 — current state and gap analysis](01-current-state-and-gap-analysis.md), [02 — PG target architecture](02-pg-target-architecture.md) |
| feeds | [06 — cost-model touchpoints](06-cost-model-touchpoints.md), [07 — verification](07-verification-and-measurement.md), [08 — roadmap](08-roadmap-and-milestones.md) |

This chapter decides **which decorrelation gaps the planner closes, in what
order, and under what semantic guards**. It is deliberately split from the
SubPlan execution work ([04](04-subplan-execution-engine.md)): decorrelation
removes SubPlan invocations; chapter 04 makes the irreducible ones cheap.

The single most important measured fact shaping this chapter
([evidence/unnest-probes-e4a43ba6.txt](evidence/unnest-probes-e4a43ba6.txt),
[evidence/review-probes-20260720.md](evidence/review-probes-20260720.md),
[measured-at-HEAD e4a43ba6]): **the EXISTS/NOT-EXISTS and correlated-scalar
unnesting passes exist in `internal/planner/unnest.go` and DO fire on
index-less inner tables — but an index on the inner correlation column
(present on every TPC-H correlation column) silently disables them via
`IndexScan.Key` absorption, while the correlated-IN semi-join pull-up fires
regardless.** Two corollaries order this chapter: (a) the collector fix that
re-enables TPC-H decorrelation is small and mechanism-confirmed (D3.0);
(b) because the machinery is live today on index-less shapes, its missing
position/aggregate guards are **live bugs** (planner infinite loop, OR-position
wrong results, count bug — §2.5) and land before or with the fix.

---

## 1. Decision index

| ID | Decision | Phase ([08](08-roadmap-and-milestones.md)) |
| --- | --- | --- |
| D3.0 | Root-cause and re-enable the existing-but-non-firing EXISTS / scalar unnesting | S1 Re-enable structural decorrelation |
| D3.1 | Move unnesting before join-order search (bushy DP) | S5 Pipeline reorder before join search |
| D3.2 | Non-equi correlation residual-lifting for IN and scalar subqueries | S4 Decorrelation coverage extensions |
| D3.3 | Tolerate nested subqueries inside EXISTS (leave inner as SubPlan) | S4 Decorrelation coverage extensions |
| D3.4 | Scalar-subquery gate refinement and count-bug policy | S4 Decorrelation coverage extensions |

---

## 2. D3.0 — Root-cause and re-enable the existing unnesting (S1)

### 2.1 The measured contradiction

At HEAD `e4a43ba6` ([evidence/unnest-probes-e4a43ba6.txt](evidence/unnest-probes-e4a43ba6.txt)):

| Probe | Shape | Expected (per M0061-0001 / M0054-0008 design docs) | Measured at HEAD |
| --- | --- | --- | --- |
| P1 | minimal correlated `EXISTS` (single equijoin) | Semi join | `Seq Scan` + `Filter: (<*planner.ExistsExpr>)` — **no unnest** |
| P3 | P1 + inner-only residual (`l_commitdate < l_receiptdate`) | Semi join | no unnest |
| P4 | minimal `NOT EXISTS` (Q22 core) | Anti join | no unnest |
| P5 | correlated scalar aggregate (Q17 core) | GROUP BY + hash join | no unnest |
| P6 | correlated `IN` | Semi join | **Semi join fires** (`Hash Join (?)` + Index Only Scan) |

The IN pull-up loop and the EXISTS/scalar pull-up loops live in the same
traversal (`unnestSubqueriesInPlan`, `internal/planner/unnest.go:9-86`) and
fire from the same `*Filter`-node positions. P6 firing proves the `*Filter`
node **is present and visited** at unnest time for at least the
operand-correlated IN shape. Therefore the EXISTS/scalar non-firing is *not*
(only) an outer-position problem — the difference must lie in what the
per-loop gates and collectors demand of the **inner** plan, or in the shape
of the outer conjunct they search for.

**Resolution (2026-07-20 review probes,
[evidence/review-probes-20260720.md](evidence/review-probes-20260720.md) §5)
[measured-at-HEAD e4a43ba6]:** the mechanism is confirmed as a sharpened form
of hypothesis 1 below. On **index-less** inner tables both loops fire
(minimal EXISTS → semi join; minimal scalar → GROUP BY + hash join); with an
index on the inner correlation column — which every TPC-H correlation column
has — the inner planner absorbs the correlation equijoin into
`IndexScan.Key`, the collectors (which harvest only inner Filter conjuncts)
find zero params, and the all-accounted walk (`walkPlanExprs`, which *does*
visit `IndexScan.Key`, unnest.go:≈310-319) sees the unaccounted
`OuterColumnRef` and bails. `CREATE INDEX`/`DROP INDEX` toggles the behavior
deterministically. Consequence: **the machinery is live today on index-less
shapes**, so the missing guards documented in §7/§8 (IN-loop top-conjunct
bail, scalar AND-reachability gate, NULL-on-empty aggregate whitelist) are
reachable live bugs, not latent ones, and must land **before or with** the
collector fix — see §2.5.

### 2.2 Hypothesis space (ranked; hypothesis 1 CONFIRMED in sharpened form, 4 dismissed)

1. **Inner-plan collector blindness — CONFIRMED as `IndexScan.Key`
   absorption** (review probes §5). `collectExistsUnnestParamsAndResiduals`
   (`unnest.go:1833`) and `collectUnnestParams` (`unnest.go:202`) walk only
   `*Filter` nodes of the inner plan (`walkFilters` handles
   Filter/Project/Aggregate/Sort/Limit/Join — nothing else). If the inner
   plan built by `planSelectWithParent` (`planner.go:10247`) no longer keeps
   the correlation conjunct in a `*Filter` node — e.g. a later pass pushed it
   elsewhere, or resolution wraps the scan differently — the collector finds
   zero equijoin params and `canUnnestExistsExpr` (`unnest.go:1924`) bails via
   `len(eup.Params) == 0`; the leftover `OuterColumnRef` then also fails the
   "all accounted" check (`unnest.go:1904`). Note `SeqScan` has **no embedded
   filter field** (`plan.go:539` — predicates above a scan are `*Filter`
   wrappers), so the failure would be about *which pass rearranged the inner
   tree*, not about scan-embedded predicates.
2. **Equijoin-pair extraction failure.** `extractEquijoinPair`
   (`unnest.go:233`) pattern-matches `BinaryOp{OpEq, OuterColumnRef,
   ColumnRef}` (either order). Recent expression-tree changes — e.g. the
   M0123-S4 string-literal cast-folding sub-slices landed on this branch in
   July 2026, and the `TypedStringLit` wrappers visible in the HEAD plans —
   may have inserted wrapper nodes (casts, typed literals) around one side of
   the correlation equality, breaking the exact-shape match.
3. **Outer conjunct search mismatch.** `unnestExistsExpr` re-locates the
   EXISTS via `findFilterContainingExistsExpr` and then requires the EXISTS
   (or a single wrapping `NOT` UnaryOp) at a **top-level conjunct**
   (`unnest.go:2012`). If a normalization pass now produces a different
   negation encoding (e.g. `Negated` flag plus NOT wrapper double-negating),
   `topConjunct` resolution could fail. Ranked lower because P1 has no
   negation and still fails.
4. **Branch divergence — DISMISSED.** No code regression is needed to
   explain the history: the loops fire whenever the inner side plans as a
   filtered scan (review probes §5); the June verification and the HEAD
   non-firing are both consistent with `IndexScan.Key` absorption plus
   schema differences.

### 2.3 Required S1 investigation protocol

1. **Reproduce below the SQL layer.** A throwaway unit test in
   `internal/planner` that builds the P1 query through `Plan()` with a stub
   catalog, captures the tree passed to `unnestSubqueriesInPlan`
   (`planner.go:945`), and asserts the post-pass tree contains
   `JoinTypeSemi`. This is the S0 dossier's per-gate classification made
   executable, and later the regression guard.
2. **Instrument every bail site.** Temporarily (behind a build tag or a
   debug flag) log which gate returned: `findExistsExprInExpr` not found /
   `canUnnestExistsExpr` param-collection empty / all-accounted failure /
   `hasNestedSub` / `topConjunct == nil` / `findFilterContainingExistsExpr`
   nil. One run of probes P1–P6 then yields the exact per-loop, per-gate
   classification. Fold the result into the [01](01-current-state-and-gap-analysis.md)
   dossier, replacing hypothesis tags with measured tags.
3. ~~Bisect~~ — no longer needed: the mechanism is measured
   (`IndexScan.Key` absorption, §2.1) and requires no regression to explain.
   The unit test (1) and gate instrumentation (2) remain as confirmation and
   as the permanent regression guard.

### 2.4 Fix directions and recommendation

| Option | Change | Pros | Cons |
| --- | --- | --- | --- |
| (i) Widen the pass's *outer* coverage: visit `Join.Predicate` conjuncts (and any other predicate-bearing nodes) in `unnestSubqueriesInPlan` | traversal only | Also helps Q21-class shapes where the EXISTS conjunct lands in a join predicate after DP | Does not address inner-collector blindness; more rewrite positions to keep semantics-safe |
| (ii) Run unnesting earlier — before predicate pushdown / bushy DP | pipeline order | Removes the whole class of "some pass moved the conjunct" fragility; PG-faithful (see D3.1) | Big-bang plan-shape churn; needs D3.1's prerequisites; wrong risk profile for a re-enable fix |
| (iii) Teach the *inner* collectors to find correlation conjuncts wherever they sit — explicitly including `IndexScan.Key` / `IndexScan.LowKey` / `IndexScan.HighKey` (the confirmed absorption site, §2.1) plus join predicates — and make `extractEquijoinPair` tolerant of benign wrappers (casts/typed literals) via a stripping helper | collectors only | Directly targets the confirmed mechanism; small, testable; benefits scalar + EXISTS + IN uniformly | Wrapper-stripping must be conservative (only provably value-preserving casts); an equijoin recovered from `IndexScan.Key` must also be *removed* from the index-scan key when the rewrite lifts it into the join (or the cloned inner scan reverts to a Seq/filtered scan) |

**Recommendation: (iii) first, then (i), defer (ii) to D3.1/S5.** The gate
instrumentation (§2.3) decides between them with data; if the log shows the
inner collectors are fine and the breakage is elsewhere (hypothesis 3/4), fix
the actual root cause instead — the protocol, not the guess, is the
commitment. D3.0's acceptance criterion ([07 V3](07-verification-and-measurement.md)):
probes P1–P6 all produce their expected join shapes, and Q4/Q17/Q21/Q22 plans
contain no `<*planner.ExistsExpr>` / correlated `<*planner.SubqueryExpr>`
filter strings (Q21's dual-EXISTS may additionally need D3.3 — the
instrumentation will say).

### 2.5 S1-blocking guards (live bugs measured 2026-07-20)

Three guards fix behavior reachable **today** on index-less shapes
([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md)
§§1–3) and are the *first* S1 deliverable, before the §2.4 collector fix
widens exposure:

1. **IN-loop top-conjunct bail** — mirror the EXISTS gate: before rewriting,
   `unnestInExpr`/`unnestNonCorrelatedInExpr` must require the found `InExpr`
   (or a single wrapping `UnaryOp(NOT)`, flipping to `JoinTypeAnti` the way
   `unnestExistsExpr` flips `negated`, unnest.go:≈2004-2010) to be
   pointer-identical to a **top-level conjunct** of the containing Filter;
   otherwise return `(nil, nil)` so the driver loop breaks and the SubPlan
   path serves it. Root cause of the infinite loop: `findFilterContainingInExpr`
   (unnest.go:≈1539) finds the sublink **anywhere** in the predicate
   (including under OR/NOT) while conjunct removal
   (unnest.go:≈1374-1385 / ≈1486-1497) matches top-level conjuncts only — the
   join is installed, the predicate never shrinks, and the driver loop
   (unnest.go:33-48) re-finds the same node forever. The transform is also
   semantically wrong at even one iteration (OR semantics lost), so the fix
   is a bail, not better removal.
2. **Scalar AND-reachability gate** — the scalar loop needs the same
   top-conjunct condition: the conjunct containing the `SubqueryExpr` must be
   AND-reachable from the Filter root. Today an OR-position scalar
   decorrelates into an INNER join **above which** the OR is evaluated, so
   rows with empty groups are dropped before the other OR arm is tried
   (measured: `{}` vs PG `{2}`, probes §3).
3. **NULL-on-empty aggregate whitelist** — see D3.4 (§7): only aggregates
   that return NULL on empty input (MIN/MAX/AVG/SUM) may decorrelate through
   the INNER-join rewrite; `count(col)` currently passes the gate and returns
   wrong results (probes §2).

**Defensive belt** (cheap, permanent): assert in the driver loop that each
iteration strictly decreases the number of sublink nodes in the Filter's
predicate; if not, break and leave the remainder to the SubPlan path. This
converts any future find/remove mismatch from an infinite planning loop into
a correct (if unoptimized) plan.

---

## 3. Coverage matrix (target state after S4)

Positions: `WHERE-conj` = top-level AND'd conjunct of WHERE / a Filter;
`OR/CASE/args` = under OR, CASE, or function arguments. PG column per
[02 §2](02-pg-target-architecture.md) (verified against
`postgres/src/backend/optimizer/prep/prepjointree.c`).

| Sublink shape | Position | Correlation | PG 18.3 | goopg HEAD [measured-at-HEAD e4a43ba6] | goopg target | Phase |
| --- | --- | --- | --- | --- | --- | --- |
| `EXISTS` | WHERE-conj | equijoin (+ residuals) | Semi join (`convert_EXISTS_sublink_to_join`) | SubPlan on indexed inner correlation cols (`IndexScan.Key` absorption); fires on index-less inners | Semi join | S1 (D3.0) |
| `NOT EXISTS` | WHERE-conj | equijoin (+ residuals) | Anti join | SubPlan on indexed inner correlation cols; fires on index-less inners | Anti join | S1 (D3.0) |
| `EXISTS` | WHERE-conj | **no equijoin, non-equi only** | Semi join (any correlation legal in pulled-up quals) | SubPlan (`len(Params)==0` bail, unnest.go:1904) | NL semi/anti join or SubPlan (cost gate, [06](06-cost-model-touchpoints.md)) | S4 (D3.2) |
| `EXISTS` | WHERE-conj | contains nested sublink | pulled up; inner sublink processed recursively | SubPlan (`hasNestedSub` bail, unnest.go:1937) | Semi/anti join, inner stays SubPlan | S4 (D3.3) |
| `x IN (sub)` | WHERE-conj | correlated equijoin | Semi join (`convert_ANY_sublink_to_join`) | **Semi join — fires** (P6) | keep | — |
| `x IN (sub)` | WHERE-conj | non-correlated | Semi join or hashed SubPlan | Semi join (`unnestNonCorrelatedInExpr`, unnest.go:1418) or cached SubPlan (Q16/Q18/Q22 [measured]) | Semi join; hashed SubPlan fallback ([04 D4.3](04-subplan-execution-engine.md)) | S3/S4 |
| `x NOT IN (sub)` | WHERE-conj | non-correlated | hashed SubPlan (NOT pulled up) | **NullAware anti join — fires at HEAD** (unnest.go:1508, M0122-0011; Q16's subquery unnests, and the residual `InExpr` in its plan is the literal `p_size IN (…)` list — [01 §4.1](01-current-state-and-gap-analysis.md)) [measured-at-HEAD e4a43ba6] | keep (beyond-PG divergence, [02 §7](02-pg-target-architecture.md)) | — |
| scalar agg subquery | WHERE-conj comparison | correlated equijoin | **SubPlan (PG never decorrelates scalars)** | SubPlan on indexed inner correlation cols (P5); fires on index-less inners — with the live count bug (§2.5) | GROUP BY + join rewrite (beyond-PG divergence, guarded by D3.4) | S1 (D3.0) |
| scalar subquery | anywhere | non-correlated | InitPlan (once) | cached SubPlan (`IsNonCorrelated`, M0058-0001) [measured-at-HEAD e4a43ba6] (Q11/Q18/Q20/Q22 filters) | keep cached SubPlan ([02 §7](02-pg-target-architecture.md)); optional hashed/Material analog [04] | S2/S3 |
| any sublink | OR / CASE / args | any | SubPlan (`pull_up_sublinks_qual_recurse` stops at non-AND) | EXISTS: SubPlan (safe, `topConjunct` bail); **IN under OR/`NOT (…)`: planner infinite loop; scalar under OR: wrong results** — live bugs, review probes §§1,3 | SubPlan — **explicit non-goal** (§7), with the §2.5 guards making it safe | guards S1 |
| `ALL` sublink | any | any | SubPlan | SubPlan (`AllOp`, plan.go:229) | SubPlan — non-goal | — |

Every "target ≠ HEAD" cell must have a matching semantics row in the
[07 V1 test matrix](07-verification-and-measurement.md) before its phase lands.

---

## 4. D3.1 — Unnest before join-order search (S5)

### 4.1 Problem

goopg runs `unnestSubqueriesInPlan` at `planner.go:945`, **after**
`tryBushyDP` (`bushy.go:66`) and predicate pushdown, and just before the
MultiHashJoin rewrite (`planner.go:951`), single-table index rewrite
(`planner.go:959`) and NLI rewrite (`planner.go:966`). PostgreSQL does the
opposite: `pull_up_sublinks` (`postgres/src/backend/optimizer/prep/prepjointree.c`)
runs in `subquery_planner` before any join-order work, so semi/anti joins are
ordinary participants in the join search. Consequences of goopg's ordering:

- Decorrelated semi/anti joins are bolted on top of an already-fixed join
  order; they can never migrate below a selective dimension join the way
  PG's Q21 plan drives the anti join from a filtered supplier set
  ([plan-compare-260718 @701a5f57] §9).
- Bushy DP explicitly walls Semi/Anti out of several rewrites
  (`bushy.go:1352-1360`, `:1941-1966`, `:2063-2074`), because they were
  introduced after the DP pass and carry an isolated subquery scope.
- The unnest pass is downstream of every predicate-moving pass, which is
  exactly the fragility class D3.0 §2.2 hypothesis 1 describes.

### 4.2 Decision

Adopt PG's ordering as the end state: **sublink pull-up runs before join-order
search**, so the DP input already contains Semi/Anti join nodes.

Prerequisites (why this is S5, not S1):

1. `OuterColumnRef`/`ColumnRef` index remapping currently assumes post-DP
   shapes (`remapOuterRefsInSubplan`, `bushy.go:1689`); pre-DP pull-up
   changes what `SourceTableIdx` (plan.go:437) refers to at rewrite time.
   Additionally (2026-07-20 review, F8): the pinned joins' `LeftKey` and
   residual column indices must be **re-resolved after DP/MHJ reorder the
   outer layout beneath them** — the existing `resolveOuterIdx` /
   M0071-0003 machinery assumes the semi/anti join is constructed post-DP;
   under S5a it is constructed pre-DP and the layout moves underneath it.
2. The DP legality rules must learn semi/anti join constraints. PG's oracle
   is `join_is_legal` + `SpecialJoinInfo`
   (`postgres/src/backend/optimizer/path/joinrels.c`): a semi/anti join's
   RHS must be joined as a unit and must not be commuted past the join.
   **Conservative first step:** pin pulled-up semi/anti joins as top-most
   joins over the DP result (exactly today's placement) while still running
   pull-up first — this de-risks the reorder into two separately landable
   halves: (S5a) reorder with pinned placement; (S5b) DP participation with
   legality rules, plan shapes improve. Legality of the pin: semi/anti joins
   are outer-schema-preserving filters whose RHS stays a unit, and the
   relative order among pinned joins equals today's loop order (review F8).
   **Scope the S5a stability claim honestly:** plans are unchanged *except
   where sublinks now decorrelate pre-pushdown* — running pull-up before
   pushdown changes which conjuncts pushdown ever sees, so shapes like Q21
   (today an MHJ leaf filter) WILL change already in S5a; that is the
   intended win, and the plan-gate acceptance must whitelist exactly those
   diffs rather than claim byte-identity.
3. Plan-gate snapshots and the Q12/Q13 tripwires
   ([07 V3](07-verification-and-measurement.md)) must exist first — this is
   the riskiest change in the bundle for unrelated plans.

**Rejected alternative:** keep post-DP unnesting and accept unoptimized
semi/anti placement. Rejected because (a) it permanently caps Q20/Q21-class
plans below PG's shapes, and (b) it preserves the pass-ordering fragility
that D3.0 exists to paper over; fixing the ordering retires the fragility
class instead of patching instances of it.

---

## 5. D3.2 — Non-equi correlation residual-lifting for IN and scalar (S4)

The EXISTS path already splits inner conjuncts into equijoin params plus
lifted non-equi residuals (`collectExistsUnnestParamsAndResiduals`,
`unnest.go:1833`, M0062-0005 — Q21's `l2.l_suppkey <> l1.l_suppkey`).
The scalar and IN paths still hard-bail when any `OuterColumnRef` sits
outside an equijoin pair (`collectUnnestParams`, `unnest.go:202/:227`).

Decision: generalize the residual mechanism to all three pull-up loops —
one shared collector, one shared residual-lifting rule:

- **≥1 equijoin pair present** → hash Semi/Anti (or the scalar
  GROUP-BY+join) with residuals AND-ed into the join predicate, exactly the
  EXISTS mechanism today (`unnest.go:2120`).
- **Zero equijoin pairs (pure non-equi correlation)** → hash join is
  impossible; the choices are an NL semi/anti join (executor support exists:
  `internal/executor/operators_nljoin.go:148-195`) or staying a SubPlan.
  This is a genuine cost decision — an NL semi join over an unindexed inner
  is O(N·M) just like the SubPlan, minus the per-invocation overhead that
  [04](04-subplan-execution-engine.md) is eliminating anyway. Defer the
  choice to the [06 §2](06-cost-model-touchpoints.md) cost gate; until S6,
  keep pure-non-equi correlation as SubPlans (with chapter 04's cheap
  execution). PG note: PG *does* pull up arbitrary-correlation EXISTS (the
  pulled-up quals simply become join quals; `convert_EXISTS_sublink_to_join`
  imposes no equi requirement), so full PG fidelity eventually requires the
  NL path — record as the S6 stretch goal.

For scalars, residual-lifting must respect D3.4's aggregate-safety gate: a
lifted residual filters *join rows before aggregation semantics are
re-established*, so residual-lifting is only legal for the same
NULL-on-empty aggregates the base rewrite accepts.

---

## 6. D3.3 — Nested sublinks inside EXISTS (S4)

`canUnnestExistsExpr` rejects the whole EXISTS when the inner plan contains
*any* further sublink (`hasNestedSub`, `unnest.go:1937` — its own comment
calls leaving them as SubPlans "safer"). PG has no such wholesale rejection:
`pull_up_sublinks_qual_recurse` recurses into pulled-up subtrees, and any
sublink it cannot itself pull up simply stays a SubPlan inside the new join's
RHS (`postgres/src/backend/optimizer/prep/prepjointree.c`).

Decision: replace the rejection with the PG behavior — unnest the outer
EXISTS; the nested sublink remains a SubPlan expression inside the cloned,
now-self-contained inner subtree, evaluated by the ordinary
[04](04-subplan-execution-engine.md) machinery. Preconditions:

1. The nested sublink must not reference the *outer* query's scope (a
   two-level `OuterColumnRef` with `Level` pointing past the pulled-up
   subquery, plan.go:437) — if it does, the clone
   (`clonePlanReplacingOuter`, `unnest.go:489`) cannot make the inner plan
   self-contained; keep the bail for exactly that case.
   **Implementation trap (2026-07-20 review, F7): the current walkers cannot
   enforce this precondition.** `walkExprTree` (unnest.go:≈415-450) visits
   `SubqueryExpr`/`InExpr`/`ExistsExpr` nodes but does **not descend into
   their `.Plan`**, so a `Level ≥ 2` reference hidden inside the nested
   sublink's plan is invisible to the all-accounted check; today the blanket
   `hasNestedSub` bail masks this. Removing the bail with the current walkers
   would pull up exactly the unsafe case — and the failure mode is nasty:
   at top level a stale `Level 2` ref raises a loud out-of-range error, but
   if the whole query is itself one sublink deep, `OuterRows[len-2]`
   (expr.go:≈372-381, top-relative indexing) silently resolves to the
   *grandparent's* row — wrong scope, wrong results, no error. D3.3 therefore
   REQUIRES a new deep walk that descends into nested sublinks' `.Plan`
   while tracking relative level, as a hard precondition.
2. Executor support for SubPlans evaluated inside a hash-join build side
   must be confirmed (S0 audit item; the build side drains the inner plan
   through the ordinary expression evaluator, so this is expected to work —
   verify, don't assume).
3. **Clone aliasing:** `cloneExprLeaf`'s default arm returns nested sublink
   nodes **by shared pointer** — the cloned tree and the original alias the
   same nested `Plan`, so any later in-place pass mutates both. D3.3 must
   either deep-copy nested sublinks during the pull-up clone or prove no
   post-unnest pass mutates sublink plans in place.

Expected unlocks: Q20 (IN whose inner contains the correlated scalar
`0.5*sum` subquery) and possibly Q21's dual-EXISTS interaction — the D3.0
instrumentation decides whether Q21 needs D3.3 at all or fires with D3.0
alone ([01 dossier](01-current-state-and-gap-analysis.md)).

---

## 7. D3.4 — Scalar gate refinement and the count bug (S4)

goopg's scalar decorrelation (`unnestSubquery`, `unnest.go:952`) rewrites

```sql
SELECT ... FROM outer o WHERE o.x < (SELECT agg(i.y) FROM inner i WHERE i.k = o.k)
```

into `GROUP BY i.k` + **INNER** hash join. This is a beyond-PG divergence
(PG never decorrelates correlated scalars — [02 §7](02-pg-target-architecture.md))
and is only correct under the current gate (single aggregate, `Aggregate`
root, no DISTINCT/Star — `canUnnestSubquery`, `unnest.go:177`) **plus** one
property the gate must be shown to enforce:

**The count bug.** For an outer row with *no* inner match:

- SubPlan semantics: the subquery returns `COUNT(*) = 0` (COUNT is non-NULL
  on empty input); `o.x > 0` may be TRUE — the outer row **qualifies**.
- INNER-join rewrite: the grouped inner has no row for `o.k`; the join drops
  the outer row — it **disappears**. Wrong result.

For NULL-on-empty aggregates (MIN/MAX/AVG/SUM), SubPlan semantics yield
`o.x <op> NULL` → not TRUE, so the row is filtered either way and the INNER
join is safe. That is precisely why TPC-H Q2 (MIN) and Q17 (AVG) are
eligible.

Decision:

1. **Audit answered — live bug, whitelist is S1-blocking.** The 2026-07-20
   review probes settled this in the bad direction
   ([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md)
   §2) [measured-at-HEAD e4a43ba6]: `canUnnestSubquery` (unnest.go:≈184-199)
   checks only Aggregate root / single aggregate / `!Star && !Distinct` —
   there is **no NULL-on-empty whitelist**. `count(*)` is kept correct only
   as a side effect of the `Star` check; `count(b)` passes the gate,
   decorrelates through the INNER join, and returns wrong results today
   (`{3}` vs PG `{2,3,4}` on the probe fixture; `sum` is correct, confirming
   the NULL-on-empty argument). Add the aggregate whitelist (MIN/MAX/AVG/SUM
   without COALESCE wrappers) in **S1**, with the
   [07 V1 M5](07-verification-and-measurement.md) count-bug regression row —
   which must probe `count(col)`, since `count(*)` alone is masked by the
   `Star` bail. (`bool_and`/`bool_or` and other NULL-on-empty aggregates may
   be added to the whitelist later with the same argument; start minimal.)
2. **Policy:** keep COUNT-shaped scalars excluded through this bundle's
   phases. The correct rewrite (LEFT JOIN + `COALESCE(agg, 0)` over the
   grouped inner) is documented future work, not S4 scope — PG itself ships
   without it, and TPC-H does not need it.
3. Multi-row scalar subqueries must still raise PG's "more than one row
   returned by a subquery used as an expression" error after the rewrite;
   the GROUP BY construction guarantees ≤1 row per key, so the rewrite is
   error-compatible only when the correlation covers a full grouping key —
   which the equijoin-pairs-only gate already ensures. Keep a matrix row
   proving it.

---

## 8. Explicit non-goals (PG-parity boundaries)

Matching PG's own refusals (all verified in
`postgres/src/backend/optimizer/prep/prepjointree.c` /
`postgres/src/backend/optimizer/plan/subselect.c`; rationale in the
prepjointree header comment):

1. **OR-position / CASE / function-argument sublinks** are never
   decorrelated (`pull_up_sublinks_qual_recurse` stops at any non-AND
   node). They stay SubPlans and are served by [04](04-subplan-execution-engine.md).
2. **Correlated `NOT IN`** is not pulled up (NULL semantics make the anti
   join unequal to NOT-ANY in the presence of NULLs); only the NullAware
   non-correlated transform (M0122-0011) stays.
3. **`ALL` sublinks** stay SubPlans.
4. **Targetlist sublinks** (scalar subqueries in SELECT list) stay SubPlans.
5. **Sublinks below outer joins:** PG restricts pull-up of sublinks in
   outer-join ON clauses (the semi-join RHS must not need to appear
   null-extended). **goopg's unnest pass currently has *no* outer-join guard
   at all** — `grep JoinType internal/planner/unnest.go` shows only
   Semi/Anti/Inner construction, no LEFT/RIGHT/FULL checks — and
   `unnestSubqueriesInPlan` happily rewrites any `*Filter` regardless of
   what join it sits above.
   **S0 audit answered (2026-07-20 probes,
   [evidence/review-probes-20260720.md](evidence/review-probes-20260720.md)
   §6) [measured-at-HEAD e4a43ba6]:** no live wrong-results shape exists
   today — but only by accident. ON-clause predicates live in
   `Join.Predicate`, which the pass never reads, **and** sublinks inside ON
   clauses are rejected at plan time with 0A000 ("EXISTS not supported in
   this context", planner.go:≈10138; IN at :≈10112) — itself a PG-compat gap
   (PG plans the same query fine). `WHERE`-clause sublinks above a LEFT JOIN
   do reach the pass and fire, which is semantically safe (WHERE applies
   post-join; probe matched PG). The S1 preemptive guard stands as designed
   (refuse pull-up when the containing Filter sits inside an outer join's ON
   or a null-extended side; keep the semi-join RHS non-null-extended) —
   **record explicitly: the current safety rests on the 0A000 crutch, so
   whoever closes that compat gap must land this guard in the same change.**

---

## 9. MultiHashJoin interaction invariant

`rewriteMultiWayChain` (`planner.go:951`) collapses ≥3-way hash-join chains
into `MultiHashJoin`, whose residual-filter partitioning treats subquery
expressions as opaque and dumps them into per-leaf filters
(`walkColumnRefs`, `internal/executor/multi_hash_join.go:386` →
`leafFilters`). Two invariants for all D3.x work:

1. **No re-collapse:** a decorrelated `JoinTypeSemi`/`JoinTypeAnti` node is
   never absorbed into a `MultiHashJoin`. Verified explicit (2026-07-20
   review, F8): `collectMultiHashTables` bails unless
   `j.Type == JoinTypeInner` (`bushy.go:988`) — a semi/anti node terminates
   the chain walk, and the recursion (`bushy.go:≈1204-1213`) still collapses
   inner chains *below* a pinned join, same as today. Keep the invariant
   pinned with a test as planned; cite bushy.go:988.
2. **No residual duplication:** when unnesting lifts residuals onto a join
   predicate, the original conjunct must be removed from every filter the
   MHJ partitioner might later bucket (`stripOuterRefConjuncts`,
   `unnest.go:1758` handles the inner side; the outer Filter's conjunct
   removal must be verified against the MHJ path). A SubPlan surviving in a
   `leafFilters` bucket after its decorrelation would silently execute the
   subquery *and* the join — wrong cost, and for anti joins wrong results.
   Add a plan-gate assertion: post-unnest trees contain no
   `ExistsExpr`/`InExpr` node that is also represented as a join.

---

## 10. Open questions

1. ~~Which gate actually bails for P1/P3/P4/P5?~~ **Answered 2026-07-20:**
   `IndexScan.Key` absorption → all-accounted bail (§2.1). Remaining: confirm
   per TPC-H query with the S0 counters (Q21/Q20 may hit G2/G3 first).
2. ~~Does the outer-join audit find live wrong-results shapes?~~ **Answered
   2026-07-20:** no — but only via the 0A000 plan-time rejection (§8 item 5);
   no regress-port test covers the ON-clause sublink shapes yet (they error).
3. After S5b, can DPccp reorder around pinned semi/anti joins profitably for
   Q20/Q21, or do those queries need the NLI semi/anti path
   ([06 §2](06-cost-model-touchpoints.md)) to reach PG's plan shape?
4. Is `unwrapTrivialWrappers` (`unnest.go:1708`) + `stripOuterRefConjuncts`
   sufficient for the D3.2 shared collector, or does residual-lifting for
   scalars need a distinct safety walk (aggregate-input columns vs residual
   columns)?
5. Should the D3.0 wrapper-stripping helper (cast/typed-literal tolerance in
   `extractEquijoinPair`) be shared with the join-condition extractor that
   M0058-0004 added for OR-of-ANDs, to keep sibling paths in sync?
