# M0145-0005 — single-pass DP over the jointree

Status: slice 1 landed (knob-arm `splitOuterSpine` retired) and slice 4
landed (one-relation + degenerate scopes through the same entry) /
remainder sliced below. Task: `.ralph/fix_plan.md` M0145-0005. Parent:
M0145-0001 (IR contract + retirement matrix), M0145-0003 (semi/anti leaf
entries), M0145-0004 (appendrel leaves). Kind: impl.

## What this is

Task text: *the search consumes the IR directly; semi/anti entries are legal
searched partners via a `join_is_legal` port over the SJInfo-equivalent,
joinrels.c:350.* The end state is one DP problem per jointree scope — every
leaf a searched item, every outer/semi/anti link a `SpecialJoinInfo` in
`root->join_info_list` constraining ordering through `joinIsLegal` — with the
node-tree decomposition, spine pinning, and splice-time re-resolution all
retired.

## Recon finding: the substrate is already complete

Mapping the retirement matrix against the code (2026-09-21) found the DP
machinery itself finished; what holds the task open is problem *construction*:

- `joinSearchOneLevel` is already PG's three-phase DP (left/right-sided,
  bushy, clauseless) — `make_join_rel` at joinrels.c:700+.
- `joinIsLegal` is the joinrels.c:350 port in full: SEMI/ANTI matching with
  unique-ified-RHS handling, `reversed` swap, LEFT/FULL MinLefthand
  association constraints, illegal-pair rejection.
- `buildJoinRelRestrictList(outer, inner, sjinfo)` already implements PG's
  outer-join clause distribution — nullable-side clauses admitted as
  post-join filters (`isOuterJoinFilterClause`), both-side clauses as join
  quals.
- `joinPublishesInner(sjinfo)`, `sizeJoinRel(…, sjinfo)`,
  `addPaths(…, sjinfo)`, `Path.Jointype`/`Path.SJInfo`,
  `planJoinTypeFor` → executor `JoinType{Left,Right,Semi,Anti}` — the whole
  path from legality to emission is wired (FULL refused at pathgen, executor
  capability, per the C-03c ledger row).
- `extractSearchLeaves` already descends INTO `Join{Left|Right}` children:
  member leaf scans, leaf-range relsets, ON-qual rebasing
  (`rebaseChainQual`), and the `outerChainLink` records feeding
  `outerLinksHaveSJInfos`/`outerOnQualsOK`/`deriveOuterLinkConstants` — all
  exercised in production today by *nested* pinned outer items
  (`a JOIN (b LEFT c)` — e.g. TPC-DS Q72's problem items 2-3).
- `makeRelFromJoinlist` already recurses pinned LEFT/RIGHT items as
  searched sub-problems (the `default:` arm), with a fail-closed SJInfo
  presence check — `make_rel_from_joinlist` (allpaths.c:3352).

What remains is therefore construction-side, not capability-side:

- `splitOuterSpine` peels top-level pinned items into a node-chain spine —
  but see §"Slice 1": the only pins left are FULL and outer-over-FULL,
  which the search can never build, so the peel is already dead on
  reachable inputs;
- `extractSearchLeaves` still derives leaf scans, widths, and ON quals by
  walking the NODE chain rather than reading the IR — the source of the
  spans/offset/remap validation family;
- semi/anti members arrive as `semiAntiChainLink` synthetic appended
  leaves instead of numbered jointree entries;
- Phase A/B (`runJoinSearchBelowPinned`) still searches twice around the
  pinned semi/anti spine;
- the `prefix-size`/`isSimpleSingle`/`GOOPG_ONEREL_SEARCH` floor still
  short-circuits one-relation scopes PG plans unconditionally.

## Slice 1 — retire the peel on the knob arm (this loop)

Implementation recon sharpened the recon above: since C-04a/b relaxed LEFT
and RIGHT to collapse-dependent, **`joinPinned` pins only FULL** (plus a
LEFT/RIGHT stacked over a FULL pin via `pinnedOverAPinnedSide`), and
`spineLinkSearchable` never certifies FULL — so a *certified non-empty
spine is unreachable*. Every reachable statement is one of:

- **no pinned items** — `a LEFT b LEFT c` deconstructs to flat leaf items
  `[a,b,c]` + two `SpecialJoinInfo`s; `innerPrefixBelowOuterSpine` peels
  nothing, `spine` was already empty, and the whole statement was already
  a single searched problem on BOTH arms (the walk's `outerChainLink`s
  feed `onOuter` conjuncts; `joinIsLegal` orders the DP);
- **a pin the search cannot build** — FULL or outer-over-FULL: the peel
  failed certification and the statement fell back to the syntactic tree.

The peel/splice machinery (`splitOuterSpine`, `spineLinkSearchable`,
`prefixNullable`, `traceSeamSpine`, the `low.Left = searched` graft) was
therefore already dead on every reachable input. Slice 1 makes the knob
arm stop calling it: `tryPGShapedJoinSearch` feeds `node` and
`ctx.joinlist` to the machinery directly, and pinned-top statements now
decline one gate later — `extractSearchLeaves`'s leaf-count check (a FULL
join is an opaque leaf while the joinlist counts its members) or
`makeRelFromJoinlist`'s `pinnedUnsearchable` arm — with the same
`used=false` fall-back to the syntactic tree. `spineAbove` reads false
uniformly on the arm, so `outputEligible` no longer special-cases a spine
that cannot exist there.

