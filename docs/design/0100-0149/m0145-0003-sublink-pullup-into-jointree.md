# M0145-0003 — sublink pull-up into the jointree

Status: COMPLETE 2026-09-22 (EXISTS/NOT EXISTS flat-body arm + ANY arm) /
gates in §"Measurement". The body-local-qual refusal that cost TPC-H Q4 and Q21
their semijoins was fixed 2026-09-21 — see §"Body-local quals are base
restrictions". Closed on a CLASSIFIED census rather than an empty one — see
§"Closing measurement (2026-09-22)". The unbuilt scope is recorded in the
M0145-0001 escalation block (that root is now `[!]` -- its last five completed
descendants all showed `Movement: none`); it has zero measured demand today and
must not be started without re-running the census.

## The ANY arm — `convert_ANY_sublink_to_join` (landed 2026-09-21)

The census below picked this arm; it is now in. `anyPullupConjunct` recognises
a plain-equality `*InExpr` over a retained body, and `pullUpAnyBody` produces
the same `jtPulledBody` the EXISTS arm produces, so the seam, the leaf
numbering and `classifyPulledQuals` are all unchanged.

### The one genuinely new mechanism: the link predicate is synthesised

An EXISTS body carries its correlation in its own WHERE, so the pull-up hands
those conjuncts through and the seam finds the spanning one. An ANY body need
not be correlated at all — `x IN (SELECT y FROM t)` has no cross-scope
reference anywhere in the body, because the correlation IS the testexpr, and
the testexpr lives in the OUTER qual. So the arm builds the missing conjunct,
`outerOperand = bodyTarget`, in the space the EXISTS body quals already use:
body-local columns as plain `*ColumnRef`, outer columns as
`*OuterColumnRef{Level: 1}` (`outerOperandAsLevel1`). `rebasePulledQual` then
rebases both halves and validates every outer index against the emitting
bindings, `classifyPulledQuals` sees a qual spanning the emitting rels and the
body's rels, and files it as the link predicate. Nothing downstream learns that
one of its inputs was synthesised — and a coordinate this arm got wrong fails
closed in the rebase rather than reading the wrong column.

### `NOT IN` is refused, and that is PG's rule

`NOT IN` is `<> ALL`, an ALL_SUBLink. `pull_up_sublinks_qual_recurse` converts
ANY and EXISTS only (prepjointree.c:665/731): the three-valued NULL semantics
of `<> ALL` are not an anti-join's — a single NULL on the inner side makes the
whole predicate NULL, where an anti-join emits the outer row. goopg's LEGACY
unnest does convert `NOT IN` to an ANTI join; that divergence is pre-existing
and is ledgered separately rather than extended onto the new pipeline.

### Measured

Same channel, same arm, before and after (TPC-DS SF0.25 plans, knob on):

| bucket | before | after |
|---|---|---|
| `(pulled)` | 9 | **21** |
| `InExpr` unrecognised | 34 | **1** |
| `any-body-scope-not-bindable` | — | 15 |
| `any-nested-sublink` | — | 6 |
| `SubqueryExpr` (not a gap — PG leaves scalar sublinks as SubPlans) | 15 | 15 |
| `ExistsExpr` (below conjunct top level) | 2 | 2 |

Of the 34 `IN` conjuncts: 12 now pull up, 15 decline at body-scope binding, 6
carry a nested sublink, 1 is a non-plain form. The next two gates are named by
the census itself rather than guessed.

**Correctness evidence.** The default-arm gates cannot exercise this code at
all, so the knob arm was swept: TPC-DS SF0.25 with `GOOPG_JOINTREE_PIPELINE=1`
returns `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0` — row counts AND value
checksums correct on every executable query with the arm live. Default-arm
gates stay green and plan-identical (`same=99 changed=0`), TPC-H acceptance arm
24/24.

## The ANY residue, named (2026-09-21): CTE bodies, not opaque bodies

The ANY arm left two buckets. `bindPulledBodyScope` and `flattenPulledBodyTree`
now return the sub-reason they failed on, so the larger one could be counted
instead of read off the ledger's assumption. TPC-DS SF0.25 plans, knob arm:

