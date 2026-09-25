# M0142-0008-producer — teach the IN-unnesting paths to set `Join.SJInfo`

Status: implemented (Loop #31); verification = unit tests + corpus
inertness measurement (§4's measurement answer: corpus-unreachable today)

Task: `.ralph/fix_plan.md` `M0142-0008-producer` — UNFROZEN by owner decision
2026-09-20 ("the sanctioned route"; the banner's hard constraint stands: do
NOT lift Q78's `outer-over-derived` firewall or take any equivalent shortcut
to obtain reachability).

## 1. Why this task exists

The M0142-0008a-3 plumbing chain (c1–c22, all `[x]`) built and verified the
entire semiAnti DP-search admission path: `extractSearchLeaves` exposes a
pinned `Join{Type:Semi/Anti}`'s RHS as a `semiAntiChainLink` carrying
`j.SJInfo` (`joinsearchseam.go:1393`), renumbers the SJInfo's
`SynLefthand`/`SynRighthand` in place to the real leaf bits (`:1445-1447`),
appends it to `ctx.joinInfoList` (`:624-625`, c6), and gates admission on
`semiAntiLinksHaveSJInfos` (`:628`) + `semiAntiOnQualsOK`.

Corpus measurement (design doc §58 of
`m0142-0008a-1-semi-anti-sji-design.md`, c22): `jointypeForDirection`'s
`case parser.JoinRight, parser.JoinSemi, parser.JoinAnti:` arm
(`joinpaths.go:216-243`) is entered **zero times** across all 96 TPC-DS
queries. Structural cause: no code path anywhere hands it a
`parser.JoinSemi` `*SpecialJoinInfo` for a searched pair —

1. EXISTS/IN semi joins are pinned pre-DP by `unnestExistsExpr`/S5a (§57) —
   but their links would be searchable IF the pinned join carried SJInfo;
2. `unnestExistsExpr` DOES set `SJInfo` (`unnest.go:4837`,
   `existsUnnestSJInfo` `:4398`) — the one working producer;
3. the two IN-unnesting paths do not: `unnestInExpr`'s join at
   `unnest.go:3458` and `unnestNonCorrelatedInExpr`'s join at `:3592` are
   `Join{Type:Semi/Anti, Algo:Hash}` with `SJInfo` left nil, so
   `semiAntiLinksHaveSJInfos` decline-gates every IN-derived link;
4. `reduce_outer_joins`'s LEFT→ANTI demotion (c19) is unconditionally
   `parser.JoinAnti` — never SEMI.

This task closes gap 3: make the IN-unnesting joins carry the same SJInfo
shape `existsUnnestSJInfo` already produces for EXISTS.

## 2. PG oracle

`postgres/src/backend/optimizer/prep/prepjointree.c`
`pull_up_sublinks`/`convert_ANY_sublink`: an IN/ANY sublink is pulled up as a
SEMI join whose `SpecialJoinInfo` is built by
`postgres/src/backend/optimizer/plan/initsplan.c`
(`deconstruct_jointree` → `make_outerjoininfo`/`compute_semijoin_info`).
`compute_semijoin_info` (initsplan.c:2059-2145) sets
`sjinfo->lhs_strict` from clause strictness and collects
`semi_rhs_exprs` = the RHS operand of each semijoin equality clause
(initsijplan-side). goopg's `existsUnnestSJInfo` already ports this shape;
this task extends the same construction to the IN paths.

## 3. The change

Both IN paths always build an **equi-keyed** join (`semiPred` is always
`outerKey = innerKey` at `unnest.go:3447` and `:3586`; `LeftKey`/`RightKey`
always set), so `LhsStrict`, `SemiCanBtree`, `SemiCanHash` are legitimately
`true` for the SEMI case — this is not tuning; it is what the constructed
join is. The synthetic `{1}`/`{2}` relsets are correct placeholders: the
seam walk renumbers them in place to real leaf bits (`joinsearchseam.go:
1445-1447`).

A new helper `inUnnestSJInfo(jt, rhsExprs)` mirrors `existsUnnestSJInfo`:

- `SynLefthand={1}`, `SynRighthand={2}`, `Jointype=parser.JoinSemi/JoinAnti`
  (ANTI when `effNegated`);
- `MinLefthand={1}`, `MinRighthand={2}` — the join always has at least the
  operand-equality conjunct spanning both sides;
- `LhsStrict=true` for SEMI and for non-NullAware ANTI; `false` for a
  NullAware ANTI (`NOT IN`) — a null-aware clause is not a plain strict
  equality, and `LhsStrict` feeds `joinIsLegal`'s commute checks where
  over-claiming could admit a reordering PG would refuse
  (fail-closed default, matching PG's `initsplan.c` conservative init);
- SEMI only: `SemiCanBtree=SemiCanHash=true` (the key is always `=`),
  `SemiRhsExprs` = the RHS operands of the join's equality conjuncts.

Per-site `SemiRhsExprs` source:

- `unnestInExpr` (correlated IN): the operand↔inner-output equality's RHS
  is `innerPlan.Output()[0]` (use the output column itself, which carries
  the real `SourceTableIdx` — NOT `innerKey`, whose `SourceTableIdx` is
  deliberately 0 and would trip `createUniquePath`'s schema-drift guard);
  plus each correlation param's `SubCol` (each param is a pulled-up
  equality conjunct, exactly the EXISTS construction). No
  `srcTableOffset`: the IN path does not call `remapSourceTableIdx`.
- `unnestNonCorrelatedInExpr`: `SemiRhsExprs = [innerOut[0]]` for the same
  reason.

`unnestScalarWithResiduals` (`unnest.go:2971`) is `JoinTypeInner` — out of
scope, correctly. The c6-era comment at `joinsearchseam.go:618` names a
third site (`:4726`); that was `unnestExistsExpr`'s join literal before it
grew `SJInfo:` — no third site remains.

## 4. Expected effect and measurement

With SJInfo set, an IN-derived link passes `semiAntiLinksHaveSJInfos` and
(after `semiAntiOnQualsOK`) enters the searched pair space with a
`parser.JoinSemi` SJInfo — the precondition `jointypeForDirection`'s
SEMI/ANTI/RIGHT arm and the landed `-3c` hash unique-ify substitution
require.

### 4.1 Measured (Loop #31, staged tree a6a2609b, GOOPG_PGSHAPED_DP_TRACE=1
full-corpus SF0.25 sweep `sweep-20260920-063101.txt` + `…-064700.txt`)

- **Corpus reachability: still zero, and the reason is upstream of this
  gate.** The traced sweep logged **0** `seam-decline reason=semianti-*`
  lines of either class and **0** `DPPATH … jointype=semi|anti` lines
  across all 99 queries — identical to the pre-change c5 measurement.
  Per-query attribution (TPC-DS Q56, three `IN (subquery)` statements,
  private trace run): every IN statement declines at
  `seam-decline reason=leaf-count nrels=4 nleaves=2`
  (`joinsearchseam.go:325`) — `extractSearchLeaves` ran, but the walked
  chain flattened to only the real outer leaves and the decline fires
  BEFORE `semiAntiLinksHaveSJInfos` (:628) is ever consulted. The whole
  corpus's seam-decline census this run: leaf-count×62,
  outer-over-derived×6, outer-spine×4, lateral×4 — zero semianti-class.
  So the producer is a precondition that downstream chain items
  (a-3 increment (ii)'s splice-path retirement / leaf-admission work)
  must still unblock; it cannot move a corpus plan by itself today.
- **The producer itself is verified**: `in_unnest_sjinfo_test.go` pins
  all four shapes (correlated SEMI/ANTI, non-correlated SEMI, NullAware
  ANTI) and proves a real `unnestNonCorrelatedInExpr`-planned join feeds
  `extractSearchLeaves` a link whose `sjinfo` is non-nil and passes
  `semiAntiLinksHaveSJInfos` — the exact gate that decline-gated these
  links before.
- **No plan or values movement**: `tpcds-sf025-regression.sh sweep`
  PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan shapes
  `same=99 changed=0`; `tpch-spotcheck` PASS (Q12=2, Q13=33 — the
  re-pinned post-reload anchors). The wall-time/ledger reporting items
  are vacuous: no query's plan changed.
- `CATEGORIES:` unchanged by construction (shape-delta zero);
  `CATEGORIES-EXCL-MATCH: 0`. Stats epoch: N/A — no estimate-audit or
  capture artefact taken this task; the acceptance arm ran pinned at
  `GOOPG_ANALYZE_SEED=20260905` on both sides. Planning route: unchanged
  (PG-shaped search default). Movement: none. Parent: M0142-0008a-3.

## 5. Risks / fail-closed analysis

- Wrong-SJInfo failure modes are already gated: `semiAntiOnQualsOK`
  declines links whose on-quals don't map cleanly onto lhs|rhs leaf bits;
  `createUniquePath`'s schema-drift guard declines `SemiRhsExprs` that
  don't match `child.Output()`'s `SourceTableIdx`. Both fail closed.
- `Join.SJInfo` is consumed only by the seam walk and carried through to
  plan nodes (`createplanjoin.go:594`, `createplannl.go:158`); setting it
  cannot change anything for statements whose plans never reach the seam.
- NOT-IN (`NullAware`) ANTI links get an SJInfo too — `jointypeForDirection`'s
  unique-ify fallback gates on `JoinSemi`, so ANTI is inert there regardless.

## 6. Resume notes

Reachability is still zero after landing — measured cause (Loop #31):
every IN-subquery statement declines at `leaf-count`
(`joinsearchseam.go:325`, `len(scans) != nprefix+len(semiAnti)`) BEFORE
the sjinfo gate runs — `extractSearchLeaves` flattened the walked chain
to only the real outer leaves (Q56: nrels=4, nleaves=2, no synthetic
leaf ever added, i.e. the pinned Semi join did not sit in the chain the
walk flattened, or its subtree was not flattenable). The producer's
SJInfo is therefore never consulted on the corpus today — verified only
by the unit tests' direct `extractSearchLeaves` call.

The next suspects, in order: (a) why the walked chain for an
IN-unnested statement contains the pinned join in a position the walk
cannot link — check what `predp.go`'s Phase B `chain == spineJoins[0]`
actually hands over for `Filter(Semi(...))` trees (the Filter wrapper
above the join is a plausible flattening blocker); (b)
`semiAntiOnQualsOK` (per-link pred shape for IN — operands reference
`in.Operand`, an arbitrary expr); (c)
`problemPairsOuterWithDerived`'s derived-leaf guard (c8's finding — a
multi-rel IN RHS is opaque and legitimately declined there; the
firewall stays per the owner constraint).