No reachable behaviour changes on either arm: the flat-outer-join path is
byte-identical (spine was already empty), and FULL-topped statements still
decline. The change is purely the retirement — the first mechanism of the
retirement matrix gone from the jointree arm's live path, kept for the
legacy arm until M0145-0008.

### Corpus witnesses

Because the slice changes no reachable plan shape, the evidence is
identity: TPC-H Q13 (`customer LEFT JOIN orders`), TPC-DS outer-join
queries, and the FULL-join statements must produce identical plans and
row counts on both arms — verified by the spotcheck, SF0.25 sweep, and
acceptance arm. White-box pins in `joinsearch_m0145_test.go` cover the
searched-spine invariant, the both-arms parity, and the FULL fail-closed
path.

## Slice 4 — one-relation and degenerate scopes through the same entry (landed)

`make_one_rel` runs `set_base_rel_pathlists` (allpaths.c:221) for every
base rel unconditionally, before `make_rel_from_joinlist` ever counts
items — so the jointree arm's correct semantics is "every scope reaches
the seam; the seam's own gates decide". Slice 4 lifts the three places
the knob arm still routed scopes around the search:

- `planSelectImpl`'s `isSimpleSingle` node-building bypass and the
  WHERE-arm rule-chooser guard each gain `&& !jointree` — a
  single-FROM-item statement now plans through `planFromClause` and the
  generic WHERE arm (Filter → pull-up → `tryJoinSearch`), so a flat
  correlated `EXISTS`/`NOT EXISTS` over one table reaches
  `pullUpSublinksIntoJointree` for the first time instead of the
  post-hoc unnest.
- The filterless arm's `joinTreeHasOuterLink(node) || appendrelMember`
  gains `|| jointree` — every WHERE-less scope reaches the seam on the
  knob arm: a bare `SELECT … FROM t` (the one-relation problem) and a
  filterless inner join alike. The "gated round" the legacy arm's
  comment defers is what the pipeline knob itself gates.
- The seam floor takes the PG-faithful value unconditionally on the arm:
  `(jointreePipeline || ctx.appendrelMember) && floor > 1` — nprefix=1
  is a searched problem. `GOOPG_ONEREL_SEARCH` keeps governing the
  legacy arm alone; `minSearchRels`'s other readers were already inside
  the lifted guards, so the env knob's whole remaining meaning is the
  legacy floor.

The one-relation protocol itself (`makeRelFromJoinlist`'s
`jl.nrels()==1` arm) is unchanged and stays gated on `scanLeafFor`
rebuildability + `r.info.table != nil`: a real-table leaf gets base-rel
path generation and the searched-subtree tag; a non-table single leaf
(CTE scan, subquery, function, VALUES) declines inside and keeps the
syntactic leaf — value-identical fail-closed, same as the member-scope
protocol M0145-0004 already exercises end-to-end.

What the lift intentionally changes: the rule-chooser's rewrites —
`injectLikeRangePredicates`, `reduceNotNullQuals` (including the
always-false → childless `Result{OneTimeFilter: false}` shape),
`planIndexScanFromWhere` — are skipped on the knob arm, exactly the
trade E-21 documented for `GOOPG_ONEREL_SEARCH`: value-preserving
missed optimisations, never wrong answers (PG performs the reduction
inside `distribute_qual_to_rels`; the generic arm does not implement it
for any arm — ledgered).