| bucket | count |
|---|---|
| `(pulled)` | 21 |
| `any-body-leaf-(*optimizer.CTEScan)` | **15 — all of the former `any-body-scope-not-bindable`** |
| `SubqueryExpr` (not a gap) | 15 |
| `any-nested-sublink` | 6 |
| `ExistsExpr` (below conjunct top level) | 2 |
| `InExpr` (non-plain / NOT IN) | 1 |

Every one of the 15 is the same shape: the ANY body's FROM is a **CTE
reference** (`WHERE x IN (SELECT … FROM some_cte)`), the TPC-DS idiom. The
ledger had these filed under the opaque-body arm — bodies whose planned Node
must ride along as an opaque citizen — and that is not what they are.

### Why admitting them is NOT a one-line relaxation

`flattenPulledBodyTree` requires bare `*SeqScan` leaves. Lifting that to admit
`*CTEScan` would push a statistics-free leaf into the DP, and
`itemIsDerived`'s classifier (relfromjoinlist.go:557) treats exactly
`*CTEScan`/`*WorkTableScan` as DERIVED — the input class the Q78
`outer-over-derived` firewall exists to keep out of the search (C-04a: Q78 went
15 s to 327 s TIMEOUT when derived inputs reached join ordering). The project
banner states the constraint directly: the Q78 firewall is a hard constraint on
every pull-up/flattening task.

So this bucket is blocked on **B-06 (CTE-output statistics)** — the same
blocker M0145-0005 slice 5 recorded for the `outer-over-derived` decline
family, and the same one M0145-0006's `outer-over-derived` row names. It is not
the opaque-body arm, and building the opaque-body arm would not move it.

The remaining actionable bucket is `any-nested-sublink` (6): PG recurses
`pull_up_sublinks` into a pulled body's own quals
(`pull_up_sublinks_qual_recurse`), which goopg does not yet do.

## Which arm next — the pull-up decline census (2026-09-21)

The M0145-0007 sublink-route census measured this pull-up's coverage at under
2% of sublink-planning events (knob arm: 294 pinned-spine vs 5
jointree-pullup). That makes "which deferred arm to build next" a question
about the SHAPE of the other 98%, and the task's deferred list ranks the arms
by how big they sound rather than by how often they fire. So they were counted.

`notePullupDecline` (nlicensus.go, `GOOPG_NLI_CENSUS=1`) reports one line per
WHERE conjunct the pull-up sees: the gate it fell at, or `(pulled)`. Conjuncts
with no sublink in them are not reported — an ordinary `a = 1` is not a missed
pull-up. The classifier names the sublink by its Go type via `ExprSubplans`
rather than a hand-written switch, so a sublink type nobody taught it still
appears (the exprwalk RC-1a rule applies to measurement too: the arm nobody
built would otherwise be the arm that never shows up).

TPC-DS SF0.25, knob arm, plans channel:

| bucket | count | reading |
|---|---|---|
| `InExpr` | **34** | `IN` / `NOT IN` / `= ANY` — PG pulls these up (`ANY_SUBLINK`, prepjointree.c:665). **The gap.** |
| `SubqueryExpr` | 15 | scalar sublinks — PG does NOT pull these up either; they stay SubPlans. Not a gap. |
| `(pulled)` | 9 | the flat EXISTS arm's successes |
| `ExistsExpr` | 2 | EXISTS not at conjunct top level (under OR, etc.) — PG handles some of these in its OR arm (prepjointree.c:797), so a small real gap |

No `pullUpExistsBody` gate (`body-not-simple`, `no-level1-correlation`,
`no-spanning-conjunct`, …) fired at all: every recognised EXISTS conjunct was
pulled. The EXISTS arm's gates are NOT the limiter — the limiter is that most
corpus sublinks are `IN`/`ANY`.

**Next arm: `convert_ANY_sublink_to_join`** (subselect.c:1333). It is 34 of the
45 unpulled sublink conjuncts, and the two arms PG implements are exactly ANY
and EXISTS, so finishing ANY completes goopg's `pull_up_sublinks` scope rather
than extending past it. The opaque-body arm and outer-local-only correlation
stay deferred with no corpus witness ranking them above it.
Task: `.ralph/fix_plan.md` M0145-0003. Parent: M0145-0001 (the IR
contract this implements one slice of) / M0145-0002 (the harness that
measures it). Kind: impl.

## What this is

