# M0145-0005 — single-pass DP over the jointree

Status: slices 1 (knob-arm `splitOuterSpine` retired), 2 (pulled
semi/anti as real leaf items), 3 (IR-direct leaf materialisation —
`jtScopeTable`/`extractScopeLeaves`), 4 (one-relation + degenerate
scopes through the same entry) and 5-partial (searched-subtree opacity
for the residual pushdown family) landed; the rest of slice 5 is
ledgered below. Task:
`.ralph/fix_plan.md` M0145-0005. Parent:
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
  leaves instead of numbered jointree entries — slice 2 retired this
  for pulled bodies (real `joinlist` leaf items now); only
  chain-extracted Semi/Anti still arrive that way;
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

## Slice 2 — pulled semi/anti as real leaf items (landed)

M0145-0003's pulled bodies already carried everything a real leaf entry
needs (scans, widths, body-local bindings, bound quals, and enough
material to build the SpecialJoinInfo). What kept them synthetic was a
numbering accident: `ctx.joinlist` is built in `planFromClause`, before
WHERE resolution discovers the pullable sublinks — so the pulled leaves
had no joinlist positions and the seam appended them as synthetic slots
behind `semiAntiChainLink` records, with a walk-order→spans remap to
paper over the coordinate split. Slice 2 numbers them instead:

- **`pullUpSublinksIntoJointree` appends one `leafItem` per pulled leaf
  to `ctx.joinlist`** — `pu.base` = the joinlist's relation count at
  pull-up time, `pu.nLeaves` = the total pulled count — exactly as
  `pull_up_subqueries` appends the subquery's RTEs to the parent's
  range table. `ctx.joinlist`'s only consumer is the seam, so the
  append cannot desync another reader; the pulled items are real
  searched members the moment the record exists.
- **`splicePulledLeaves`** (replacing `integratePulledSublinks`)
  inserts the pulled scans/widths at `[nReal, nprefix)` — their
  joinlist positions — ahead of the chain-extracted synthetic tail, and
  shifts every extracted leaf index at-or-above `nReal` up by
  `nPulled` (link hands, extracted SJInfo fields, outer-link sides,
  inner-ON `belowNullable`). The tail invariant — non-emitting leaves
  occupy `[nReal, nleaves)` — is preserved, so the leaf-count,
  tail-order and offset-agreement checks run unchanged; they just now
  treat pulled leaves as non-emitting rather than synthetic.
