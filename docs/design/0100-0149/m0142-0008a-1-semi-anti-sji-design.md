# M0142-0008a-1 — SEMI/ANTI decorrelation as a DP search participant: PG legality read + concrete goopg design

Status: design-only, no production diff. Filed by M0142-0008a's scoping recon
(`docs/design/0100-0149/m0142-0008a-scoping-recon-semi-anti-decorrelation-census.md`).
This doc is the K24-mandated "further scoping pass" for -2/-3: a full read of
PG's `join_is_legal`/`SpecialJoinInfo` construction machinery, followed by a
concrete mapping onto goopg's *current* code — not the code as M0142-0008a's
recon described it, which turns out to have been an incomplete picture (see
§1).

## 0. Summary of findings (read this first)

1. **This is PG's own previously-scoped-and-deferred "S5b" work item, not a
   fresh gap.** It was named, designed at a sketch level, and deliberately
   deferred with an explicit, checkable reopen criterion in 2026-07-21. M0142-0008a's
   recon did not find or cite this — §1 below is the correction.
2. **goopg already has the exact data structure and legality algorithm PG
   uses** (`SpecialJoinInfo`, `joinIsLegal`), fully ported and unit-tested
   for `JOIN_SEMI`/`JOIN_ANTI` — §2. M0142-0008a-1's original framing ("read
   PG's machinery and design a goopg data structure") is therefore already
   half-answered: **no new struct and no new legality algorithm are needed.**
3. What's actually missing is **construction and wiring**, not legality
   logic, and it has *four* separable holes, not one — §3. The costliest of
   the four (§3.4) is a real, previously-undocumented regression risk that
   any implementation of -3 must address before it can land: goopg's generic
   DP joinrel path generator currently **declines to build a hash join for
   SEMI/ANTI at all**, while `unnestExistsExpr` builds one directly today for
   every census query that has an equijoin pair. Wiring -3 naively — without
   also lifting that decline — would make hash-decorrelated queries (Q4,
   Q21, Q22, TPC-DS query10/16/35/69/94) regress to nested-loop-only.
4. §4 gives per-item concrete resume points for -2 and -3, each tied to file
   + line, plus the open questions a future scoping pass still needs before
   coding starts.
5. **M0142-0008a-3(iii)'s trace-through is now done — CONFIRMED generic, not
   assumed.** §5 traces `createHashJoinPlan`/`planJoinTypeFor`/`joinInputsFor`
   (`createplanjoin.go`) and finds no `Jointype`-specific refusal for
   `Semi`/`Anti` anywhere in the Path-to-Join lowering; PG's
   `create_unique_path` entanglement the original comment worried about turns
   out to be real but narrowly scoped to ONE cost-refinement input
   (`hashJoinFinalCostInputFor`'s inner-unique optimisation), which already
   fails closed to a safe default for non-`JoinInner` rather than blocking
   anything. Lifting `jointypeForDirection`'s `nestloopOnly` gate is therefore
   executor-safe; the only residual cost is a costing-tightness gap, not a
   correctness risk.

## 1. This is S5b, and its reopen criterion may already be met

`internal/optimizer/predp.go`'s file header says outright:

> DP participation for semi/anti (S5b) is deferred by user decision
> (2026-07-21), so DP runs only on the subtree BELOW the pinned spine.

The full design is in
`docs/design/correlated-subquery-planning/03-planner-decorrelation-extensions.md`
§4.2 (D3.1): S5a ("pull sublinks up before join search, keep the resulting
semi/anti joins **pinned** as before") and S5b ("DP participation with
legality rules [so] plan shapes improve") were named as two separately
landable halves of the same reorder from day one, with S5a explicitly called
"the conservative first step". `docs/design/correlated-subquery-planning/IMPLEMENTATION-TODO.md`
Round 2 (`R2-4`/`R2-5`) shows S5a landed (`a607cf2b`) and S5b was deferred by
user decision in the same round, with the deferral ledger row spelling out an
explicit reopen test:

```
.ralph/deferral_ledger.md:487
| - | 2026-07-21 | csq-R2 | S5a sublink pull-up runs BEFORE join-order
search for EXISTS/IN WHEREs; semi/anti pinned above DP subtree;
reresolveJoinByName; GOOPG_UNNEST_PREDP=off rollback | S5b DP participation
deferred by user decision: prizes captured by NLI semi/anti; bushy reorders
historically regressed (M0076/Q5) | reopen if plan-compare names a PG-slower
query differing ONLY by semi/anti placement | risk/benefit judged poor;
pinned placement legal by construction |
```

M0142-0008a's own census (8 corpus queries: TPC-H Q4/Q21/Q22, TPC-DS
query10/16/35/69/94) is strong circumstantial evidence the reopen criterion
is met — these are exactly the queries whose plans should differ from PG on
semi/anti placement/algorithm and nothing else. **It is not yet a confirmed
match**: the ledger's wording is "differing ONLY by semi/anti placement",
and the census didn't check whether each of the 8 queries' PG-divergence is
isolated to placement/algorithm or entangled with other open gaps (row-count
error, missing Incremental Sort, etc. — several of these queries are also
named against unrelated M0141/M0138 gaps elsewhere in `fix_plan.md`). **-2/-3
should not proceed without first re-running plan-compare and confirming, per
query, that semi/anti placement/algorithm is the ONLY divergence** — otherwise
the same risk/benefit judgment that produced the 2026-07-21 deferral (low
measured prize, bushy-reorder regression precedent at M0076/Q5) may still
apply unchanged.

## 2. PG oracle: `join_is_legal` + `make_outerjoininfo`, and goopg's existing port

### 2.1 `join_is_legal` (`postgres/src/backend/optimizer/path/joinrels.c:350`)

Given two candidate rels and their proposed `joinrelids`, scans
`root->join_info_list` for every `SpecialJoinInfo` whose RHS overlaps the
proposal, and for each:

- skips SJs the proposal is still purely inside the RHS of (still building
  up that RHS) or that are already satisfied within one input;
- for `JOIN_SEMI`, also skips once the RHS has been joined to anything else
  within an input (post unique-ification, no longer relevant);
- **admits** the join if one input's relids cover `min_lefthand` and the
  other's cover `min_righthand` (in either orientation — `reversed_p` records
  which), or — SEMI only — if the RHS is being unique-ified
  (`create_unique_path`) and joined to just one side;
- otherwise **rejects**, unless the SJ is a `JOIN_LEFT` whose ordering
  constraint the proposal can associate into (the `must_be_leftjoin` path —
  never applies to SEMI/ANTI, which always reject a bad overlap outright).

A LATERAL-reference check follows (irrelevant here — see §4's open questions,
goopg's `EXISTS`/`NOT EXISTS` pull-up produces no LATERAL reference; it is a
plain correlated-equality/residual predicate, not a `FROM ... LATERAL`
subquery).

### 2.2 `make_outerjoininfo` (`postgres/src/backend/optimizer/plan/initsplan.c:1708`)

Builds one `SpecialJoinInfo`, called bottom-up during jointree
deconstruction (so `root->join_info_list` already holds every SJ
syntactically below the current one):

- `SynLefthand`/`SynRighthand` = the join's syntactic sides, verbatim.
- `MinLefthand` = `clause_relids ∩ left_rels` (the correlation predicate's
  own referenced LHS rels); `MinRighthand` = `(clause_relids ∪
  inner_join_rels) ∩ right_rels`.