The first real divergence point of the jointree pipeline: on
`GOOPG_JOINTREE_PIPELINE=1`, a WHERE-clause `EXISTS`/`NOT EXISTS` whose
body is a flat inner/cross jointree of base relations is pulled up
**before join-order search** — PG's `pull_up_sublinks` position
(`subselect.c`, driven from `prepjointree.c`) — instead of the legacy
route's post-hoc `unnestSubqueriesInPlan` + chain-splice. The pulled
body's relations enter the ONE search problem as leaf entries, its quals
join the parent's conjunct stream, and a `SpecialJoinInfo` records the
SEMI/ANTI ordering restriction — exactly the shape
`deconstruct_recurse` produces for a `JOIN_SEMI`/`JOIN_ANTI` member
(joins.c: `list_concat` under join_collapse_limit: the body flattens
into the parent joinlist and the constraint lives entirely in the
SJInfo).

The difference from the chain-splice is *where the leaves come from*.
The splice decomposed a finished `Join` plan and asked whether its leaf
count, span layout and walk order happened to agree — the mismatch class
that produced the `leaf-count`/`semianti-not-tail` declines and the Q78
panic family. Here the leaves exist by construction: the same pass that
numbers them binds them, so there is no walk/decompose disagreement to
decline on.

## Oracle anchors

`convert_EXISTS_sublink_to_join`
(postgres/src/backend/optimizer/plan/subselect.c:1447-1515), ported
gate-for-gate into `pullUpExistsBody`
(internal/optimizer/jointreepullup.go):

| PG check | goopg gate |
|---|---|
| `subselect->cteList` non-empty → refuse | `sublinkBodyIsSimple` CTE arm |
| `simplify_EXISTS_query` (drop targetlist; refuse if unsimplifiable) | `sublinkBodyIsSimple` — no GROUP BY/HAVING, DISTINCT, LIMIT/OFFSET, FOR UPDATE, aggregates, window funcs, set-ops, VALUES; plus goopg's own narrowing to a flat jointree of bare table refs (`sublinkBodyFromIsFlat`) |
| `contain_vars_of_level(subselect, 1)` on the rest of the Query (WHERE removed) → refuse | no Level-1 `OuterColumnRef` in the body's own inner-join ON quals (ON quals are the only non-WHERE part of a flat body that can carry refs) |
| `contain_vars_of_level(whereClause, 1)` must be true | `exprListHasOuterRefAtLevel(quals, 1)` |
| `contain_volatile_functions(whereClause)` → refuse | `exprListHasVolatileBuiltin` — deny-list mirrors the executor's `volatileBuiltins`, plus routine-registry volatility (same resolution order as `subPlanExprVolatile`) |
| `replace_empty_jointree` / nonempty jointree | `sublinkBodyFromIsFlat` requires ≥1 FROM item; `flattenPulledBodyTree` requires the bound tree to decompose to bare `*SeqScan` leaves |
| body RTEs appended wholesale; quals merged into parent | `integratePulledSublinks` appends leaf scans + `semiAntiChainLink` to the problem tables |

Two additional goopg-side gates PG gets free:

- `exprListHasLocalAndLevel1Ref` — the WHERE must carry a conjunct
  reading a body-local column AND a Level-1 parent ref together, else
  nothing can become the link predicate (the seam would decline on the
  nil pred anyway, but by then the pull-up's marks would already have
  suppressed the pre-DP arm for sibling sublinks — declining at pull-up
  keeps the statement exactly where the legacy pipeline finds it).
- `exprHasSublinkPlan` on bound quals — a nested sublink's Level-1 refs
  only bind while the body evaluates as one unit.

`NOT EXISTS` arrives as `UnaryOp(OpNot, ExistsExpr)` — the parser's
canonical spelling (unnest.go:4516), matching PG's `NOT (SubLink)` —
`existsPullupConjunct` flips once across the wrapper (same rule the
legacy unnest applies at unnest.go:4778); a bare `ExistsExpr` honours
its own `Negated` flag.

## Mechanism

