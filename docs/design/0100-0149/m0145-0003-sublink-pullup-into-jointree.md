# M0145-0003 — sublink pull-up into the jointree

Status: landed (EXISTS/NOT EXISTS flat-body arm) / gates in §"Measurement".

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