A latent crash the lift exposed and fixed in the same commit:
`pullUpExistsBody` called `resolveExpr(sub.Where, bodyCtx)` unguarded —
a WHERE-less EXISTS body (`EXISTS (SELECT 1 FROM t)`) is a nil Expr and
the resolve walked it. Reachable before this slice through multi-table
outers on the knob arm, newly reachable through the far more common
single-table shape; the body now contributes an empty conjunct set and
declines at the correlation check, the outcome upstream's
`contain_vars_of_level(whereClause, 1)` prescribes.

Two test premises were also retired, not weakened: the dual-pipeline
"delegating stub" byte-equality pin
(`TestJointreePipelineDispatchDelegates`) and the pull-up
decline-parity byte-equality pin both predated any searched
single-table scope; they now assert the real contract — same plan
shape, same decorrelation outcome, same join type — plus the
routing marker (`treeHasSearched` on knob-on, absent knob-off).

### Corpus witnesses

- TPC-H Q1 / Q4 / Q6 — single-FROM-item statements; under the knob they
  now produce searched plans (Q4's `EXISTS` becomes a searched SEMI
  join through the pull-up, matching PG's
  `pull_up_sublinks`-before-join-planning order).
- Every single-table subquery/CTE member in the corpus — same routing.
- Filterless inner joins under the knob — searched for the first time;
  the SF0.25 capture's moved-plan names are the measurement.

## Remaining slices (ledgered)

| slice | scope | retires |
|---|---|---|
| 2 | semi/anti as real leaf items in the jointree problem — leaf entries numbered in-binding-order (M0145-0003's `pulled` entries already carry spans/SJInfos) instead of synthetic appended slots | `semiAntiChainLink` synthetic-leaf splice, `remapWalkOrderFlatToSpans`, `semiAntiOnQualsOK`, `admitSemiAnti` flag, the `searchJl` leafItem-append workaround |
| 3 | IR-direct leaf materialisation: `jtScope` (or the resolveContext's already-built binding table) supplies bindings/spans/neededCols/outputCols without walking the node chain; clause distribution by coordinate ownership | `extractSearchLeaves`, `buildLeafSpans` as node-walk mechanism, `spans`/`offset-disagreement`/`residual-hits-pad` validation family, `localizeExprToLeaf`, `rebaseChainQual`/`rebaseSemiAntiChainQual` |
| 5 | misc decline-family retirement as corpus admits | `outer-on-qual`, `inner-on-qual-*`, `outer-over-derived` (post-B-06), `pushPredicatesIntoCrossJoins`/`pushSingleSideQualsIntoInnerJoinInputs`/`rewriteScanInputsWithSingleTablePredicates`/`pushOuterQualsIntoLaterals` |

The executor-capability refusals (FULL hash, partial shapes the executor
cannot run) stay at path generation until the D3 executor substrate lands —
they are not planner-flow divergence.

## Oracle anchors

| PG | goopg |
|---|---|
| `deconstruct_jointree` (initsplan.c) | `deconstructJointreeScopedSJI` (collapse.go:378) — already emits `joinlist` + `joinInfoList` |
| `make_rel_from_joinlist` (allpaths.c:3352) | `makeRelFromJoinlist` (relfromjoinlist.go:315) — recursion incl. pinned sub-joinlists |
| `join_is_legal` (joinrels.c:350) | `joinIsLegal` (joinsearchlevel.go) |
| `build_joinrel_restrictlist` (relnode.c:1285) | `buildJoinRelRestrictList` (joinrestrict.go:357) |
| `distribute_qual_to_rels` outer-join filter marking | `isOuterJoinFilterClause` (joinrestrict.go:392) |
| `add_paths_to_joinrel` jointype pass (joinpath.c:2398) | `addPaths(…, sjinfo)` → `Path.Jointype` → `planJoinTypeFor` |
| `make_one_rel` unconditional base-rel pathlists (allpaths.c:221) | the floor slice 4 retires (`prefix-size`/`GOOPG_ONEREL_SEARCH`) |

## Measurement

- Units: white-box pin that a certified two-link LEFT spine reaches
  `searchOneProblem` under the knob (searched `Join{Left}` emission,
  SJInfo-driven legality) and that the legacy arm is unchanged.
- `scripts/tpch-spotcheck.sh` (Q12/Q13 canonical counts; Q13 is the
  slice's own witness).
- `scripts/tpcds-sf025-regression.sh sweep` — value identity on the knob
  arm; capture diffs name the moved outer-join witnesses.
- TPC-H acceptance arm (`PGSHAPED=1` + `JOINTREE_PIPELINE=1`) — 22/22
  value identity.