`pullUpSublinksIntoJointree(f.Predicate, ctx, cat, ps)` runs in the
WHERE arm of `planSelectImpl` — the `jointree` divergence point — and
stamps `ctx.jtPullup` with coordinate-agnostic bound material
(`jtPullup.bodies`, one `jtPulledBody` per sublink): leaf scans and
widths from a *provisional* `planFromClause` on the retained
`.Subquery` parse tree, the bound body quals (WHERE + inner ON), and
the join type. The predicate itself is **not** rewritten: `pu.pulled`
marks the consumed conjuncts by pointer identity, so a decline anywhere
downstream leaves `pred` untouched and the whole statement falls back
to the exact legacy shape (already-planned body → SubPlan or post-hoc
unnest). The body's eager `.Plan` — built by `planExistsExpr` during
WHERE resolution, before pull-up runs — doubles as that fallback.

`integratePulledSublinks` runs inside `tryPGShapedJoinSearch`
(joinsearchseam.go) immediately after `extractSearchLeaves` — the one
point the emitting prefix's real leaf count is known — and appends the
pulled leaves at problem tail positions (`nprefix + nExtracted + k`),
builds one `semiAntiChainLink{flattened: true}` per body, and pools the
rebased quals. Everything downstream — synthetic-leaf accounting, tail
ordering, `buildLeafSpans`, `pgShapedOffsetChecksOK`,
`semiAntiOnQualsOK`, the SJInfo dedup append into `ctx.joinInfoList`,
`partitionConjunctsForJoinPlanning`, `planJoinlistSearch`, residual
computation — runs unchanged, because a pulled link is the same record
the chain walk already produced.

## Coordinates

Per the M0145-0001 contract: `ColumnRef.Index` is a flat output-column
coordinate; body-local refs initially index the body's own
leaf-concatenated space and `OuterColumnRef{Level:1}` indexes the
parent's emitting space. `rebasePulledQual` (inside a `cloneExprRefs`
Rewrite closure) maps:

- `*ColumnRef` → `pullSpans[leaf].lo + local`, where `leaf` is read off
  the body's own binding offsets (`bodyLeafOf`);
- `*OuterColumnRef{Level:1}` → `*ColumnRef` at the same flat index
  (PG's `IncrementVarSublevelsUp(.., -1, 1)`), with a
  `sourceIdx`-agreement check against the emitting binding it lands in
  (the M0071-0009 self-join disambiguation);
- `*OuterColumnRef{Level:≥2}` → `Level--` — a reference above the
  statement stays an outer reference, now one hop shallower, and is
  counted by `corrAbove` exactly like a natively correlated qual.

The pulled leaves' walk-order-flat and out-of-band span bases coincide
(`sum(widths[:pos])` in both spaces, since no real leaf follows a
synthetic one), so the seam's own `remapWalkOrderFlatToSpans` pass is
the identity on pulled quals — the same P0-H11 class of guarantee
(`cumOffsets`→`leafSpans`) the route-a splice needed, obtained here by
construction instead of by post-hoc repair: `allSpans` is built with
the same cumulative rule `buildLeafSpans` applies, and the test pins
attribution against `buildLeafSpans` output directly.

## Qual classification

Per bound conjunct, after rebase, by `relidsOfExpr` against the full
span table:

- **spanning** (relids ∩ emitting ≠ ∅ and ∩ rhs ≠ ∅) → the link
  `pred` — becomes the semijoin's join clause;
- **body-local** (relids ⊆ rhs) → `link.bodyQuals` — partitioned to
  leaf-local filters or intra-RHS join clauses, PG's
  `distribute_qual_to_rels` behaviour inside the pulled RHS;
- **outer-local** (relids ⊆ emitting, SEMI only) → `pu.outerQuals`,
  appended to the parent's conjunct stream where the partition lands
  them on the emitting leaf they read — `EXISTS(... WHERE outer.x=5)`
  filters the rows the semijoin would keep either way;
- **outer-local under ANTI** → decline: `NOT EXISTS(... WHERE
  outer.x=5)` must keep the `x!=5` rows, and the link carries no
  join-clause slot a non-spanning qual could occupy (ledgered);
- **unattributable / empty relids** → decline.

`pu.outerQuals` is the one pool addition the seam tail learns:
appended beside the link conjuncts, before partitioning, already in
problem space.

## Eligibility result (this slice)

- EXISTS and NOT EXISTS, any flat body of bare tables/inner joins.
- Multi-relation bodies: each body rel is its own leaf; intra-body
  quals distribute inside the RHS (TPC-DS Q10's two-rel body searches
  and picks up a Gather path).
- `SELECT *` bodies (the dominant benchmark form) pass — no call in
  the target list.

Deferred to the ledger: non-flat bodies as opaque semi/anti citizens
(the task's second arm — needs the body's param references to execute
inside the problem, a larger mechanism), `IN`/`NOT IN` pull-up
(`convert_ANY_sublink_to_join` is a different mechanism — hashed
subplan — not this file's), outer-local-only correlation (the
`exprListHasLocalAndLevel1Ref` decline), and outer-local quals under
ANTI (no join-clause slot exists for them — see above).

## Measurement

`scripts/jointree-parity-capture.sh tpcds-sf025` both arms
(tmp/jtcap-m3/, this loop):

- **A/B knob-vs-off**: `queries=99 match=91 shapediff=5 error=3
  timeout=0` — the five shape diffs are exactly the task's named
  witnesses (Q10, Q16, Q35, Q69, Q94); the three ERRORs carry the
  marker on BOTH arms (pre-existing unsupported SQL — Q36/Q70/Q86
  querygen skips).
- **vs PG**: D1-sublink 8→6 — two queries' first divergence moved past
  the sublink stage entirely (their next divergence now classifies
  D3-partialpath, hence D3 31→33); D2/D4/D6/verdict unchanged; match=2
  floor held (Q9, Q41).
- **TPC-H both arms**: `match=22 shapediff=0` — fully inert; TPC-H's
  EXISTS queries (Q4/Q21/Q22) already produced the semijoin PG picks
  via the legacy route, so the mechanism converges on an identical
  plan.
- Executor support: EXPLAIN-only capture; value correctness for the
  pulled shapes is pinned by the unit suite (searched semijoin + real
  leaf RHS + no residual ExistsExpr + sibling-conjunct preservation)
  and by the unchanged default pipeline's value gates.

## Gates

- `RALPH_PRECOMMIT_SCOPE=units` — PASS (includes the new
  `jointreepullup_test.go`: splice → searched SEMI/ANTI join, real
  leaf RHS, no residual sublink, multi-rel body, `SELECT *`, sibling
  conjunct preserved, 11-case decline parity, two white-box
  `integratePulledSublinks` tests).
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2, Q13=33).
- `scripts/tpcds-sf025-regression.sh sweep` — PASS
  (`MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`, plan channel
  `same=99 changed=0` — the default pipeline is untouched).