- A scan over every **already-built** SJ in `root->join_info_list`, growing
  `MinLefthand`/`MinRighthand` when the current join's LHS/RHS overlaps a
  lower join's RHS and ordering must be preserved to stay correct — for
  `JOIN_SEMI`/`JOIN_ANTI` specifically, PG *always* takes the "preserve
  ordering" branch on overlap (lines 1888, 1936-1939: `jointype ==
  JOIN_SEMI || jointype == JOIN_ANTI` is one of the disjuncts forcing it) —
  semi/anti joins never commute with a lower outer join, only their
  `MinLefthand`/`MinRighthand` can be forced wider by one.
- If either min side computes empty, punt to the full syntactic side (never
  leave a min side empty).
- `LhsStrict` = whether the clause is provably non-null-producing for some
  LHS rel — used only by `JOIN_LEFT`'s own identity-3 commutation check, not
  consulted for a `SpecialJoinInfo` whose own `Jointype` is SEMI/ANTI.
- `compute_semijoin_info` (not read in full — low priority here) populates
  `semi_operators`/`semi_rhs_exprs` for `JOIN_SEMI` only, used by
  unique-ification, not by ordinary hash/NL path generation.

### 2.3 goopg already has both halves, fully wired — for ordinary FROM-clause joins

`internal/optimizer/specialjoin.go` is explicitly "the goopg analogue of PG's
make_outerjoininfo" and its `SpecialJoinInfo` struct (lines 19-43) mirrors
PG's field-for-field, including the SEMI/ANTI-specific fields. Both are
**already exercised for `JOIN_SEMI`/`JOIN_ANTI`**:
`internal/optimizer/specialjoin_test.go` has 6 passing `TestJoinIsLegal*Semi*`
and `TestJoinIsLegal*Anti*` cases (`TestJoinIsLegalSemiMatch`,
`SemiReversed`, `SemiUniqueifiedSkip`, `AntiMatch`,
`AntiCannotAssociateIntoRHS`, `SemiCannotAssociateIntoRHS`, …).
`internal/optimizer/joinsearchlevel.go:198`'s `(*searchCtx).joinIsLegal` is
the goopg port of §2.1 in full, consulting `s.joinInfoList`.

The construction side — `makeSpecialJoinInfoScoped` (`specialjoin.go:127`) —
is the §2.2 port: it computes `MinLefthand`/`MinRighthand` from
`clause_relids` exactly as PG does, including the "always preserve ordering
for SEMI/ANTI on overlap" rule (lines 202-218, literally testing
`jointype == parser.JoinSemi || jointype == parser.JoinAnti` at the same two
sites PG does), and the "empty min → punt to syn" rule. It is called from
exactly one production site: `internal/optimizer/collapse.go:514`, inside
`deconstructJointreeScopedSJI`, which walks `s.FromExprs` — the **parser's**
explicit `JOIN ... ON` tree — assigning each leaf an index via a `sjiScope`
built for that same walk. `planner.go:3051` runs this once, early, per
statement: `rctx.joinlist, rctx.joinInfoList =
deconstructJointreeScopedSJI(s.FromExprs, ...)`, and `rctx.joinInfoList` is
what `joinsearch.go`'s `buildInitialRels`/`newSearchCtx` and
`joinsearchlevel.go`'s `joinIsLegal` consume for the rest of planning.

**Conclusion: there is no new struct to design and no new legality algorithm
to port.** `SpecialJoinInfo` + `joinIsLegal` + `makeSpecialJoinInfoScoped`
are all present, correct per their own unit tests, and already handle
`JOIN_SEMI`/`JOIN_ANTI` as jointypes. M0142-0008a-2's stated scope
("implement the SpecialJoinInfo-equivalent construction") is therefore
**smaller than filed** — see §4.1.

One caveat on "already handle SEMI/ANTI": today `makeSpecialJoinInfoScoped`
is **never actually called with `jointype == parser.JoinSemi`** in
production — the parser only emits `INNER/LEFT/RIGHT/FULL/CROSS`
(`specialjoin.go:105-108`), and the sole live ANTI producer is
`reduceOuterJoins`'s LEFT-JOIN-IS-NULL demotion (a *different* mechanism
from EXISTS/NOT EXISTS decorrelation — no correlated subquery involved). So
the SEMI/ANTI arms of `joinIsLegal` are tested in isolation but have never
been exercised end-to-end from a real query. That's a real, if narrow,
integration risk for -2/-3 to retire with new integration tests, not just
unit fixtures.

## 3. The actual gap, precisely localized

### 3.1 Wrong representation, wrong pipeline stage

`unnestExistsExpr` (`internal/optimizer/unnest.go:4110`) builds its
`Join{Type: Semi/Anti}` node by operating on the **lowered `Node` plan
tree** (`Filter`/`ExistsExpr`/`OuterColumnRef`, post `planFromExprs`), not on
`parser.FromExpr`. It runs from `planner.go:1534`
(`unnestSubqueriesInPlan(node)`, inside the S5a `unnestPreDPEnabled()`
branch) — **chronologically after** `rctx.joinlist`/`rctx.joinInfoList` were
already built and snapshotted at `planner.go:3051`, from `s.FromExprs` alone.
There is no code path today that calls `makeSpecialJoinInfoScoped` (or
anything else) for the semi/anti join `unnestExistsExpr` builds, and no code
path that appends anything to `ctx.joinInfoList` for it.

### 3.2 The search never sees the RHS as a participant

`runJoinSearchBelowPinned` (`internal/optimizer/predp.go:73`)'s `descend`
loop (lines 83-115) walks **only** `x.Left` at each pinned `*Join{Semi,Anti}`
node to find `origChain` — it never visits `x.Right` (the decorrelated inner
clone). The subsequent `tryJoinSearch(f.Child, f.Predicate, ctx, cat)` call
(line 139) therefore only ever builds `bindings`/`scans`/`relInfos` from the
ordinary FROM items on the LHS. The inner clone's base rel(s) never become
`RelOptInfo`s, so there is nothing for `joinIsLegal`/`makeJoinRel` to place
them against even if a `SpecialJoinInfo` existed.

### 3.3 `RelSet` numbering is per-search-call, not global — a real adapter problem

`internal/optimizer/joinsearch.go:388`'s `buildInitialRels` assigns
`RelSet(1)<<uint(i)` **from the position in that call's own `bindings`
slice** — not from `SchemaColumn.SourceTableIdx` (a separate, 1-based,
whole-statement counter from `planFromExprs`/`nextSourceIdx`). Any
`SpecialJoinInfo` fed to a given `tryJoinSearch` call must have its
`Syn/MinLefthand`/`Syn/MinRighthand` expressed in **that call's own leaf
ordinal numbering** — which is exactly why `ctx.joinInfoList`'s existing
entries only work today: they were built by the SAME walk of `s.FromExprs`
that also produced `bindings`' order (collapse.go's comment at
`planner.go:3038`: "the FROM walk that numbered these bindings is still the
current walk"). A pulled-up EXISTS's inner clone has no such correspondence
yet — assigning it one (and keeping it consistent across `origChain`'s
*already-established* numbering) is a concrete piece of plumbing -2/-3 must
add, not a conceptual gap.

### 3.4 The costliest hole: SEMI/ANTI are hash-declined in the generic joinrel path generator today

`internal/optimizer/joinpaths.go`'s `jointypeForDirection` doc (lines
141-148) and `addPathsToJoinrel` (`nestloopOnly := jt == parser.JoinSemi ||
jt == parser.JoinAnti`, line 265, gating out the entire `hash_inner_and_outer`
block at line 318) are explicit and deliberate: **the generic DP-search path
generator only ever offers nested-loop (`addNestLoopPath`/`addNLIPaths`) for
a `JOIN_SEMI`/`JOIN_ANTI` joinrel — never a hash join.** The doc's own
rationale: PG's hash path for SEMI is entangled with `create_unique_path`
unique-ification, which goopg has no analogue of, so the safe choice was to
decline the keyed operators outright ("costs nothing while the paths are
unreachable" — true only because nothing reaches this jointype through the
search today).

**But `unnestExistsExpr` already builds a HASH semi/anti join directly, every
time an equijoin pair exists** (`unnest.go:4306` onward — `outerKey`/
`innerKey` are hash build/probe keys; only the zero-equijoin-pair case
(`S4a`/D3.2, `unnest.go:4125`) falls back to nested-loop). That is the common
case for the census: Q4/Q21 (equality correlation columns), and the TPC-DS
"channel comparison" idiom (equality on a shared dimension key) all have at
least one equijoin pair. **If -3 wires `runJoinSearchBelowPinned`'s search to
own the semi/anti join's placement/algorithm without first lifting the
`nestloopOnly` gate for this specific case, every one of these queries
regresses from Hash Semi/Anti to Nested-Loop-only** — a correctness-safe but
likely severe performance regression on the census's larger inner sides
(e.g. Q21's anti join, TPC-DS's multi-million-row fact tables).

This is not a reason to avoid the gate — `addHashJoinPath`
(`internal/optimizer/pathgen.go:78`) already threads `Jointype: jt` straight
through to the `Path`/plan node the same way it does for `LEFT` (line
114-120's comment: "the operator and the join semantics are orthogonal") —
`createPlan`'s hash-join lowering is generic over `Jointype`, so there is
reason to believe the **executor** side is not actually the blocker PG's
`create_unique_path` entanglement describes for goopg; the entanglement the
doc cites is about *PG's* alternate unique-ify strategy, which goopg has
never needed because `unnestExistsExpr` already proves a direct hash-semi/
anti path works. This needs confirming (not assuming) before -3 can safely
lift the gate — see the open question in §4.2.

## 4. Concrete resume points

### 4.1 M0142-0008a-2, re-scoped

Original filed scope: "implement the SpecialJoinInfo-equivalent construction
for correlated EXISTS/NOT EXISTS, gated behind a rollback flag... without yet
changing joinsearchlevel.go's enumeration — a landable, unit-testable slice
on its own (no plan-shape change expected)."

Given §2.3, this is now: **teach `unnestExistsExpr` (or a small helper it
calls) to build a `SpecialJoinInfo` using the existing
`makeSpecialJoinInfoScoped` algorithm — not its `sc`/`item`/`lower`
parser-facing signature, which has no meaning here, but the same *shrink*
logic** (compute `clause_relids` from `params`/lifted residuals'
`OuterRef`/`SubCol` pairs intersected against `SynLefthand`/`SynRighthand`,
apply the SEMI/ANTI-always-preserve-ordering scan against `ctx.joinInfoList`
per §2.2's bottom-up rule, punt-to-syn on empty). Attach the result to the
`*Join` node as inert metadata (a new field, e.g. `Join.SJInfo
*SpecialJoinInfo`) with **nothing reading it yet** — satisfies "no plan-shape
change expected" trivially, since nothing consumes it. Needs new unit tests
mirroring `specialjoin_test.go`'s existing Semi/Anti cases but built from a
real `unnestExistsExpr` fixture (retires the "never exercised end-to-end"
risk flagged in §2.3's caveat). Sits behind the same `GOOPG_UNNEST_PREDP`
family or a dedicated sibling flag.

### 4.2 M0142-0008a-3, re-scoped into three separable increments

Original filed scope bundled "wire the enumeration" and "let addPath
cost-compare placement/algorithm" as one task. Given §3's four holes, that is
at least three separately landable, separately sizable pieces:

1. **RHS-as-participant (§3.2/§3.3).** Extend `runJoinSearchBelowPinned`'s
   descend to also walk `x.Right` per pinned join, feed the inner clone's
   base rel(s) into the SAME `bindings`/`scans`/`relInfos` triple as
   `origChain`'s ordinary items (giving them `RelSet` bits in the shared
   per-call numbering), and pass -2's now-correctly-numbered
   `SpecialJoinInfo` into `ctx.joinInfoList` for that call. **Open question
   this increment must answer, not assume:** should the inner clone's own
   internal joins (when the EXISTS body itself joins ≥2 tables — TPC-DS's
   store/web/catalog idiom needs checking per-query) become *separate* search
   leaves (full participation, matching what PG's `pull_up_sublinks_jointree_recurse`
   does — it recurses into `j->rarg` too), or should the whole RHS enter as
   one opaque pre-planned unit (matching today's `unnestExistsExpr`, simpler,
   smaller diff, still gets placement+algorithm choice for the semi/anti join
   itself)? The design bundle's own S5a/S5b split ("conservative first step:
   pin... (S5a); DP participation... (S5b)") suggests the atomic-RHS version
   is the right-sized S5b, with RHS-internal reordering as a further-future
   increment — recommend scoping -3 to the atomic-RHS version first and
   filing RHS-internal-reorder separately if it turns out to matter for the
   census.
2. **Legality wiring.** Once (1) makes the RHS a real `RelOptInfo`,
   `joinsearchlevel.go`'s `makeJoinRel`/`joinIsLegal` already handle
   `JOIN_SEMI`/`JOIN_ANTI` per §2.3 — this increment is mostly integration
   verification (the "never exercised end-to-end" gap) plus retiring
   `runJoinSearchBelowPinned`'s post-search spine re-resolution
   (`reresolveJoinByName`, predp.go:148-201) for the cases that are now
   natively searched — keep it for whatever stays pinned (Q22's
   `whereEligibleForPreDPUnnest` total-bypass class, per M0142-0008a's
   independent resume-point hint).
3. **Lift the hash-decline gate (§3.4).** Before (1)+(2) can avoid a
   regression, confirm whether `createPlan`'s hash-join lowering is actually
   generic over `Jointype: Semi/Anti` today (trace one `unnestExistsExpr`-
   built Hash Semi/Anti plan through `createPlan` and compare against what a
   search-generated `Path{Kind: PathHashJoin, Jointype: Semi}` would lower
   to) — if confirmed generic, narrow `jointypeForDirection`'s
   `nestloopOnly` gate (`joinpaths.go:265`) to exclude the natively-searched
   EXISTS/NOT EXISTS case (or drop it entirely and re-verify PG's
   `create_unique_path` concern doesn't apply), with `hashJoinCost`/row
   estimate functions audited for SEMI/ANTI-correct (non-multiplying)
   semantics per the existing doc comment's warning.

### 4.3 Before any of -2/-3 start

Re-run plan-compare for the 8 census queries and confirm, per query, whether
semi/anti placement/algorithm is the ONLY PG divergence (§1) — this is what
actually reopens S5b and should gate whether -2/-3 are worth doing at all
versus queries where the divergence is dominated by an unrelated M0141/M0138
gap.

## 5. -3(iii) trace-through — CONFIRMED, not assumed (M0142-0008a-3 increment (iii))

§3.4's open question was whether `createPlan`'s hash-join lowering is
"actually generic over `Jointype: Semi/Anti`" or whether PG's
`create_unique_path` entanglement (the reason `jointypeForDirection` declines
the keyed arms for SEMI/ANTI — `joinpaths.go:141-148`) also blocks the
executor/lowering side. Traced bottom-up rather than assumed:

1. **The Path→Join lowering itself never discriminates by jointype for hash
   joins.** `createHashJoinPlan` (`createplanjoin.go:545-600`) calls
   `planJoinTypeFor` (`:325-349`) to get the executor `JoinType`, and that
   function is a plain `switch` over every jointype the search can legally
   produce a `Path` for — `JoinSemi`/`JoinAnti` map to `JoinTypeSemi`/
   `JoinTypeAnti` on exactly the same footing as `JoinInner`/`JoinLeft`/
   `JoinRight` (`:337-340`). The function's own doc comment (`:560-564`)
   states this plainly: "Today the search only ever files INNER hash paths
   … so this is the same value it always was" — i.e. the code path was never
   narrowed to INNER, it was simply never *called* with anything else,
   because `jointypeForDirection` never emits a `PathHashJoin` candidate for
   SEMI/ANTI in the first place. The lowering function itself contains no
   refusal to remove.
2. **`joinInputsFor`'s schema/layout handling is already SEMI/ANTI-aware.**
   `publishedSchema`/`publishedLayout` (`:291-303`) both special-case
   `JoinTypeSemi`/`JoinTypeAnti` to publish the outer-only slice of the
   merged schema — the same narrowing `unnestExistsExpr`'s hand-built nodes
   rely on today. `narrowBuildInput` (`narrowoutput.go:52`) has no
   jointype branch at all — it narrows the build side purely from attr-needed
   columns, independent of what kind of join it feeds.
3. **The executor's runtime hash-join operator is already proven correct for
   Hash Semi/Anti in production** — not hypothetically: `unnestExistsExpr`
   and its IN/scalar-subquery siblings (`unnest.go:3200-3208, 3335-3342,
   4383-4399`) construct `Join{Type: JoinTypeSemi/Anti, Algo: JoinAlgoHash}`
   nodes directly (bypassing the DP search's `Path` representation entirely)
   for every census query with an equijoin pair, and these are what TPC-H
   Q4/Q21/Q22 and the TPC-DS channel-comparison queries execute today with
   canonical row counts. Whatever code path actually *runs* a hash join at
   execution time cannot be jointype-INNER-only, because it already runs
   SEMI/ANTI ones every day.
4. **Even the PARALLEL hash-join variant already treats SEMI/ANTI as
   first-class.** `hashJoinIsPartialCapable` and `partialHashJoinTypeOK`
   (`parallel.go:873-921`) both list `JoinTypeSemi`/`JoinTypeAnti` (alongside
   `JoinTypeInner` and conditionally `JoinTypeLeft`) as partial-capable,
   citing PG's own `hash_inner_and_outer` parallel block
   (`joinpath.c:2418`) as filing partial hash joins for exactly this set.
   This is further, independent evidence that goopg's executor has no
   SEMI/ANTI-specific hash-join gap — parallel hash join is a strictly
   harder case (shared build, cross-worker visibility) and it is already
   considered safe here.
5. **PG's `create_unique_path` concern is real, but narrower than §3.4
   assumed — it lives in exactly one place, and that place already fails
   closed.** `hashJoinFinalCostInputFor` (`hashjoin_innerunique.go:32-64`,
   the goopg analogue of PG's `compute_semi_anti_join_factors`/inner-unique
   costing refinement) explicitly early-returns the zero-value
   `hashJoinFinalCostInput{}` for `jt != parser.JoinInner` (`:34`), with a
   doc comment stating outright: "goopg can prove only the INNER case here
   … SEMI and ANTI have their own executor and join-semantics work, and
   remain on their existing paths." That zero value "deliberately selects
   the old non-unique bucket walk" — i.e. a SEMI/ANTI hash path costed after
   the gate is lifted would simply skip this one refinement and cost via the
   same conservative default every non-provably-unique INNER hash join
   already costs through today. It is a costing-*tightness* gap (a SEMI/ANTI
   hash join might be costed slightly higher than PG's fully-refined
   estimate), not a correctness or executor-capability gap.
6. **`cardinality.go` already has explicit non-multiplying SEMI/ANTI row
   estimation arms** (`:274, 294, 424, 432, 920, 925`) that operate on the
   already-lowered `*Join` node — these apply identically regardless of
   which producer built the node (`unnestExistsExpr` today, a future
   DP-search `Path` tomorrow), so no new row-estimation logic is needed
   either.

**Verdict for -3(iii): CONFIRMED generic — no lowering or execution change is
needed to let the DP search emit a Hash Semi/Anti path.** The only code that
needs to change to lift §3.4's gate is `jointypeForDirection`'s
`nestloopOnly` line (`joinpaths.go:265`, and the `!nestloopOnly` guard at
`:318`) — narrowing or removing it is executor-safe by the trace above. What
should NOT be assumed still true without its own check when -3(iii) is
actually implemented: `hashJoinFinalCostInputFor`'s fail-closed default means
the newly-enabled SEMI/ANTI hash paths will be costed conservatively (never
wrong, possibly non-optimal versus a hand-tuned PG-parity cost), so the
plan-compare re-check in §4.3 is still the gate that decides whether that
conservative costing is tight enough to win the `addPath` competition against
the existing nested-loop paths for each census query — this trace-through
answers "is it safe to build the path", not "will the cost model pick it".

## 6. Section 4.3 gate re-run — TPC-DS half of the 8-query census (2026-09-16)

§4.3 said: re-run plan-compare for the census and confirm, per query, whether
semi/anti placement/algorithm is the ONLY PG divergence, before -2/-3 start.
This loop ran that check for the 5 TPC-DS queries (query10/16/35/69/94);
**TPC-H's 3 (Q4/Q21/Q22) remain unmeasurable** — the shared `:65433` bench
cluster's `tpch` database still holds only the M0142-0003j/-0003k scratch
tables (re-confirmed live this loop: `\dt` lists `agg_data`, `lrs_acct`, …,
zero TPC-H tables), and the reload is still blocked on the human-authorized
shared-cluster-write decision. Method: `bench/tpcds/server.sh start sf1 pg`,
one warm-ANALYZE'd goopg session (per-connection stats, same protocol as
`cmd/estimate-audit`) plus one plain PG session (global stats), plain
`EXPLAIN` (no `ANALYZE` execution needed for a shape comparison) on all 5
queries both sides, `max_parallel_workers_per_gather = 2` pinned identically.
Result files: `tmp/m0142-0008a-census/{goopg,pg}_explains.txt`. Also ran every
query for real on both engines to rule out a correctness gap riding along
with the shape diff — **all 5 results matched PG exactly** (Q10/Q16/Q69/Q94
row-for-row identical; Q35's few cosmetic `0` vs `0.00000000000000000000`
numeric-display and `char(N)` trailing-space diffs are pre-existing, unrelated
formatting, not new).

**First, a correction to the premise this whole census carried since
M0142-0008a**: goopg does *not* currently plan these 5 queries through
nested-loop-only paths waiting on a hash-decline gate. `unnestExistsExpr`'s
S5a hand-built nodes (§0/§5) already choose **Hash** Semi/Anti unconditionally
for every one of the 5 — the census's original framing ("goopg loses the
Hash-vs-NLI competition because Hash is declined") doesn't describe what's on
disk. The real, verified mechanism is the mirror image: **`unnestExistsExpr`
hardcodes `Algo: JoinAlgoHash` with no cost comparison against NLI at all**
(§0/§5's own citations, `unnest.go:3200-3208, 3335-3342, 4383-4399`) — there
is currently no competition to lose or win; Hash is simply the only option
this producer ever emits. That is exactly the wiring gap -2/-3 target (give
the DP search the participant so `addPath` can cost-compare Hash against NLI
instead of one producer picking unconditionally), so the census's target
mechanism is still correct even though its stated symptom was backwards.

**Per-query verdict** (PG shape vs. goopg shape, both `EXPLAIN`-only, no `.txt`
reproduced inline — see the capture files):

| query | goopg (today) | PG 18.3 | isolated to semi/anti placement/algorithm? |
|---|---|---|---|
| **Q10** | `Hash Semi Join` (customer⋈store_sales) with the two OR'd EXISTS (web_sales/catalog_sales) as hashed `SubPlan`s in a residual `Filter` | **No semi-join node at all** — `HashAggregate` de-duplicates `store_sales.ss_customer_sk`, then `Nested Loop` probes `customer_pkey` by that unique set, with the OR'd EXISTS as hashed `SubPlan` filters on the *index probe* | **NO.** PG used `create_unique_path` (semi-join → uniquify + inner join), a distinct path-generation strategy §3.4/finding 5 named but that neither -2 nor -3 build. Lifting the hash-decline gate and wiring DP participation cannot reach this shape — a different, currently-unfiled mechanism would be needed. |
| **Q16** | `Hash Anti Join(Hash Semi Join(...))`, hardcoded Hash throughout | `Nested Loop Semi Join(Nested Loop Anti Join(...))`, fully index-driven (`catalog_returns_pkey`, `date_dim_pkey`) | **Plausibly yes** — placement (nesting order: Anti-over-Semi in both) already matches; only the per-join algorithm (Hash vs indexed NLI) differs, which is precisely what cost-competing via DP-search participation would let goopg's own cost model decide. (See also the EXPLAIN cosmetic bug noted below, found on this query — execution itself is unaffected: goopg's result matches PG exactly.) |
| **Q35** | Same `Hash Semi Join` shape as Q10 | Same `create_unique_path` shape as Q10, **plus** an `Incremental Sort` at the top (`Presorted Key: ca.ca_state`) | **NO** — entangled with Q10's create_unique_path gap *and* a second, already-filed, unrelated gap (M0141-S7 Incremental Sort). Neither is in -2/-3's scope. |
| **Q69** | `Hash Anti(Hash Anti(Hash Semi(...)))`, uniform Hash | `Nested Loop Anti(Nested Loop Anti(Parallel **Hash** Semi Join(...)))` — PG itself picks Hash for the innermost EXISTS (`ss_customer_sk`, same as goopg) and NLI only for the two outer NOT EXISTS | **YES — cleanest of the 5.** Nesting order matches goopg's exactly (Anti(Anti(Semi))); PG even agrees with goopg's Hash choice for one of the three joins. The only divergence is per-predicate algorithm choice on the other two, which is exactly the cost-competition -2/-3 would introduce. |
| **Q94** | `Hash Anti(Hash Semi(...))`, hardcoded Hash | `Nested Loop Anti(Nested Loop Semi(...))`, fully index-driven | Same read as Q16: placement matches, algorithm differs, same EXPLAIN cosmetic bug present (see below). |

**Verdict on the reopen criterion**: **3 of 5 (Q16, Q69, Q94) support it**
(divergence isolated to semi/anti algorithm choice, which -2/-3 targets); **2
of 5 (Q10, Q35) do not** — both need PG's `create_unique_path` strategy, a
mechanism outside -2/-3's scope entirely, and Q35 separately needs M0141-S7.
This is a **partial, not a blocking, confirmation**: unlike the 2026-07-21
`csq-R2` deferral (low measured prize, bushy-reorder regression precedent),
here a majority of the measured sample directly supports proceeding, and the
2 that don't fail for a *different*, independently-nameable reason rather than
undermining the mechanism itself. **Recommendation: M0142-0008a-2 is cleared
to start.** `create_unique_path` needs its own scoping recon, filed as
**M0142-0008c** (fix_plan.md) since it is a materially different, previously
uncensused mechanism (a new *Path*-generation strategy, not a join-algorithm
choice within an existing one).

**Incidental discovery, not part of the semi/anti question**: both Q16 and
Q94's goopg `EXPLAIN` output mislabels the *outer*, correlated relation's
alias in the innermost `Hash Cond`/`Join Filter` lines — Q16 prints
`Hash Cond: (cs2.cs_order_number = cs2.cs_order_number)` where the real SQL
correlates `cs1.cs_order_number = cs2.cs_order_number` (`cs1`/`cs2` both alias
`catalog_sales`; the outer `cs1` is mislabeled `cs2`, colliding with the
genuinely-inner `cs2` printed two lines below it), and Q94 does the
identical thing with `ws1`/`ws2`. **Verified execution-only cosmetic**: both
queries' actual results match PG row-for-row, so the join itself resolves the
correct columns — only the plan-printer's alias resolution for a
self-correlated EXISTS where inner and outer share a table is wrong. Filed as
**M0142-0008d** (fix_plan.md) since a wrong alias in EXPLAIN is a real,
user-visible PG-compatibility defect (a DBA reading this plan sees the wrong
join key) even though it never reaches execution.

## 7. M0142-0008a-2 landed (2026-09-16)

Implemented per §4.1, with one adaptation the section flagged as open: §4.1
said to call `makeSpecialJoinInfoScoped`'s *shrink logic*, not its
`sc`/`item`/`lower` signature. In practice that meant a **new, small helper**
(`existsUnnestSJInfo`, `unnest.go`, inserted immediately before
`unnestExistsExpr`) rather than a call into `specialjoin.go` at all —
`unnestExistsExpr` has no `sjiScope`/catalog/`parser.FromExpr` to resolve
against, only already-resolved `unnestParam{OuterRef, SubCol}` pairs and
lifted-residual `Expr`s, so the shrink computation is re-expressed directly
over that data instead of forced through the parser-facing entry point.

**RelSet numbering choice.** §4.2 item 1 flagged the atomic-RHS-vs-full-
participation question as open for -3 and recommended atomic-RHS as the
right-sized first step. -2 commits to that recommendation now, one increment
early: `existsUnnestSJInfo` uses a self-contained 2-bit scheme
(`LHS=RelSet(1)`, `RHS=RelSet(2)`) rather than any of the join search's real
per-call numbering (which does not exist at this pipeline stage — this
rewrite runs before the DP search, not inside it). Because every
`unnestExistsExpr` join has by construction exactly one param/residual set
tying one outer column to one inner column, `clause_relids` always spans
both bits whenever any param or residual exists (guaranteed — the function's
own belt check refuses a keyless join with no residual), so `MinLefthand ==
SynLefthand` and `MinRighthand == SynRighthand` always hold for this
producer: a 2-relation join has nothing to shrink from. The "shrink" pass
(specialjoin.go:190-219, the lower-outer-join ordering scan) is still called
in spirit but is a structural no-op here (`lower` is implicitly empty —
`ctx.joinInfoList` belongs to ordinary jointree deconstruction, which this
rewrite runs independently of).

**What -3 must NOT assume.** If -3 lands the atomic-RHS version, this
numbering is directly reusable as the join's own 2-relation local view, but
the DP search's *global* `RelSet` bits (per §3.3, per-search-call) are a
different, larger numbering the RHS's base rel(s) must be assigned into —
`existsUnnestSJInfo`'s output is NOT pre-numbered for that global space and
-3 will need to remap or rebuild it, not consume the bits as-is.

**Verification.** `Join.SJInfo` is set but has **zero readers** anywhere in
the tree today (confirmed: it is a new field, and nothing added in this
change reads it) — "no plan-shape change expected" (§4.1) is therefore
structural, not merely measured, but it was measured anyway per the
milestone group's gate discipline: full `internal/optimizer` suite green
(no behavioral test depends on the new field), and the TPC-DS SF0.25 sweep
(`scripts/tpcds-sf025-regression.sh sweep`) reports `PLAN-SHAPE: queries=99
same=99 changed=0` and `MISMATCH=0` against the pre-change baseline — byte-
identical plans and results. TPC-H's Q12/Q13 spot-check gate SKIPPED (known,
pre-existing: `:65433`'s `tpch` DB still holds no TPC-H tables — M0142-0003k,
unrelated to this change). Three new unit tests
(`exists_unnest_sjinfo_test.go`) built from real `unnestExistsExpr` fixtures
(hash-keyed Semi, hash-keyed Anti, keyless nested-loop Semi from matrix M14)
pin the computed `SpecialJoinInfo` values, closing the "never exercised
end-to-end" gap §2.3 flagged in the existing `specialjoin_test.go` Semi/Anti
cases (which stand in with `LEFT JOIN` because the parser has no SEMI JOIN
syntax).

**Next step**: M0142-0008a-3, re-scoped into three increments by §4.2. Per
§4.2's own recommendation, increment (3) (lift the hash-decline gate) should
land before or alongside (1) (RHS-as-participant) — §5's trace-through
already confirmed `createPlan`'s hash-join lowering is generic over
Semi/Anti, so (3) is unblocked to implement; (1) is the larger, still-open
design question (atomic-RHS vs full participation for the EXISTS body's own
internal joins).

## 8. M0142-0008a-3(iii) landed (2026-09-16) — HASH lifted, MERGE stays declined

Implemented per §4.2 item 3 and §5's trace-through, with one correction §5
itself did not surface: §5 traced only `createHashJoinPlan`'s lowering and
the hash-join executor — it never examined the MERGE arms, but
`joinpaths.go`'s single `nestloopOnly` boolean gated both keyed families
(hash AND merge) as one block (`addPathsToJoinrel`'s own comment named
"both merge arms, the serial hash arm and its partial twin" as one group).
Grepping `internal/executor/join_merge_stream.go` for `Semi`/`Anti` returns
**zero matches** — unlike `join_batch.go:340,363`'s explicit hash-join
early-exit/dedup handling, goopg's merge-join executor has never been given
(or verified to already have) Semi/Anti semantics. Lifting the combined gate
wholesale would have enabled untested merge paths alongside the
now-confirmed-safe hash paths, which §5's own verdict does not license.

**What landed**: `joinpaths.go`'s single `nestloopOnly` boolean is split into
two independently-gated groups inside `addPathsToJoinrel`. `mergeDeclined :=
jt == parser.JoinSemi || jt == parser.JoinAnti` is unchanged in effect from
the old `nestloopOnly` and still gates `sortInnerAndOuter`/
`matchUnsortedOuterMerge`/`matchUnsortedOuterMergePartial`. The hash arms
(`addHashJoinPath`, `addPartialHashJoinPath`) are now **unconditional** —
reachable for SEMI/ANTI on the same footing as every other jointype. Updated
in the same change: the file's stale "SEMI/ANTI contract" doc comment (it
previously asserted goopg's hash executor would "MULTIPLY rows" if used for
SEMI — false; `join_batch.go` already runs Semi/Anti hash joins correctly in
production via `unnestExistsExpr`), and four unit tests that encoded the old
nestloop-only assumption (`TestAddPaths_SemiAntiNestloopOnly`,
`TestDPPATHAdjudicatesOfferedAndAccepted`,
`TestEnumTraceSemiPairingIsNestloopOnly`, `TestSemiAdmissionFilesPricedNLI`)
— each now asserts hash is offered/reachable and merge is not, rather than
asserting neither.

**A live-producer risk this section closes, not just -2's inert one.**
Unlike -2 (whose `Join.SJInfo` field had zero readers, making "no plan-shape
change" structural), this gate sits inside the DP search's own path
generator, which already runs for one real SEMI/ANTI producer today:
`reduceOuterJoins`'s LEFT→ANTI demotion (S9.3, `reduce_outer_joins.go`)
mutates the parser's `FromExpr` tree *before* `deconstructJointreeScopedSJI`
(`planner.go:3042` runs before `:3051`), so a `LEFT JOIN ... WHERE
right.col IS NULL` idiom produces a real `SpecialJoinInfo{Jointype: JoinAnti}`
that reaches `ctx.joinInfoList` and the ordinary DP search — `addPathsToJoinrel`
was therefore already being called with `jt == JoinAnti` in production before
this change, independent of M0142-0008a-3(i)/(ii) (EXISTS/NOT EXISTS
decorrelation) landing at all. Lifting the hash decline could in principle
have changed a real plan shape today, not just prepared for a future one.
**Measured, not assumed**: `scripts/tpcds-sf025-regression.sh sweep` (full
99-query TPC-DS SF0.25 corpus) reports `PLAN-SHAPE: queries=99 same=99
changed=0`, `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0` against the
pre-change baseline — byte-identical plans and results. `scripts/tpch-
spotcheck.sh` SKIPPED (pre-existing, unrelated: `:65433`'s `tpch` DB still
holds no TPC-H tables, M0142-0003k). The zero-change result means either no
corpus query's `reduceOuterJoins`-produced ANTI joinrel has a usable
equijoin key, or the newly-admitted hash path's conservative
`hashJoinFinalCostInputFor`-fail-closed cost never beats the existing
nested-loop/NLI candidates for the ones that do — either way, today's shipped
plans are unaffected; the risk was real but did not materialise on this
corpus. `internal/optimizer`'s full suite is green (updated tests pass;
no other test depends on the old nestloop-only behavior).

**Deferred, not closed**: MERGE stays declined for SEMI/ANTI because
`join_merge_stream.go` has never been traced or verified for Semi/Anti
early-exit/dedup semantics — a distinct, unstarted piece of work from this
increment's hash trace. See `.ralph/deferral_ledger.md` row `M0142-0008a-3iii`.

**Next step**: M0142-0008a-3 increments (i) (RHS-as-participant — make the
decorrelated EXISTS/NOT EXISTS RHS a real DP-search leaf) and (ii) (legality
wiring / end-to-end integration verification) are still open; §4.2's own
open question (atomic-RHS vs full internal-join participation) is unresolved
and gates (i). Once (i)/(ii) land, this loop's hash-admission work is what
lets `addPath` actually cost-compare Hash against NLI for the EXISTS/NOT
EXISTS-decorrelated joins the original census (M0142-0008a-3(iii)'s §4.3
gate re-run, §6 above) named as the prize — TPC-DS query10/16/35/69/94 and,
once `:65433` is reloaded, TPC-H Q4/Q21.

## 9. M0142-0008d root-cause found — re-scoped, NOT a small fix (2026-09-16)

M0142-0008d was filed by §6's census as "EXPLAIN mislabels the outer
relation's alias" and described as small/self-contained. This section's
investigation (empirical repro, not just reading) finds the real defect and
corrects the sizing: it is a genuine cross-scope identity collision, and a
faithful fix needs the same tree-wide-rewrite shape as `clonePlanReplacingOuter`
(~500 lines, 15 `Node` cases), not a one-site patch.

**Repro** (`/tmp` throwaway cluster, port 5533, not TPC-DS-dependent — the
bug is general to any self-correlated EXISTS, confirmed independent of the
TPC-DS schema):

```sql
CREATE TABLE catalog_sales (cs_order_number int, cs_warehouse_sk int, cs_ship_date_sk int);
EXPLAIN SELECT count(distinct cs1.cs_order_number)
FROM catalog_sales cs1
WHERE EXISTS (
  SELECT 1 FROM catalog_sales cs2
  WHERE cs2.cs_order_number = cs1.cs_order_number
  AND cs2.cs_warehouse_sk <> cs1.cs_warehouse_sk
);
--        ->  Hash Semi Join
--              Hash Cond: (cs1.cs_order_number = cs1.cs_order_number)   -- should be cs1 = cs2
--              Join Filter: (cs1.cs_warehouse_sk <> cs1.cs_warehouse_sk) -- should be cs1 <> cs2
--              ->  Seq Scan on catalog_sales cs1
--              ->  Seq Scan on catalog_sales cs2
```

(TPC-DS Q16/Q94 show the identical bug with the losing side flipped — both
print `cs2`/`ws2` instead of `cs1`/`ws1`, confirming the direction of the
collision is walk-order-dependent, not fixed.)

**Root cause.** `ColumnRef.SourceTableIdx`/`OuterColumnRef.SourceTableIdx`
(`plan.go:434,458`) is a *per-query-level* counter that restarts at 1 for
every subquery scope (documented at `explain_names.go:70-81`). Two
independent tables that each happen to be first-in-their-FROM-list — here
`cs1` (outer level 0) and `cs2` (EXISTS body, its own level 1) — land on the
*same* raw `SourceTableIdx` value while they are two separate scopes with
their own scan node and RTID. `unnestExistsExpr`'s hash-key and residual
construction (`joinpaths`-adjacent code at `unnest.go:4379-4396`, the
`outerKey`/`innerKey` `ColumnRef` literals, and `liftResidualConjuncts`'s
`*ColumnRef` case at `unnest.go:~4056`) copies each side's `SourceTableIdx`
verbatim from its pre-flatten per-level value into the merged Join's
Predicate/LeftKey/RightKey. Once the EXISTS body is spliced into the outer
tree as an ordinary `Join.Right` child (no longer a "hanging" sublink body
reached via `NodeSubplans`), `explain_names.go`'s `collect()` walks it as
part of the *same* tree and both the `cs1` scan and the `cs2` scan attempt to
register the *same* `SourceTableIdx` key in `nm.bySrc: map[int16]int32`
(`explain_names.go:82`, `explainSingleSourceIdx`, `explain_names.go:355`).
That map holds one relation name per raw value; whichever scan node's
registration wins the walk-order race is the name *every* `ColumnRef`
carrying that raw value renders with — including the ColumnRef that meant
the *other* table. This is the same `bySrc` mechanism §0/`explain_names.go`'s
own doc comment credits for correctly resolving an `OuterColumnRef` printed
*as* an `OuterColumnRef` (Q30's `ctr1.ctr_state` case, still correct today,
re-verified) — it was designed for exactly one collision direction (outer
level 0 always registers before a still-nested sublink body) and silently
breaks once `unnestExistsExpr` turns the inner scope into an ordinary sibling
in the *same* level instead.

**Blast radius is narrower than "any column in the EXISTS body", not wider.**
Two things keep this from being a bigger bug than it looks:
- The Semi/Anti join's own `schema` field is `outerChild.Output()` only
  (`unnest.go:4470`) — nothing above the join ever sees an inner-scope
  column, so the collision is reachable *only* through the join's own
  `Predicate`/`LeftKey`/`RightKey` — confirmed by testing a residual that is
  *not* lifted (`cs2.cs_ship_date_sk > 3`, left on the scan): it renders
  bare/unqualified (`Filter: (cs_ship_date_sk > 3)`), because upstream's own
  `varprefix=false` rule for a plain scan qual (`formatExprQual`'s
  `*optimizer.ColumnRef` comment) means a scan-local filter never calls
  `column()` with `qualify=true` in the first place — the collision is dormant
  there regardless of which scan won the `bySrc` race.
- Node *labels* ("Seq Scan on catalog_sales cs2") are correct in both repros
  above — label disambiguation is a separate pass (`nodeLabels`,
  `explain_names.go:83-92`, M0128-P5.1) untouched by this bug.

**Why the fix is not a one-site patch.** Fixing only `outerKey`/`innerKey`/the
residual `ColumnRef` case's `SourceTableIdx` does not fix the bug: `collect()`
resolves a `SourceTableIdx` back to a relation name by reading it off the
*scan node's own schema* (`explainSingleSourceIdx(node)` over `n.Output()`),
not off the join-level `ColumnRef`s. A ColumnRef with a fresh, non-colliding
value would only fall through to bare/unqualified (`bySrc` has no entry for
it) rather than resolve to the right name — correct-ish but not PG-faithful.
A fully correct fix must renumber the *scan node's own* `Output()` schema
(and every intermediate node's own schema copy — `SeqScan`/`IndexScan`/
`Project`/`Aggregate`/`CTEScan`/etc. each store `schema` directly; only
`Filter`/`Sort`/`Limit`/`Memoize` delegate to `Child.Output()` and need no
change, per `plan.go`'s `Output()` methods) for the *entire* `innerPlan`
subtree to a value range disjoint from the outer scope, consistently with
whatever value the join-level `ColumnRef`s are given. The only existing code
that walks every `Node` kind `innerPlan` can contain post-strip is
`clonePlanReplacingOuter` (`unnest.go:1492-1998`, 15 `Node` cases, ~500
lines) — a faithful fix is a sibling of that function (a
`remapSourceTableIdx(node Node, offset int16) Node` walking the same case
set, rewriting every `SchemaColumn.SourceTableIdx` and every
`ColumnRef`/`OuterColumnRef.SourceTableIdx` it finds by `offset`), not a
3-line change to `unnest.go:4379-4396`. Sizing this against the M0142-0008a
scoping precedent (a task this size gets split, not blindly implemented):
this is its own K24-class increment.

**Resume point** (filed as **M0142-0008e**, below — 0008d is closed as this
recon): implement `remapSourceTableIdx` mirroring `clonePlanReplacingOuter`'s
case set, apply it to `innerPlan` in `unnestExistsExpr` (`unnest.go:4277`,
right after `clonePlanReplacingOuter` builds it) with an offset guaranteed
larger than any `SourceTableIdx` used in `outerChild.Output()`, and apply the
*same* offset to `innerKey`/the residual's inner-side `ColumnRef`s built
afterward. Needs a targeted unit test asserting the EXPLAIN text directly
(`cs1.cs_order_number = cs2.cs_order_number`, not the self-comparison) —
the existing plan-shape/row-count gates (TPC-DS SF0.25, TPC-H spot-check)
cannot catch this class of bug at all, since execution is unaffected and
row counts are correct; only the printed text is wrong. Not on the critical
path for M0142-0008a-3(i)/(ii)/(iii)'s TPC-DS query10/16/35/69/94 prize —
purely a display-correctness defect, deferred without blocking anything.

## 10. M0142-0008e landed (2026-09-16)

Implemented exactly the shape §9 scoped, with two additions the case-by-case
build surfaced.

`remapSourceTableIdx(node Node, offset int16) (Node, error)` and its Expr-side
helper `remapExprSourceTableIdx` sit right after `clonePlanReplacingOuter` in
`unnest.go`, mirroring its 15-case `Node` switch (`Join`,
`NestedLoopIndexJoin`, `Filter`, `Project`, `Aggregate`, `Sort`, `Limit`,
`SeqScan`, `IndexScan`, `BitmapHeapScan`, `Values`, `CTEScan`,
`MaterializedCTEScan`, `Gather`, `GatherMerge`). Two deliberate departures
from a literal mirror:

- **Schema fields, not just exprs.** `clonePlanReplacingOuter` never needs to
  touch a node's own `schema` field (replacing `OuterColumnRef` with
  `ColumnRef` doesn't change column count or naming), but that field is
  *exactly* what `explain_names.go`'s `collect()` reads to resolve a raw
  `SourceTableIdx` to a relation name — so every case here also remaps
  `n.schema` (or, for `SeqScan`/`IndexScan`/`BitmapHeapScan`/`Values`/
  `CTEScan`/`MaterializedCTEScan`, remaps *only* `n.schema`, since those leaf
  kinds carry no OuterColumnRef-bearing exprs left to touch after
  `clonePlanReplacingOuter` already ran).
- **Built on the exhaustive walker, not a hand-copy of
  `cloneExprReplacingOuter`'s hand-written expr switch.** `remapExprSourceTableIdx`
  clones via `CloneExprReplacingColumnRefs` (`walk_export.go`), which is
  itself built on `cloneExprRefs`'s exhaustive, gate-tested 32-type coverage
  (`exprwalk.go`) rather than a fifth hand-written switch over `Expr`. This
  closes off the RC-1a defect class (a new Expr type silently passing through
  unshifted) that a literal `cloneExprReplacingOuter`-style copy would have
  reopened.

Scan-node probe/residual exprs (`IndexScan.Key/Keys/LowKey/HighKey/Cond`,
`BitmapIndexScan.Key/Keys/Pred`, `BitmapHeapScan.Cond/BitmapQual`) are
deliberately left unshifted: PG's `varprefix=false` rule for scan quals
(re-confirmed by §9's own repro) means they never render qualified regardless
of `SourceTableIdx`, so shifting them would add case-set surface for zero
visible effect.

`liftResidualConjuncts` is now a thin `offset=0` wrapper around a new
`liftResidualConjunctsWithOffset`, whose `*ColumnRef` arm adds the same
`srcTableOffset` `unnestExistsExpr` used to remap `innerPlan` — otherwise the
residual's inner-side `ColumnRef` (built fresh from the PRE-remap EXISTS body,
since `collectUnnestParamsAndResiduals` harvests it before the remap runs)
would carry the stale value and reopen the exact collision one level up (the
join `Predicate` instead of the join key). The other two callers
(`unnestScalarWithResiduals`, `unnestInExpr`) pass `0` — see the ledger row
filed alongside this task for why they were not measured for the same bug.

**Verification.** `TestExplainSelfCorrelatedExistsDoesNotAliasCollide`
(`internal/executor/exists_unnest_alias_test.go`) runs §9's exact repro
end-to-end (`CREATE TABLE t(a int, b int)` + the self-correlated EXISTS) and
asserts the EXPLAIN text names both `t1` and `t2`. Confirmed to actually catch
the bug class (not just pass vacuously): temporarily forcing
`srcTableOffset = 0` reproduces the exact `Hash Cond: (t1.a = t1.a)` /
`Join Filter: (t1.b <> t1.b)` collision from §9's repro, and the test fails on
it. `go test ./internal/optimizer/... ./internal/executor/...` is green,
including `TestExprSwitchInventoryIsPinned` (its inventory entry was renamed
`unnest.go:liftResidualConjunctsWithOffset` in the same commit). No plan-shape
or row-count gate applies — `SourceTableIdx` is read only by
`explain_names.go`, never by planning or execution — and the TPC-H spot-check
gate is independently SKIPPED right now for the pre-existing, documented
M0142-0003k data-reload blocker, unrelated to this change.

## 11. M0142-0008a-3(i) — RHS-as-participant recon: the mechanism is bigger than "feed extra bindings" (2026-09-16)

Increment (1) of §4.2 reads as plumbing ("assigning [the RHS] a `RelSet` bit
... is a concrete piece of plumbing -2/-3 must add, not a conceptual gap") and
recommends the atomic-RHS shape. This recon traced the concrete mechanism
`runJoinSearchBelowPinned` (`predp.go:73`) would need to call to make that
true, and finds it is not a local plumbing change to that one function — it
needs a new node-tree concept threaded through a second subsystem. **No
production code changed.**

**Confirmed starting shape.** `runJoinSearchBelowPinned`'s descend loop walks
only `x.Left` at each pinned `*Join{Semi,Anti}` and calls `tryJoinSearch`
exactly once, at the `Filter` immediately wrapping `origChain` — every pinned
spine join above that point is reattached afterward by `reresolveJoinByName`,
unchanged in shape. `tryJoinSearch` → `tryPGShapedJoinSearch`
(`joinsearchseam.go:215`) reads `ctx.bindings`/`ctx.joinlist`/
`ctx.joinInfoList` directly — there is no parameter seam for injecting one
extra ad hoc participant into a single call.

**Confirmed: -0008a-2's `existsUnnestSJInfo` (`unnest.go:4398`) already
anticipated exactly this** — its own comment names "the design's atomic-RHS
recommendation for -3" and uses a self-contained 2-bit LHS/RHS numbering,
explicitly deferring the real per-call bits to -3. This recon is the -3 side
of that handoff.

**The blocking finding.** `extractSearchLeaves` (`joinsearchseam.go:1070`),
the function `tryPGShapedJoinSearch` uses to flatten a chain into
`scans`/`onQuals`/`outerLinks`, only descends through `*Join{Cross, Inner,
Left, Right}` — anything else (including `*Join{Semi, Anti}`, and any other
`Node` kind) is treated as **one opaque scan leaf** (the `!isJoin ||
j.Type not in {...}` branch at the top of its `walk` closure). That is
actually the RIGHT primitive for "atomic RHS" — no new code would be needed
IF `x.Right` could simply be spliced into `origChain`'s tree as one more
cross-joined leaf and handed to the existing walk. It cannot, for one
concrete reason: **`x.Right` is not opaque to this walk's *type test* unless
its own top node happens to not be `*Join{Cross,Inner,Left,Right}`.** `x.Right`
is `unnestExistsExpr`'s already-planned EXISTS-body subtree; when the EXISTS
body itself joins ≥2 tables (the TPC-DS "channel comparison" idiom named in
§3.4, and the open question in §4.2 item 1 flags this exact case), its own
top node is very likely `*Join{Type: Inner}` — the walk would recurse INTO
it and try to re-flatten its internal joins as new search leaves, using
`Node`s that were never registered in `ctx.bindings`/`relInfos` and whose
sub-join `Predicate`s are resolved in a numbering space `rebaseChainQual`
has no way to reconcile with the outer chain's. That silently produces
leaf-count mismatches or, if the counts happen to line up, wrong-column
`baseRelInfo` estimates — not a decline, a wrong plan.

Two ways to close this, neither of them a same-loop-sized plumbing change:

1. **A new opaque-participant wrapper node** (e.g. `*OpaqueSearchLeaf{Node
   Node, Rows float64, Width int}`) that `extractSearchLeaves`'s type test
   special-cases as an unconditional stop, with `x.Right` wrapped in it
   before splicing. This is the smaller diff to `extractSearchLeaves` itself
   (one more case in the type test) but pushes the real cost onto every
   OTHER place that switches on leaf `Node` kind expecting a "real" scan —
   `baseSeqScanCostInputs`, `newPrebuiltPath`, `createPlan`'s leaf-lowering
   arm, and `explain_names.go`'s `bySrc` walk (the exact class of Node-kind
   switch M0142-0008d/e's "15/32-case exhaustiveness" fix pattern exists to
   police) would each need to either unwrap it or gain a case, or the
   already-planned `x.Right` subtree needs re-deriving as `baseRelInfo`
   stats (rows/width) rather than executed literally, since `createPlan`
   does not know how to lower an opaque wrapper it has never seen.
2. **Bypass `extractSearchLeaves` for this one caller**: have
   `runJoinSearchBelowPinned` hand-construct the extended
   `bindings`/`scans`/`relInfos`/`joinlist` (origChain's own, unchanged, plus
   one manually-appended entry for `x.Right` with directly-computed
   `baseRelInfo` stats) and call `planJoinlistSearch` directly instead of
   going through `tryJoinSearch`/`tryPGShapedJoinSearch` at all. This avoids
   touching the shared seam but means re-deriving, by hand, several things
   `tryPGShapedJoinSearch` currently does for every other caller (conjunct
   partitioning via `partitionConjunctsForJoinPlanning`, the pinned-spine
   width/identity-boundary contract `assertSpineConsumesIdentityBoundaryMap`
   already asserts) — a second, parallel seam implementation to keep in sync
   with the first (`pattern_sibling_paths_must_agree` risk).

Neither option is what "extend the descend loop to also walk `x.Right`"
reads as at filing time. **Also still open, independent of which option is
chosen:** the post-search splice. Today `reresolveJoinByName` assumes the
pinned spine's *shape* survives search unchanged (only leaf identities
inside `origChain` moved) — once the RHS is a real relset bit, the search's
own winning tree decides WHERE among `origChain`'s relations the semi/anti
join point sits, so the post-search step must recover that point from the
winning `Path`/`Node` rather than re-wrap a shape it already knows, which
`reresolveJoinByName` was never built to do.

**Verdict: -3(i) is a re-scope, not a start.** Recommend filing the concrete
next step as a design-only sub-task (size option 1 vs 2 above against a real
multi-table-EXISTS-body TPC-DS witness — `query10`/`query35`'s class,
per M0142-0008c's own census — before writing any DP-search code), rather
than attempting the descend-loop extension directly. Filed as
**M0142-0008a-3i-recon2** in `fix_plan.md`.

## 12. M0142-0008a-3i-recon2 — the named witness was wrong, and the real blast
radius is far smaller than either §11 option (2026-09-16)

**No production code changed. Design-only, per the task's own filing.**

### 12.1 `query10`/`query35` are the WRONG witness — they carry no Semi/Anti
join at all

§11's own closing recommendation named `query10`/`query35` as "a real
multi-table-EXISTS-body TPC-DS witness," attributing the class to
M0142-0008c's census. Reading that census
(`tmp/m0142-0008a-census/{pg,goopg}_explains.txt`, captured 2026-09-16, still
on disk) shows this is backwards: PG's **actual chosen plan** for both Q10 and
Q35 has **zero** Semi/Anti join nodes anywhere in the tree. PG reaches both
via `create_unique_path` instead — `HashAggregate` dedupes
`store_sales.ss_customer_sk`, then a plain `Nested Loop` probes
`customer_pkey` by that unique set, with the two OR'd EXISTS arms folded into
`hashed SubPlan` filters on the index probe. That is exactly M0142-0008c's
own subject (`create_unique_path`), not -3(i)'s (RHS-as-DP-search-participant
inside a pinned Semi/Anti join). Sizing -3(i)'s options against Q10/Q35 would
size the wrong mechanism — no amount of RHS-participant work converges goopg
onto PG's Q10/Q35 shape, because PG's shape for these two queries never goes
through a Semi/Anti join to begin with. (goopg's own current Q10/Q35 plans
DO use `Hash Semi Join` — confirming goopg failed to consider PG's
`create_unique_path` alternative, which is precisely 0008c's open question,
not a costing gap inside the semi-join path.)

**Corrected witness: Q69** (same five-query census) is the real
multi-table-EXISTS-body class -3(i) targets, and it is a richer witness than
originally hoped — it exercises the mechanism **three times in one query**.
PG's chosen plan has a `Parallel Hash Semi Join` (against `store_sales ⋈
date_dim`, filtered by `d_year`/`d_moy`) feeding two stacked `Nested Loop
Anti Join`s (against `web_sales ⋈ date_dim` and `catalog_sales ⋈ date_dim`
respectively) — every one of the three RHS bodies is a genuine 2-relation
join, the "channel comparison" idiom §3.4 already named as the costliest
hole. goopg's own Q69 plan already independently arrives at the matching
`Hash Semi Join` / `Hash Anti Join` shape with the same 2-table RHS bodies —
so, unlike Q10/Q35, the join-node *kind* already matches PG's; what's
untested is whether the RHS's cost/cardinality is being priced as a real
participant or is an artifact of the existing (pre-`existsUnnestSJInfo`)
plumbing. Q16 and Q94 also show genuine `Semi`/`Anti Join` nodes in PG's
chosen plan and are secondary witnesses of the same class. **Any future
-3(i) work should scope and verify against Q69, not Q10/Q35**; -0008c should
keep Q10/Q35.

### 12.2 Re-sizing option 1 against Q69: three of the four named call sites
already handle an opaque leaf generically — only one needs a new line

§11 sized option 1 (a new opaque-participant wrapper node) as expensive
because it assumed `baseSeqScanCostInputs`, `createPlan`'s leaf-lowering arm,
and `explain_names.go`'s `bySrc` walk would each need a new per-Node-kind
case. Reading those functions (not just grepping for `CTEScan`, which
undersells it — the reach is broader and mostly already generic) refutes
most of that:

- **`extractSearchLeaves`'s type test** (`joinsearchseam.go:1070`) already
  stops at ANY node that is not `*Join{Cross,Inner,Left,Right}` — this was
  already known from §11, restated here as the one confirmed real gate.
- **`baseSeqScanCostInputs`** (`joinsearch.go:479`) already has a universal
  fallback: `if _, ok := leafBaseScan(leaf).(*SeqScan); !ok { return
  estScanPages(fallbackRows, fallbackWidth), fallbackRows, 0 }`. Its own
  doc comment says this explicitly: "a subquery or CTE leaf has no
  `baserel->tuples` to speak of" — ANY non-`*SeqScan` leaf, of ANY concrete
  Node kind, already gets a correct fallback with **zero new code**.
- **`createPlan`'s leaf-lowering arm** is `PathPrebuilt` (`path.go:49-55`,
  `createplan.go:13-59`): "wraps an already-constructed executor Node…
  createPlan on a PathPrebuilt returns the wrapped node unchanged" —
  confirmed by `createplan_test.go`'s own assertion text
  ("createPlan(PathPrebuilt) must return the wrapped node unchanged"). This
  is generic over ANY Node kind already — **zero new code**. It is also
  exactly the mechanism EVERY existing search leaf already goes through
  (`joinsearch.go:434`, `newPrebuiltPath(rel, leaf)`), not something specific
  to base tables.
- **`EstimateRows`/`cardinality.go`** is the one REAL gap: its switch
  (`cardinality.go:43-137`) has no `default:` arm and falls through to
  `return 0` for an unhandled concrete Node kind. BUT for Q69's specific
  case, `x.Right`'s top node is literally `*Join{Type:Inner}` (per §11's own
  finding), and `EstimateRows` already has `case *Join: return
  estimateJoin(x)` — the full real join-cardinality estimator. **No new
  case is needed for Q69's witness at all**; it would only be needed if some
  OTHER query's EXISTS body's top node were a kind `EstimateRows` doesn't
  already list (e.g. a bare `*Aggregate` or `*SetOp` top — both of which
  are, in fact, already-handled cases too).
- **`explain_names.go`'s `bySrc` walk** was not fully re-verified this loop
  (out of scope for a design-only pass) but is the one place M0142-0008d/e's
  precedent applies directly: a labeling mismatch here is display-only, not
  a row-count/plan-shape defect, and can be deferred the same way -0008d was
  before -0008e fixed it.

**Net finding: for Q69's witness, THREE of the four named call sites need
zero new code, and the fourth needs zero new code too** (because the
concrete Node kind already has a case) — the false general fear in §11 was
treating "some Node kind switch might not have a case" as if it always
applies, when in this concrete instance it doesn't.

### 12.3 The real, narrower gap — and reusing `CTEScan` is a trap, not a
shortcut

If cost/cardinality/createPlan already work generically for an arbitrary
wrapped subtree, the ONLY thing missing is a signal `extractSearchLeaves`'s
type test can key on to stop at `x.Right` even though its top node is
`*Join{Inner}` (which the test would otherwise wrongly recurse into).

The obvious shortcut — reuse the existing `CTEScan` wrapper (`plan.go:1659`,
already a generic "wrap an already-planned subtree as one opaque leaf," and
already handled by name in `cardinality.go`, `joinsearch.go:514`, and
`narrowcostinputs.go:122`'s "already-planned sub-problem subtree" fallback
class) — **is a trap, not a free ride**: `cteScanOp`
(`internal/executor/operators_cte_dml.go:306`) is not a transparent
passthrough. Its materializing mode buffers ALL rows on first `Open()` and
replays them from `ctx.CTERowCache` keyed by `DeclKey()` (bare `Name` when
built outside `preplanWithClause`) on every subsequent `Open()` in the same
statement. Q69 needs **three** independent RHS wraps (the Semi Join's and
both Anti Joins'); reusing bare `*CTEScan{Name: "", cte: nil}` for all three
would either collide on the same cache key (wrong: the second and third
would replay the first's rows) or require inventing a disambiguating unique
name per call site anyway — at which point it is no longer "free reuse," it
is carrying CTE-only caching semantics a plain join input was never meant to
have, for no benefit.

**A plain no-op `*Filter{Predicate: nil, Child: x.Right}` wrapper looks like
the cheaper correct answer, unverified this loop:**
`extractSearchLeaves`'s `isJoin` test is false for `*Filter` (stops
immediately, no recursion into `x.Right`); `leafBaseScan`
(`joinsearch.go:539`) unwraps `*Filter` chains down to the real leaf for
classification, so `initialRelRows`'s `default:` branch still correctly
calls `EstimateRows(leaf)` (the Filter-wrapped node, NOT the unwrapped one —
`EstimateRows`'s own `*Filter` case recurses into `Child` and applies
`filterSelectivity`, which returns `1.0` for a nil `Predicate`, i.e. an exact
pass-through); `*Filter` is already fully generic in `createPlan`, the
executor `Build` switch, and (unlike a purpose-built new type) in
`explain_names.go` too, since real `Filter` nodes are the single most common
node kind in the tree already. This has **not been confirmed with a live
instrumented run** — it is a static-read conclusion from the four functions
above, not a tested one, and the remaining, still-open problem from §11
(`reresolveJoinByName`'s post-search splice assuming the pinned spine's shape
survives search unchanged) is completely orthogonal to which leaf-wrapper
choice is made and must still be solved separately.

### 12.4 What plumbing is still real, regardless of wrapper choice

Neither §12.2 nor §12.3 makes registering `x.Right` as a new search
participant free — `runJoinSearchBelowPinned` (or its caller) still needs to:
(a) build a `RelOptInfo`/`baseRelInfo` entry for the wrapped leaf and append
it to `bindings`/`relInfos` before the call into `tryJoinSearch`, (b) extend
`ctx.joinInfoList`/`SJInfo` bookkeeping so the search's own join-legality
checks (§2) see the Semi/Anti restriction against the new bit correctly, and
(c) resolve the post-search splice (§11's still-open `reresolveJoinByName`
problem, unaffected by anything in §12.2/12.3). None of that is new relative
to §11 — what changed is which of the FOUR downstream call sites §11 worried
about are actually load-bearing (one, not four) and which existing node type
is safe to reuse as the leaf wrapper (plain `*Filter`, not `*CTEScan`).

**Concrete next step** (unfiled as a numbered task pending a decision on
priority — this recon leaves -3(i) still not started): build a small,
throwaway instrumented probe against the Q69 witness (mirrors the style of
§5's -3(iii) trace-through) that (1) confirms `x.Right`'s top node really is
`*Join{Inner}` for all three of Q69's EXISTS bodies, (2) confirms wrapping it
in a bare `*Filter{Predicate:nil}` and splicing it into `origChain` as one
more leaf produces the SAME row/cost estimate `EstimateRows`/
`baseSeqScanCostInputs` already compute for it today (i.e., the wrapper is
provably a no-op for cost/cardinality, only changing what
`extractSearchLeaves` does with it), before writing any real DP-search
plumbing for (a)/(b)/(c) above.