- **`classifyPulledQuals`** (replacing `buildPulledSemiAntiLink`)
  rebases each body's quals directly into problem space and classifies
  them `distribute_qual_to_rels`-style — spanning correlation and
  body-local conjuncts into the conjunct pool (the partition lands
  them on the semijoin's join clause / inside the RHS), SEMI-only
  outer-local hoists into `pu.outerQuals` — and appends each body's
  `SpecialJoinInfo` to `ctx.joinInfoList`. No link record survives.
- **A pulled-leaf construction arm** between the emitting loop and the
  synthetic loop gives each pulled leaf a span-offset binding with the
  real `table`/`alias` off its `*SeqScan` (flattenPulledBodyTree's
  guarantee — anything else is a desync decline) and the same
  `estimateBaseRelInfo` + `applyRelSizeFallback` stats path the
  flattened-chain arm uses, marked `isSemiAntiSyntheticLeaf` so the
  boundary filler treats its coordinates as non-emitting.

Retired on this slice: `integratePulledSublinks`,
`buildPulledSemiAntiLink`, the pulled half of the synthetic-leaf splice
(pulled bodies produce no `semiAntiChainLink` at all — the white-box
tests pin `len(semiAnti)==0` after integration), the `admitSemiAnti`
flag itself (the one production call site had passed literal `true`
since b2 — dead flexibility), and the pulled share of the `searchJl`
leafItem-append workaround (it survives only for chain-extracted
leaves). `remapWalkOrderFlatToSpans` stays — the chain arm still needs
it — with a `pulledBase`/`pulledLeaves` argument translating walk
indexes across the insertion; `semiAntiOnQualsOK` stays for the same
reason. `pgShapedOffsetChecksOK` now takes the non-emitting RelSet
directly (`leafRangeRelSet(nReal, len(scans))`), which collapses the
pulled and synthetic cases into one skip-set.

### Evidence

White-box: `TestJointreePullupRealLeafItems`/`…Anti` drive pull-up →
splice → classify directly and pin the leaf item in `ctx.joinlist`
(`pu.base==1`, `it.rel==1`), the spliced scan position, the cumulative
(non-synthetic) span, the pooled correlation conjunct's `{0,1}` relids,
the `JoinSemi`/`JoinAnti` SJInfo on `joinInfoList` — and
`len(semiAnti)==0`, the retirement itself. A DPTRACE probe confirmed
the search enumerates the pulled leaves as real problem rels
(`rels=jtp_o,a,b`) and prices the SEMI pair. Gates: optimizer suite,
tpch-spotcheck (Q12=2/Q13=33), TPC-DS SF0.25 96/96 with 99/99 plan
shapes identical, TPC-H acceptance arm 24/24 under
`JOINTREE_PIPELINE=1`+`PGSHAPED=1`.

## Slice 3 — IR-direct leaf materialisation (landed)

`extractSearchLeaves` re-derived the search problem's leaf scans and
link quals by walking the built NODE chain — a second traversal of
structure `planFromItem` had just walked, re-deriving leaf order, leaf
ranges, link types, and the `width`/`realWidth`/`belowNullable`/
`preserved` bookkeeping every coordinate decision below the seam reads.
The walk existed because the information was never recorded. Slice 3
records it: a **`jtScopeTable`** (jointreescope.go) emitted at
construction time in exactly the DFS order the walk produces.

- **`planFromItem` emits the table as a fold-machine mirroring the
  walk's dispatch**: the item's base node is leaf 0; each join adds one
  link and one right-side leaf — descendable (Inner/Cross/Left/Right)
  joins record an inner/outer link plus a plain leaf, demoted Semi/Anti
  (the only Semi/Anti a `planFromClause` chain can carry) record a
  semi/anti link plus a SYNTHETIC leaf, and any other join type (FULL)
  folds the accumulated chain into one opaque leaf, discarding the
  leaves and links inside it — the walk's "not an admitted join type"
  arm verbatim.
- **`planFromClause` concatenates the per-item tables** in FROM order
  (`appendTable` shifts the incoming link ranges by the accumulated
  leaf count, the same shift it applies to binding offsets across the
  comma items) and pins the result to the chain root:
  `rctx.jtScope.root = root`.
- **`extractScopeLeaves`** (joinsearchseam.go) rebuilds the identical
  `(scans, widths, onQuals, outerLinks, semiAnti)` tuple from the
  table. Every quantity is the walk's own arithmetic re-keyed onto leaf
  ranges: `width`/`realWidth` are prefix sums over the leaf table
  (synthetic leaves skip `realWidth`); `base`/`rightBase` are the
  accumulators sampled at `loLeft`/`loRight`; `belowNullable` is a
  range-containment union over already-processed outer links' subtree
  ranges; `preserved` is the descendant test — a link is unpreserved
  exactly when a RIGHT-typed link's range encloses it, since every link
  sits in its ancestors' LEFT subtree (a right subtree is always one
  leaf). The rebases, the keyed-pred fold, the FlattenedRHS expansion
  and the `MinLefthand`/`MinRighthand` narrowing run unchanged on
  `jn.Predicate` — the table changes WHERE the structure comes from,
  not what the quals say.
- **The root-pointer pin** is the fail-closed contract: the seam reads
  the table only when `jointreePipeline && ctx.jtScope != nil &&
  ctx.jtScope.root == chain`. A chain the table was not built beside —
  the S5a post-unnest Phase B chain is the reachable one — falls back
  to `extractSearchLeaves` unchanged, so a pre-search rewrite that
  grafts a different root can never be mis-described by stale
  construction-time metadata.

Retired on the jointree arm: `extractSearchLeaves`'s node walk (the
function itself stays — the legacy arm and the root-pin fallback still
use it). What did NOT retire, contra the original ledger line: the
coordinate-translation family (`rebaseChainQual`,
`rebaseSemiAntiChainQual`, `remapWalkOrderFlatToSpans`,
`buildLeafSpans`, `localizeExprToLeaf`, `pgShapedOffsetChecksOK`). The
slice's ledger framed them as walk machinery, but they are not
discovery — they translate between two coordinate spaces that both
still exist (a Join node's Predicate is written in its own concat
coordinates whatever path reads it, and the problem's span space is a
deliberate re-keying of leaf order). Removing the walk removes the
second traversal, not the spaces.

### Evidence