- `scripts/tpch-acceptance-arm.sh` vs `tmp/acc-base-route-a.txt` —
  PASS (24/24 value-MATCH).
- `internal/optimizer` package — PASS including the exprwalk census
  (two new closures pinned `nonRecursiveClassifier`).

## Body-local quals are base restrictions (fixed 2026-09-21)

`classifyPulledQuals` sorts each rebased body qual into spanning / RHS-only /
emitting-only. The RHS-only branch used to require `searchConsumes(rebased,
spans)` unconditionally, which asks whether `buildRestrictInfos` yields the
clause — and that function's `add` closure drops everything with
`relLevel(relids) < 2` by design, because the restrictInfo list holds JOIN
clauses only. A qual confined to ONE body rel therefore failed structurally,
and since the branch's failure refuses the whole body while `pulled` has
already suppressed the legacy pre-DP route for the scope, the statement lost
its semijoin from BOTH routes and kept the sublink as a per-row subplan.

That shape is common, not exotic: TPC-H Q4's body carries
`l_commitdate < l_receiptdate`, Q21's carries the same family. The cost was
12.98s vs 0.37s on Q4.

The branch now conditions the test on rel count, because rel count is exactly
what selects the downstream placement mechanism:

- **≥ 2 rels** — a join clause; `buildRestrictInfos` files it; `searchConsumes`
  is the correct test (refusal renamed `body-join-qual-not-consumable`).
- **1 rel** — a base restriction; it rides the conjunct pool into
  `partitionConjunctsForJoinPlanning`, which routes it to
  `locals.byBinding[leaf]`, where the seam wraps the pulled leaf in a
  `LeafLocal *Filter` and prices it through `estimateBaseRelInfo`.

PG's analogue is `distribute_qual_to_rels`
(`postgres/src/backend/optimizer/plan/initsplan.c`), which places a single-rel
qual on that rel's `baserestrictinfo` and lets the pull-up proceed; it has no
consumability precondition at all.

The pin is `TestJointreePullupBodyLocalQual`, which asserts both halves: the
body is admitted with BOTH conjuncts in the pool (spanning relids 11 and
body-local relids 10), and the resulting knob-on plan carries a semi join whose
RHS contains a `LeafLocal` filter — i.e. the qual was not merely admitted but
actually placed. The test fails on the pre-fix condition (verified by reverting
it), so it pins the defect rather than the code.

Timings and the sibling-path audit are in
`m0145-0008-cutover-readiness-timing-ab.md` §"The fix, measured".

## Files

- `internal/optimizer/jointreepullup.go` — pull-up pass, body binder,
  rebase, link builder, SJInfo builder, volatile gate.
- `internal/optimizer/jointreepullup_test.go` — the pins.
- `internal/optimizer/planner.go` — `planSelectImpl(jointree bool)`;
  `resolveContext.jtPullup`; WHERE-arm call site; pre-DP skip.
- `internal/optimizer/jointreepipeline.go` — dispatch comment.
- `internal/optimizer/joinsearchseam.go` — `integratePulledSublinks`
  call site; `splitAndExcludingPulled` conjunct pool; `outerQuals`
  append; `corrAbove` over pooled conjuncts.
- `internal/optimizer/exprwalk_inventory_test.go` — two
  `nonRecursiveClassifier` pins.

## Closing measurement (2026-09-22) — why this task ends here

Re-ran the knob-arm TPC-DS SF0.25 capture with both census channels on
(`GOOPG_NLI_CENSUS=1`, `GOOPG_PGSHAPED_DP_TRACE=1`), captures in
`tmp/jtcap-m3-loop60/` and `tmp/jtcap-m3-l60t/`.

Movement against the task's own stated targets:

| channel | before | after |
|---|---|---|
| `PULLUPCENSUS` `(pulled)` | 9 | **27** |
| `PULLUPCENSUS` `InExpr` | 34 | **1** |
| seam `leaf-count` decline | 26 | **10** |
| vs-PG `D1-sublink` | 8 | **5** |

The reason to stop is not that the numbers improved — it is that **all 60
census fires are now classified**, and none of them asks for an arm that is
buildable today:

- `(pulled)` **27** — successes.
- `any-body-leaf-(*optimizer.CTEScan)` **15** — blocked on B-06
  (CTE-output statistics, M0145-0009). Admitting these leaves first would
  push a statistics-free input into the DP, which is what the Q78
  `outer-over-derived` firewall exists to prevent (C-04a: 15 s → 327 s
  TIMEOUT).
- `SubqueryExpr@scalar` **15** — not a gap. PG does not pull scalar
  sublinks up either; they stay SubPlans.
- `ExistsExpr@or` **2**, `InExpr@or` **1** — not a gap.
  `pull_up_sublinks_qual_recurse` returns non-AND clauses unmodified
  (prepjointree.c:877), so PG does not reach an OR-position sublink either.

A `SUBLINKCENSUS` cross-check shows 285 sublinks routing `pinned-spine`
against 23 `jointree-pullup` — the never-reached population M0145-0017
already closed as a measured no-gap.

The `IN`/`NOT IN` work previously listed as still-open is **done**: the ANY
arm landed it, and the single surviving `InExpr` is an OR-position one PG
also declines.

### The unbuilt scope, and why it is not built

The opaque-body arm, outer-local-only correlation and ANTI outer-local quals
remain unimplemented. They are recorded in the M0145-0001 escalation
block, with a standing instruction not to start without re-running the census
first, because **both currently have zero measured demand** — no bucket fires
for them in TPC-DS SF0.25, and TPC-H was already fully inert for this
mechanism (`match=22 shapediff=0`). Filing them as their own tasks was
refused: M0145-0001's last five completed descendants all carry
`Movement: none`, so the root is `[!]` pending an owner decision.

This is the method this task established in its own decline-census loop:
pick the next arm FROM the census rather than by size. Applied honestly, that
method says to stop here. The generalisable point is that a census is
valuable in both directions — it is what told us to build the ANY arm (34 of
45 unpulled conjuncts), and it is what now says no remaining arm is worth
building. A residue that is fully *classified* is a finished task; a residue
that is merely *smaller* is not.