White-box: `TestScopeExtractionMatchesWalk` runs both extraction paths
over a 20-shape matrix (comma items, inner/outer/RIGHT chains,
multi-item mixes, the LEFT inner-only `Filter{LeafLocal}` wrap, the
FULL fold, grouped-join opaque leaves, the `!preserved` decline, and
leading/non-leading demoted-ANTI synthetic leaves) and pins field-wise
identity — same scan NODE POINTERS, equal widths, identical rebased
preds (reflect.DeepEqual), equal preserved/nullable/belowNullable
relsets, equal semiAnti links including `sjinfo` pointer identity and
bodyQuals. `TestScopeExtractionDeclinesLikeWalk` pins the fail-closed
arms (descendable-join leaf, nil leaf). A DPTRACE probe confirmed the
search enumerates scope-extracted problems end-to-end (pulled EXISTS:
`rels=a,b`, SEMI pair priced, searched subtree emitted). Gates:
optimizer suite, units, tpch-spotcheck (Q12=2/Q13=33), TPC-DS SF0.25
96/96 with 99/99 plan shapes identical, TPC-H acceptance arm 24/24
under `JOINTREE_PIPELINE=1`+`PGSHAPED=1`.

## Slice 5 — searched-subtree opacity for the residual pushdown family (landed, partial)

Decline census (`tmp/m0144-0011a2-census.log`, pre-slice-3): 132
seam-decline fires — `leaf-count` 104, `outer-over-derived` 12,
`outer-spine` 8 (since retired), `lateral` 8. `outer-on-qual` and
`inner-on-qual-*` have **zero corpus witnesses** — there is no decline
class to migrate; those rows are retired by absence rather than by
code.

- `outer-over-derived` stays: hard firewall on B-06 CTE-output
  statistics (R42/Q78 class), not planner-flow divergence.
- `lateral` stays: `chainCarriesLateral` fires on real lateral deps
  (Q30/Q68); retiring it needs parameterized-path legality — a project
  of its own.
- `leaf-count` — dominant family. A joinlist-vs-bindings probe ruled
  out grouped `j.Right` joins (they are one binding AND one joinlist
  item — admitted as 2-rel problems). The fires are FULL-join folds
  (opaque leaf covering multiple joinlist rels, `a FULL JOIN b JOIN c`
  → nprefix 3, scans 2) — the executor-substrate-blocked class the doc
  already ledgered as staying at path generation.
- The pushdown family cannot die wholesale — the four passes are
  post-search cleanups that declined and legacy-arm statements still
  need. What it can do is stop crossing the searched boundary; the
  audit found three of four already opacity-correct via `pushOneConjunct`/`rewriteJoinsToNLI`'s P5.9-b prunes.

Landed: two holes closed, one counter-pin pinned.

- `pushSingleSideQualsIntoInnerJoinInputs` had no `isSearchedTree`
  awareness anywhere: the walker descended into searched roots, the
  Filter-level entry (`pushInnerJoinInputQuals`) planted conjuncts onto
  a searched join's inputs, and the `pushConjunctIntoSubtree` descent
  could reach searched grandchildren. Fixed: top-level walker prune,
  searched-child refusal in `pushInnerJoinInputQuals`, and a
  `pushTrace.noSearched` flag (`pushConjunctIntoSubtreeTracedNoSearched`)
  on all statement-level descents — including the sibling-seeding in
  `deriveConstAcrossJoinEquality`.
- `findUniqueSeqScanByColumn` (`absorbConjunctsIntoSubtree`'s hunt)
  walked into searched subtrees — a residual `col=const` could find
  and IndexScan-rewrite the scan the costed search elected, and for a
  nullable-held conjunct that's the wrong-answer class (pushed below
  the null-extension it keeps rows the residual drops). Fixed: prune
  at `isSearchedTree`.
- Counter-pin: `pushConjunctIntoSubtree` stays permissive. The
  CTE-inline pass's conjuncts arrive from OUTSIDE the searched body's
  scope — crossing the boundary is the parse-level qual pushdown PG
  itself performs (R42's measured witness). A blanket guard there is
  the regression, not the fix.

Pins (`searched_opacity_test.go`): searched-root and searched-
grandchild refusal, unsearched-side positive control, CTE-inline
crossing counter-pin, scan-hunt opacity + unsearched findability.

## Slice 4's missed optimisations — the NOT NULL reduction (landed 2026-09-21)

Slice 4 routed the jointree arm's single-table+WHERE scopes through the GENERIC
arm so their base rels carry a real pathlist. The rule chooser it bypassed was
doing two things nothing else does, and the doc recorded them as
"value-preserving missed opts": `injectLikeRangePredicates` +
`planIndexScanFromWhere`, and `reduceNotNullQuals`.

"Value-preserving" is true and is not the whole story. `reduceNotNullQuals` is
goopg's port of `restriction_is_always_true`/`restriction_is_always_false`
(initsplan.c's `add_base_clause_to_rel`), so skipping it does not produce wrong
rows — it produces a plan PG would never emit, and it becomes a REGRESSION the
moment M0145-0008 flips the knob. Measured before the fix:

| statement | default arm | knob arm |
|---|---|---|
| `WHERE not_null_col IS NULL` | `Result` (childless, One-Time Filter false) | `Filter{SeqScan}` |
| `WHERE not_null_col IS NOT NULL` | bare `SeqScan` (qual dropped) | `Filter{SeqScan}` |
| `WHERE nn IS NULL AND k = 5` | `Result` (childless) | `Filter{SeqScan}` |

The reduction now runs in the generic arm too, gated to the jointree arm and to
single-binding scopes — the same condition the chooser uses. Two details the
placement forced:

- The always-false arm builds the CHILDLESS `Result` for the reason the chooser
  states: PG emits no scan under a false One-Time Filter.
- Downstream, `whereQual != nil` is read as "there is a Filter to search
  under", and the pre-DP arm asserts `node.(*Filter)` on that basis. A reduced
  scope has neither, so the clause is marked spent (`whereQual = nil`) and the
  whole sublink/pull-up/search block is skipped. Without that, an always-false
  WHERE that still contained an `EXISTS` would have reached an unchecked type
  assertion on a `*Result`.

Knob-gated deliberately: on the default arm this generic arm is reached by
MULTI-relation scopes, and a single-binding scope only lands here under
`GOOPG_ONEREL_SEARCH`. Widening it there is corpus-visible and is ledgered
rather than smuggled in.

Evidence: arm-equality pins (`notnull_reduce_jointree_test.go`) assert the two
pipelines agree on all three shapes — the property M0145-0008 depends on.
Default-arm gates plan-identical (`same=99 changed=0`); knob-arm sweep
`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`; acceptance arm 24/24.

The LIKE-range pair (`injectLikeRangePredicates` + `planIndexScanFromWhere`)
is still skipped on the knob arm and stays ledgered — it is an index-selection
optimisation, not a PG-shape rule, so it does not carry the same cutover risk.

## The LIKE-range pair: measured, and the ledgered resume point corrected (2026-09-21)

The third of slice 4's skipped optimisations is `injectLikeRangePredicates` +
`planIndexScanFromWhere`. Two measurements decided not to port it, and the
second one corrects what the ledger previously said to do.

### Corpus witnesses: zero

TPC-H's query set carries no `LIKE` predicate at all. TPC-DS carries exactly
two, both `hd_buy_potential LIKE 'Unknown%'` — a prefix pattern that could use
an index — but `household_demographics.hd_buy_potential` has no index in the
benchmark schema, so there is no index path to elect on either arm. The gap is
real for user queries and invisible to both gates.

### The previously-ledgered resume point is HAZARDOUS as written

It read: "teach the search's base-rel pathgen about the synthetic range
conjuncts, so `tryRangeIndexScan` can elect the index through the search". Done
literally — feeding the synthetic `col >= 'foo' AND col < 'fop'` into the rel's
restriction clauses — that DOUBLE-COUNTS the LIKE's selectivity: the same
restriction is estimated twice, once as the LIKE and once as the range, and the
rel's row estimate collapses. That is the silent cost-model damage class this
project has been bitten by before, and it would be invisible to every gate
because no corpus query exercises it.

PG does not do that. `like_support.c`'s header states the mechanism exactly:
the derived clauses are *approximate INDEX-SCAN quals* — "any tuples that pass
the operator clause itself must also satisfy the simpler indexscan condition"
— and the original operator is re-applied as a qpqual, "in essence, we're using
a regular index as if it were a lossy index". The derived bounds never become
restriction clauses, so the estimate keeps coming from the LIKE alone.

The chooser arm's existing `injectLikeRangePredicates` is closer to this than
it looks: it injects into a SEPARATE copy (`whereForIndex`) handed only to
`planIndexScanFromWhere`, while the Filter keeps the original `whereQual`. The
port therefore has to reproduce that separation at path level — derived bounds
as the index condition, original LIKE as the recheck and as the selectivity
source — not merge the two lists.

Corrected resume point recorded in `.ralph/deferral_ledger.md`.

## Remaining slices (ledgered)

| slice | scope | retires |
|---|---|---|
| 5 (partial) | decline families without corpus witness or with hard blockers | `outer-on-qual`, `inner-on-qual-*` (retired — zero corpus fires); `outer-over-derived` (post-B-06), `lateral` (needs parameterized-path legality) remain ledgered |

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
