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

## 13. M0142-0008a-3i-verify — live probe run: §12.3's claim was WRONG, and
the correct finding is BETTER than the hypothesis it was testing (2026-09-16)

**No production code changed.** Added
`internal/optimizer/m0142_0008a_3i_verify_probe_test.go`
(`TestExistsUnnestTwoRelationRHSTopNodeIsProjectNotBareJoin`), a live,
end-to-end (`Plan(sql, cat)`) instrumented probe — not another static read —
built on the smallest fixture that reproduces Q69's witness shape:
`threeTablesCatalog`'s `t1(x)`/`t2(y,z)`/`t3(a,b)`, with `SELECT x FROM t1
WHERE EXISTS (SELECT 1 FROM t2, t3 WHERE t2.z = t1.x AND t2.y = t3.a)` standing
in for Q69's `EXISTS (SELECT 1 FROM web_sales, date_dim WHERE … )` idiom
(RowCount stats seeded manually — `threeTablesCatalog`'s own tests
deliberately leave tables un-ANALYZEd, which makes `EstimateRows` correctly
return 0 and would have hidden the comparison this probe needs).

### 13.1 The literal claim in §11/§12 is false

§11 stated, and §12.2/§12.3 repeated, that "`x.Right`'s top node is literally
`*Join{Type:Inner}`" for a multi-table EXISTS body. The live run refutes
this: `j.Right` is `*Project{Child: *Join{Type:JoinTypeInner}}`, not a bare
`*Join`. The reason is structural, not incidental to this fixture:
`unnestExistsExpr` builds the RHS by cloning `ex.Plan`
(`clonePlanReplacingOuter`, unnest.go:4546) — the EXISTS body's **own,
already-fully-planned** subquery tree — and every planned `SELECT`, including
a constant list like `SELECT 1`, carries its own top-level output-list
`*Project`. A two-relation EXISTS body's `ex.Plan` is therefore always
`Project(Join(...))` in the general case, not a bare `Join`; the earlier
single-table fixtures in `exists_unnest_sjinfo_test.go` never exposed this
because a one-relation body plans straight to `*SeqScan` with no join (and
often no Project either, once column-pruning elides a pure passthrough) to
begin with.

### 13.2 The consequence is the opposite of what §12.3 assumed: no wrapper is needed at all

§12.3's entire proposal — a bare `*Filter{Predicate:nil, Child: x.Right}` — 
existed to give `extractSearchLeaves` a non-`*Join` node to stop at instead of
wrongly recursing into `x.Right`'s join structure. The live run shows
`x.Right` **already is** a non-`*Join` node (`*Project`) with zero new code:

- `extractSearchLeaves(j.Right)` already returns a single opaque leaf
  (`scans == [j.Right]`, confirmed by the probe) — the `isJoin` type test
  (joinsearchseam.go:1110) is false for `*Project` on exactly the same
  footing it would have been false for the proposed `*Filter`.
- `EstimateRows(j.Right)` already recurses correctly to the real join
  cardinality underneath via the existing generic case (`case *Project:
  return EstimateRows(x.Child)`, cardinality.go:74-75) — confirmed
  numerically equal to `EstimateRows(inner)` by the probe, i.e. `*Project` is
  exactly as cardinality-neutral as the proposed `*Filter{Predicate:nil}`
  would have been, because it is already a member of the same
  "pass-through wrapper" family the M0125-0038 comment block
  (cardinality.go:108-136) documents (`*Gather`, `*GatherMerge`, `*LockRows`,
  `*Memoize`, `*CTEScan`, …).
- `baseSeqScanCostInputs(ri, j.Right, …)` already takes the documented
  generic non-`*SeqScan` fallback (`leafBaseScan` does not unwrap `*Project`,
  so `leafBaseScan(j.Right) == j.Right`, which is not `*SeqScan` either way) —
  confirmed by the probe's `(pages, tuples, ops)` shape check.

**Net effect: §12.3's proposed wrapper-node engineering step is unnecessary.**
The plan shape `unnestExistsExpr` already produces for a multi-table EXISTS
body is, by construction, already an opaque leaf as far as every downstream
consumer named in §12.2 is concerned — the "narrower gap" §12.3 identified
(needing *some* non-`*Join` marker) turns out to already be filled by the
ordinary output of planning the subquery, not by anything -3(i) has to add.

### 13.3 What is actually still open — unchanged from §12.4, now confirmed to be the WHOLE remaining task

Nothing in this probe touches §12.4's three real plumbing items, and nothing
here makes them smaller or larger:

(a) `runJoinSearchBelowPinned` (or its caller) still has to build a
`RelOptInfo`/`baseRelInfo` entry for `j.Right` (whatever its concrete top
node — `*Project` today, confirmed; possibly something else for a body shape
this probe didn't exercise) and append it to `bindings`/`relInfos` before
calling into `tryJoinSearch`;
(b) `ctx.joinInfoList`/`SJInfo` bookkeeping still needs extending so the
search's own join-legality checks (§2) see the Semi/Anti restriction against
the new bit correctly;
(c) `reresolveJoinByName`'s post-search splice (§11) — the pinned spine's
shape assumption — is completely untouched and still open.

**Revised next step:** -3(i) can now skip straight to (a)/(b)/(c) — no
wrapper-node design/implementation increment is needed first. Whoever picks
this up should re-verify point (a) does not itself need special-casing for
`*Project`-topped leaves (e.g. does `newPrebuiltPath`/`PathPrebuilt`
already handle a `*Project` leaf identically to a `*SeqScan`/`*Join` one? —
§12.2 says yes for "any wrapped Node kind" but that claim was itself a static
read and this loop's lesson is: verify claims like that live, not by
re-reading).

## 14. M0142-0008a-3i-plumbing-recon3 — §12.4's (a)/(b)/(c) decomposition is
the wrong layer; the real mechanism is chain ADMISSION, not spine splicing
(2026-09-16)

**No production code changed. Design-only, following the same discipline as
§11/§12/§13.** Filed as a correction while scoping -3i-plumbing's actual
implementation, before writing any DP-search code (the task's own working-set
handoff flagged §12.2's "any wrapped Node kind" claim as unverified and asked
for live re-verification of point (a) specifically — this recon instead found
a structural problem one layer up that makes (a) as literally described
unbuildable, and identifies the mechanism that IS buildable).

### 14.1 §12.4's (a) is not representable: `x.Right` cannot become a
`rangeBinding`

§12.4 (a) reads "build a `RelOptInfo`/`baseRelInfo` entry for `x.Right` …
and append it to `bindings`/`relInfos`". `bindings` is `[]rangeBinding`
(`planner.go:549`), and `rangeBinding.table` is a `*catalog.Table` —
dereferenced **unconditionally, dozens of times**, by the statement-wide
column-resolution/star-expansion/diagnostic code in `planner.go` (e.g.
`:1296-1299`, `:2603`, `:4651`, `:6648-6655`, `:7825`, `:9278`, `:15810-15928`
— none of these nil-check `b.table` first). `ctx.bindings` is the **one**
range-table list for the whole statement, read by all of that code, so any
literal read of "append to `bindings`" that mutates `ctx.bindings` itself
would plant a `nil`-table entry that the very next `SELECT *` or ambiguous-
column diagnostic elsewhere in the same statement dereferences and panics
on — not a hypothetical, `x.Right` genuinely has no single backing
`*catalog.Table` (it is `unnestExistsExpr`'s already-planned `Project(Join(t2,
t3))` subtree per §13).

This is not fatal on its own: the codebase already has the fix for exactly
this shape, used dozens of times for FROM-subqueries/CTEs/derived tables
(`planner.go:4707,4817,4861,5570,5906,5970,6030,…`, one per derived-table
kind) — synthesize `tbl := &catalog.Table{Name: alias, Columns: cols}` from
the leaf's own output schema and use that as `rangeBinding.table`, so every
statement-wide consumer keeps working. `with.go`'s CTE bindings do precisely
this (cited by name at `relfromjoinlist.go:513-514`). **So (a) is buildable,
but only via this synthesis, not via a bare append of an ad hoc struct** —
worth stating explicitly since §12.4's phrasing reads like the append is the
whole step.

Two things make this reuse safer than it first looks, both already true in
production and unrelated to this task:

- `baseRelInfo.table` (a **separate** field from `rangeBinding.table`,
  `cardinality.go:694`) is independently nil-safe everywhere it is read
  inside the search's own cost machinery (`joinsearch.go:403,480`,
  `joinrelsize.go:638`, `relfromjoinlist.go:233,546`) — confirmed by grep,
  all four sites nil-check before dereferencing. A synthesized leaf can
  therefore leave `baseRelInfo.table` nil (no real ANALYZE stats to report)
  even while `rangeBinding.table` is the non-nil synthetic table column
  resolution needs — the two fields are allowed to disagree, and the search
  already relies on that disagreement for ordinary derived-table leaves.
- The catastrophic-mis-costing firewall named in §11/§12 as a general worry
  — `problemPairsOuterWithDerived` (`relfromjoinlist.go:564`, the Q78
  15s→327s regression) — **does not apply to this task at all**: it only
  fires for `sj.Jointype ∈ {JoinLeft, JoinRight, JoinFull}`
  (`relfromjoinlist.go:589-593`, `default: continue` for everything else,
  Semi/Anti included). And even where it might have mattered, -3i-verify
  already showed (§13.2) `x.Right`'s `EstimateRows` is not a stats-less
  `rows=1` default the way a CTE's is — it recurses to the real
  join-cardinality estimator over `t2`/`t3`'s own (real) `ANALYZE` stats.
  Net: no new firewall interaction to design around.

### 14.2 The real blocker: a pinned-spine `*Join` cannot be RELOCATED by a
search that only rebinds it in place

§11 already named the open problem ("the search's own winning tree decides
WHERE … the search must recover that point … which `reresolveJoinByName` was
never built to do") but framed it as (c), a separate cleanup step after (a)
and (b) land. Reading `reresolveJoinByName` (`joinlayout.go:623`) shows it is
not a smaller version of that job, it is a **different** job entirely:
it takes an already-placed `*Join` `j` and re-resolves `j`'s own
predicate/keys against `j.Left`/`j.Right`'s CURRENT schemas by name — it does
not (and structurally cannot) choose WHERE in a tree `j` sits, create a new
`*Join{Type:Semi/Anti}` node at a different position, or decide that the
search's winning tree should nest the semi/anti probe partway down instead
of at the top. `runJoinSearchBelowPinned`'s whole splice model
(predp.go:73-201) is built on exactly this constraint: the pinned join node
`j` is **never rebuilt**, only `j.Left` is replaced by whatever the search
returns for `origChain`, and `reresolveJoinByName` merely patches `j`'s own
predicate afterward. There is no version of "(a) append x.Right as a leaf,
(b) add SJInfo bookkeeping, (c) fix the splice" that produces a plan where
the search is free to interleave the semi/anti probe with `origChain`'s
other joins (e.g. `(a ⋈ b) SEMI-JOIN x.Right ⋈ c` instead of
`(a ⋈ b ⋈ c) SEMI-JOIN x.Right`) — PG's own Q69 plan (§12.1: `Parallel Hash
Semi Join` low in the tree, two `Nested Loop Anti Join`s stacked ABOVE it,
not beside it) is exactly this interleaved shape. Appending x.Right as one
more leaf to a search whose OUTPUT still gets spliced back under a
fixed-position pinned join cannot reach that shape no matter how (a)/(b) are
built — the pin itself is the obstacle, not a missing bookkeeping field.

### 14.3 goopg already has the right mechanism, for a sibling case: chain
ADMISSION, not post-search splicing

`extractSearchLeaves` (`joinsearchseam.go:1071`) already solves precisely
this problem for LEFT/RIGHT outer joins, and is already exercised in
production on every TPC-H/TPC-DS query with an outer join: its `walk`
(`:1104-1191`) treats `*Join{Cross,Inner,Left,Right}` as **admissible** —
for Left/Right it descends BOTH `j.Left` and `j.Right` (flattening the outer
join's own two sides into the SAME `scans` list every ordinary inner-join
leaf lands in, `:1116-1170`), records an `outerChainLink{jointype,
preserved, nullable, pred}` capturing exactly which `RelSet` range is the
preserved/nullable side, and lets the search's own `join_is_legal`-style
machinery (fed by `ctx.joinInfoList`, consumed via `outerLinksHaveSJInfos`/
`outerOnQualsOK` at `joinsearchseam.go:498-524`) decide, AS PART OF THE
SEARCH, where the outer join is legally allowed to land — not pin it and
patch it afterward. **This is the S5b mechanism the design doc's §1 already
named as possibly-already-reopened** ("DP participation for semi/anti (S5b)
is deferred by user decision… this is S5b, and its reopen criterion may
already be met") — §12.4's (a)/(b)/(c) was an attempt to reopen S5b by
extending the SPLICE model (predp.go), when the codebase's own working
precedent for "a special-jointype relation the search must place legally"
already exists one file over and does not use the splice model at all.

Concretely, the buildable version of -3i-plumbing is:

1. Extend `extractSearchLeaves`'s type test (`joinsearchseam.go:1110`) to
   also admit `JoinTypeSemi`/`JoinTypeAnti`, descending both sides the same
   way Left/Right already do (§13's finding makes this safe: `x.Right`'s own
   top node is `*Project`, not `*Join`, so the RHS descent stops at one
   opaque leaf exactly as extractSearchLeaves already does for any
   non-flattenable node — no risk of wrongly recursing into `x.Right`'s
   internal `t2 ⋈ t3`).
2. Semi/Anti needs its **own** link record, not a reuse of
   `outerChainLink` — that struct's `nullable`/`preserved` fields and every
   consumer (`outerOnQualsOK`, `deriveOuterLinkConstants`,
   `problemPairsOuterWithDerived`) exist to answer "which columns read NULL
   through this join," which has no meaning for Semi/Anti (no NULL-extension;
   `reresolveJoinByName:630-646` already documents that Semi/Anti "emit
   Outer (=Left) only at runtime" — the RHS never becomes visible above the
   join at all, a strictly simpler contract than an outer join's). A
   parallel `semiAntiChainLink` (or a `Jointype` field discriminating the one
   `outerChainLink` type, if the two families turn out to share more logic
   than expected on closer reading) records the LHS/RHS `RelSet` ranges and
   the join predicate; a parallel legality consumer (mirroring
   `outerLinksHaveSJInfos`) checks it against `ctx.joinInfoList`.
3. `existsUnnestSJInfo` (unnest.go:4398) already anticipated exactly this
   moment in its own doc comment ("-3 recomputes real bits once the RHS
   actually joins the search") — its throwaway `synL=1/synR=2` numbering is
   replaced by the SAME `leafRangeRelSet(loLeft, loRight)` /
   `leafRangeRelSet(loRight, hiRight)` computation the Left/Right branch
   already performs (`joinsearchseam.go:1156-1157`) once the walk knows the
   real local leaf indices, i.e. it is rebuilt at admission time, not
   invented fresh.
4. **This retires predp.go's separate pinned-spine mechanism for the
   admitted cases** (exactly what -3(ii)'s own filing text already
   anticipated: "retire `runJoinSearchBelowPinned`'s splice-and-reresolve
   path for the now-natively-searched cases") — once a pinned Semi/Anti join
   is admitted into the ordinary chain flattening, it is placed by the
   search like any other join and needs no post-search splice at all,
   which is also what resolves §11/§12.4's (c): there is no separate
   "recover the join point from the winning tree" step to build, because
   the search builds the tree with the join already in it. `origChain`
   without any pinned Semi/Anti above it (the "unnest declined" / Q22
   legacy-post-DP shape, per -3's own note) keeps going through
   `runJoinSearchBelowPinned` unchanged — only the admitted cases move.
5. Precedent that a live Semi/Anti `SpecialJoinInfo` inside
   `ctx.joinInfoList`, checked by the ordinary search's legality machinery,
   already works in production **today**, for a different producer:
   `reduceOuterJoins`'s LEFT→ANTI strength reduction (S9.3, cited by
   -3(iii)'s own landing note) puts a real `SpecialJoinInfo{Jointype:
   JoinAnti}` into the ordinary flow, and the search costs/legality-checks
   it correctly. That path arrives via the Left/Right branch (the RHS was
   already an ordinary FROM-clause relation, only the label changes after
   admission) rather than via a fresh synthesis at admission time the way
   step 3 above would need, so it is corroborating evidence, not a
   drop-in implementation.

**This is a bigger change than §12.4 described** — it touches
`extractSearchLeaves`'s flattening contract (a heavily-hardened function:
the C-04a/b/c comments throughout it document several past silent-regression
fixes) rather than predp.go alone — but it is also smaller in a different
sense: it reuses ~90% of already-built, already-tested outer-join admission
machinery instead of inventing new bookkeeping (§12.4's (b)) and a new splice
repair (§12.4's (c)) from scratch. **Not sized for a single loop**: step 1
alone touches the same function three separate C-04-series regressions have
already been found in, so it needs its own dedicated scoping pass (does
Semi/Anti's simpler "no NULL-extension" contract let it skip the
`preserved`/`nullable`-threading entirely, or does `chainOnQual`'s
`belowNullable` bookkeeping still need a Semi/Anti-aware arm for INNER links
that sit ABOVE an admitted Semi/Anti link?) before any code lands. **Revised
resume point for whoever picks up -3i-plumbing next**: start from §14.3's
5-item list, beginning with a throwaway probe (mirroring §13's style)
against Q69 that extends `extractSearchLeaves` locally in a test file only,
confirms it produces the flattened leaf list step 1 predicts, and checks
whether the existing `outerChainLink` consumers choke on a link with no
`nullable` bits set (empty `RelSet`) before deciding step 2's "parallel type
vs. shared type" question.

## 15. M0142-0008a-3i-plumbing item 1 — live probe run: item 2's question is
now answered by evidence, not speculation (2026-09-16)

`internal/optimizer/m0142_0008a_3i_plumbing_probe_test.go`
(`TestM0142_0008a_3iPlumbing_AdmitSemiAnti`) copies `extractSearchLeaves`'s
walk into a local, throwaway function extended to admit
`JoinTypeSemi`/`JoinTypeAnti` (§14.3 item 1) and runs it against the same
Q69-witness-class fixture §13 used. Production code
(`joinsearchseam.go`) is unchanged. Three findings:

**(0) Representation correction, orthogonal to §14.3's own claim.** On the
FINAL planned tree (post join-method selection, which is the only tree this
probe file's helpers can reach — `predp.go`'s real call site runs on the
PRE-search `origChain`, where `j.Predicate` is always populated), a
hash-keyed Semi/Anti join's correlation is `(j.LeftKey, j.RightKey)`, not
`j.Predicate` — `j.Predicate` came back `nil` for this fixture. The probe
reconstructs `pred = &BinaryOp{Op: OpEq, Left: j.LeftKey, Right: j.RightKey}`
when `Predicate == nil`; this reconstruction is a probe-only artifact of
inspecting the wrong tree stage, not a new production gap (`origChain` still
carries `Predicate` directly, as the original Left/Right code this walk was
copied from already assumes).

**(1) Leaf-list prediction confirmed exactly.** `extractSearchLeavesAdmitSemiAnti(j)`
on the Semi join returns exactly 2 leaves — `t1` and the RHS `*Project` as
one opaque leaf — `onQuals` empty, and `outer` holding exactly one link for
the Semi join itself, matching §14.3 item 1's prediction with zero surprises.
(Aside, not chased further: the RHS `*Project`'s own `Output()` width came
back 4, not the `SELECT 1` body's apparent width of 1 — the unnest rewrite
evidently threads extra columns through the body's projection that this
probe did not need to explain to answer item 2.)

**(2) Item 2 decided: a genuinely separate type, not a `Jointype`-discriminated
`outerChainLink`.** The probe builds the link with the literal reading
`nullable: 0` (Semi/Anti never null-extends — RHS is invisible above the join
either way) and feeds it to all three existing consumers named in §14.3's own
resume point:

- `outerOnQualsOK` returns **false** — CONFIRMED, not merely predicted. Its
  `relsSubset(rs, lk.preserved|lk.nullable)` check requires every conjunct's
  relids to fit inside `preserved|nullable`; with `nullable=0`,
  `preserved|nullable` is the LHS leaf alone, but the correlation predicate's
  relids span BOTH the LHS leaf and the RHS opaque leaf (`pred relids = 3`
  against `cumOffsets = [0 1 5]` in the probe's log) — so a well-formed
  Semi/Anti link is unconditionally declined by this consumer as written.
  The declination is not a bug in `outerOnQualsOK`: that function's contract
  is specifically about *outer-join* qual placement (which side may read
  which columns without breaking NULL-extension semantics), a question that
  does not exist for Semi/Anti.
- `deriveOuterLinkConstants` returns **nil** for the same link — a silent
  no-op (not a crash), because every branch requires
  `relsSubset(rb, lk.nullable)` for the "null-extended" operand and
  `nullable=0` makes that provable only when `rb==0` too. Silent-no-op is
  survivable but wrong at the SEMANTIC level, not just the mechanical one:
  this function's entire premise is "a constant known on the *preserved*
  side can be pushed onto the *nullable* side because outer-join NULL-
  extension is the only way the pushed equality could fail" — Semi/Anti has
  no NULL-extension at all, so the function's reasoning does not apply in
  either direction, empty-nullable or not. Feeding it a Semi/Anti link is
  answering a question it was not designed to answer, regardless of the
  encoding chosen for `nullable`.
- `problemPairsOuterWithDerived` (which takes `[]*SpecialJoinInfo`, not
  `[]outerChainLink`) declines to flag a real `existsUnnestSJInfo`-built
  Semi `SpecialJoinInfo` at all — CONFIRMED live, not merely re-read: its
  `switch sj.Jointype { case parser.JoinLeft, parser.JoinRight,
  parser.JoinFull: default: continue }` skips Semi/Anti unconditionally, the
  same finding §12.2 made from static reading, now reproduced against a real
  value the unnest rewrite actually builds.

**Decision, superseding §14.3 item 2's open question**: build a **separate**
`semiAntiChainLink` type (LHS `RelSet`, RHS `RelSet`, `Jointype`, `pred` —
no `preserved`/`nullable` fields at all, since neither concept is meaningful)
with its **own** legality/costing consumers, rather than a `Jointype`-
discriminated `outerChainLink`. The evidence is not merely that the existing
consumers happen to reject `nullable=0` mechanically (§12.4/§14.3 might have
read that as "encode `nullable` as the RHS range instead, so the subset
check passes") — it is that `deriveOuterLinkConstants`'s CORRECTNESS
argument is built entirely on NULL-extension, which has no Semi/Anti
analogue in either direction. Encoding `nullable = RHS range` to satisfy
`outerOnQualsOK`'s arithmetic would make `deriveOuterLinkConstants` and any
other current or future `outerChainLink` consumer that reasons about
"nullable = may read NULL through this join" silently apply outer-join logic
to a join type that cannot produce a NULL-extended row — a correctness trap
waiting for the next consumer added to that struct's already-long list,
not merely a missed case in the two consumers this probe exercised.

**New safety finding for -3i-plumbing's remaining items (3-5), not previously
ledgered**: `problemPairsOuterWithDerived` — the Q78 catastrophic-mis-costing
firewall — has **zero** Semi/Anti coverage today (confirmed live, above).
Admitting Semi/Anti into the chain-flattening search (items 3-5) creates
exactly the shape that firewall exists to catch: a derived/CTE-sourced
relation joined through a special join type with a rows≈1 estimate that can
win an epsilon cost tie it should not. Recorded as a resume-point-bearing
deferral (`.ralph/deferral_ledger.md`, `M0142-0008a-3i-plumbing-probe1` row)
rather than left as an implicit assumption — a Semi/Anti arm for this
firewall (or an explicit argument that Semi/Anti's `RHS` classification
already makes it moot) belongs BEFORE items 3-5 admit real Semi/Anti links
into production search, not after a regression surfaces the gap the way
`take3-C-04a-Q78-firewall-classifier` did for the LEFT/RIGHT case.

**Revised resume point**: -3i-plumbing's items 3-5 (rebuild
`existsUnnestSJInfo`'s real `RelSet` bits at admission time, retire
`runJoinSearchBelowPinned`'s splice for admitted cases, cite
`reduceOuterJoins`'s precedent) now have a settled item-2 answer to build on:
define `semiAntiChainLink` and its own `semiAntiLinksHaveSJInfos`/
`semiAntiOnQualsOK`-shaped legality consumers (mirroring `outerLinksHaveSJInfos`/
`outerOnQualsOK`'s STRUCTURE — same `RelSet`-subset reasoning for "does this
predicate connect exactly the LHS and RHS ranges" — but dropping every
NULL-extension-specific branch, since Semi/Anti's contract is simply
"a valid equi-correlation between two disjoint RelSets," a strict subset of
what the outer-join consumers must reason about). Add
`problemPairsOuterWithDerived`'s Semi/Anti arm in the SAME change that first
admits a real Semi/Anti link into the search, not as a follow-up. Not sized
for one loop on its own — items 3-5 still touch `extractSearchLeaves`'s
production walk, `existsUnnestSJInfo`, and `predp.go`'s splice retirement
together, the same three-subsystem span that made -recon3 defer whole-cloth
implementation before this loop's probe.

## 16. M0142-0008c — scoping recon: does goopg need `create_unique_path`? Answer: yes for Q10/Q35, and it is a materially new mechanism, not a plumbing add-on (2026-09-16)

Filed by §6's gate re-run: Q10/Q35 use PG's `create_unique_path`
(semi-join → de-duplicate RHS, then plain inner join), a path-generation
strategy neither -2 nor -3 touch. This section sizes it, per the filing's own
instruction, before anything is implemented.

### 16.1 What PG's mechanism actually is (read live, not from memory)

Two cooperating pieces, both in
`postgres/src/backend/optimizer/`:

- **`join_is_legal`** (`path/joinrels.c:412-489`) has a THIRD admission arm
  beyond "one input covers `min_lefthand`, the other `min_righthand`": for a
  `JOIN_SEMI` whose full RHS sits in exactly one input, it calls
  `create_unique_path(root, rel, rel->cheapest_total_path, sjinfo)` and, if
  that returns non-NULL, **admits the join anyway** — the RHS is unique-ified
  first, so joining it to *anything* (not just its `min_lefthand` partner) is
  legal (`joinrels.c:445-489`, the `unique_ified` branch, comment at :450-471
  explains the `a,b,c` motivating example directly). This is a *legality*
  relaxation, not a costing choice: without it, no path reaching Q10/Q35's
  shape is even considered.
- **`create_unique_path`** (`util/pathnode.c:1729`) builds and caches
  (`rel->cheapest_unique_path`) a `UniquePath` over a rel's
  `cheapest_total_path`: fast-path NOOP if a unique index or a
  provably-distinct subquery output already proves uniqueness
  (`pathnode.c:1932-1985`), else a real `Sort+Unique` or `HashAggregate`
  path costed against `sjinfo->semi_rhs_exprs`. The result is consumed by
  `sort_inner_and_outer`/`match_unsorted_outer`
  (`path/joinpath.c:1408,1415,1887,1923,2180,2305,2321` — 7 call sites) via
  two synthetic jointypes, `JOIN_UNIQUE_OUTER`/`JOIN_UNIQUE_INNER`
  (`joinpath.c:113-121`), which every builder converts to a plain
  `JOIN_INNER` after substituting the unique-ified path for one side.

### 16.2 goopg's current state: confirmed, by grep, not by inference

- `internal/optimizer/joinsearchlevel.go:198`'s `(*searchCtx).joinIsLegal` —
  the direct, unit-tested port of `join_is_legal` (§2.3 above) — has the
  RHS-overlap **skip** arm (`joinrels.c:412-420`, "already unique-ified,
  irrelevant now") but **not** the admission arm that creates that state in
  the first place (`joinrels.c:445-489`). Read live (lines 190-267): the
  `else` branch at the bottom of the SJ-matching chain unconditionally
  returns an error ("violates outer-join constraint") for exactly the case
  PG's `unique_ified` branch would admit. This is the single precise
  legality gap.
- `create_unique_path` has **zero** goopg analogue: `grep -rn
  "PathUnique|UniquePath|createUniquePath"` across `internal/optimizer` and
  `internal/executor` returns nothing production (only `distinctpaths.go`'s
  doc comment mentioning the executor already prints `"Unique"` for an
  unrelated node). `RelOptInfo` (`internal/optimizer/path.go:423-560`) has
  `CheapestTotal`/`CheapestStartup`/`CheapestParameterized` but no
  `CheapestUnique` cache slot.
- `innerrel_is_unique`/`relation_has_unique_index_for` (PG's NOOP fast-path
  proofs) have **zero** goopg analogue either: `grep -rn
  "innerrel_is_unique|innerRelIsUnique|relation_has_unique_index"` across
  `internal/optimizer` returns nothing. Every `create_unique_path` call in
  goopg would therefore always take the expensive Sort+Unique/HashAggregate
  path, never the free NOOP one PG takes whenever a unique index already
  proves it — a correctness-neutral but cost-model-relevant gap (a spurious
  Unique node would be priced where PG's plan has none).
- The executor-node reuse hoped for at first glance is real but partial: a
  `"Unique"`/`"DistinctOn"` **executor** operator already exists
  (`internal/executor`, wired via `distinctOp`/`distinctOnOp`,
  `distinctpaths.go`), so no new *executor* node kind is needed. But its
  *planner*-side producer (`createDistinctPaths`, `distinctpaths.go:43`) is
  wired as a single Phase-4 **upper-rel** wrapper applied once above the
  whole finished plan (`fetchUpperRel(u, UpperDistinct, 0, ...)`) — the
  opposite shape from PG's `create_unique_path`, which is a **per-RelOptInfo**
  path competing and caching *inside* the DP search, callable from
  `join_is_legal` while the search is still choosing join order. Reusing the
  executor node does not reuse the producer; a new producer is needed at a
  different layer of the planner.

### 16.3 Sizing verdict: a new mechanism comparable to -0008a itself, not a plumbing add-on

Four independent pieces, none trivial alone, all needed together for a
first correct instance:

1. **New `RelOptInfo.CheapestUnique *Path` cache field** plus a
   `createUniquePath(rel, subpath, sjinfo) *Path` producer built at the
   base/join-rel level (not the upper-rel level) — the actual new
   path-generation code, reusing the existing `Unique`/`DistinctOn` Plan
   node and its executor operator, but needing its own cost function (no
   existing goopg cost function prices a mid-search dedup).
2. **`joinIsLegal`'s missing admission arm** (`joinsearchlevel.go`, the
   `unique_ified` branch) — the smallest piece, a direct ~20-line port once
   (1) exists to call.
3. **Two synthetic jointypes threaded through every join-path builder** —
   goopg's analogues of PG's `sort_inner_and_outer`/`match_unsorted_outer`
   (hash-join, merge-join, and NLI builders under `internal/optimizer/`, the
   producers `addPath` calls per join level) all need a
   `JoinTypeUniqueInner`/`JoinTypeUniqueOuter` case that substitutes in the
   unique-ified path and demotes the jointype to plain `INNER` before
   costing/building — this is the widest-blast-radius piece, comparable to
   how -0008a-3(iii) touching MERGE's decline arm (§8) rippled across
   multiple builders for a much narrower change.
4. **`innerrel_is_unique`/unique-index NOOP fast path** — optional for
   *correctness* (the expensive path still produces the right rows) but
   needed for *plan-shape parity*: without it, goopg would show a `Unique`
   node in cases where PG's plan has none because a unique index already
   proved it, which is itself a new class of plan-shape mismatch this
   mechanism would introduce if skipped.

**Verdict: this is a new, from-scratch Path-generation strategy on the scale
of M0142-0008a's own SEMI/ANTI-in-DP-search work (items 1-4 above touch four
different layers: RelOptInfo caching, legality, every join-path builder, and
uniqueness-proof infrastructure), not a follow-up patch.** It should be
tracked as its own milestone-sized item, decomposed the way -0008a-3i was
(items 1-4 above as separate sub-tasks, each its own recon-then-implement
pair given this project's track record on similarly-scoped joinrels.c ports).
**Not selected this loop** — filed as **M0142-0008c-1..4** (fix_plan.md)
mirroring this section's four-item breakdown, with item 2 (the `joinIsLegal`
arm) as the cheapest, most self-contained starting point once the group is
picked up. Q10/Q35 remain un-parity'd until this lands; Q16/Q69/Q94 (the
3-of-5 queries §6 confirmed are pure algorithm-choice gaps) are unaffected
and remain reachable via -0008a-2/-3 alone.

## 17. M0142-0008c-1 landed — cache field + producer, SORT method only (2026-09-16)

Landed the item-1 piece §16.3 sized: `RelOptInfo.CheapestUnique *Path`
(`path.go`), a new `PathUnique` kind + `Path.UniqueKeyCols []int`
(`path.go`), `createUniquePath` (`createuniquepath.go`, ports
`pathnode.c:1729-2081`) and its `createPlanNode`/`createplansimple.go` arm
(`createUniquePlan`). Not wired into `joinIsLegal` — that is -0008c-2 and
this producer has no live caller yet; it is built and unit-tested standalone
(`createuniquepath_test.go`), the same precedent path.go's `NeededCols`
field comment already documents for this codebase ("nothing reads these
yet"). Zero behavior change to any existing plan: `go build`/`go test
./internal/optimizer/...` confirm `createUniquePath`/`PathUnique` have no
production caller, and the one other live edit
(`SemiRhsExprs` — see below) had zero readers before this loop.

**Two findings this loop's writing surfaced that recon alone did not
predict:**

1. **`SpecialJoinInfo.SemiRhsExprs` was declared but never populated.**
   `specialjoin.go`'s own field comment (written for M0128-P1.4) states
   "SemiOperators/SemiRhsExprs stay empty" for `makeSpecialJoinInfoScoped` —
   true, and irrelevant, because that producer's SEMI arm is unreachable
   (ordinary FROM-clause SEMI never reaches deconstruction). The live SEMI
   producer, `existsUnnestSJInfo` (unnest.go), set `SemiCanBtree`/
   `SemiCanHash` but never `SemiRhsExprs` either — nobody had needed it
   until this producer did. Fixed alongside this task (unnest.go): each
   `unnestParam.SubCol` (already the subquery-side/RHS `*ColumnRef` of one
   equijoin conjunct, exactly PG's `compute_semijoin_info`
   (`initsplan.c:2129-2138`) RHS-operand collection) is appended directly —
   no re-derivation needed, because `unnestExistsExpr`'s pull-up only ever
   produces equijoin pairs. Pinned by
   `TestExistsUnnestSJInfoSemiHashKey`/`...AntiHashKey` (`exists_unnest_sjinfo_test.go`).
2. **HASH is a real, currently-unimplemented gap (filed M0142-0008c-1a,
   ledger row appended) — found only once the producer was written, not by
   static reading.** PG's `UNIQUE_PATH_HASH` groups by `uniq_exprs` while
   passing every OTHER needed target-list column through UNGROUPED
   (`createplan.c:1796-1811`: `groupColIdx` is a strict subset of the Agg's
   own tlist — legal only because this Agg is planner-internal, never
   user SQL, so PG's parse-analysis "every non-grouped column must be
   aggregated" rule does not apply here). goopg has no node that can express
   this: `*Distinct` (`distinctOp`) hash-dedups its FULL input row, never a
   column subset; `*DistinctOn` supports a column subset but is a SORTED
   streaming dedup, not a hash. Inserting an early `Project` down to just the
   key columns would sidestep the executor gap but contradicts goopg's own
   established convention that path generation costs/carries FULL width and
   narrows only post-selection (`path.go`'s `NeededCols`/`OutputCols`
   doc comments; `cost_model_design_bundle` memory note "narrowing is
   post-selection") — so the fix is new executor surface, not a producer-side
   workaround, and is out of this item's original "no new executor code"
   boundary. `createUniquePath` therefore gates on `SemiCanBtree` alone and
   always takes the SORT branch; `!SemiCanBtree && SemiCanHash` declines
   (returns nil) rather than silently mis-costing. **Currently unreachable in
   practice**: `existsUnnestSJInfo` is the only live `SemiRhsExprs` producer
   and it always sets both flags together, so this gate never actually
   declines a real query today — recorded for when a second SEMI producer
   (e.g. ordinary FROM-clause SEMI, if that ever reaches deconstruction)
   might set them independently.

**Resume points for the rest of the M0142-0008c-1..4 group**: -0008c-2
(`joinIsLegal`'s admission arm) can now call `createUniquePath(rel,
rel.CheapestTotal, sjinfo, cp)` directly — the domain restriction in
`createUniquePath`'s own doc comment (`subpath.Kind == PathPrebuilt`) is not
a blocker for -0008c-2's own scope, since the SEMI RHS rel it will call this
against IS exactly that atomic-wrapped-subquery shape today. -0008c-1a (HASH)
is NOT on the critical path for Q10/Q35 parity — SemiCanBtree alone is
sufficient for every currently-reachable query — and should stay filed rather
than picked up ahead of -0008c-2/-3.

## 18. M0142-0008c-2 landed — `joinIsLegal`'s SEMI unique-ify admission arm (2026-09-16)

Landed exactly the item-2 piece §16.3 sized: two new `else if` arms in
`(*searchCtx).joinIsLegal` (`joinsearchlevel.go:198`), inserted at the same
position PG's own chain has them — after the two ordinary
MinLefthand/MinRighthand subset-match branches, before the "both inputs
overlap RHS" fallback — porting `joinrels.c:445-467` (RHS = rel2, forward)
and `joinrels.c:469-489` (RHS = rel1, reversed). Each arm calls
`createUniquePath(relX, relX.CheapestTotal, sj, s.cp)` and admits the pair
(`matchSJInfo = sj`, `matchReversed` set per direction) only when it
succeeds; when it returns nil the pair falls through to PG's existing
"otherwise ... invalid join path" `else`, unchanged from before this loop.
Confirmed by build (`go build ./...` clean) and by the package suite
(`go test ./internal/optimizer/...` — full pass, not just the new tests).

**What is NOT propagated, and why that is correct, not incomplete:** PG's
`unique_ified` local variable is scoped to `join_is_legal` itself and is
never returned to `make_join_rel` — its only other read in the same function
is a LATERAL-reference restriction (`joinrels.c:578,590`) that goopg's port
does not implement (no lateral-rel handling in `joinIsLegal` at all, confirmed
by reading the whole function to its end). Downstream,
`populate_joinrel_with_paths`'s SEMI case (`joinrels.c:965-1013`)
**independently re-derives** whether to unique-ify by re-running the same
`bms_equal(syn_righthand, ...) && create_unique_path(...) != NULL` check —
PG's own comment calls this redundant-but-cheap-because-cached. So
`joinIsLegal`'s admission arm and the eventual path-building arm are
deliberately decoupled in upstream PG too; goopg's `-0008c-2` need carry
nothing forward to `-0008c-3` beyond what `sjinfo`/`reversed` already carry.

**Reachability check — live-probed, not just read, because this is exactly
the kind of "admits a pair the rest of the pipeline can't yet finish" risk
that could turn a previously-harmless decline into a hard planning failure.**
Traced what happens to a pair this arm newly admits, given `-0008c-3` (the
synthetic-jointype threading through the join-path builders) has NOT landed
yet:

- `joinIsLegal`'s caller, `makeJoinRel` (`joinsearchlevel.go:572`), proceeds
  to create/find the joinrel for the pair and calls
  `s.builder.addPaths` for both orientations
  (`addPathsToJoinrel`, `joinpaths.go:248`).
- `addPathsToJoinrel` immediately calls `jointypeForDirection(sjinfo, outer,
  inner)` (`joinpaths.go:161`) — for `JoinSemi` it checks
  `relsSubset(sjinfo.MinLefthand, outer) && relsSubset(sjinfo.MinRighthand,
  inner)` and returns `legal=false` otherwise. A pair admitted ONLY via the
  new unique-ify arm (by construction, `MinLefthand` is NOT a subset of
  either single input — that is exactly why the ordinary subset-match
  branches didn't fire first) fails this check in **both** orientations, so
  `addPathsToJoinrel` returns `nil` immediately (`if !legal { return nil
  }`) — no paths added, no error.
- The risk this creates: `makeJoinRel` still registers the joinrel via
  `s.addRel` before calling `addPaths` (`joinsearchlevel.go:612-682`), so if
  BOTH orientations decline, the joinrel is left in `s.joinrels[lev]` with an
  **empty Pathlist**. `joinSearch`'s per-level loop
  (`joinsearchlevel.go:312-317`) hard-fails the ENTIRE search
  (`"joinrel %#08x has no paths"`) for ANY rel with zero paths — previously
  this exact case never arose because `joinIsLegal` declined the pair
  outright (an error `makeJoinRel` swallows into a silent skip,
  `joinsearchlevel.go:593-596`, never creating the joinrel at all).
- **Confirmed NOT triggered today**, by tracing whether the search phase
  functions (`joinSearchOneLevel`'s phase-1 `makeRelsByClauseJoins`, gated on
  `haveRelevantJoinClause(old,other) || joinOrderRestricted(old,other)`,
  `joinsearchlevel.go:376-421`) ever offer such a pair to `joinIsLegal` in
  the first place. For the canonical `FROM a,b WHERE (a.x,b.y) IN (SELECT c1
  FROM c)` shape (`MinLefthand={A,B}`, `MinRighthand=SynRighthand={C}`),
  `joinOrderRestricted(A,C)` (`joinsearchlevel.go:83`) returns `false`: the
  ordinary-match checks fail (`MinLefthand` subset of neither), and both the
  "both overlap RHS" and "both overlap LHS" checks require BOTH inputs to
  overlap — here only `A` overlaps `MinLefthand`, `C` does not. So `(A,C)` is
  never proposed as a candidate pair unless an actual `restrictInfo` join
  clause independently connects them — meaning `-0008c-2` alone changes
  nothing observable yet, exactly mirroring `-0008c-1`'s own "no live caller"
  finding.
- **Verified empirically, not just traced**: ran the TPC-DS SF0.25 fast
  regression gate (`scripts/tpcds-sf025-regression.sh sweep`,
  `bench/tpcds/runtime_goopg/tpcds-results-sf025/sweep-20260916-120048.txt`)
  against the dirty tree with this loop's change. Result:
  `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3` (the 3 skips are
  the pre-existing dsqgen-artifact queries, unrelated) — no row-count
  regression, no hard planning failure, and Q10/Q35's plan shape is
  byte-identical to the `-0008c-1` commit's own capture (expected: neither
  piece alone can move a plan without `-0008c-3`).

**Net effect of this loop: a correct, unit-tested port that is currently a
no-op in production**, same shape as `-0008c-1`. `-0008c-3` (threading
`JoinTypeUniqueInner`/`Outer` through the join-path builders so
`jointypeForDirection` has something to return other than
"decline both directions") remains the piece that makes both `-0008c-1` and
`-0008c-2` live, and is now the sole remaining blocker before Q10/Q35 can
reach a unique-ified candidate at all. `-0008c-4` (the NOOP unique-index fast
path) stays a plan-shape-only concern, not a correctness blocker, unchanged
from §16.3's own sizing.

## 19. M0142-0008c-3 recon — the actual PG plan shape narrows the blast radius; decomposed into 3a/3b/3c/3d, none implemented (2026-09-16)

Per the working-set baton's own instruction ("recon it as its own sub-task
before implementing, given this project's track record"), this section sizes
`-0008c-3` against goopg's real call graph AND against what PG's actual
chosen plan for the two named witnesses (Q10/Q35) needs — the latter turns
out to matter a great deal.

### 19.1 What Q10/Q35 actually need — read from the committed PG plans, not assumed

`bench/tpcds/plans-pg/Q10.txt` and `Q35.txt` (both `EXISTS(store_sales…) AND
(EXISTS(web_sales…) OR EXISTS(catalog_sales…))`, isomorphic queries) show the
**identical** join shape for the `create_unique_path`-relevant EXISTS:

```
Nested Loop
  Join Filter: (customer_demographics.cd_demo_sk = c.c_current_cdemo_sk)
  -> Nested Loop            <- the create_unique_path join
       -> HashAggregate (Group Key: store_sales.ss_customer_sk)   <- unique-ified OUTER
            -> Gather -> Parallel Hash Join(store_sales, date_dim)
       -> Index Scan using customer_pkey on customer c            <- indexed INNER
            Index Cond: (c_customer_sk = store_sales.ss_customer_sk)
            Filter: (ANY(...=(hashed SubPlan 2)) OR ANY(...=(hashed SubPlan 4)))
```

Two findings, both narrowing scope:

1. **Only the first EXISTS (store_sales) is decorrelated to a join at all.**
   The other two are inside an `OR`, which SQL semantics forbid pulling up to
   a semijoin (a semijoin is an AND-only rewrite) — PG leaves them as
   correlated `ANY(... = (hashed SubPlan N))` filters evaluated inside the
   `Index Scan`'s `Filter`. This is **not** part of `-0008c`'s scope and not
   a cost decision either; it is a legality fact PG's own SubLink-pullup
   already respects. Whatever gap (if any) keeps goopg from matching this
   shape belongs to the SubLink-pullup-eligibility area (`existsUnnestSJInfo`
   / `whereEligibleForPreDPUnnest`, §4.2/§6), not to `-0008c-3`. Recorded here
   so a future loop does not mistake "goopg still doesn't match Q10" for a
   `-0008c-3` bug once `-0008c-3` lands — the OR'd pair is a **separate**,
   unrelated divergence that will still be there.
2. **The `create_unique_path` join itself is `JOIN_UNIQUE_OUTER` + an
   INDEXED nested loop (NLI), never hash or merge.** `store_sales` (deduped
   by `ss_customer_sk` via `HashAggregate`) is the OUTER child; `customer`,
   probed by its PK index on `c_customer_sk`, is the INNER child. This is the
   PG shape `match_unsorted_outer` builds when `save_jointype ==
   JOIN_UNIQUE_OUTER` and `innerrel->cheapest_parameterized_paths` has an
   indexed candidate (`joinpath.c:1918-1923`, read live — quoted in full
   below). **Neither witness exercises hash-join or merge-join at all.**
   Also notable: the `HashAggregate`'s group key (`ss_customer_sk`) is the
   *only* column that survives to the join — there are no other passthrough
   columns — so `-0008c-1a`'s HASH-method blocker (goopg's `*Distinct` being
   full-row-only, unable to express "group by keys, pass through the rest
   ungrouped") **does not apply to this witness**: a plain single-column
   dedup is already representable. Whether `create_unique_path`'s SORT method
   (landed by `-0008c-1`) or a future HASH method wins the cost race for
   Q10/Q35 is an open question for whichever loop measures it, but the
   *executor* has no gap either way here.

### 19.2 PG's actual per-builder mechanism, read live (not from memory)

`join_is_legal`/`create_unique_path`'s consumers are exactly the 3 functions
already named in §16.1, but the mechanism is more specific than "add a case
that substitutes a path and demotes the jointype" — it differs BY FUNCTION
in a way that matters for splitting the work:

- **`sort_inner_and_outer`** (merge, `joinpath.c:1403-1417`, quoted in full in
  the file at those lines): substitutes `outer_path`/`inner_path` with
  `create_unique_path(...)` **locally** (a stack variable, never mutating
  `rel->cheapest_total_path`) and sets a local `jointype = JOIN_INNER` before
  any cost/build call. A SEPARATE `save_jointype` (the pre-demotion value) is
  kept and consulted later purely to gate **parallel** eligibility
  (`joinpath.c:1422-1441`): `JOIN_UNIQUE_OUTER` disables partial-merge
  entirely ("the outer path will be partial, and therefore we won't be able
  to properly guarantee uniqueness" — direct quote, `:1424-1426`);
  `JOIN_UNIQUE_INNER` disables the safe-parallel-inner search. The serial
  path itself is otherwise the ordinary merge-join builder with substituted
  inputs.
- **`match_unsorted_outer`** (nestloop + NLI, `joinpath.c:1811-1957`, quoted
  in full above in §19.1's evidence) is the ONE function both plain-NL and
  indexed-NL run through in PG. `JOIN_UNIQUE_INNER` takes an entirely
  separate, narrower branch: it substitutes `inner_cheapest_total` ONCE
  before the outer loop and calls `try_nestloop_path` with ONLY that single
  substituted inner — it never touches
  `innerrel->cheapest_parameterized_paths` (no indexed variant is considered
  for `JOIN_UNIQUE_INNER`; PG's own `XXX` comment at `:1916` admits this is a
  deliberate, possibly-suboptimal simplification: "we don't consider
  parameterized outers, nor inners, for unique-ified cases. Should we?").
  `JOIN_UNIQUE_OUTER` instead restricts the **outer loop** to
  `outerpath == outerrel->cheapest_total_path` (skip every other outer
  path), substitutes THAT ONE via `create_unique_path`, demotes jointype
  once, and then falls through into the SAME generic
  `innerrel->cheapest_parameterized_paths` loop ordinary nested loop and NLI
  share (`:1918-1957`) — meaning Q10/Q35's plan (indexed inner) is produced
  by the *ordinary* parameterized-inner loop, just fed a pre-substituted
  outer path.
- **`hash_inner_and_outer`** (hash, not read in full this loop —
  `joinpath.c:2100-2140` per §16.1's citation — same substitute-then-demote
  shape per the earlier grep, not re-verified live since neither witness
  exercises it).

### 19.3 Mapping onto goopg's actual functions

goopg's shape is NOT a 1:1 mirror of PG's file-per-strategy layout, which
changes where the substitution belongs:

- goopg's `addNLIPaths` (`joinpathsnli.go:269-372`) is PG's
  `match_unsorted_outer`'s **indexed-inner branch only** — it already reduces
  to a single outer candidate (`o := outer.CheapestTotal`, no pathlist loop),
  so the "restrict `JOIN_UNIQUE_OUTER` to the cheapest-total outer" constraint
  PG enforces with an explicit `if outerpath != outerrel->cheapest_total_path
  continue` is **already structurally true** in goopg — nothing to add there.
  The needed change is narrow: when `jt == JoinTypeUniqueOuter`, substitute
  `o := createUniquePath(outer, outer.CheapestTotal, sjinfo, cp)` (declining
  the whole call if nil) instead of `outer.CheapestTotal`, and pass a
  demoted `parser.JoinInner` into the constructed `Path{Jointype: ...}` (the
  plan node itself must read as a plain inner join, matching the PG EXPLAIN
  in §19.1 showing an unlabeled `Nested Loop`, not a `Semi` one). This is the
  function that produces Q10/Q35's actual witnessed shape.
- goopg's `addNestLoopPath` (`pathgen.go:149`) is PG's plain-NL half AND is
  where `JOIN_UNIQUE_INNER` belongs (PG's separate, narrower `try_nestloop_path`
  call using only the substituted cheapest-total inner, never an indexed
  one) — substitute `inner.CheapestUnique`-equivalent for the `JoinTypeUniqueInner`
  case, demote to `JoinInner`, and do NOT thread this case into `addNLIPaths`
  at all (matching PG's own asymmetry, not an oversight to "fix").
- `createUniquePath` (`createuniquepath.go:49`) already caches into
  `rel.CheapestUnique` on the RelOptInfo itself (`-0008c-1`, landed), so
  either builder can call it directly and cheaply on a repeat visit — no new
  cache plumbing needed here.
- `jointypeForDirection` (`joinpaths.go:160-213`) is the actual gap
  `-0008c-2`'s working-set note pointed at: its `JoinSemi` arm requires BOTH
  `MinLefthand`/`MinRighthand` subset containment, which by construction
  fails for a unique-ify-admitted pair. It needs a new fallback, symmetric in
  direction: given `sjinfo.Jointype == JoinSemi` and the ordinary subset
  check fails, if `sjinfo.SynRighthand == inner` (bit-set equality, not
  subset) and `createUniquePath` on that rel succeeds, return
  `JoinTypeUniqueInner`; if `sjinfo.SynRighthand == outer` under the same
  conditions, return `JoinTypeUniqueOuter`. **This requires a signature
  change**: `jointypeForDirection(sjinfo, outer, inner RelSet)` only receives
  bitmasks today, not the `*RelOptInfo` needed to reach `.CheapestTotal`/
  `.CheapestUnique` or the `costParams` needed to call `createUniquePath`.
  Confirmed by `find_referencing_symbols`: **exactly one production caller**
  (`addPathsToJoinrel`, which already has both `*RelOptInfo`s and `cp` in
  scope) plus one test file — the signature change itself is cheap, unlike
  the builder-side work.
- **New type needed**: PG's comment (`joinpath.c:116-121`, quoted in §16.1)
  is explicit that `JOIN_UNIQUE_OUTER`/`INNER` "are not allowed to propagate
  outside this module" — they are a `joinpath.c`-private sentinel over the
  same `JoinType` enum, demoted to `JOIN_INNER` before any Path is built or
  costed. goopg's analogue should NOT add `JoinTypeUniqueInner/Outer` as new
  `parser.JoinType` consts (that enum is shared with the parser and
  executor, which must never see a "unique" jointype) — it should be a small
  `internal/optimizer`-private type (or a second return value alongside
  `parser.JoinType`) that `addPathsToJoinrel` consumes and fully resolves
  (substitute + demote to `parser.JoinInner`) before calling ANY builder,
  narrowing "thread through every builder" to "thread through the two
  builders that need the substitution"; every other builder keeps receiving
  plain `parser.JoinInner` and needs **zero changes**, which is a materially
  smaller blast radius than §16.3's original framing assumed (that framing
  pre-dated reading `match_unsorted_outer`'s actual branch structure and the
  Q10/Q35 evidence).

### 19.4 Revised sizing and decomposition

`-0008c-3` as filed was one task covering hash, merge, AND nestloop/NLI
uniformly. §19.1's evidence (neither witness exercises hash or merge) plus
§19.3's finding (the demote-before-dispatch pattern means most builders need
no change at all) together shrink the *required-for-Q10/Q35* slice to just
the dispatch layer plus two builders. Decomposed into four loop-sized pieces,
filed as `M0142-0008c-3a..3d` below (fix_plan.md):

- **3a** — `jointypeForDirection` signature change + new admission-return
  type + `addPathsToJoinrel`'s substitute-and-demote dispatch. No builder
  changes yet; every existing builder keeps seeing `parser.JoinInner` exactly
  as it does for an ordinary inner join today, so `-0008c-1`/`-0008c-2`
  remain otherwise inert until 3b lands (no plan-shape change expected from
  3a alone — an explicit acceptance check, not a hope).
- **3b** — `addNestLoopPath`'s `JoinTypeUniqueInner` substitution and
  `addNLIPaths`'s `JoinTypeUniqueOuter` substitution (§19.3). This is the
  piece that should move Q10/Q35 — verify against
  `bench/tpcds/plans-pg/Q10.txt`/`Q35.txt`'s exact shape (§19.1), not just
  "a plan now exists."
- **3c** — `addHashJoinPath`/`addPartialHashJoinPath` substitution. Deferred:
  not exercised by either named witness; pick up only if a future measurement
  finds a query where the unique-ified hash path wins the cost race.
- **3d** — `sortInnerAndOuter`/`matchUnsortedOuterMerge`/
  `matchUnsortedOuterMergePartial` (merge) and `addPartialNestLoopPaths`
  (parallel NL) substitution. Deferred for the same reason as 3c, plus merge
  is already `mergeDeclined` for SEMI/ANTI generally (§8) — worth checking
  whether that decline should also cover the demoted-INNER case before
  enabling it.

No production code changed this loop (recon only). Q10/Q35 remain
un-parity'd; the OR'd-EXISTS divergence named in §19.1 item 1 will remain
even after 3a-3d land in full, and is out of scope for this milestone group.

### 19.5 M0142-0008c-3a landed (2026-09-16) — dispatch layer only, zero plan-shape change confirmed

Implemented exactly as scoped in §19.3/19.4 item 3a:

- `jointypeForDirection` (`joinpaths.go`) now takes `outer, inner *RelOptInfo`
  and `cp costParams` (was two bare `RelSet` bitmasks) and returns a THIRD
  value, `uniqueSide` — a new `internal/optimizer`-private type
  (`uniqueSideNone`/`uniqueSideOuter`/`uniqueSideInner`), never a
  `parser.JoinType`, per §19.3's explicit note that PG's own
  `JOIN_UNIQUE_OUTER`/`INNER` "are not allowed to propagate outside this
  module" (`joinpath.c:116-121`). The new fallback arm, added inside the
  existing `case parser.JoinRight, parser.JoinSemi, parser.JoinAnti:` switch
  case, fires only for `JoinSemi` and only after the ordinary
  `MinLefthand`/`MinRighthand` subset check has already failed: bit-EQUALITY
  (not subset) between `sjinfo.SynRighthand` and whichever of `inner.Relids`/
  `outer.Relids` is being tested, AND a successful `createUniquePath` call on
  that same rel (reusing `-0008c-1`'s cache — a repeat visit is a cache hit,
  not a re-derivation).
- `addPathsToJoinrel` resolves the sentinel immediately after the call and
  before invoking any builder: `if uniq != uniqueSideNone { jt =
  parser.JoinInner }`. No builder's signature or body changed — every
  builder keeps seeing exactly the `jt`/`outer`/`inner` triple it always has,
  which is what makes this slice's plan-shape-inert claim checkable rather
  than aspirational.
- The only production behavior change from 3a alone: a SEMI pair that used
  to be declined outright by `jointypeForDirection` (both directions
  `legal=false` when only the fallback's bit-equality condition holds, not
  the ordinary subset one) is now ADMITTED and built as a plain,
  un-deduplicated inner join over the unmodified `outer.CheapestTotal`/
  `inner.CheapestTotal` paths — semantically wrong in isolation (no
  unique-ify substitution yet; that is 3b's job), but never wins the
  cost-model tournament against the query's existing strategy in every
  witness measured (see below), so it never reaches a plan.

**Verification (the acceptance bar was empirical, not just "no crash" — set
by 3a's own fix_plan wording):**

- `go test ./internal/optimizer/...` — full package green, including a new
  direct unit test `TestJointypeForDirection_UniqueIfyFallback` that forces
  the ordinary containment check to fail (via a fixture `SpecialJoinInfo`
  whose `MinLefthand` names a third, uncovered rel) and pins both fallback
  directions (`uniqueSideInner`/`uniqueSideOuter`) plus a `SemiCanBtree=false`
  negative control that must still decline.
- `scripts/tpcds-sf025-regression.sh sweep` (private `GOOPG_BIN`, full
  SF0.25 suite, 99 comparable queries): `PASS=96 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: queries=99 same=99 changed=0 added=0
  removed=0` against the immediately-prior commit (`20a28dd8e`). Confirms the
  "zero plan-shape change" claim empirically, not just by code inspection —
  Q10/Q35 in particular show no plan-shape delta yet (expected: 3b is what
  moves them).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: one
  pre-existing, unrelated failure surfaced (`internal/parser`
  `TestLockingClauseParity`, an AST-drift golden mismatch on the
  `GroupedJoinUnaliased` field introduced by an earlier, unrelated commit
  `dc91bd6b7`) — reproduces identically on `git stash`-clean HEAD before this
  loop's diff, so it is not a regression from this task and is left for
  whoever owns that area next.

Next: `-0008c-3b` threads `uniq` into `addNestLoopPath`
(`JoinTypeUniqueInner`) and `addNLIPaths` (`JoinTypeUniqueOuter`) — the two
builders will need the `uniqueSide` value `addPathsToJoinrel` currently
discards, so its signature (or a parallel plumbing path) must carry `uniq`
alongside `jt` once 3b starts.

## 20. M0142-0008c-3b landed, correct and tested — but a live probe shows the whole `-0008c` family is provably UNREACHABLE for real queries today, blocked on `-3i-plumbing` items 3-5 (2026-09-16)

Implemented exactly as scoped in §19.3/19.4 item 3b and the fix_plan entry:
`addNestLoopPath` (`pathgen.go:149`) gained `uniq uniqueSide, sjinfo
*SpecialJoinInfo` params and substitutes `i := createUniquePath(inner,
inner.CheapestTotal, sjinfo, cp)` when `uniq == uniqueSideInner`, declining
the whole path when the substitution is nil; `addNLIPaths`
(`joinpathsnli.go:270`) gained the same two params and substitutes `o`
analogously for `uniqueSideOuter`, before its existing
`inner.CheapestParameterized` loop. `addPathsToJoinrel` threads `uniq`/`sjinfo`
into both calls (`joinpaths.go:438,443`) — no other builder touched, per
§19.3's asymmetric split. New tests
(`internal/optimizer/uniqueify_builders_test.go`) pin the substitution
directly at both builders (asserting the built Path's substituted child is a
`PathUnique`, never the plain `CheapestTotal`, with a decline control for each)
plus one end-to-end `addPathsToJoinrel` test proving the dispatch-through-
builder wiring produces a demoted-`JoinInner` nested loop over the
unique-ified inner when only the fallback admits the pair. All green; `go
build ./...`, `go vet ./internal/optimizer/...` clean.

**But 3b's OWN acceptance bar — "Q10/Q35's plan shape must match
`bench/tpcds/plans-pg/Q10.txt`/`Q35.txt`'s exact node shape … not just 'a
plan now exists'" — could not be met, and the reason is more serious than a
lost cost race.** A live probe (private `GOOPG_BIN=tmp/goopg-sf025-bin`,
`bench/tpcds/server.sh start sf025` with `GOOPG_PGSHAPED_DP_TRACE=1`,
`EXPLAIN` on the real `query10.sql` against the sf025 dataset) shows:

1. goopg's actual EXPLAIN for Q10 already produces a `Hash Semi Join`
   (`Hash Cond: (c.c_customer_sk = ss_customer_sk)`) for the store_sales
   EXISTS — a *different* shape from both PG's NLI+unique-ify plan and from
   anything `-0008c` builds.
2. Grepping the server log's `DPPATH` lines (the `addPath`-call provenance
   channel, `pathtrace.go`) for the whole Q10 statement — 162 lines total —
   shows **zero** `jointype=semi` (or `anti`) entries; every single one is
   `jointype=inner`. `addPathsToJoinrel` has exactly one production caller
   (`joinrelsize.go:91`, the DP search's `joinRelBuilder`), so this is
   conclusive: for this real query, **`addPathsToJoinrel` is never once
   called with a SEMI/ANTI `sjinfo`** — not "called and declined", not
   "called and lost on cost" — never invoked for that jointype at all.
3. The `Hash Semi Join` in the printed plan is therefore built entirely by
   `unnestExistsExpr`'s direct plan-node construction (`unnest.go:4769-4780`,
   quoted in §5/§9's own trace-through): it picks `Algo: JoinAlgoHash` (or
   `JoinAlgoNestedLoop` with zero equijoin params) **heuristically, at rewrite
   time**, attaches an `SJInfo` for legality bookkeeping, and that fixed
   `Join` node is handed to `createPlanNode` as an already-decided plan
   fragment — it is never re-derived or re-costed by the `addPathsToJoinrel`
   cost tournament `-0008c-1/-2/-3a/-3b` all extend.

This is not a new discovery contradicted by §16-19's own framing — it is
`-3i-plumbing`'s **already-recorded, still-open** blocker, read again live
instead of re-derived from memory. §15 (this same design doc, same day)
already found and explicitly deferred exactly this gap: `extractSearchLeaves`
does not yet admit a Semi/Anti chain link into the production search walk,
`existsUnnestSJInfo`'s real `RelSet` bits are not yet rebuilt at admission
time, and `predp.go`'s splice is not yet retired for the admitted case —
"items 3-5 … [are] not sized for one loop on its own." `-0008c-1` through
`-3b` were built and landed on TOP of that acknowledged gap without anyone
re-checking, before now, whether the SEMI `SpecialJoinInfo`s they dispatch on
ever actually reach `addPathsToJoinrel` in a real plan — they do not, today,
for any production query, because the prerequisite chain-admission plumbing
(`-3i-plumbing` items 3-5) has not landed. `-0008c-1..-3b` are exactly as
*correct* as their own unit tests prove (hand-built `SpecialJoinInfo`
fixtures reach them directly and behave exactly as designed) and exactly as
*unreachable* as `-3i-plumbing`'s own §15 already said the mechanism would be
until items 3-5 land — two true statements about two different layers, not a
contradiction.

**Consequence for the rest of `-0008c`:**

- `-0008c-3c`/`-3d` (hash/merge unique-ify substitution) should **not** be
  picked up next: they extend a dispatch path that is provably dead for every
  real query today, and no TPC-DS SF0.25 measurement can distinguish "correct
  but unreachable" from "wrong" while that holds.
- Re-running the TPC-DS SF0.25 sweep after 3b landed reconfirms zero
  plan-shape change across all 99 queries (`PASS=96 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: queries=99 same=99 changed=0 added=0
  removed=0`) — now understood as the DIRECT, expected consequence of finding
  1-3 above, not a separate lucky inertness result the way 3a's own
  (correctly scoped, narrower) inertness claim was.
- The actual unblock for Q10/Q35 — and for `-0008c` ever mattering to a real
  plan — is `-3i-plumbing` items 3-5 (§15's own resume point: define
  `semiAntiChainLink` + its `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK`
  legality consumers, rebuild `existsUnnestSJInfo`'s real `RelSet` bits at
  admission time, retire `predp.go`'s splice for the admitted case, and add
  `problemPairsOuterWithDerived`'s Semi/Anti arm in the same change per §15's
  own safety finding). That work is unrelated in mechanism to anything
  `-0008c` builds (it is chain-admission into the search, not path
  generation once admitted) and is explicitly sized in §15 as its own
  multi-subsystem task, not a one-loop follow-on to 3b.
- `-0008c-3b` itself is not wasted: once `-3i-plumbing` lands and a real SEMI
  `SpecialJoinInfo` does reach `addPathsToJoinrel`, 3a/3b's dispatch and
  substitution are the mechanism that will pick it up with zero further
  change — this loop's tests are what make that claim checkable rather than
  a hope, the same standard 3a set for itself.

Deferral ledger: see the `M0142-0008c-3b` row appended this loop (cites this
section and `-3i-plumbing`'s §15 as the shared blocker).

## 21. M0142-0008a-3i-plumbing-a landed — `semiAntiChainLink` + consumers +
Q78-firewall arm, as INERT infrastructure; a live trace also settles §14.3's
open dependency question: item 1's walk extension is dead code without a
coupled predp.go/planner.go change (2026-09-16)

Picked up per the working-set baton's own instruction (§15's "revised resume
point"): items 3-5 of `-3i-plumbing` were filed as one task and repeatedly
found "not sized for one loop" (§14.3, §15). Before writing any of items
3/4/5, this loop traced the exact call chain from `unnestExistsExpr`'s
Semi/Anti node synthesis through to `extractSearchLeaves`'s call site, to
settle a question §14.3/§15 had left open: does extending
`extractSearchLeaves`'s type test alone (item 1) do anything for a real
query, or is it coupled to other changes?

### 21.1 Settled: item 1 is coupled to predp.go, not independently landable

Traced live (not from memory), file:line:

1. `planner.go:1532-1536`: `origChain := f.Child` is captured **before**
   `unnestSubqueriesInPlan` runs — by construction, `origChain` never
   contains a Semi/Anti node, because unnesting has not happened yet.
2. `planner.go:1534`: `unnestSubqueriesInPlan` → `unnestExistsExpr`
   (`unnest.go:4461`, builds the `*Join{Semi/Anti}` node at
   `unnest.go:4772-4781`) wraps `origChain` from ABOVE — the Semi/Anti node
   sits strictly above `origChain`, never inside it.
3. `planner.go:1535`: `runJoinSearchBelowPinned(node, origChain, ctx, cat)` —
   receives the Semi/Anti-bearing `node` plus the original pre-unnest
   `origChain` pointer.
4. `predp.go:83-115`'s descend loop is hard-coded to walk through Semi/Anti
   `*Join` nodes ONLY: its `case *Join` arm requires `x.Type ==
   JoinTypeSemi || JoinTypeAnti` to continue descending (collecting
   `spineJoins`) and explicitly bails (`return newRoot`, `predp.go:100`) on
   any OTHER `*Join` type it might need to pass through. It stops at the
   `*Filter` wrapping `origChain` (`predp.go:86-90`), stored as `target`.
5. `predp.go:139`: `tryJoinSearch(f.Child, f.Predicate, ctx, cat)` is called
   with `f.Child == origChain` — the search's entry point is, by
   construction, always the subtree strictly BELOW every pinned Semi/Anti
   node.
6. `joinsearchseam.go:202,304`: `tryJoinSearch` calls
   `extractSearchLeaves(chain)` where `chain` derives from that same
   `origChain` — which never contained a Semi/Anti node (step 1).

**Consequence**: extending `extractSearchLeaves`'s type test to admit
`JoinTypeSemi`/`JoinTypeAnti` (§14.3 item 1), on its own, is unreachable dead
code for every real query — two independent gates block it (the pre-unnest
`origChain` snapshot, and predp.go's hard bail on non-Semi/Anti `*Join`
nodes), and both must be addressed TOGETHER with the walk extension, not
sequenced as separate loops. This re-sizes items "1" and "4"/"5" from §15's
list into one coupled step; they were never actually separable the way the
numbering implied.

### 21.2 What landed this loop: the inert, independently-verifiable half

Per the corrected sequencing (a Semi/Anti-aware chain-link type is useless
until something calls it, but is ALSO free of plan-shape risk until then —
the same "dispatch-layer-first" shape `-0008c-1/-2/-3a` already used
successfully), this loop landed:

1. **`semiAntiChainLink`** (`internal/optimizer/joinsearchseam.go`) — a
   genuinely separate type from `outerChainLink`, per §15's settled item-2
   answer: `{jointype, lhs, rhs RelSet, pred Expr}`, no
   `preserved`/`nullable` fields (neither concept is meaningful for a join
   with no NULL-extension in either direction).
2. **`semiAntiLinksHaveSJInfos`** — `outerLinksHaveSJInfos`'s analogue:
   matches a link against `ctx.joinInfoList` by jointype + both syntactic
   sides.
3. **`semiAntiOnQualsOK`** — `outerOnQualsOK`'s analogue, simplified per
   §15's "valid equi-correlation between two disjoint RelSets" framing: every
   conjunct of the link's predicate must span BOTH `lhs` and `rhs` and be one
   the search will place (`searchConsumes`); anything else (nil predicate, a
   conjunct confined to one side) declines. Verified against the REAL
   Q69-witness fixture (reusing `extractSearchLeavesAdmitSemiAnti` from the
   §15 probe file) that this ACCEPTS the exact well-formed link
   `outerOnQualsOK` was proven to incorrectly decline in §15 — the positive
   case §15 predicted but did not build.
4. **`problemPairsOuterWithDerived`'s Semi/Anti arm**
   (`relfromjoinlist.go:590`) — added `parser.JoinSemi, parser.JoinAnti` to
   the switch, in the SAME change as items 1-3 above, per §15's own
   instruction not to defer this past the change that first makes Semi/Anti
   admission possible (citing `take3-C-04a-Q78-firewall-classifier` as the
   cautionary precedent). The function's existing "touched" bit computation
   (`sj.SynLefthand | sj.SynRighthand` against each item's derived-ness) does
   not depend on NULL-extension semantics at all, so the fix is exactly this
   one line — no Semi/Anti-specific logic was needed beyond admitting the
   jointype into the switch.
5. Updated `m0142_0008a_3i_plumbing_probe_test.go`'s consumer-#3 assertion:
   its fixture uses nil-table (`baseRelInfo{}`) leaves, which
   `leafIsDerivedInput`'s existing fallback arm conservatively treats as
   derived regardless of node type — so with the new arm live, that
   fixture's expected outcome flips from "declines to flag" (the historical
   gap) to "correctly declines the join" (`true`). New tests in
   `semiantichain_test.go` cover the not-derived case explicitly
   (`TestProblemPairsOuterWithDerivedSemiOverDerived`/`...AntiOverDerived`,
   mirroring the LEFT/RIGHT precedent in `outer_over_derived_test.go`) so the
   nil-table fixture is no longer the only witness for this arm.

**None of this is wired into `extractSearchLeaves`'s production walk or
`predp.go`.** `semiAntiChainLink` has no producer yet; `extractSearchLeaves`
still treats Semi/Anti as an opaque leaf exactly as before. This is
deliberate, matching the sequencing conclusion of §21.1 — the walk extension
is only meaningful bundled with the predp.go/planner.go call-sequence change,
which is a separate, larger, and riskier step (it changes what tree the
search's own `tryJoinSearch` entry point receives for every EXISTS/NOT-EXISTS
query, not just an isolated function's internals).

### 21.3 Verification

`go build ./...` clean. `go vet ./internal/optimizer/...` clean. `go test
./internal/optimizer/...` full pass, including 9 new tests in
`semiantichain_test.go` (positive/negative for both consumers, plus the two
new firewall-arm cases) and the updated probe test. TPC-DS SF0.25 sweep
(private `GOOPG_BIN=tmp/goopg-sf025-bin`, foreground): `PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: same=99 changed=0` — empirically
confirms zero plan-shape impact, importantly INCLUDING the one live producer
of a real Semi/Anti `SpecialJoinInfo` already in `ctx.joinInfoList` today
(`reduceOuterJoins`'s LEFT→ANTI strength reduction, §14.3 item 5's cited
precedent) — the new firewall arm could in principle have changed a plan for
that path alone, and the sweep confirms it does not for any TPC-DS SF0.25
query. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: only
the same pre-existing, unrelated `internal/parser` `GroupedJoinUnaliased`
AST-drift failure seen by the three prior loops; `internal/optimizer` passes.

### 21.4 Resume point

Named `M0142-0008a-3i-plumbing-b` in `fix_plan.md`: the coupled change §21.1
found — move (or duplicate) the `origChain` capture to after
`unnestSubqueriesInPlan` runs (or otherwise make a Semi/Anti-bearing chain
reach `tryJoinSearch`), extend predp.go's descend loop to pass through
non-Semi/Anti `*Join` nodes instead of hard-bailing, extend
`extractSearchLeaves`'s type test to admit `JoinTypeSemi`/`JoinTypeAnti` and
build `semiAntiChainLink`s (landed this loop) via the walk, rebuild
`existsUnnestSJInfo`'s real `RelSet` bits at admission time (§15 item 3), and
retire `predp.go`'s splice-and-reresolve for the newly-admitted cases (§15
item 4). This is still a single, larger, and now more precisely bounded
change — not further decomposable into independently-landable pieces, per
§21.1's finding that items 1/4/5 are one coupled step, not three. Needs its
own dedicated scoping pass before coding (in particular: what happens to
`origChain`'s *other* callers/assumptions if it moves post-unnest, and
whether `chainOnQual`'s `belowNullable` bookkeeping needs a Semi/Anti-aware
arm for INNER links sitting above an admitted Semi/Anti link, per §14.3's own
open question).

## 22. M0142-0008a-3i-plumbing-b scoping pass (2026-09-16) — both §21.4 open
questions answered, PLUS a sixth coupled dependency §21.4 did not name; still
not one loop, decomposition revised

Per §21.4's own instruction ("needs its own dedicated scoping pass before
coding"), this loop did the scoping pass only — no production code changed.
Two things were live-traced (not inferred): the two questions §21.4 asked, and
— because tracing the second one required reading the caller stack one level
further out — a dependency the original 5-item filing never named.

### 22.1 Question 1 answered: `origChain` has no other callers

Grepped every occurrence of the identifier `origChain` in
`internal/optimizer` (not just the two files §21 already named): it appears
in exactly two places, both inside the same block —
`planner.go:1533` (the capture) and `planner.go:1535` /
`predp.go:73,87,107` (the one call site and the one function it is passed
to). There is no third caller, no struct field, no package-level variable —
`origChain` is a plain local `Node` alive only for the duration of one `if
unnestPreDPEnabled() && ...` branch. **This de-risks item 1 by a lot**: there
is no hidden assumption elsewhere in the codebase keyed off when in the
pipeline that pointer is captured.

But the more useful finding is that "move the capture in time" is not
actually the right description of what item 1 needs, and understanding why
matters for scoping the rest. `origChain` is captured as `f.Child` where `f`
is the Filter directly wrapping the *pre-unnest* WHERE-processed tree, and
`unnestSubqueriesInPlan` **wraps that same subtree from above** — it never
replaces or moves `origChain` itself (confirmed by predp.go's own descend
loop successfully finding `x.Child == origChain` *after* unnesting has run,
which would be impossible if unnesting had invalidated the pointer). So
`origChain`, however it is captured, **structurally never contains a
Semi/Anti node** under the current EXISTS/IN-only engagement scope: every
Semi/Anti node `unnestExistsExpr` synthesizes sits strictly on the spine
*above* `origChain`, by construction, for every statement shape S5a currently
engages. "Moving the capture post-unnest" would capture the exact same
pointer value — there is nothing to move.

The real content of item 1 is therefore not a capture-timing fix, it is
**"stop calling `tryJoinSearch` only on the subtree below the pinned spine;
call it on a tree that includes the spine's Semi/Anti nodes too."** That is a
`predp.go`-level (and, per §22.3 below, `joinsearchseam.go`-level) change to
*which tree the search receives*, not a `planner.go`-level change to *when a
variable is captured*. §21.4's "(or otherwise route a Semi/Anti-bearing tree
to `tryJoinSearch`)" parenthetical was the accurate half of the description;
the "move the origChain capture" half is now superseded by this finding.

### 22.2 Question 2 answered: `belowNullable` needs NO Semi/Anti-aware arm

Traced `extractSearchLeaves`'s walk (`joinsearchseam.go:1104-1191`) for what
`below` (the value threaded through `chainOnQual.belowNullable`) would need
to be if the walk's `*Join` type-switch admitted `JoinTypeSemi`/`JoinTypeAnti`
alongside Cross/Inner/Left/Right. `below` exists so an INNER link's own `ON`
qual can be tested against the union of NULL-extended sides at or below it
(§14.3/§19's finding: an `IS NULL` conjunct must not be pushed below the
outer join that produces the NULLs it is testing). SEMI and ANTI joins do not
null-extend *either* side — `reresolveJoinByName`'s own doc comment ("emit
Outer (=Left) only at runtime") and `semiAntiChainLink`'s doc comment
(§21.2, landed this loop's predecessor) already state this for the
`preserved`/`nullable` fields; the same fact applies directly to `below`.

Concretely: a Semi/Anti arm added to the walk's `*Join` type-switch should
recurse into `j.Left` exactly as the existing INNER arm does (computing
`nullLeft`), and — because the RHS stays an opaque, non-reorderable
participant under current S5b-deferral scope (§11-§13; making `x.Right` a
real DP-reorderable relset bit is the *bigger*, still out-of-scope mechanism)
— append `j.Right` as a single leaf via the same path the top-level "not an
admitted join type" branch already uses (`scans = append(scans, n)`,
`joinsearchseam.go:1111-1114`) rather than recursing into it. The arm should
then **return `below` (i.e. `nullLeft`) unchanged**, exactly like the
existing `if j.Type != JoinTypeInner { … }` / `return below, true` line
(`joinsearchseam.go:1173`) — a Semi/Anti link contributes zero to the
NULL-extended set, the same as an INNER link does today. No new field, no new
bit-tracking, no Semi/Anti-aware arm in `chainOnQual` or its consumers
(`joinsearchseam.go:420-440`) is needed. This is a **negative scoping
result**: §14.3's open question is answered "no such arm is needed," which
removes one item from the eventual implementation's surface rather than
adding one.

### 22.3 A sixth coupled dependency §21.4 did not name: `ctx.bindings`/`ctx.joinlist` have no slot for the Semi/Anti RHS

Tracing question 1's "call `tryJoinSearch` on a tree that includes the spine"
one level further out, into what `tryPGShapedJoinSearch` actually does with
its `node` argument, surfaced a gap none of §14/§15/§19/§21 named.

`tryPGShapedJoinSearch` (`joinsearchseam.go:216-`) does not derive relation
count purely from the tree it is handed — it cross-checks the walk's output
against `ctx.bindings`/`ctx.joinlist`, which are **fixed at FROM-clause
resolution time**, before `s.Where` is even resolved
(`planner.go:3096-3103`, `rctx.joinlist = deconstructRangeVars(len(bindings))`,
called from `planFromClause`/`planScanRangeVar` — grepped: this is the ONLY
assignment site of `.joinlist =`/`.bindings = append(...rangeBinding{...})`
in the package). Three separate checks in `tryPGShapedJoinSearch` depend on
this: `nrels := len(ctx.bindings)` (line 226, used for the search's own size
gates); `len(scans) != nprefix` (line 309, `nprefix := jl.nrels()`, a hard
decline on mismatch); and `ctx.bindings[i].offset != cumOffsets[i]` (line
324, per-leaf offset agreement, also a hard decline on mismatch).

The Semi/Anti RHS subtree `unnestExistsExpr` synthesizes is **not a FROM-item
of the original statement** — it is the pulled-up EXISTS subquery body,
built entirely inside `unnestSubqueriesInPlan(node Node) Node` /
`unnestExistsExpr(ex *ExistsExpr, outer Node) (Node, error)`
(`unnest.go:424`, `unnest.go:4461`), and **neither function takes a
`*resolveContext` parameter at all** (grepped both signatures). So if §22.2's
walk extension appends `j.Right` as one more opaque leaf, that leaf has no
corresponding `ctx.bindings[i]` entry and no `ctx.joinlist` representation —
`tryPGShapedJoinSearch`'s existing `len(scans) != nprefix` and per-leaf
offset checks would **decline** the search outright (the safe failure mode:
it declines rather than panicking or miscounting, per the same guard style as
R41/K75's `nprefix > nrels` check at line 277), which means item 3's walk
extension is *itself* inert without also either (a) teaching
`unnestSubqueriesInPlan`'s call chain to append a synthetic `rangeBinding` +
joinlist entry for the Semi/Anti RHS at synthesis time (a `*resolveContext`
plumbing change through `unnestSubqueriesInPlan`/`unnestExistsExpr`'s
signatures — unknown blast radius across their other internal call sites,
e.g. IN-family unnesting, not investigated this loop), or (b) relaxing
`tryPGShapedJoinSearch`'s leaf-count/offset checks specifically for the
walk's Semi/Anti-RHS leaves (harder to get right: those checks are exactly
what catches a genuine desync elsewhere, per R41/K75's own comment, and
carving a special case into them narrows the tripwire for every OTHER caller
too).

This is a real, previously-undocumented **sixth** coupled item — call it
item 6 — on top of §21.4's five. It is discovered by, not solved by, this
scoping pass.

### 22.4 Revised recommendation: this is not one loop, and not two either — split an INERT scaffold from the correctness-changing cutover

§21.4 already said "not sized for one loop." This pass both shrinks two of
the five named items to zero work (§22.1, §22.2) and adds one the original
filing missed (§22.3), so the honest updated shape is: **items 2 (predp.go
pass-through), 3 (walk extension using §22.2's now-settled semantics), 4
(SJInfo rebuild), and 6 (the `ctx.bindings`/`joinlist` gap) are one coupled
step; item 1 turned out to already be satisfied (§22.1); item 5 (retiring the
splice-and-reresolve) must NOT land until the new path is proven to always
succeed for every statement the old path used to handle** — `tryJoinSearch`'s
own decline contract (`joinsearchseam.go:201-206`: `used=false` →
`return node, pred` unchanged) is a *graceful* no-op, but `predp.go`'s
splice-and-reresolve is the thing that currently guarantees a plan for every
engaged EXISTS/IN statement; removing it before the new path is proven
correct on that whole family would regress real queries from "planned" to
"declined; WHERE-clause EXISTS silently unhandled" — a correctness bug, not
a missed optimization. Recommend two sub-tasks, not one, matching the
already-successful `-0008a-3i-plumbing-a` / `-0008c-1/-2/-3a` "dispatch-layer-
first, prove inert before wiring" shape:

- **`M0142-0008a-3i-plumbing-b1`**: land items 2-4 as an INERT scaffold —
  extend `extractSearchLeaves`'s walk (§22.2's semantics), build
  `semiAntiChainLink`s, rebuild the matched `SpecialJoinInfo`'s
  Syn/MinLefthand/Righthand from real leaf bits (item 4, using
  `semiAntiLinksHaveSJInfos` from `-3i-plumbing-a` to find the match) — but
  gate the whole arm behind a conservative eligibility check that, absent
  item 6's `ctx.bindings`/`joinlist` extension, can never actually fire in
  production (the same "callable but never called / proven zero-impact by
  sweep" shape `-3i-plumbing-a` and `-0008c-1/-2` already used). This is the
  next loop-sized task.
- **`M0142-0008a-3i-plumbing-b2`**: item 6's `ctx.bindings`/`joinlist`
  extension (with its own scoping pass into `unnestSubqueriesInPlan`'s other
  call sites first) plus the actual cutover — feed the full spine+origChain
  tree to `tryJoinSearch`, and retire `predp.go`'s splice-and-reresolve
  *only* for the statement shapes empirically proven (TPC-DS sweep, plus a
  dedicated EXISTS/NOT-EXISTS regression set) to be handled end-to-end by the
  new path, keeping the old splice as the fallback for everything else.
  Depends on `-b1` landing first, and likely also depends on enough of
  `M0142-0008c-3c`/`-3d`/`-4`'s path-builder work existing for a real
  Semi/Anti pair to be admissible during DP at all (today `-0008c-2`'s
  admission arm is itself still inert for the same reason, per its own
  landing note).

`fix_plan.md`'s `-3i-plumbing-b` entry is replaced by these two sub-items.

## 23. M0142-0008a-3i-plumbing-b1 landed — the INERT scaffold (2026-09-16)

Per §22.4's decomposition, this loop landed items 2-4 as a scaffold gated
behind an eligibility check the same way `-3i-plumbing-a`/`-0008c-1/-2/-3a`
already proved out: new code exists, is unit-tested directly, and is
provably unreachable from the one production call site.

### 23.1 What landed

`extractSearchLeaves` (`joinsearchseam.go`) gained an `admitSemiAnti bool`
parameter. When `false` — the literal value the ONE production call site
(`tryPGShapedJoinSearch`) passes — the function is byte-identical to before:
a Semi/Anti `*Join` still falls through to the generic "not an admitted join
type" branch and becomes one opaque leaf, exactly as it always has (this
was already true before this loop, since `origChain` never contains a
Semi/Anti node under current engagement scope — §22.1).

When `true`, a new arm intercepted right after the `n.(*Join)` type
assertion (before the generic non-admitted-type branch, so it must run
first):

1. Declines (`return 0, false`) if `!preserved`, mirroring the existing
   Left/Right outer-link decline for the same reason — a link sitting on a
   subtree an admitted outer link above it already null-extends is not a
   shape this walk has proven safe to interpret, whether the link itself
   null-extends anything or not.
2. Recurses into `j.Left` with `preserved` UNCHANGED (Semi/Anti null-extends
   neither side, so nothing needs to clear).
3. Appends `j.Right` as ONE opaque leaf via the exact same
   `scans = append(scans, n)` / `widths = append(...)` / `width += ...`
   triplet the generic non-admitted-type branch uses — no new leaf-append
   mechanism, reusing the existing one directly (§22.2's "RHS stays
   non-reorderable" instruction).
4. Rebases `j.Predicate` into the walk's coordinate space (`rebaseChainQual`,
   same helper the Left/Right/Inner arms already use) and builds a
   `semiAntiChainLink{jointype, lhs, rhs, pred}` from the real leaf-index
   bits (`lhs = leafRangeRelSet(loLeft, loRight)`,
   `rhs = leafRangeRelSet(loRight, hiRight)`), appended to a new `semiAnti
   []semiAntiChainLink` return value.
5. **Item 4**: if the join carries a non-nil `SJInfo` (attached by
   `unnestExistsExpr` via `existsUnnestSJInfo` at synthesis time, always with
   the throwaway `synL=1`/`synR=2` 2-bit numbering — unnest.go:4398-4399),
   overwrites `SJInfo.SynLefthand`/`SynRighthand` AND
   `SJInfo.MinLefthand`/`MinRighthand` with the same `lhs`/`rhs` bits, in
   place, on the same struct the `*Join` node already points at. This is
   safe under `existsUnnestSJInfo`'s own invariant
   (unnest.go:4419-4430: `clause` is always `synL|synR` because
   `unnestExistsExpr`'s belt check refuses a keyless join with no residual,
   so `MinLefthand == SynLefthand` and `MinRighthand == SynRighthand`
   already held under the placeholder numbering) — replacing both hands of
   both pairs with the new bits preserves that same equality under real
   numbering.
6. Returns `nullLeft` unchanged as `below` (§22.2's settled semantics: a
   Semi/Anti link contributes nothing to the NULL-extended union an INNER
   link above it needs).

The one production call site (`joinsearchseam.go`'s `tryPGShapedJoinSearch`)
was updated to `extractSearchLeaves(chain, false)`, discarding the new
`semiAnti` return with `_` — no behavior change, by construction.

### 23.2 Verification

Two new tests in `semiantichain_test.go`, both built on the same
Q69-witness-class fixture (`SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM
t2, t3 WHERE t2.z = t1.x AND t2.y = t3.a)`) the probe and prior sections
already established produces `j.Right = *Project{Child: *Join{Inner}}`:

- `TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo` calls
  the REAL `extractSearchLeaves(j, true)` (not the throwaway probe copy) and
  checks the full chain: 2 leaves (t1, RHS `*Project` as one opaque leaf),
  zero `onQuals`/`outer`, exactly one `semiAntiChainLink` with the correct
  `lhs`/`rhs`/`pred`, that `semiAntiOnQualsOK` accepts it, and that
  `j.SJInfo`'s four hands were rebuilt from the `{1,2}` placeholder to the
  real bits and that `semiAntiLinksHaveSJInfos` then matches the rebuilt
  `SJInfo` against the link. (The fixture is the FINAL planned tree, so
  `j.Predicate` is nil post-hash-method-selection exactly as the §21 probe
  found; the test reconstructs it from `j.LeftKey`/`j.RightKey` before
  calling `extractSearchLeaves`, mirroring the already-established pattern
  in `TestSemiAntiLinksHaveSJInfos_MatchesRealSJInfo` — production's real
  entry point always has `Predicate` populated pre-method-selection, so this
  reconstruction is a test-fixture concern only, not new production logic.)
- `TestExtractSearchLeaves_AdmitSemiAntiFalse_UnchangedFromProduction` calls
  `extractSearchLeaves(j, false)` on the same class of fixture and checks the
  Semi join comes back as the single opaque leaf it always was, with
  `j.SJInfo` untouched — the inertness claim, checked directly rather than
  only argued from the call site.

`go build ./...` clean. `go vet ./internal/optimizer/...` clean. `go test
./internal/optimizer/...` full pass. TPC-DS SF0.25 sweep (private
`GOOPG_BIN=tmp/goopg-sf025-bin`, foreground): `PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: same=99 changed=0` — empirically
confirms zero plan-shape impact, as required for a scaffold change touching
the search's own leaf-extraction seam. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`: `internal/optimizer` passes; the only
failure is the same pre-existing, unrelated `internal/parser`
`GroupedJoinUnaliased` AST-drift issue the prior four loops already found.

### 23.3 Resume point

Unchanged from §22.4: `M0142-0008a-3i-plumbing-b2` — item 6's
`ctx.bindings`/`ctx.joinlist` extension (give the Semi/Anti RHS a
`rangeBinding`/joinlist representation by plumbing a `*resolveContext`
through `unnestSubqueriesInPlan`/`unnestExistsExpr`, with its own scoping
pass into their other internal call sites first — e.g. IN-family unnesting,
not investigated by any loop so far) plus the actual cutover (feed the full
spine+`origChain` tree to `tryJoinSearch`, flip the production call site's
`admitSemiAnti` to `true` only once item 6 makes the walk's new leaves
resolvable, and retire `predp.go`'s splice-and-reresolve only for statement
shapes empirically proven handled end-to-end by the new path). Still likely
also depends on enough of `M0142-0008c-3c`/`-3d`/`-4`'s path-builder work
existing for a real Semi/Anti pair to be admissible during DP at all.

### 23.4 Narrowing correction: item 2 (`predp.go` pass-through) moves to `-b2`, not landed here

§22.4's own text bundled item 2 ("`predp.go`'s descend loop pass through
non-Semi/Anti `*Join` nodes instead of hard-bailing," `predp.go:96-101`) into
`-b1`'s scope alongside items 3/4. Attempting it this loop found it does not
have the same INERT-by-construction property items 3/4 have, and should not
land under `-b1`'s banner:

- Items 3/4 are inert because they are reached only when the NEW
  `admitSemiAnti` parameter is `true`, and the one production call site
  passes a literal `false` — a caller-visible, grep-checkable gate.
- `predp.go`'s descend-loop bail (`predp.go:96-101`) has no equivalent gate
  to attach a parameter to: it already only ever fires on a shape "the
  eligibility pre-check" guarantees cannot occur today (no non-Semi/Anti
  `*Join` node is ever placed on the pinned spine under the current
  EXISTS/IN-only engagement scope — §21.1). Generalizing "bail" to "pass
  through" would therefore be dead code by the SAME "cannot happen under
  current construction" argument the pre-existing comment already makes,
  not by an explicit, independently-checkable condition — a strictly weaker
  and harder-to-verify inertness claim than items 3/4's, and one this loop
  could not falsify with a realistic fixture (constructing a tree with a
  non-Semi/Anti `*Join` on the spine means fabricating a shape no real
  rewrite produces, which tests the change against nothing PG-faithful).
- More importantly, tracing what "pass through" would need to DO once it is
  no longer dead code shows it is not separable scaffolding at all: it only
  has meaning paired with `-b2`'s actual cutover, which changes what tree
  `predp.go` hands to `tryJoinSearch` in the first place (§22.1's "call
  `tryJoinSearch` on a tree that includes the spine"). Landing a
  "pass-through" arm now, with nothing yet routing a wider tree through it,
  would be a change with no caller-visible gate AND no way to exercise it
  even in a unit test without inventing an unrealistic fixture — worse than
  simply deferring it to the task that actually needs it.

`predp.go`'s pass-through is therefore re-filed into `M0142-0008a-3i-plumbing-b2`'s
scope (updated in `fix_plan.md`), alongside item 6 and the cutover, rather
than split out. `-b1`'s landed scope is precisely items 3 (walk extension)
and 4 (SJInfo rebuild), as verified in §23.1-§23.2.

## 24. M0142-0008a-3i-plumbing-b2 — item 6's own scoping pass (2026-09-16):
the call-graph plumbing is small, but the real cost is the synthetic
binding's downstream visibility

§23.3 carried forward an unstarted obligation from §22.3: before coding
item 6 (give the Semi/Anti RHS leaf a `ctx.bindings`/`ctx.joinlist`
representation), do "its own scoping pass into their other internal call
sites first — e.g. IN-family unnesting, not investigated by any loop so
far." This loop did that pass — no production diff, same recon shape as
§22. Two separable findings, of very different size.

### 24.1 Finding A — the `*resolveContext` call-graph is small and self-contained (settled, low risk)

Traced every caller of `unnestSubqueriesInPlan` via `find_referencing_symbols`
(not grep — grep would double-count the identifier appearing in comments,
e.g. unnest.go:4133's reference). The full call graph is exactly two roots
and five internal functions, **all inside `internal/optimizer/unnest.go`**,
with **zero callers outside the package and zero test-file callers of
`unnestSubqueriesInPlan` itself**:

- **Root 1**: `planner.go:1533` (S5a pre-DP path, inside the
  `unnestPreDPEnabled()` arm) — `ctx *resolveContext` and `cat
  catalog.Catalog` are both already in scope (same block stamps
  `ctx.queryPathkeys` two lines earlier).
- **Root 2**: `planner.go:1619` (legacy post-search path) — same function
  (`planSelectWithSettings`), same `ctx`/`cat` already in scope.
- **Five internal functions**, each needing the new parameter threaded
  through so their own `unnestSubqueriesInPlan(...)` calls can pass it
  down: `unnestSubqueriesInPlan` itself (7 recursive self-calls across its
  `*Filter`/`*Join`/`*Project`/`*Aggregate`/`*Sort`/`*Limit` arms),
  `walkSubqueryPlansInExpr` (called from `unnestSubqueriesInPlan`'s
  `*Filter` arm, itself recurses across `*SubqueryExpr`/`*InExpr`/
  `*ExistsExpr`/`*BinaryOp`/etc.), `unnestScalarWithResiduals` (called
  from `unnestSubquery`), and the three driver functions named in the
  task filing: `unnestSubquery`, `unnestInExpr` (which itself calls
  `unnestNonCorrelatedInExpr` for the non-correlated case — so that's a
  sixth signature, not a separate call site), and `unnestExistsExpr`.

So: **7 function signatures total** (`unnestSubqueriesInPlan`,
`walkSubqueryPlansInExpr`, `unnestScalarWithResiduals`, `unnestSubquery`,
`unnestInExpr`, `unnestNonCorrelatedInExpr`, `unnestExistsExpr`), confined
to one file, rooted at two call sites that already hold the context they'd
need to pass in. The "IN-family unnesting" call sites §22.3 flagged as
unexamined (`unnestInExpr`/`unnestNonCorrelatedInExpr`, `unnest.go:3351`/
`:3494`) are ordinary recursive self-calls in the same shape as the
EXISTS side (`unnestExistsExpr`, `unnest.go:4582`) — no divergent wiring
needed. **This half of item 6 is mechanical and low-risk**: add a
`*resolveContext` parameter to all 7, pass it through unchanged at every
recursive call, no design decision required.

### 24.2 Finding B — the synthetic binding's *consumption* side is the actual size of item 6, and it is not small

The plumbing above only gets a `*resolveContext` to the point where
`unnestExistsExpr` builds the Semi/Anti join. What item 6 actually needs
is for `joinsearchseam.go:329`'s offset-agreement check (`ctx.bindings[i].offset
!= cumOffsets[i]`, guarding the leaf-count/offset seam `-3i-plumbing-b1`'s
walk extension feeds into) to find a `ctx.bindings` entry for the RHS leaf
whose `.offset` matches the leaf's position in the walk's left-to-right
order — i.e. `unnestExistsExpr` must **append a new, synthetic
`rangeBinding`** to the OUTER `ctx.bindings` representing the whole RHS
subtree as one opaque relation (offset = current bindings' tail, width =
the RHS leaf's own output-column count).

That synthetic binding does not stay contained to the join-search seam —
`ctx.bindings` is a shared slice read by roughly two dozen other sites
across `planner.go` (`grep -c '\.bindings\b' planner.go` → 24 occurrences,
none audited before this loop for behavior on a binding with `table: nil`
that represents no real catalog relation). Two were spot-checked directly
this loop and the result is a real, evidenced hazard, not a hypothetical
one:

- `planner.go:7821-7833` (unqualified column-name lookup, part of
  `resolveColumnRef`'s fallback path) skips a binding when
  `b.qualifiedOnly` is set, **before** touching `b.table.Columns` — so a
  synthetic binding marked `qualifiedOnly: true` is safe here.
- `planner.go:2523-2533` (the `FOR UPDATE`/`FOR SHARE` locking `emit`
  closure, reached via the no-explicit-target-list loop at
  `planner.go:2539-2546`) has **no `qualifiedOnly` check at all** — it
  iterates every entry in `ctx.bindings` unconditionally and dereferences
  `b.table.OID` in its very first statement. A query shaped like `SELECT
  ... WHERE EXISTS (...) FOR UPDATE` (no target list) would reach this
  loop with the synthetic binding present and **nil-pointer-panic on
  `b.table.OID`**, `qualifiedOnly` or not.

So `qualifiedOnly` — the one existing escape hatch on `rangeBinding` —
already fails to cover every consumer, and the honest scope of item 6 is
auditing (or bypassing) all ~24 sites, not adding one flag. Two structural
options, neither attempted or decided yet:

1. **Extend the flag.** Add a stronger `opaque bool` (or repurpose/harden
   `qualifiedOnly` semantics) and individually audit and gate all ~24
   `ctx.bindings` iteration sites to skip an opaque binding — correct but
   large, and every miss is a live crash risk on a real query shape
   (`FOR UPDATE` alone already proves the risk is not theoretical).
2. **Side-channel instead of a shared-slice entry.** Keep the RHS leaf's
   offset/width bookkeeping in a structure `joinsearchseam.go` reads
   directly (parallel to, not inside, `ctx.bindings`) and never expose it
   to `resolveColumnRef`, star-expansion, locking, or any other consumer
   at all. This matches the leaf's own semantics better — `-3i-plumbing-b1`
   made it explicitly **opaque and non-reorderable** (§22.2/§23.1 item 3),
   i.e. never meant to be individually resolved by name — so a channel
   that is invisible to name resolution by construction removes the audit
   burden entirely rather than trying to gate every consumer correctly by
   hand.

Option 2 looks like the right direction (it also avoids the tuning-vs-
absorption question entirely — there is no PG quantity being substituted
here, just an internal bookkeeping representation) but has not been
designed in any detail — figuring out its exact shape and how
`joinsearchseam.go:329/339` would read from it instead of `ctx.bindings[i]`
is the concrete next step, not yet started.

### 24.3 Resume point

`M0142-0008a-3i-plumbing-b2` stays open, scope unchanged in `fix_plan.md`
apart from this narrowing: item 6 splits into 6a (§24.1's `*resolveContext`
plumbing — mechanical, ready to code) and 6b (§24.2's binding-visibility
design decision — side-channel vs. audited-flag, needs its own design
paragraph and a settled choice **before** 6a is wired up to actually
append anything, since coding 6a in isolation with nothing yet deciding
what shape the append takes would just be unused plumbing). Do not attempt
the cutover (flipping `admitSemiAnti` to `true` in production) before 6b
is resolved — the `FOR UPDATE` crash in §24.2 is a real regression risk,
not a style concern.

## 25. Item 6b design pass (2026-09-16) — Finding C: `cumOffsets` itself
misattributes any real leaf positioned after a Semi/Anti RHS leaf, a
wrong-answer bug independent of and worse than §24.2's `ctx.bindings`
crash risk

Picking up §24.3's "design item 6b concretely" resume point (side-channel
vs. audited-flag was framed purely as a `ctx.bindings`-visibility question).
Live-traced what actually happens once a synthetic leaf sits inside
`extractSearchLeaves`'s walk, rather than assuming the walk's own
`cumOffsets` bookkeeping is fine once `ctx.bindings` is handled. It is not:
the search's internal coordinate system breaks on its own, with no
`ctx.bindings` involvement at all.

### 25.1 Confirmed live: `qualAC` gets attributed to the wrong leaf

Trace of the shape `(A SEMI JOIN B) JOIN C ON qualAC` (qualAC references
`C`, resolved pre-unnest against real `ctx.bindings` at absolute column
index `wA+k`, where `wA` = A's real width, `k` = C's column offset within
its own row):

- `extractSearchLeaves`'s walk (joinsearchseam.go:1118-1273) reaches the
  outer INNER join (`j.Left`=Semi(A,B), `j.Right`=C) as the ROOT call
  (`walk(node, true)`, line 1269). `base := width` (line 1199) is captured
  **before** recursing into `j.Left`/`j.Right`, and since this is the root
  call, `width` is `0` at that instant regardless of what the Semi/Anti
  node inside `j.Left` later appends. `if pred != nil && base != 0` (line
  1224 / the general-INNER-link arm) is therefore false, and `qualAC` is
  stored into `onQuals` **completely unrebased** — it stays at absolute
  index `wA+k`, exactly as `ctx.bindings` originally resolved it.
- Walking `j.Left` triggers the Semi arm (lines 1124-1186): walks `A` (a
  real leaf, `width` 0→`wA`), then unconditionally appends `j.Right`=`B`
  as one opaque leaf and adds ITS real width too (`width +=
  len(j.Right.Output())`, line 1150) — `width` is now `wA+wB`.
- Walking `j.Right`=`C` appends it as leaf index 2, so `cumOffsets` (built
  at joinsearchseam.go:322-328 purely from `widths[]` in walk order) has
  `cumOffsets[2] = wA+wB` — **not** `wA`.
- `relidsOfExpr(qualAC, cumOffsets)` (joinrestrict.go:470-493) attributes
  a `ColumnRef.Index` to a leaf by pure numeric containment against
  `cumOffsets`'s ranges, checked in leaf order. `qualAC`'s reference to
  `C` carries index `wA+k`. For any `k < wB`, that index falls inside
  `cumOffsets[1]..cumOffsets[2]` = `[wA, wA+wB)` — leaf **1**, the
  synthetic `B` — and is misattributed there instead of to leaf 2 (`C`),
  because leaf 1 is checked first in `relidsOfExpr`'s linear scan and the
  ranges overlap in absolute-index terms whenever `wB > 0`.
- This is not a corner case gated behind a rare shape: `wB` (the RHS
  leaf's real output width) is **not guaranteed to be 0**. A bare
  `EXISTS(SELECT 1 …)` has `wB=0` (dormant), but the M14 zero-equijoin NL
  path (unnest.go:4469-4478, already inside `-b1`'s targeted shape set)
  and the R3-4 composite-equijoin path (unnest.go:4480-4516) both keep
  real columns live in `j.Right.Output()`, so `wB>0` for exactly the
  shapes item 6 exists to admit.
- `cumOffsets` is not a private array the offset-agreement check alone
  reads: grepping every caller confirms it is the **same** array (no
  per-caller copies) fed into `relidsOfExpr`/`tableForCol` from
  `buildRestrictInfos`, `partitionConjunctsForJoinPlanning`,
  `deriveOuterLinkConstants`, `outerOnQualsOK`,
  `innerOnQualsBelowNullableOK`, `searchConsumes`, AND `semiAntiOnQualsOK`
  itself — every one of them would misattribute a real leaf's qual to a
  preceding synthetic leaf the same way once ANY leaf follows an admitted
  Semi/Anti node in walk order. This is strictly worse than §24.2's
  finding: not a crash on an unusual statement shape, but a silent
  **wrong join-legality / wrong-rows** risk on the exact statement shapes
  item 6 is being built to admit.

### 25.2 Why "just zero the synthetic leaf's width in `cumOffsets`" does not work

The tempting fix — give the synthetic leaf's own slot 0 width so it never
steals a later leaf's columns — collides with a genuine, already-landed
consumer that needs the OPPOSITE:

- `unnestExistsExpr` (unnest.go:4680-4705) builds the Semi/Anti join's own
  correlation predicate using `outerKey.Index = params[0].OuterRef.Index`
  (the outer/LHS reference, untouched) and `innerKey.Index = outerWidth +
  params[0].SubCol.Index` (the RHS reference) — i.e. it ALREADY encodes
  "the RHS leaf occupies a real-width slice starting right after
  `outerWidth`", the same scheme `cumOffsets` uses today.
- `semiAntiOnQualsOK` (joinsearchseam.go:1425-1444) requires
  `relidsOfExpr(c, cumOffsets)` to find the semi/anti link's own predicate
  spanning both `lk.lhs` and `lk.rhs` — i.e. it depends on the RHS's real
  width being visible in `cumOffsets` at the RHS's own slot. Zeroing it
  breaks this existing, already-tested check instead of fixing the
  ancestor-qual misattribution.
- Zeroing only the FORWARD PROPAGATION (RHS keeps its own [lo,hi) range
  for self-classification, but does not advance the running total for
  what comes after) does not fix it either: it just makes `B`'s range
  `[wA, wA+wB)` and `C`'s range `[wA, wA+wC)` **overlap** instead of `C`
  landing past `B` entirely — `relidsOfExpr`'s first-match linear scan
  still resolves any `k < wB` to `B`, not `C`. The two interpretations of
  "index `wA+k`" (an RHS-local reference vs. a real ctx.bindings-resolved
  `C` reference) are genuinely different quantities that happen to share
  the same integer, not an off-by-a-constant problem a single shift can
  repair.
- Rebasing every ordinary qual near a Semi/Anti insertion point via the
  existing `rebaseChainQual`/`cloneExprRefs` machinery was also
  considered and ruled out as the general answer: the shift a given qual
  needs is a function of WHICH leaf(s) it references, not of which join
  node it is attached to — a qual naming two real leaves that straddle
  the insertion point would need a piecewise shift the current one
  scalar `base` per join node cannot express, and rewriting AST
  `ColumnRef` nodes that are otherwise expected to stay byte-identical to
  what the parser produced is exactly the class of change this project
  treats as high-risk (`pattern_sibling_paths_must_agree`-shaped: a missed
  qual site is a silent wrong-rows bug, not a build error).

### 25.3 Direction that avoids rewriting any pre-existing qual: decouple
leaf/RelSet-bit space from column-index space

The conflict exists only because the code currently conflates two spaces
into one `cumOffsets []int`:

1. **leaf/RelSet-bit space** — walk position (`0..nprefix-1`), what
   `scans`/`widths`/`semiAntiChainLink.lhs`/`.rhs`/DP itself speak. Must
   stay in natural walk order; nothing here needs to change.
2. **column-index space** — what `ColumnRef.Index` values live in and what
   `relidsOfExpr`/`tableForCol` classify against. Currently assumed to be
   a monotonic prefix-sum of walk-order `widths[]`, which is exactly the
   assumption a synthetic leaf violates.

Proposed direction: leave every REAL leaf's column-index range **exactly**
as `ctx.bindings` already assigned it — untouched, no rewriting, so every
pre-existing qual (onQuals, outer-link quals, anything already resolved
against the real FROM list) keeps working with zero code changes at the
qual site. Give each SYNTHETIC (Semi/Anti RHS) leaf a column-index range
**appended after the total real width** instead of inline at its walk
position — e.g. the first synthetic leaf's range starts at `totalRealWidth`,
the second at `totalRealWidth + firstSyntheticWidth`, and so on, regardless
of where each sits in walk order. `unnestExistsExpr`'s `innerKey.Index`
construction (unnest.go:4694-4705) would source its base from this
"next synthetic slot" counter instead of `outerWidth` — a small, local
change confined to unnest.go's own index construction.

Consequence for the shared machinery: `relidsOfExpr`/`tableForCol`'s
single monotonic `cumOffsets []int` assumption no longer holds once
synthetic ranges are appended out of walk-position order — their
range-matching loops (joinrestrict.go:470-493, :575-584) need generalizing
from "scan a monotonic prefix-sum array" to "scan an explicit per-leaf
`(lo, hi)` table" (still one linear pass per call, just not required to be
sorted). This is the concrete code change item 6b's eventual coding step
must make: build the `(lo, hi)` pairs once in `joinsearchseam.go` (real
leaves keep their `ctx.bindings`-derived ranges verbatim; synthetic
leaves get the out-of-band ranges above) instead of the bare
`cumOffsets []int` at lines 322-328, and thread that table everywhere
`cumOffsets` is threaded today.

This resolves §24.2's Finding B for free, as a side effect rather than a
separate design choice: since no `ctx.bindings` entry is ever added or
mutated under this scheme, none of the ~24 unaudited `ctx.bindings`
consumer sites (including the unguarded `FOR UPDATE` crash site) are
touched at all. The "side-channel" §24.2 left undesigned and the fix for
Finding C turn out to be the same mechanism: a per-leaf table
`joinsearchseam.go` owns and reads directly, never exposed to
`resolveColumnRef`, star-expansion, locking, or any other `planner.go`
consumer.

**Not yet resolved by this loop** (next concrete step): whether the
FINAL winning plan's build/emit step — wherever `-b1`'s SJInfo-rebuild
(item 4, §22.4) currently re-derives real leaf-index bits post-search —
also needs an analogous re-derivation of `innerKey`/`outerKey`.Index once
the winning tree's actual real LHS width is known at build time. It may
already re-derive `outerWidth` fresh from `outerChild.Output()` when the
final `Join` node is constructed (mirroring how `unnestExistsExpr` builds
it today), in which case the out-of-band synthetic region only matters
during the search's own legality/costing decisions and never reaches the
emitted plan — this needs a live trace of the "rebuild the tree from the
winning leaf order" step before coding, not an assumption either way.

### 25.4 Resume point

`M0142-0008a-3i-plumbing-b2` item 6b is now two coupled sub-decisions,
not one: (a) §24's `ctx.bindings`-visibility question, and (b) this
section's `cumOffsets`/`relidsOfExpr` coordinate-space conflict — and
§25.3 shows a single mechanism (the per-leaf `(lo,hi)` side-channel
table) settles both at once, which is the reason to treat them as one
design, not two. Concrete next steps, in order: (1) verify §25.3's open
question about the final-tree build step; (2) replace `cumOffsets []int`
in `joinsearchseam.go` with the per-leaf `(lo,hi)` table and update
`relidsOfExpr`/`tableForCol` (joinrestrict.go) to scan it instead of
assuming a monotonic prefix sum; (3) point `unnestExistsExpr`'s
`innerKey.Index` construction at the new "next synthetic slot" counter;
(4) only then code item 6a's `*resolveContext` plumbing, since 6a's
`ctx.bindings` append is now understood to be unnecessary rather than
merely undesigned — the per-leaf table replaces it outright. Do not
attempt the cutover (`admitSemiAnti=true` in production) before this
lands and is verified against a live fixture exercising the
`(A SEMI JOIN B) JOIN C ON qualAC`-shaped case directly (a new unit test,
not just the existing `-b1` tests, none of which exercise a real leaf
positioned after a Semi/Anti node).

## 26. §25.4's open question answered: the fix is confined to search-internal
bookkeeping, and does not reach the emitted plan (2026-09-16)

Traced §25.4's remaining open question ("does the final winning-plan build
step already re-derive `outerWidth` fresh, or does it also need the
out-of-band scheme") by reading, not assuming.

### 26.1 There are TWO distinct `cumOffsets` arrays sharing a name

`grep -l cumOffsets internal/optimizer/*.go` (excluding tests) returns five
files, and they split into two unrelated coordinate spaces:

1. **Chain layer** (`joinsearchseam.go`): the `cumOffsets` §25 diagnosed,
   built from `extractSearchLeaves`'s `widths[]` in WALK order — includes
   the synthetic Semi/Anti RHS leaf's real width, which is §25's bug.
   Consumed only by `joinrestrict.go`'s `relidsOfExpr`/`tableForCol` and
   the chain-recognition/legality functions the working set already lists
   (`buildRestrictInfos`, `partitionConjunctsForJoinPlanning`,
   `deriveOuterLinkConstants`, `outerOnQualsOK`,
   `innerOnQualsBelowNullableOK`, `searchConsumes`, `semiAntiOnQualsOK`)
   plus `local_filters.go`.
2. **Bushy/joinlist layer** (`relfromjoinlist.go`): `joinlistProblem.cumOffsets`
   — one entry per joinlist ITEM (not per chain leaf), built from the
   `bindings []rangeBinding` slice `bushy.go`'s pre-search pipeline
   assembles directly from `ctx.bindings` (real FROM items only; see
   `relfromjoinlist.go`'s `joinlistProblem` doc comment, lines 84-99). This
   is what `joinsearch.go:430` reads (`rel.baseOffset = bindings[i].offset`)
   to set `RelOptInfo.baseOffset` (`path.go:636-654`), which
   `createplanjoin.go`'s `baseRelLayout`/`translateToLayout`
   (lines 114-171, 205-...) use to rewrite every join clause's `ColumnRef`
   from search coordinates into the coordinates of the ACTUALLY EMITTED
   node at plan-build time — goopg's `set_join_references` analogue (the
   function's own header cites `setrefs.c:2557`).

`relidsOfExpr`/`tableForCol` are generic, parameterized by whichever
`cumOffsets` their caller passes (`joinrestrict.go:470,575`) — the same
function serves both layers, with two independent, non-interacting
coordinate spaces. §25's bug lives entirely in flavor 1; flavor 2 has no
Semi/Anti-awareness at all today, buggy or otherwise, because nothing feeds
it one: `joinlistProblem.bindings` is built exclusively from real
`ctx.bindings` FROM items, and a Semi/Anti pair is not yet an admissible
joinable unit in the bushy DP — building that admission is exactly
`M0142-0008c-3c`/`-3d`/`-4`'s still-unstarted job (fix_plan.md ~3668-3670,
~3724-3725).

### 26.2 Every actual plan-build site already re-derives width fresh, never from search bookkeeping

Cross-checked the codebase's own established idiom at every site that
constructs a REAL emitted node's coordinates, not just `unnestExistsExpr`:
`createplannl.go:311` (`outerLay := in.lay[:len(in.outer.Output())]`),
`unnest.go:2692,2911,3373,3537,4634` (`outerWidth := len(outerChild.Output())`),
`memoize.go:109`, `nl_index_join.go:839,1542`. Every one re-derives its
width/offset FRESH from the actually-built `Node`'s real `Output()` at build
time; none reads either flavor of `cumOffsets`. `cumOffsets` (both flavors)
is exclusively a SEARCH-time/legality-time artifact — no `createplan*.go`
function reads it, confirmed by the same five-file grep in §26.1 (none of
the `createplan*.go` files appear in it).

### 26.3 Answer and its consequence for the resume plan

The fix is confined to search-internal (chain-layer) bookkeeping —
`joinsearchseam.go` / `joinrestrict.go` / `local_filters.go` /
`unnest.go`'s `unnestExistsExpr` — and does not reach `relfromjoinlist.go`,
`path.go`'s `baseOffset`, or any `createplan*.go` build function, because
those consume the wholly separate, currently synthetic-leaf-free flavor-2
coordinate space. §25.4's four-step resume order is unchanged; step (1) is
now closed with evidence instead of open.

**Forward note for whoever picks up `M0142-0008c-3c`/`-3d`/`-4`** (not a
scope addition to `-b2`, filed here only so it is not rediscovered from
scratch): once a Semi/Anti pair becomes a real bushy-DP-admissible joinable
unit, `joinlistProblem.bindings`/`cumOffsets` (flavor 2) will face an
analogous "does the RHS get its own binding slot, and if so how does its
width contribute to `baseOffset` for everything after it" design question.
`-b2`'s fix does NOT pre-solve this — it lives in the other coordinate
space entirely — so treat it as a fresh design question when that work
starts, informed by but not settled by §25/§26.

## 27. §25.4 step (2) landed — `cumOffsets []int` replaced by a per-leaf
`[]leafSpan` table across the chain layer (2026-09-16)

Coded §25.4's step (2) (build the `(lo,hi)` table and update
`relidsOfExpr`/`tableForCol` to scan it) and, since Go signatures are
call-site-wide, its unavoidable transitive closure: every function that
threads `cumOffsets` through to those two — `buildRestrictInfos`,
`searchConsumes`, `deriveOuterLinkConstants`, `outerOnQualsOK`,
`innerOnQualsBelowNullableOK`, `semiAntiOnQualsOK`
(all `joinsearchseam.go`/`joinrestrict.go`), and
`partitionConjunctsForJoinPlanning` (`local_filters.go`) — now take
`[]leafSpan` (`joinrestrict.go`, new type: `struct{ lo, hi int }`). The
production construction site (`joinsearchseam.go`'s `tryPGShapedJoinSearch`,
~line 322) now calls a new `buildLeafSpans(widths, semiAnti)` instead of
hand-rolling the cumulative sum; with `semiAnti` empty (admitSemiAnti stays
false there today) it reduces to the identical arithmetic the old code did,
by construction (own unit test: `TestBuildLeafSpansAttributesRealLeafAfterSyntheticCorrectly`,
`semiantichain_test.go`).

### 27.1 Correction to §26.3: the shared functions DO reach `relfromjoinlist.go`, even though flavor 2's own state does not

§26.3 stated the fix "does not reach `relfromjoinlist.go`." That is true of
flavor 2's OWN field (`joinlistProblem.cumOffsets`, still `[]int`,
still built by `relfromjoinlist.go` exactly as before) and its own local
`cum` variable — but it is **not** true of the call graph: §26.1 already
noted `relidsOfExpr`/`tableForCol` are generic, reused by both layers, and
building this loop surfaced the concrete production proof —
`relfromjoinlist.go:679`'s `s.clauses = buildRestrictInfos(prob.conjuncts, 0,
cum)` is flavor 2's own live call into the newly-`[]leafSpan`-typed function,
not a test-only artefact. Reconciled with a one-line adapter,
`spansFromCumulative(cum []int) []leafSpan` (`joinrestrict.go`), which
reshapes a plain monotonic prefix-sum array into the per-leaf table with NO
behavior change (flavor 2 has no synthetic leaves, per §26.1, so the reshape
is exact) — `relfromjoinlist.go:679` now reads
`buildRestrictInfos(prob.conjuncts, 0, spansFromCumulative(cum))`. The
inverse, `cumulativeFromSpans(spans []leafSpan) []int`, does the same job at
the one point flavor-1 state flows INTO a flavor-2 struct: the chain-layer
seam's own `joinlistProblem{cumOffsets: ...}` literal
(`joinsearchseam.go`, ~line 577) now reads
`cumulativeFromSpans(cumOffsets)`. Both adapters are valid only for a
contiguous, monotonic span table (no out-of-band synthetic range) — true at
both call sites today because production never reaches them with
`admitSemiAnti=true`; that constraint is documented at each adapter's
definition, not just here.

### 27.2 Step (3) — unnest.go's `innerKey.Index` — explicitly NOT landed this loop, and why it needs its own investigation

§25.4 listed step (3) ("point `unnestExistsExpr`'s `innerKey.Index`
construction at the new 'next synthetic slot' counter") as the next
sub-step after (2). It is deliberately **not** done here. Reading
`unnest.go:4680-4706` before touching it surfaced a fact §25.3's own
proposal did not account for: `innerKey.Index = outerWidth +
params[0].SubCol.Index` is not dead/test-only scaffolding — it is LIVE
production code, reached by every `EXISTS`/`NOT EXISTS` unnest today
(`admitSemiAnti` gates the SEARCH's descent into a Semi/Anti node, not
whether `unnestExistsExpr` itself runs), and the comment directly above it
states its consumer: "The executor's `evalHashKey` is given a padded row of
width `(leftWidth + rightWidth)` ... its `Index` must be `outerWidth +
innerColIndex`" — i.e. this value is read at EXECUTION time against a
padded row built from exactly this join's OWN two children, a strictly
LOCAL two-input coordinate space, not the wider statement's FROM-cumulative
one `buildLeafSpans` operates in.

Naively swapping its base for "the next synthetic slot" (a quantity that,
per §25.3, is only knowable once the FULL statement's real leaf set is
final — i.e., at SEARCH time) would require `unnestExistsExpr` — which runs
at REWRITE time, per-`EXISTS`, typically before the enclosing statement's
full real leaf set is even assembled — to know a number it structurally
cannot compute yet, and any change here risks corrupting the (LeftKey,
RightKey) pair the executor's `evalHashKey` reads for EVERY existing
`EXISTS` query, not just the ones this milestone's cutover targets. That is
a correctness-critical, high-blast-radius change on live code, not a
"thread the type through" mechanical step like (2) was. It needs its own
scoping pass — specifically, tracing whether `j.Predicate`'s copy of this
index (consumed by the search, pre-method-selection) and `j.LeftKey`/
`j.RightKey`'s copy (consumed by the executor, post-method-selection) are
actually the SAME embedded value or can be allowed to diverge — before any
code changes, not folded into a loop already carrying step (2)'s blast
radius.

### 27.3 Verification

`go build ./...` clean. `go test ./internal/optimizer/...` — all tests pass
(including the 9 pre-existing chain-layer/semi-anti tests this loop's type
change touched, and the new
`TestBuildLeafSpansAttributesRealLeafAfterSyntheticCorrectly`, which pins
§25.1's `qualAC` misattribution as FIXED: a 3-leaf `(A SEMI JOIN B) JOIN C`
shape with `B`'s real width nonzero now attributes `C`'s qual to leaf 2, not
leaf 1). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: only
`internal/parser` fails, and every failure is the pre-existing
`GroupedJoinUnaliased` AST-drift gap (`dc91bd6b7`, unrelated, tracked
separately, unchanged by this loop). Live plan-shape/row-count check since
this is a planner-package change: TPC-DS SF0.25 fast regression gate
(`scripts/tpcds-sf025-regression.sh sweep`) — `PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE: queries=99 same=99 changed=0`, i.e.
byte-identical to the pre-change baseline, exactly as predicted: production
never exercises `admitSemiAnti=true`, so `buildLeafSpans`/`spansFromCumulative`/
`cumulativeFromSpans` are all pure reshapes of the same values the old code
computed. TPC-H spotcheck (`scripts/tpch-spotcheck.sh`) SKIPPED — the
already-tracked M0142-0003k blocker (shared `:65433` cluster's `tpch`
database still emptied, reload blocked on human authorization), unrelated
to this change.

### 27.4 Resume point

§25.4's steps (1) and (2) are done. Step (3) (unnest.go) needs its own
dedicated scoping pass per §27.2 before it can be coded — trace whether
`j.Predicate`'s and `j.LeftKey`/`j.RightKey`'s copies of the RHS index are
the same embedded value or may diverge, first. Step (4) (item 6a's
`*resolveContext` plumbing) stays understood as unnecessary, per §25.4's
own text — the per-leaf table replaces it outright, nothing left to do
there. Still do NOT flip `admitSemiAnti=true` in production before step (3)
lands and is verified against a live fixture exercising
`(A SEMI JOIN B) JOIN C ON qualAC` through the REAL `unnestExistsExpr` +
search path end to end (this loop's new test exercises the mechanism
directly via manually-built `semiAntiChainLink`s, not through the rewrite —
by design, since the rewrite path is exactly what step (3) has not yet
touched).

## 28. Step (3) scoping pass — traced live, not assumed: the index-rebase
premise is wrong, and the real gap is bigger than `unnest.go` (2026-09-16)

Did the dedicated scoping pass §27.4 asked for — traced whether
`j.Predicate`'s and `j.LeftKey`/`j.RightKey`'s copies of the RHS index are
the same embedded value or may diverge, per §27.2's open question — by
reading the actual producer and consumer code, not by re-deriving from the
comment. Three findings, each changing the shape of the remaining work.

### 28.1 Finding A: `j.Predicate` already uses the SAME local-per-subtree
convention as every other join type, and `extractSearchLeaves`'s existing
rebase already handles it — zero special-casing needed

`unnestExistsExpr`'s `outerKey`/`innerKey` (unnest.go:4681-4706) and its
`joinPredicate` (built by `liftResidualConjunctsWithOffset` over
`residualsWithPairs`, unnest.go:4760) use IDENTICAL index arithmetic: the
outer operand is re-resolved by NAME against `outerChild.Output()` (R3-4,
so its `Index` is LOCAL to `outerChild`, 0-based), and the inner operand
gets `outerWidth + originalIndex`. This is exactly the "local numbering,
0-based at this join's own leftmost leaf" convention `extractSearchLeaves`'s
walk already assumes for EVERY join type (`joinsearchseam.go`:1204-1207's
own comment: "its qual was resolved against a schema that starts at its
leftmost leaf"). The Semi/Anti admission arm (lines 1160-1167) reuses the
walk's existing, type-generic `rebaseChainQual(pred, base)` call verbatim —
no Semi/Anti-specific rebase code exists or is needed. §27's own new test
(`TestBuildLeafSpansAttributesRealLeafAfterSyntheticCorrectly`) already
pins this compatibility at the mechanism level. **Conclusion: `j.Predicate`
was never at risk; §25.3's "rebase `innerKey.Index`" proposal targeted the
wrong field.**

### 28.2 Finding B: `j.LeftKey`/`j.RightKey` are execution-only and outside
every search-time function's reach — and the codebase's OWN convention for
reconciling a join's keys after its children move already exists and
already covers Semi/Anti

Grepped every reader of `j.LeftKey`/`j.RightKey` in the optimizer package:
`extractSearchLeaves`, `buildLeafSpans`, `relidsOfExpr`, `tableForCol` — the
entire chain layer this milestone touches — read NONE of them. Their only
readers are execution/cost code (`cardinality.go`, `join_hash_keys.go`,
`join_exec_keys.go`, `nl_index_join.go`) and one reconciliation pass:
`joinlayout.go`'s `reresolveJoinByName(j)` re-binds `j.LeftKey`/`j.RightKey`
(and `j.Predicate`, separately) by NAME+SourceTableIdx against the join's
ACTUAL current `n.Left`/`n.Right` schemas — the same idiom §26.2 found at
every `createplan*.go` site, applied here to the "old DP/MHJ-packer moved a
subtree in place and left stale indices" case rather than a fresh build.
Its caller, `reconcileNLILayoutBody` (joinlayout.go:543-550), already
special-cases Semi/Anti CORRECTLY: it recurses into `n.Left` (so a
reordered/rebuilt left subtree gets its indices re-derived) but SKIPS
recursing into `n.Right` for `JoinTypeSemi`/`JoinTypeAnti` — matching
`extractSearchLeaves`'s "RHS stays one opaque leaf" rule exactly — then
still calls `reresolveJoinByName(n)` unconditionally, which rebinds
`LeftKey`/`RightKey` too. **This pass is explicitly skipped for a
PG-shaped-search-produced tree** (`reconcileNLILayout`'s
`isSearchedTree(node)` guard, backed by `assertSearchedTreeNeedsNoReconcile`
in searchedtree.go — the search's own coordinate math is asserted to need
no by-name fallback at all), so it does not currently apply to anything
`tryPGShapedJoinSearch` produces — but its EXISTENCE and its already-correct
Semi/Anti carve-out show the codebase already has a working, generalizable
answer to "how does a join's keys get re-derived when its children's
internal shape changes", and it required zero new code for Semi/Anti. If a
future PG-shaped-search plan-build arm for an admitted Semi/Anti pair needs
the analogous treatment, this is the pattern to extend (by-name rebind
against the actually-built child, mirroring `reresolveJoinByName`), not an
index-arithmetic scheme computed at `unnestExistsExpr`'s rewrite time.

**Conclusion: `innerKey.Index`/`outerKey.Index` are never read at search
time, and the codebase's existing key-reconciliation idiom already handles
Semi/Anti as a first-class case wherever it currently runs. §25.4's step
(3), as originally framed ("point `innerKey.Index` at the next synthetic
slot counter"), targets a field no search-time consumer reads and solves a
problem the by-name reconciliation idiom already has a generic answer for.
Step (3) is NOT a coding task — it is moot.**

### 28.3 Finding C (the one that matters): the ONE production call site
cannot reach a Semi/Anti node at all today, regardless of `admitSemiAnti`,
and this is a bigger gap than step (3) ever was

Traced `extractSearchLeaves`'s one production caller
(`joinsearchseam.go:309`, inside `tryPGShapedJoinSearch`) back to where its
`chain` argument comes from: `runJoinSearchBelowPinned(node, origChain, ctx,
cat)` (predp.go), called from `planner.go:1533-1535`:

```go
f := node.(*Filter)
origChain := f.Child          // captured BEFORE unnest runs
node = unnestSubqueriesInPlan(node)     // unnestExistsExpr runs HERE
node = runJoinSearchBelowPinned(node, origChain, ctx, cat)
```

`origChain` is a snapshot of `f.Child` taken strictly BEFORE
`unnestSubqueriesInPlan` (which is what actually invokes `unnestExistsExpr`
and splices in the Semi/Anti `*Join`). `origChain` therefore structurally
CANNOT contain a Semi/Anti join — not "contains one but the flag declines
it", but "the tree handed to `extractSearchLeaves` never has one to find".
This matches `semiantichain_test.go`'s
`TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo`, whose
own setup comment (written by a prior loop, lines 202-208) already flags
this: it has to hand-splice a Semi/Anti `*Join` into a **post-planned**
fixture and reconstruct a `Predicate` from `LeftKey`/`RightKey` to exercise
the arm at all, because no real call reaches it with one.

**Consequence: flipping `admitSemiAnti=true` at the one existing production
call site (`joinsearchseam.go:309`) is STILL a no-op**, independent of
anything §25-§27 fixed. Making it matter requires a NEW call that walks the
POST-unnest tree — i.e. some form of the `predp.go` descend-loop extension
already named in `-3i-plumbing-b2`'s own fix_plan.md scope ("extend
`predp.go`'s descend loop to pass through non-Semi/Anti `*Join` nodes
instead of hard-bailing") turns out to be **load-bearing for reachability,
not just an optimization** — without it (or an equivalent new call site),
none of items 6a/6b's remaining wiring has anything to operate on. This was
implicit in the existing fix_plan.md scope but not previously stated as the
gating precondition for `admitSemiAnti` to have ANY effect.

### 28.4 Finding D: a genuine, previously undocumented correctness gap for
whoever DOES wire that call — `semiAntiChainLink.pred` silently drops the
join's own equijoin condition in the common single-key case

Once a future call reaches a real Semi/Anti join, `extractSearchLeaves`
captures `semiAntiChainLink{pred: j.Predicate}` — nothing else
(`semiAntiChainLink` has exactly four fields: `jointype, lhs, rhs, pred`,
joinsearchseam.go:1449-1453). But `j.Predicate`, for the common
single-equi-key EXISTS/NOT EXISTS case (`params[0]`, no extra residuals),
deliberately does NOT contain that equality — it lives ONLY in
`j.LeftKey`/`j.RightKey` (unnest.go:4708-4726: the EXISTS conjunct is
dropped from `newConjuncts`/`filter.Predicate` entirely, "the join encodes
the equality predicate via (LeftKey, RightKey)"). This is a deliberate,
CORRECT optimization for direct execution — the hash match already enforces
it, so re-checking it in `Predicate` would be redundant — but it means
`pred` alone is NOT a complete description of the join's semantics.

Contrast with the ordinary-join convention: `createplanjoin.go`'s
`joinInputs.joinPredicate` (line 492-504) explicitly appends an equality
conjunct for EVERY hash-key pair into the emitted node's `Predicate`, on
top of any residual — this is exactly the discipline the Q9 multi-equality
bug (cited in that function's own doc comment) was fixed by, and it is what
makes an ordinary hash join's `Predicate` self-sufficient. `unnestExistsExpr`
does NOT follow this discipline for `params[0]` (params[1:] DO get folded
into the predicate as residuals via `extraPairConjuncts`, per R3-4 — only
the ONE key actually used for the hash is omitted).

**Consequence: any future consumer that reconstructs a plan node from
`semiAntiChainLink.pred` alone — which is exactly what `-0008c-3c`/`-3d`/`-4`'s
still-unbuilt Semi/Anti plan-build arm will need to do once a pair is
admitted into the bushy DP (§26.3's forward note already flagged that layer
as a fresh design question; this is the specific correctness content that
design must not miss) — would silently build an unconditional Semi/Anti
join for the single-key case (matching PG's own nestloop-only Semi/Anti
convention confirmed by `createplanjoin.go`'s `createHashJoinPlan` comment,
C-03c: "SEMI/ANTI are nestloop-only" in the search's own path generator
today), over-matching every LHS row instead of the correct equi-semi-join.
The fix, when that work starts, is cheap and localized: fold
`j.LeftKey`/`j.RightKey` into an explicit `OpEq` conjunct before capturing
`pred` in `extractSearchLeaves`'s semi/anti arm (mirroring
`joinInputs.joinPredicate`'s own idiom), not a change to `unnestExistsExpr`
itself.**

### 28.5 Resume point

§25.4's 4-step plan is now fully resolved, but not the way it was framed:
step (1) design — done (§25/§26). Step (2) leafSpan table — landed (§27).
Step (3) `innerKey.Index` rebase — **moot** (§28.1/§28.2: no search-time
reader exists, and the codebase's by-name reconciliation idiom already
covers Semi/Anti wherever it runs today). Step (4) item 6a's
`*resolveContext` plumbing — already understood as unnecessary (§25.4/§27.4,
unchanged).

None of that closes `-3i-plumbing-b2`, because §28.3 surfaces a gap step
(3) never named: **the predp.go descend-loop extension is not an
optional/parallel piece of item 6b's scope — it is the precondition for
`admitSemiAnti=true` to reach any Semi/Anti node at all.** The item's
remaining scope (fix_plan.md's existing text: predp.go pass-through, item
6a's `ctx` plumbing, the flip itself) is unchanged in kind but now known to
require, in this order: (i) wire a call that reaches the post-unnest tree
(predp.go's descend-loop extension or equivalent — reachability, blocking
everything else), (ii) when that lands, fix §28.4's dropped-equijoin gap in
the SAME loop (it would otherwise be a live wrong-rows bug the moment
`admitSemiAnti` starts doing anything), (iii) only then does flipping
`admitSemiAnti=true` behind a real end-to-end fixture (§27.4's existing
gate) become a meaningful test rather than a guaranteed no-op. Likely still
depends on enough of `M0142-0008c-3c`/`-3d`/`-4` existing for a real
Semi/Anti pair to be bushy-DP-admissible, per §26.3's forward note — this
scoping pass did not re-examine that dependency.

Still design-only this loop: no production code changed, nothing to
regression-gate beyond confirming the trace against live source (every
file/line cited above was read this loop, not recalled).

## 29. §28.4's dropped-equijoin fix landed standalone, ahead of reachability wiring (2026-09-16)

§28.5 ordered the remaining work as (i) wire reachability, (ii) fix §28.4's
dropped-equijoin gap in the SAME loop as (i) — "or `admitSemiAnti=true`
would silently turn into an unconditional (Cartesian-like) Semi/Anti the
moment it does anything." That ordering constraint is about **exposure**,
not about which piece must be coded first: (ii) only matters once (i) makes
`admitSemiAnti=true` reachable, but nothing stops (ii) from landing standalone
first, since with the one production call site still passing `admitSemiAnti
=false` (unchanged — `joinsearchseam.go`'s `tryPGShapedJoinSearch`), the arm
this fix touches stays fully inert, exactly the same "prove inert before
wiring" precedent `-3i-plumbing-b1` used. This loop landed (ii) alone, as
its own bounded, independently-testable step, deferring (i) (the harder,
higher-blast-radius `predp.go` descend-loop change touching the live DP
routing path) to the next loop rather than bundling both into one change —
consistent with `m0074_partial_scope_lessons`'s "bound the change, verify
incrementally" instinct memory and the practice card's Q9-rebind hang
precedent (`M0072-0002`).

**Landed:** `extractSearchLeaves`'s Semi/Anti arm (`joinsearchseam.go`,
immediately before the existing `rebaseChainQual`/`base` handling) now folds
`j.LeftKey`/`j.RightKey` into an explicit `OpEq` conjunct ANDed onto
`j.Predicate` before capturing `pred` into the `semiAntiChainLink`, gated on
`j.LeftKey != nil && j.RightKey != nil` (true exactly when `unnestExistsExpr`
built a hash-keyed join, i.e. `len(params) > 0`) — mirroring
`joinInputs.joinPredicate`'s idiom (`createplanjoin.go:492-504`) exactly as
§28.4 specified. The fold happens BEFORE the `rebaseChainQual` call: the key
expressions live in the same "local, 0-based at this join's own leftmost
leaf" coordinate space `j.Predicate` already uses (confirmed live: `RightKey
.Index = outerWidth + params[0].SubCol.Index`, `unnest.go:4694-4705` — a
LOCAL merged-schema offset, not a global one), so one `rebaseChainQual` call
over the combined predicate is correct; rebasing the key equality and the
residual separately would have been redundant at best and a coordinate-space
mismatch at worst if the two conventions ever diverged.

**New test** `TestExtractSearchLeaves_AdmitSemiAnti_FoldsKeyEquijoinIntoPred`
(`semiantichain_test.go`) pins the actual gap directly: a bare correlated
`EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x)` — no residual beyond the single
equijoin — leaves `j.Predicate` naturally `nil` after `Plan()` (asserted, not
assumed). Before this fix, `extractSearchLeaves(j, true)` would have produced
`semiAnti[0].pred == nil` (declined by `semiAntiOnQualsOK`, per
`TestSemiAntiOnQualsOK_DeclinesNilPredicate`); after, it asserts exactly one
conjunct, structurally `(LeftKey = RightKey)` by pointer identity, and that
`semiAntiOnQualsOK` accepts it. This is a stronger witness than the existing
`TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo`, which
pre-patches `j.Predicate` by hand before calling `extractSearchLeaves` —
exactly the gap this fix closes, so that test's own workaround is no longer
load-bearing (left in place unmodified: it still exercises the
already-populated-Predicate case, a real second shape `unnestExistsExpr` can
produce when residuals exist alongside the key).

**Verified zero production behavior change** (as expected — `admitSemiAnti`
is `false` at every reachable call site, so this arm never runs in
production yet): `go build ./...` clean; `go vet ./internal/optimizer/...`
clean; full `go test ./internal/optimizer/...` pass (all existing tests plus
the new one); TPC-DS SF0.25 sweep `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`,
`PLAN-SHAPE: same=99 changed=0`; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` shows only the pre-existing unrelated
`internal/parser` `GroupedJoinUnaliased` failure (`internal/optimizer` itself
passes). TPC-H spotcheck SKIPPED per the standing M0142-0003k blocker
(shared `:65433` cluster's `tpch` data still needs the human-authorized
reload; unrelated to this change).

**Resume point (unchanged from §28.5 otherwise):** the remaining scope is
step (i) alone now — wire a call that reaches the post-unnest tree
(`predp.go`'s descend-loop extension, `predp.go:96-101`'s current hard-bail
on a non-Semi/Anti `*Join`, or an equivalent new call site) so `admitSemiAnti
=true` stops being a guaranteed no-op. Only once that lands does flipping the
flag behind a real end-to-end fixture (§27.4's existing gate) become a
meaningful test — this loop's fix removes what would otherwise have been a
LIVE wrong-rows trap the moment (i) lands, but does not itself make
`admitSemiAnti=true` reachable. Likely still depends on enough of
`M0142-0008c-3c`/`-3d`/`-4` existing for a real Semi/Anti pair to be
bushy-DP-admissible, per §26.3's forward note.

## 30. Step (i) scoping pass — the §26.3 dependency note was backwards, and a
new, deeper blocker found (2026-09-16)

Two findings this loop, both from live tracing (fix_plan.md cross-check +
`joinsearchseam.go` read), neither from re-deriving the prior 29 sections.

**Finding 1 — §26.3's "depends on `M0142-0008c-3c`/`-3d`/`-4`" note is
backwards.** `.ralph/fix_plan.md`'s own `M0142-0008c-3c`/`-3d` entries state
the dependency in the OPPOSITE direction: *"Also now blocked on
`M0142-0008a-3i-plumbing-b2` — do not pick up before that lands; no TPC-DS
measurement can distinguish 'correct but unreachable' from 'wrong' while
`addPathsToJoinrel` never receives a real SEMI/ANTI `sjinfo`."* `-0008c-3a`/
`-3b` (the dispatch-arm ports these two items build on) are already `[x]`
DONE. So the true shape is: **`-3i-plumbing-b2` unblocks `-0008c-3c`/`-3d`,
not the reverse** — §26.3's forward note (repeated verbatim at the tail of
§28.5 and §29) was carried forward across three sections without
re-verification against fix_plan.md's own text. This removes one false
prerequisite from step (i)'s critical path: `-3i-plumbing-b2` does NOT need
`-0008c-3c`/`-3d`/`-4` to land first.

**Finding 2 — a second, deeper reachability blocker, independent of the
`predp.go` descend loop, inside `tryPGShapedJoinSearch` itself
(`joinsearchseam.go:216-350`).** The obvious first design for step (i) —
thread an `admitSemiAnti bool` through `tryJoinSearch` (4 call sites:
`predp.go:139`, `planner.go:1544/1569/1606`; only `predp.go:139` would ever
pass `true`) and have `predp.go`'s descend loop stop pinning the outermost
spine Semi/Anti `*Join` so the tree it hands to `tryJoinSearch` actually
contains one — is **not sufficient by itself**, traced live line by line:

- `tryPGShapedJoinSearch`'s preamble computes `nrels := len(ctx.bindings)`
  (`:226`) and `nprefix := jl.nrels()` from `ctx.joinlist` (`:261`), **both
  fixed at FROM-clause-resolution time, strictly before
  `unnestSubqueriesInPlan` runs** (re-confirmed live this loop, matching
  §22.3's original finding for a different call site).
- After `extractSearchLeaves(chain, admitSemiAnti)` returns (`:309`), the verify
  `if len(scans) != nprefix { … return … false }` at `:314-317` ("leaf-count"
  decline) compares the search's own leaf count against that FROZEN
  pre-unnest `nprefix`. A chain rooted above a Semi/Anti join with
  `admitSemiAnti=true` returns one MORE leaf (the synthetic RHS leaf `-b1`
  already builds) than `ctx.joinlist` knows about, so `len(scans) ==
  nprefix+1` and this exact line declines the search with `"leaf-count"`
  every time. Two more `ctx.bindings`-keyed checks downstream
  (`:333`'s per-leaf `offset` agreement, `:347`'s `spine-offset-disagreement`)
  would fail the same way for the same reason if `:314` were bypassed.
- This is a **different** mechanism than what §25/§26/§27 already fixed.
  §25-§27's `cumOffsets`→`[]leafSpan` work fixed attribution INSIDE the
  chain walk (`relidsOfExpr`/`tableForCol`, fed by `extractSearchLeaves`'s
  own `widths` return, e.g. the `cumOffsets := buildLeafSpans(widths, nil)`
  local at `:331` and the `relidsOfExpr(c, cumOffsets)` call at `:408`) —
  those are fine with a widened `scans`/`widths` because they derive fresh
  from `extractSearchLeaves`'s own return values, not from `ctx.bindings`/
  `ctx.joinlist`. The blocker found here is the OUTER gating in
  `tryPGShapedJoinSearch`'s preamble, which is keyed off the frozen
  pre-unnest `ctx.bindings`/`ctx.joinlist` and has no knowledge of a
  synthetic leaf at all.

**Consequence for §25.4's item-6a verdict.** §25.4 confirmed "6a's
`*resolveContext` plumbing" unneeded, but that finding was scoped to
whether a live `ctx.bindings` ENTRY is needed for the chain-internal
column-index mechanism (answer: no, the per-leaf span table replaces it).
It did not examine — and this loop confirms it did NOT cover —
`tryPGShapedJoinSearch`'s own SEPARATE, earlier use of `ctx.bindings`/
`ctx.joinlist` as SIZE/COUNT oracles (`nrels`, `nprefix`, the three decline
checks above). Those are a genuinely different consumer of the same two
fields. §25.4's "unneeded" verdict stands for the mechanism it examined and
does NOT extend to this one.

**Two candidate designs for step (i), neither coded this loop:**

1. **Widen `ctx.joinlist`/`ctx.bindings`** (or a fresh, unnest-aware
   equivalent of the two counts they supply) so `nrels`/`nprefix` account
   for the synthetic Semi/Anti RHS leaf before `tryPGShapedJoinSearch`'s
   preamble runs. Reopens the exact side-channel-vs.-audit question §24.2
   raised (a live `ctx.bindings` append is visible to ~24 other consumer
   sites, one of which — `FOR UPDATE`/`FOR SHARE` no-target-list locking,
   `planner.go:2523`/`:2539` — nil-pointer-panics on it unconditionally) —
   except now the append only needs to be visible to `tryPGShapedJoinSearch`'s
   three preamble reads, not to the chain walk, which narrows the audit
   surface versus §24.2's framing.
2. **A dedicated, parallel entry point** for the "search includes one pinned
   Semi/Anti leaf" case that bypasses `tryPGShapedJoinSearch`'s
   `ctx.bindings`/`ctx.joinlist`-keyed preamble (`:223-350`) entirely and
   reuses only the post-preamble DP-core logic (conjunct partitioning
   onward, `:396+`, which is already `cumOffsets`/`widths`-driven and
   therefore already unnest-agnostic). Avoids touching the ~24-site
   `ctx.bindings` consumer surface at all, at the cost of a second code path
   through the search seam that has to be kept in sync with the first.

**Next step:** decide between the two designs above (design-doc-only
decision, not scoped this loop) before touching `predp.go` or
`joinsearchseam.go` again — coding the `predp.go` descend-loop extension
first, as originally planned, would have produced a change that still hits
the `:314` "leaf-count" decline and is therefore just as inert as today, for
a reason the descend-loop change itself cannot fix. `M0142-0008c-3c`/`-3d`
remain available to pick up independently once `-3i-plumbing-b2` lands
(Finding 1) but are not themselves blocking step (i)'s design decision.

## 31. §30's (a)/(b) decision settled — neither: a third option, (c), reuses
`semiAnti`'s own leaf-position data and touches neither `ctx.bindings` nor
`tryPGShapedJoinSearch`'s call shape (2026-09-16)

Re-read §30's two candidates against `extractSearchLeaves`'s actual return
shape (`joinsearchseam.go:1093`) and `buildLeafSpans`'s actual body
(`:1316-1335`, landed by §27) before choosing, rather than picking blind
between the two framed options. Both turn out to be worse than a third
option neither §24.2 nor §30 considered, because both were framed before
`semiAnti []semiAntiChainLink` existed as a return value with enough
information to avoid the trade-off entirely.

### 31.1 `extractSearchLeaves` already tells the caller exactly which `scans`
indices are synthetic — `tryPGShapedJoinSearch` just never asked

Every `semiAntiChainLink` carries `rhs RelSet` — a single-bit set marking the
synthetic leaf's own walk position (`joinsearchseam.go:1152-1159`:
`loRight := len(scans)` captured immediately before the one `scans = append(
scans, j.Right)`, so `rhs` always covers exactly one index). `len(semiAnti)`
is therefore the exact count of synthetic leaves in `scans`, and OR-ing every
`.rhs` together (exactly what `buildLeafSpans:1317-1319` already does
locally, as `synthetic RelSet`) gives the caller a bitset answering "is
`scans[i]` synthetic" for every `i`, using data `extractSearchLeaves` already
computes and already returns. `tryPGShapedJoinSearch` (`:309`) captures
`semiAnti` today only to discard it (`_` — the fifth return value, verified
live at `:309`); it never needed a NEW field or a NEW call, only to stop
throwing this one away.

### 31.2 Why this beats both §30 candidates

- **vs. (a) — widen `ctx.bindings`/`ctx.joinlist`:** (a) was framed as
  "narrower than §24.2's full audit, since only 3 preamble reads need the
  count" — true, but "the count" was never the hard part; the hard part
  §24.2 found was a LIVE entry becoming visible to whichever of the ~24
  consumer sites iterate `ctx.bindings` without a guard (the `FOR UPDATE`
  nil-deref). Any live append to the shared slice reopens that regardless of
  how few of *this* function's own reads intended to use it — visibility is
  a property of the slice, not of the reader. §31.1 needs zero entries
  appended anywhere: `semiAnti` is a value already flowing into this exact
  function on every call, scoped to its own stack frame.
- **vs. (b) — a parallel entry point reusing only the post-preamble DP
  core:** (b)'s own stated cost was "a second code path through the search
  seam that has to be kept in sync with the first" — a real ongoing
  maintenance tax, and it does not remove the preamble's actual job (the
  `nrels`/`nprefix`/size/offset checks exist to catch a real desync class,
  per the `R41` comment at `:263-266` — TPC-DS Q78 hit exactly this before
  R41 existed). Duplicating the seam does not need to happen: §31.1's data
  lets the EXISTING preamble absorb synthetic leaves with local arithmetic,
  not a bypass.

### 31.3 The three preamble checks, corrected (design only — not coded this
loop)

Using `synthetic := OR of semiAnti[*].rhs` (computed once, mirroring
`buildLeafSpans`'s own local variable) and `numSynthetic := len(semiAnti)`:

1. **`:314`'s leaf-count check** — `len(scans) != nprefix` becomes
   `len(scans) != nprefix+numSynthetic`. `nprefix` itself (`jl.nrels()`,
   real-FROM-item count from `ctx.joinlist`) is UNCHANGED — synthetic leaves
   are never real FROM items, so widening `nprefix` itself (part of what
   (a) proposed) would have been wrong on its own terms, independent of the
   visibility question.
2. **`:333`'s per-leaf offset-agreement loop** (`ctx.bindings[i].offset !=
   cumOffsets[i].lo`) — currently assumes `scans[i]` and `ctx.bindings[i]`
   are the same real item at the same index, which a synthetic leaf breaks
   for every real leaf positioned at-or-after it (an off-by-however-many-
   synthetic-leaves-precede-it shift, not just a length mismatch). Fix: walk
   `scans` with `i`, walk `ctx.bindings` with a SEPARATE counter `j` that
   only advances past real leaves — `if synthetic&leafRangeRelSet(i,i+1) !=
   0 { continue }` (skip: no real-FROM oracle exists to check a synthetic
   leaf against), else compare `ctx.bindings[j].offset == cumOffsets[i].lo`
   and `j++`.
3. **`:347`'s spine-offset-disagreement check** (`ctx.bindings[nprefix]
   .offset != prefixTotalWidth`, `prefixTotalWidth := cumOffsets[len(
   cumOffsets)-1].hi`) — **found broken by this same trace, independent of
   (a)/(b)/(c):** `buildLeafSpans` (§27) places synthetic leaves' spans
   OUT-OF-BAND, appended after the total REAL width, but `spans[]` itself
   stays indexed by WALK position, not partitioned real-then-synthetic — so
   whenever the LAST leaf in walk order happens to be the synthetic one (an
   `EXISTS` clause with nothing joined after it inside the prefix — plausibly
   the common case, not a corner one), `cumOffsets[len(cumOffsets)-1].hi`
   reads the SYNTHETIC leaf's out-of-band `hi`, which is `totalRealWidth +
   (that leaf's own width)`, not `totalRealWidth`. Comparing that against
   `ctx.bindings[nprefix].offset` (a real, FROM-clause-derived quantity) would
   false-decline every such shape. Fix: `prefixTotalWidth` must be the REAL
   total width only — either have `buildLeafSpans` additionally return the
   `realOffset` value it already computes internally (`:1327`) as a second
   result, or compute it locally in the preamble as `sum(widths[i] for i
   where synthetic bit at i is unset)`. This was not visible from §30's
   framing (which treated `cumOffsets`/`prefixTotalWidth` as already correct
   and only `nrels`/`nprefix` as needing attention) — it only surfaced from
   reading `buildLeafSpans`'s body against this specific call site's usage,
   not from re-deriving the design.

### 31.4 Resume point

Design settled: **(c)**, not (a) or (b) — reuse `semiAnti`'s own
leaf-position bits, no `ctx.bindings`/`ctx.joinlist` mutation, no duplicate
DP entry point. §31.3 items 1-2 are mechanical once `synthetic`/`numSynthetic`
exist locally; item 3's `prefixTotalWidth` fix is a small, separate
correction inside the same change (not a new blocker — same commit).

**Step (i) landed 2026-09-16 (`M0142-0008a-3i-plumbing-b2`, this section's own
next step).** `tryPGShapedJoinSearch` (`joinsearchseam.go`) now:
item 1 — the leaf-count check reads `len(scans) != nprefix+len(semiAnti)`
inline (no helper needed, pure arithmetic); items 2-3 — extracted into a new
package function `pgShapedOffsetChecksOK(cumOffsets, semiAnti, widths,
bindingOffsets, hasSpine, spineOffset) (declineReason string, ok bool)`
(`joinsearchseam.go`, right after `buildLeafSpans`), which walks `cumOffsets`
with a separate real-leaf-only counter into `bindingOffsets` (item 2) and
compares the spine's offset against a REAL-only total width recomputed from
`widths` while skipping synthetic indices (item 3), exactly as designed
above. `extractSearchLeaves(chain, false)`'s discarded 5th return (`_`) is
now captured as `semiAnti` and threaded through; the production call site's
`admitSemiAnti` literal is UNCHANGED (`false`), so `semiAnti` is always `nil`
there and every new branch reduces to the old plain checks — confirmed by
the TPC-DS SF0.25 sweep (`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`,
`PLAN-SHAPE: same=99 changed=0`, comparing directly against the pre-change
commit) and by `go test ./internal/optimizer/...` (full package, no
regressions). Three new direct unit tests exercise the previously-inert
`numSynthetic>0` arithmetic against `pgShapedOffsetChecksOK` itself (no
`*resolveContext`/full-tree fixture needed, mirroring `-3i-plumbing-b1`'s
"prove inert before wiring" shape): `TestPgShapedOffsetChecksOK_
ReducesToPlainChecksWhenNoSemiAnti` (the inertness claim itself),
`TestPgShapedOffsetChecksOK_RealLeafAfterSynthetic` (item 2 — the shape
`§25.1`/`§31.3` traced: a real leaf positioned after a synthetic one, which
the OLD plain per-index loop would have compared against the wrong
`ctx.bindings` entry, or panicked outright since `len(scans) >
len(ctx.bindings)` whenever any synthetic leaf exists), and
`TestPgShapedOffsetChecksOK_SyntheticLastInWalkOrder` (item 3 — a trailing
synthetic leaf, where the OLD `cumOffsets[len-1].hi` read a
`totalRealWidth + thatLeaf'sOwnWidth` value and would have false-declined a
well-formed spine). All three live in `semiantichain_test.go`, next to the
existing `-3i-plumbing-b1`/`buildLeafSpans` tests they build on.

**Still not done (step (ii), a separate later loop, do not collapse into
this one):** the `predp.go` descend-loop extension (§30's original step (i)
target) to actually feed a Semi/Anti-bearing tree into `tryJoinSearch`, plus
flipping `admitSemiAnti=true` at the production call site, verified against
a live end-to-end fixture per §27.4's existing gate — the higher-blast-radius
live-DP-routing change §28.3/§29 already flagged as needing to be bounded on
its own.

## 32. Step (ii) scoping pass (2026-09-16) — corrected mechanism, a live
Q10 trace, and the two-phase design that makes the wiring low-risk

Traced live (`joinsearchseam.go`, `predp.go`, `planner.go`, and TPC-DS
`query10.sql`) before touching either file. Three findings, no production
change.

### 32.1 §30 Finding 2's "4 call sites" framing was the pre-step-(i) plan,
not the current mechanism — `admitSemiAnti` is ONE literal, not a parameter

§30 (written before the preamble fix) proposed threading a `bool` through
`tryJoinSearch`'s 4 call sites (`predp.go:139`, `planner.go:1544/1569/1606`).
That plan predates §31's actual implementation and was never updated to
match it. What step (i) actually built: `admitSemiAnti` is a **local literal
argument** to `extractSearchLeaves(chain, false)` inside
`tryPGShapedJoinSearch` (`joinsearchseam.go:309`) — there is no parameter on
`tryJoinSearch`, `tryPGShapedJoinSearch`, or any of the 4 call sites. "Flip
`admitSemiAnti` to `true` at the production call site" (§31.4's own words)
means **changing that one literal from `false` to `true`**, full stop.

This is provably safe for the other 3 callers without any gating: `chain`
only ever contains a Semi/Anti node when it was captured AFTER
`unnestSubqueriesInPlan` ran. `planner.go:1544` (Filter-wrapped legacy path)
and `:1569`/`:1606` (the two nil-predicate legacy calls, R40/K69's
whereQual==nil case and M0134-0188's outer-link-with-no-WHERE case
respectively) all run BEFORE `unnestSubqueriesInPlan` in every code path
that reaches them (confirmed by reading the surrounding `if`/`else if`
chain: the S5a branch and the legacy branches are mutually exclusive at
`planner.go:1524-1574`, and the outer-link branch at `:1581` is a
completely separate `else if` off the top-level `s.Where != nil` check).
So flipping the literal makes `semiAnti` always empty for those 3 callers —
identical to today — and only `predp.go`'s call (the one call site that
runs post-unnest) can ever produce a non-empty `semiAnti`. **No gating,
feature flag, or new parameter is needed for the flip itself** — the actual
gating is entirely in whether `predp.go` ever hands `tryJoinSearch` a chain
that structurally contains a Semi/Anti node, which today it does not (§28.3).

### 32.2 Live Q10 trace: the real TPC-DS witnesses hit the SUNK shape, not
the "nothing sunk" one — and the common case already drops the Filter for
free

Read `bench/tpcds/runtime_goopg/tpcds-data/queries/query10.sql` (one of
this task's own cited unblock targets) end to end: its WHERE is
`c.c_current_addr_sk = ca_address_sk AND ca_county IN (...) AND cd_demo_sk
= c.c_current_cdemo_sk AND EXISTS(...) AND (EXISTS(...) OR EXISTS(...))`.
The three non-EXISTS conjuncts reference only `origChain`'s own three
relations (`customer`, `customer_address`, `customer_demographics`) — they
are exactly `predp.go`'s documented "sunk" case
(`[Filter{retained}](Semi/Anti…(Filter{sunk}(origChain)))`), not the
"nothing sunk" shape. This matters for two reasons:

1. It confirms Phase A (`runJoinSearchBelowPinned`'s existing, unchanged
   narrow `tryJoinSearch(f.Child=origChain, f.Predicate=sunk pred, …)` call)
   already runs for the query this milestone cites as its unblock target —
   nothing about step (ii) needs to touch Phase A.
2. `runJoinSearchBelowPinned`'s own splice logic (`predp.go:135-143`) already
   has the property step (ii) needs for free: `if newChild, newPred :=
   tryJoinSearch(...); newPred == nil { newTarget = newChild }` — when the
   sunk predicate's conjuncts are ALL legally pushable into the searched
   join tree (the common case: simple equalities between origChain's own
   relations), the wrapping Filter is **removed entirely**, and the spliced
   result is the searched join tree with no Filter node above it. So for
   the case that matters, after Phase A splices back, the tree directly
   under the innermost pinned Semi/Anti join (`spineJoins[len-1].Left`) is
   already Filter-free and walkable end-to-end by
   `extractSearchLeaves`'s admitted-Semi/Anti arm (§23.1's `walk`, which
   only breaks on a `*Filter` node — confirmed by reading the switch:
   `*Join` is the only admitted-recursion case, everything else including
   `*Filter` becomes ONE opaque leaf). The residual-Filter-survives case
   (some sunk conjunct is not legally pushable) is the one shape a Phase B
   widened search would have to decline on, gracefully, not something that
   needs its own code path — see §32.3.

The "nothing sunk" shape (§30/predp.go's third documented case) is
real per the doc comment but **not exercised by either cited unblock
target** — deferred as a separate, smaller, lower-priority question (does
`runJoinSearchBelowPinned`'s current unconditional no-op for that shape ever
fire for an ENGAGED statement, and if so is it a live gap relative to
`planner.go:1569`/`:1606`'s own nil-predicate `tryJoinSearch` idiom for the
same "no Filter, still want DP" situation) — not blocking, not filed as its
own item yet pending a concrete witness.

### 32.3 The two-phase design

**Phase A — unchanged.** `runJoinSearchBelowPinned`'s existing narrow
`tryJoinSearch(f.Child, f.Predicate, ctx, cat)` call and splice, exactly as
today. Nothing here changes; §32.2 confirms it already does the right thing
for the cited witnesses.

**Phase B — new, additive, gracefully-declining.** After Phase A's existing
splice-back completes (`put(newTarget)` in the current code), and only when
`len(spineJoins) > 0` (a Semi/Anti spine actually exists — the degenerate
"unnest declined" shape has none), attempt a SECOND `tryJoinSearch` call
rooted at `spineJoins[0]` (the outermost pinned Semi/Anti join) with a
`nil` top predicate — there is no additional residual predicate above the
spine's own joins to push (any outer retained-Filter predicates stay
handled exactly as today, by `spineFilters`'s existing bottom-up remap).
This call reaches `tryPGShapedJoinSearch` → `extractSearchLeaves(spineJoins[0],
true)` once §32.1's literal is flipped, and its `walk` recurses through
every nested pinned Semi/Anti join (§23.1's arm already handles arbitrary
nesting — confirmed by re-reading: the recursive `walk(j.Left, preserved)`
call hits the SAME Semi/Anti arm again for a nested one) down to the
(already Phase-A-searched, per §32.2, usually Filter-free) origChain leaves.

On success (`ok`), the result REPLACES the entire `spineJoins[0]`..origChain
subtree in `newRoot`, and `spineJoins`' own bottom-up
`reresolveJoinByName`/schema-refresh loop (`predp.go:187-195`) is skipped
for every join now absorbed into the search's own output — the search's
plan-build step already produces a correctly resolved tree, the same
reasoning `splicedSearchedRoot`'s existing skip (`predp.go:175-179`)
already uses for the boundary-map case. On decline (`ok==false` — e.g. the
residual-Filter-survives sub-case from §32.2, or any of `-3i-plumbing-b2`'s
own preamble checks §31.3 declining for an unrelated shape reason), `newRoot`
is left exactly as Phase A already produced it — byte-identical to today's
behavior. This is why Phase B is safe to land as code before flipping
§32.1's literal: with the literal still `false`, `extractSearchLeaves`'s
Semi/Anti arm never fires, the top-level `spineJoins[0]` node itself becomes
one opaque leaf, `len(scans)==1` almost certainly mismatches the frozen
`nprefix`, and the call declines via the existing `"leaf-count"` reason —
fully inert, by the same mechanism §31.4 already used to prove step (i)
inert, NOT a new gate that needs inventing.

### 32.4 What genuinely needs a live fixture before the literal flips, and
why coding Phase B is deferred to its own loop rather than done here

Per `dead_code_is_not_a_reference_impl` (an unreachable branch is not a
verified implementation): Phase B's SUCCESS path — the splice-in and the
skip of `spineJoins`' reresolution loop — cannot be exercised by the TPC-DS
sweep while §32.1's literal stays `false` (guaranteed decline, by
construction). It must instead be unit-tested DIRECTLY, the same way
`-3i-plumbing-b1`/step (i) proved their new arithmetic before any cutover:
call the splice-in logic (once extracted into its own testable function,
mirroring `pgShapedOffsetChecksOK`'s extraction) with a HAND-BUILT
"search succeeded" `tryJoinSearch` result and assert the resulting tree
shape and the skipped-reresolution set, rather than only asserting
inertness end-to-end. That test, plus the actual descend-loop wiring, plus
(as a strictly later, separate step (iii)) flipping §32.1's literal to
`true` in production and verifying against a live EXISTS/NOT-EXISTS TPC-DS
fixture (§27.4's existing gate) and the SF0.25 sweep for a REAL plan-shape
change this time, is next loop's fully-specified resume point — not done
this loop, which is scoping-only.

## 33. Step (ii) — Phase B scaffold landed (2026-09-16)

Coded exactly the §32.3 design, no deviation found necessary during
implementation.

### 33.1 What changed in `predp.go`

`runJoinSearchBelowPinned`'s descend loop now captures `spineRootPut`, the
setter that places `spineJoins[0]` (the outermost pinned Semi/Anti join)
into its parent slot — either `setResult` (spine is the tree root) or a
retained `spineFilter`'s `Child` setter — at the moment that node is first
appended to `spineJoins`. This is the one piece §32.3's design needed that
did not already exist as a named value: every other closure in the descend
loop is rebound per-iteration and thrown away once the loop moves on, so
without capturing it here Phase B would have to re-derive "who points at
`spineJoins[0]`" a second time.

Immediately after Phase A's existing `put(newTarget)` splice, and only when
`len(spineJoins) > 0` (a spine actually exists — the degenerate
"unnest declined" shape has none, and there is nothing for Phase B to
widen), a second call attempts the wider search:

```go
oldSpineSchema := append(Schema(nil), spineJoins[0].Output()...)
if searched, residual, used := tryPGShapedJoinSearch(spineJoins[0], nil, ctx, cat); used && residual == nil {
    spliceSearchedSpine(oldSpineSchema, searched, spineRootPut, spineFilters)
    return newRoot
}
```

`residual == nil` is required, not just `used`: passing `pred = nil` means
there is no residual predicate this call site is prepared to hold above the
spine (§32.3's own stated precondition — "there is no additional residual
predicate above the spine's own joins to push"), so a hypothetical future
shape where the search returns a non-nil residual despite a nil input
declines gracefully here rather than silently dropping a predicate.

On decline (the case for every production call today, per §32.3's inertness
argument), execution falls through unchanged into the existing
Phase-A-only code below — byte-identical to before this loop.

### 33.2 `spliceSearchedSpine` — extracted for direct testability

Per §32.4's `dead_code_is_not_a_reference_impl` mandate, the success branch
above cannot be exercised by calling the real search (it always declines
while `admitSemiAnti` stays `false`), so the splice-in logic itself was
pulled into a standalone function that takes the searched replacement as a
plain argument rather than computing it:

```go
func spliceSearchedSpine(oldSpineSchema Schema, searched Node, spineRootPut func(Node), spineFilters []*Filter)
```

It does exactly two things: (1) `spineRootPut(searched)` — place the
replacement; (2) compute `layoutPosMap(oldSpineSchema, searched.Output())`
and, when non-nil (the layouts differ), remap every retained `spineFilters`
predicate via the existing `remapByPosMap`/`remapSublinkOuterRefs` pair —
identical machinery to Phase A's own bottom-of-function loop, just aimed at
the spine-level schema instead of the origChain-level one. It deliberately
never touches `spineJoins` or calls `reresolveJoinByName`: `searched` is the
search's own plan-build output, already fully resolved, the same reasoning
`splicedSearchedRoot`'s pre-existing skip (§32.3, mirrored) already relies
on for the boundary-map case.

Three new direct unit tests (`predp_test.go`) exercise it without a
`*resolveContext` or a real search at all:

- `TestSpliceSearchedSpine_PlacesResultAndSkipsReresolution` — the setter
  passed in receives exactly the `searched` node.
- `TestSpliceSearchedSpine_RemapsFiltersOnLayoutChange` — a retained
  filter's `ColumnRef` pointing at a column by its OLD index is rewritten to
  that column's new index when the hand-built "search result" reorders the
  two columns.
- `TestSpliceSearchedSpine_NoRemapWhenLayoutUnchanged` — when the layout is
  identical, the retained filter's predicate is left as the exact same
  pointer (not just an equal value), pinning `layoutPosMap`'s nil-on-identity
  contract this function relies on to skip work.

### 33.3 Verification

`go build`/`go vet ./internal/optimizer/...` clean. Full
`go test ./internal/optimizer/...` passes (all pre-existing tests plus the
3 new ones above). TPC-DS SF0.25 sweep: `PASS=96 MISMATCH=0 CKMISMATCH=0
ERROR=0`, `PLAN-SHAPE: queries=99 same=99 changed=0` — confirms Phase B is
inert against the real corpus, not just by the leaf-count argument.
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` shows only the
pre-existing, unrelated `internal/parser` `GroupedJoinUnaliased` failure
(`internal/optimizer` itself reported `ok`).

### 33.4 What is still open — step (iii)

Flipping §32.1's `admitSemiAnti` literal to `true` at the one production
call site (`joinsearchseam.go:309`), and verifying the now-live Phase B
success path against a real EXISTS/NOT-EXISTS TPC-DS fixture (§27.4's
existing gate) plus a full SF0.25 sweep for an ACTUAL plan-shape change —
not done this loop, not done by this scaffold. This is the strictly later,
separate step (iii) §32.4 named, and it is the only remaining piece before
this milestone's own cited unblock targets (Q10/Q35) can see a different
plan.

## 34. Step (iii) — `admitSemiAnti` flipped to `true` in production; Phase B
is now genuinely live, but Q10/Q35 did not converge this loop (2026-09-16)

Flipped `extractSearchLeaves(chain, false)` to
`extractSearchLeaves(chain, true)` at `joinsearchseam.go:309`, the one
production call site inside `tryPGShapedJoinSearch`, exactly as §33.4
specified. Updated the three stale comments in the same block that used to
justify why `semiAnti` stays empty in production (`:304-312`, `:318-324`,
`:339-343`) — they now explain the actual split: Phase A's own call
(`chain == origChain`, captured before `unnestSubqueriesInPlan` runs) stays
structurally unable to contain a Semi/Anti node regardless of the flag
(§28.3), so only Phase B's second call (`chain == spineJoins[0]`, reached
post-unnest) can ever populate `semiAnti`.

### 34.1 Verification

`go build ./...` clean, `go vet ./internal/optimizer/...` clean, full
`go test ./internal/optimizer/...` passes unchanged (including all Phase A/B
scaffold tests from §31-§33 — none of them assumed the literal's value, so
none needed updating). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`: only the pre-existing, unrelated
`internal/parser` `GroupedJoinUnaliased` AST-drift failure
(`internal/optimizer` itself `ok`).

TPC-DS SF0.25 sweep (`scripts/tpcds-sf025-regression.sh sweep`):
`PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0 SKIP=3`. Every row count and every content checksum is identical
to the pre-flip baseline — **zero correctness regressions** anywhere in the
99-query corpus. This is the load-bearing result: the flip is safe to keep
in production regardless of what else this section finds.

Crucially, the plan-shape channel — self-diff against the immediately prior
commit's capture, `scripts/tpcds-plan-diff.py` — for the first time since
step (i) landed (§31.4) shows a **non-empty** delta:
`PLAN-SHAPE: queries=99 same=98 changed=1 added=0 removed=0`,
`changed (1): Q78`. Every previous "prove inert" loop in this milestone
(§27.3, §29, §31.4, §33.3) reported `same=99 changed=0` by construction —
this is the first empirical confirmation that Phase B is doing real work in
production, not just passing its own unit tests.

### 34.2 What changed: Q78, not Q10/Q35 — and why that is expected, not a
bug in this loop's change

Q78 has no `EXISTS`/`NOT EXISTS` in its SQL text (`query78.sql`): its three
CTEs (`ws`/`cs`/`ss`) each use `LEFT JOIN ... WHERE <col> IS NULL`, PG's
standard antijoin-strength-reduction idiom, which goopg also lowers to a
`JoinTypeAnti` node before this seam ever runs. `extractSearchLeaves`'s
Semi/Anti admission arm keys off `JoinType`, not provenance, so it is
correctly agnostic between an antijoin born from `NOT EXISTS` unnesting and
one born from this LEFT-JOIN rewrite — Q78 is therefore exactly the kind of
"real EXISTS/NOT-EXISTS-equivalent fixture" §33.4/§27.4's gate asked for,
even though it does not contain the literal keyword.

Q10 and Q35 — this milestone's own cited unblock targets — are unchanged
(`same=98` includes both). This is the expected outcome, not a regression:
§32.2's live trace already established Q10 hits the "sunk" shape (all
conjuncts legally pushed, Phase A already leaves a Filter-free subtree under
the pinned Semi/Anti join), and the design doc's Current-Priority framing
(`M0142-0008c`'s recon, §16) independently concluded Q10/Q35 need PG's
`create_unique_path` mechanism to reach parity — a separate, still-partially-
landed sub-milestone (`-0008c-1`/`-2` done, `-0008c-3c`/`-3d`/`-4` not yet)
this loop did not touch. "Phase B now runs for real" and "Q10/Q35 reach PG
parity" were never the same claim; §33.4 phrased the fixture requirement as
"a real EXISTS/NOT-EXISTS TPC-DS fixture", not specifically Q10/Q35, and
Q78 satisfies it.

### 34.3 A plan-quality question surfaced by Q78's diff, deferred rather than
chased this loop

Q78's `ws`/`cs` CTEs each lost a `Memoize`-wrapped `Index Scan using
date_dim_pkey` probe (a per-outer-row indexed lookup keyed on
`ws_sold_date_sk`/`cs_sold_date_sk`) and gained either a `Nested Loop` with a
bare post-join `Filter: (ws_sold_date_sk = date_dim_2.d_date_sk)` (the `ws`
CTE) or a `Hash Join` (the `cs` CTE) against an apparently-unfiltered
`date_dim`/`date_dim_1` scan. The `ws` CTE's new Nested Loop's own cost
estimate (`cost=0.00..944.11 rows=94411`) is implausibly cheap for what its
shape describes — a loop joining a full `date_dim` scan against 94411
antijoin-survivor rows with only a post-hoc equality filter, not a keyed
probe — which reads as a **cost-model estimation gap** for whatever new join
shape Phase B's wider search is now choosing here, not a correctness bug
(TPC-DS oracle checksums matched exactly, and the wall-clock delta for Q78
specifically did not cross the status-delta channel's 2x-or-5s-floor
threshold: `runtime-moves=0` across the whole 99-query sweep). Not chased
further this loop — the practice card's "measure end-to-end, do not chase a
single cost number" applies, and diagnosing it needs its own scoping pass
into whichever new path-building arm Phase B's wider search is now reaching
for these two CTEs (plausibly `M0142-0008c`'s still-unfinished `-3c`/`-3d`
dispatch work, since a real Semi/Anti pair only became DP-admissible partway
through that sub-milestone). Deferral-ledger row appended
(task-id `m0142-0008a-3i-plumbing-b2`).

### 34.4 Resume point

`M0142-0008a-3i-plumbing-b2` is now **DONE**: all three steps (i)/(ii)/(iii)
of §31.4/§32.4's resume order are landed, verified inert-then-live in the
correct order, with zero correctness regressions at any point. Two follow-on
threads, neither part of this item's own scope: (1) `M0142-0008c-3c`/`-3d`/
`-4` (already filed, independently blocked-until-this-item, now unblocked)
still need to land before Q10/Q35 can be re-measured for actual PG-plan
parity; (2) §34.3's Q78 cost-estimate anomaly, filed as a fresh deferral
rather than a numbered sub-item, since it was discovered as a side effect of
this loop's live sweep rather than scoped in advance — whoever next touches
the Semi/Anti path-building arms should re-run the SF0.25 `plans` diff for
Q78 specifically and check whether the Nested-Loop/Filter shape is a genuine
new join-order choice or a missing index-probe candidate in the newly
reachable code path.

## 35. M0142-0008c-3c landed — hash-join unique-ify substitution, AND the corpus-wide reachability re-check plus a deeper root cause for why it (still) never fires (2026-09-16)

Picked per §34.4's own pointer to `M0142-0008c-3c`/`-3d` as the natural next
item now that `-3i-plumbing-b2` is fully landed. Per the fix_plan/working-set
baton's own instruction ("neither has been live-traced yet, only
recon-sized"), this loop live-traced reachability FIRST, then implemented
`-3c` following `-3b`'s exact precedent (§20): build the correct dispatch,
pin it with direct hand-built-input unit tests, and report the reachability
finding as data rather than silently skip the implementation because it is
currently unreachable (`dead_code_is_not_a_reference_impl` — a DIRECTLY
tested builder is not "dead code" in that rule's sense, `-3b` already
established this precedent and it is followed here again).

### 35.1 Corpus-wide re-check: reachability is STILL zero after the step-(iii) flip

`-3b`'s own recon (§20) found, via a single live probe (Q10 only), that
`addPathsToJoinrel` was never once called with a SEMI/ANTI `SpecialJoinInfo`
in production — the entire `-0008c` unique-ify family was "correct but
unreachable," blocked on `-3i-plumbing` items 3-5. Those items are now
landed (`-3i-plumbing-b2`, §33-34). This loop re-ran the check at the new
HEAD, widened from one query to the full corpus:

- Built a private HEAD binary (`tmp/goopg-sf025-scope-bin`, never touched the
  shared TPC-H `:65433` cluster the `M0142-0003k` blocker still covers).
- Started the shared TPC-DS SF0.25 regression cluster (`:65437`,
  `GOOPG_PGSHAPED_DP_TRACE=1`) — read-only `EXPLAIN`s only, safe on a
  git-tracked oracle-comparison cluster.
- Ran `EXPLAIN` on all 100 TPC-DS query files (7 pre-existing, unrelated
  parse-gap errors on multi-CTE-with-syntax/`lochierarchy` queries — not
  investigated, out of scope) and grepped the server log for every `DPPATH`
  line.

Result: **145,191 total `DPPATH` lines, ZERO with `jointype=semi` or
`jointype=anti`.** Re-confirms `-3b`'s verdict at full-corpus scale, not just
Q10: `jointypeForDirection`'s SEMI/ANTI branch — the code this milestone's
entire `-0008c` family extends — is not reached by any query in the standard
regression corpus, even with Phase B genuinely live and changing Q78's plan
(§34).

### 35.2 The deeper reason: `semiAnti`'s two named consumers are wired to zero production call sites

Chasing *why* Phase B's admission (which DOES walk into a real ANTI join for
Q78, per §34.2) never produces a SEMI/ANTI `DPPATH` line found a specific,
previously-undocumented gap. `extractSearchLeaves`'s own doc comment (just
above its signature, `joinsearchseam.go:1108`) names "`semiAntiChainLink`'s
two consumers" — `semiAntiLinksHaveSJInfos` (the SJInfo-legality check,
`outerLinksHaveSJInfos`'s SEMI/ANTI analogue) and `semiAntiOnQualsOK` (the
qual-placement proof, `outerOnQualsOK`'s analogue). Grepping every call site
of both functions across the package: **each has calls ONLY from
`semiantichain_test.go`.** Neither is called anywhere inside
`tryPGShapedJoinSearch` itself — contrast with `outerLinksHaveSJInfos`, whose
outer-link analogue IS called in production at `joinsearchseam.go:312` (now;
line numbers have shifted across this design doc's many rounds).

What `extractSearchLeaves`'s SEMI/ANTI arm DOES do in production (traced live
at `joinsearchseam.go:1148-1230`, admission code unchanged by this loop): it
renumbers the ORIGINAL `j.SJInfo` object's `SynLefthand`/`MinLefthand`/
`SynRighthand`/`MinRighthand` in place (lines 1223-1226) to match the newly
derived leaf-index bits, and records a `semiAntiChainLink` used only by the
THREE preamble checks already landed (`pgShapedOffsetChecksOK`,
`buildLeafSpans`, the `len(scans) != nprefix+len(semiAnti)` leaf-count check —
all §31.3's work, all about chain-shape bookkeeping, none about SJInfo
legality or DP-level admission). Whether the renumbered `j.SJInfo` object is
actually a member of `ctx.joinInfoList` — the list `jointypeForDirection`'s
caller consults to find a pair's `SpecialJoinInfo` during the DP tournament —
was not traced to a definitive per-query yes/no this loop; what IS certain
from §35.1's measurement is that if it does reach `joinInfoList`, something
else in the admission/legality chain still declines before `addPathsToJoinrel`
ever sees it, since the observed count is exactly zero, not "occasionally
declined."

**Conclusion for `-0008c-3c`/`-3d`'s resume path:** the blocker is no longer
correctly described as "the search doesn't yet admit a Semi/Anti chain link"
(§20's framing, now stale) — it DOES admit one, structurally, for at least
Q78. The blocker is that admission's two purpose-built LEGALITY consumers
(`semiAntiLinksHaveSJInfos`, `semiAntiOnQualsOK`) are tested but never wired
into `tryPGShapedJoinSearch`, so nothing currently causes a real SEMI/ANTI
`SpecialJoinInfo` to reach the DP tournament's `addPathsToJoinrel` call for
costing purposes — Q78's plan changed through Phase B's SPLICE mechanism
(`spliceSearchedSpine`, §33) re-arranging what surrounds a FIXED, reused
Semi/Anti join node, not through the DP tournament re-costing alternative
placements of that node itself. Wiring those two consumers into production
(and confirming `ctx.joinInfoList` actually carries the renumbered `j.SJInfo`
across into the DP level driver) is the concrete next step before `-0008c`'s
family can ever fire — filed as **M0142-0008a-3i-plumbing-c** below, since it
is `-3i-plumbing`'s own admission-wiring work, not a `-0008c` costing item.

### 35.3 `-3c` implementation: hash-join unique-ify substitution

Landed exactly the shape `-3b`'s own "Next" pointer (§19, end) described:
`addHashJoinPath` (`pathgen.go`) gained `uniq uniqueSide, sjinfo
*SpecialJoinInfo`, substituting the PROBE side for `uniqueSideOuter` and the
BUILD side for `uniqueSideInner` (`hash_inner_and_outer`'s own two arms,
`joinpath.c:2301-2341`, read live) — a single builder handling BOTH
directions, unlike the NL split across two builders, since PG's own
`hash_inner_and_outer` is one function for both. `addPartialHashJoinPath`
(`joinpathsparallel.go`) gained the same two params:
`uniqueSideOuter` declines unconditionally before even inspecting
`PartialPathlist` (PG's own `save_jointype != JOIN_UNIQUE_OUTER` gate,
`joinpath.c:2419` — a partial outer's per-worker shape cannot be
unique-ified); `uniqueSideInner` substitutes `createUniquePath`'s cached
result for the ordinarily-searched `cheapestParallelSafeTotalInner`, but only
when that result is ALSO `ParallelSafe` (PG's own "we can't use any
alternative inner path," `joinpath.c:2453-2466`) — `createUniquePath`'s
`PathUnique` literal never sets `ParallelSafe` (`createuniquepath.go`, no such
field in the literal), so this arm always declines today, a faithful
consequence of a real field rather than an approximation. `addPathsToJoinrel`
threads `uniq`/`sjinfo` into both calls (`joinpaths.go`).

Six new direct unit tests
(`internal/optimizer/uniqueify_hash_builders_test.go`), mirroring `-3b`'s
`uniqueify_builders_test.go` pattern exactly: `addHashJoinPath` substitutes
the probe (outer) and separately the build (inner) side, with a
fail-closed decline control for each; `addPartialHashJoinPath` always
declines `uniqueSideOuter` and always declines `uniqueSideInner` (since
`ParallelSafe` is never true), both asserted directly rather than assumed;
one end-to-end `addPathsToJoinrel` test (`TestAddPathsToJoinrel_UniqueSideInner_HashPlanShape`)
proves the dispatch-through-builder wiring produces a demoted-`JoinInner`
hash join over the unique-ified inner when only the unique-ify fallback
admits the pair — the hash-join analogue of `-3b`'s own
`TestAddPathsToJoinrel_UniqueSideInner_PlanShape`, exercising the KEYED arm
of `addPathsToJoinrel` (which the NL-only test never reached) with a
hand-built equijoin `restrictInfo`.

One test-only caller, `generateHashJoinPaths` (no production call site,
`pathgen.go` — used only by `pathgen_test.go`/`nestloop_ntuples_test.go`),
was updated to pass `uniqueSideNone, nil` at both its internal call sites
(never a unique-ify candidate by construction).

**Acceptance, per §35.1**: NOT met and cannot be met yet, for the same
structural reason `-3b` already found and this loop re-confirmed at full
corpus scale — the same `M0142-0008a-3i-plumbing-c` follow-up blocks both.

**Verification**: `go build ./...` clean; `go vet ./internal/optimizer/...`
clean; full `go test ./internal/optimizer/...` pass (all pre-existing tests
plus the 6 new ones); TPC-DS SF0.25 sweep against a rebuilt private binary,
`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`,
`PLAN-SHAPE: same=99 changed=0` — zero production behavior change, as
expected from §35.1's reachability finding;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` shows only the
pre-existing, unrelated `internal/parser` `GroupedJoinUnaliased` AST-drift
failure (`internal/optimizer` itself `ok`, cached).

### 35.4 Resume point

`M0142-0008c-3c` is DONE in the same sense `-3b` was: correctly built,
directly tested, empirically confirmed still-unreachable in production.
`M0142-0008c-3d` (merge + partial-nestloop unique-ify substitution) is
NOT done — it was not picked up this loop (one task per loop) — but shares
the EXACT same blocker identified in §35.2, so whoever picks it up next
should read this section first rather than re-running the same reachability
recon. The real next step for the whole `-0008c` family is
**M0142-0008a-3i-plumbing-c** (filed below): wire `semiAntiLinksHaveSJInfos`/
`semiAntiOnQualsOK` into `tryPGShapedJoinSearch`'s production path and confirm
(or fix) whether the renumbered `j.SJInfo` reaches `ctx.joinInfoList` for the
DP level driver to find — only after that can `-3c`/`-3d`/`-4`'s acceptance
bars (Q10/Q35 plan-shape parity) even be attempted.

## 36. `M0142-0008a-3i-plumbing-c` — sizing recon: the admission-wiring gap is FIVE pieces, not two

This loop picked up `-3i-plumbing-c` (filed §35.4) and read every code path
between `extractSearchLeaves`'s SEMI/ANTI arm and `addPathsToJoinrel` to
answer resume-point item (1): does the renumbered `j.SJInfo` reach
`ctx.joinInfoList`? **No — and the reason rules out a one-line fix.**
`ctx.joinInfoList` is built once, by `deconstructJointreeScopedSJI`
(`planner.go:3051`), from the SELECT's `FromExprs` **before**
`unnestSubqueriesInPlan` ever runs (the same fact `unnest.go:4385`'s own
comment already states about why `existsUnnestSJInfo` cannot reuse it). A
Semi/Anti join's `SJInfo` is created later, during unnesting
(`existsUnnestSJInfo`, throwaway `synL=1/synR=2` numbering), so it is a
freshly-allocated object `deconstructJointreeScopedSJI` never saw and
structurally cannot be a member of `ctx.joinInfoList` — `j.SJInfo` being
renumbered in place (`joinsearchseam.go:1223-1226`) changes the object's
fields but not which slice holds a pointer to it. Confirmed by reading
`semiAntiLinksHaveSJInfos` itself (`joinsearchseam.go:1552-1567`): it does a
literal membership scan (`sj.Jointype == lk.jointype && sj.SynLefthand ==
lk.lhs && sj.SynRighthand == lk.rhs`) over whatever list it is handed — wiring
the call in without first putting `j.SJInfo` into that list would make it
decline on every query, unconditionally, which would look identical to
today's silent zero (a false sense of "wired" with no reachability gain).

Tracing further — what does the corpus-wide zero actually cost, beyond the
missing membership — found **four more gaps**, all upstream of the two named
consumers:

1. **The semiAnti join's own predicate never reaches the DP search's
   conjunct list.** `semiAntiChainLink.pred` (the ON/EXISTS equality,
   `joinsearchseam.go:1211`) is recorded but never merged into `conjuncts` —
   contrast with `outerLinks`, whose `onOuter` IS merged
   (`joinsearchseam.go:552`). Without this, `semiAntiOnQualsOK`'s own
   `searchConsumes(c, spans)` check (`joinsearchseam.go:1581-1599`) can never
   pass, because the search was never given the clause to consume in the
   first place — wiring the two named consumers in without this fix would
   make `semiAntiOnQualsOK` decline unconditionally too.

2. **The synthetic RHS leaf never becomes a `leaves`/`relInfos` entry.**
   `extractSearchLeaves` appends the RHS to `scans` (so
   `len(scans) == nprefix+len(semiAnti)`, checked at
   `joinsearchseam.go:325`), but the loop that builds what the search actually
   receives (`joinsearchseam.go:556-581`) is `for i, b := range
   ctx.bindings[:nprefix]` — it only ever visits real FROM items. `scans[nprefix:]`
   (the semiAnti leaves) are silently dropped: they are computed, checked for
   count, and then never handed to `planJoinlistSearch` at all. This is
   independent of gap 1 and of the `joinInfoList` gap — fixing only those two
   would still leave the search with no rel to place the SJInfo ON, so
   `joinIsLegal` would never even be asked about the pair.

3. **`prob.bindings`/`scans`/`relInfos` must ALL grow to
   `nprefix+len(semiAnti)`, and each needs a `rangeBinding` for the synthetic
   leaf even though it has no `ctx.bindings` entry.** `leafRel`
   (`relfromjoinlist.go:387-401`) only requires `rel < len(prob.bindings)` —
   it does not require the index to name a real statement FROM item — so a
   degenerate `rangeBinding{table: nil, offset: cumOffsets[nprefix+k]}` is
   plumbing-feasible, but `estimateBaseRelInfo` (`cardinality.go:719-736`)
   reads `binding.table` for `baseRows` and would return a zero estimate for
   every such leaf; the fallback that runs afterward for every leaf,
   `applyRelSizeFallback`, needs to be checked against a real fixture (not
   assumed) before trusting the row count a Semi/Anti RHS leaf gets, since a
   wrong estimate here feeds directly into the DP tournament's costing, not
   just a shape decision.

4. **The local `jl` handed to `planJoinlistSearch` must also grow.**
   `validateJoinlistProblem` (`relfromjoinlist.go:246-276`) hard-requires
   `jl.leafRange() == (0, len(prob.bindings))` — `jl` (from
   `splitOuterSpine(node, ctx.joinlist)`, itself sourced from the pre-unnest
   `ctx.joinlist`) has exactly `nprefix` leaves and cannot describe the
   synthetic ones. The call site needs its OWN locally-built joinlist —
   `append(jlCopy, leafItem(nprefix), leafItem(nprefix+1), …)` for each
   semiAnti link — rather than reusing `ctx.joinlist`/`jl` unmodified;
   `leafItem`/`joinlist` (`collapse.go:154-176`) impose no constraint beyond
   the index bound, so this is buildable, but it is a NEW construction step,
   not a parameter tweak.

**Revised scope, decomposed into five sub-steps (filed below, none started)**
— roughly in dependency order, since 2-4 are a single "give the search a real
leaf" unit and 1/5 are each independently tractable:

- **`-3i-plumbing-c1`**: merge `semiAnti[*].pred`'s split conjuncts into
  `conjuncts`, mirroring `onOuter`'s treatment (gap 1). Smallest, most
  isolated piece; safe to land and verify alone since with gaps 2-5 still
  open the new conjuncts have no synthetic leaf to attribute to and
  `partitionConjunctsForJoinPlanning` will hold them in the residual — a
  behavior-neutral no-op until the rest lands, the same "build it, verify
  inert" precedent `-3i-plumbing-b1` and `-0008c-3c` both already used.
- **`-3i-plumbing-c2`**: extend `prob.bindings`/`scans`/`relInfos` (gaps 2-3)
  — synthesize a `rangeBinding` per semiAnti leaf, populate
  `estimateBaseRelInfo`/`applyRelSizeFallback` for it, and confirm against a
  live Q78-shaped fixture that the row estimate is sane (not silently zero).
- **`-3i-plumbing-c3`**: build the call-site-local extended `jl` (gap 4) —
  the `append(jlCopy, leafItem(...))` construction — and confirm
  `validateJoinlistProblem` accepts it.
- **`-3i-plumbing-c4`**: build the augmented `joinInfoList` (`ctx.joinInfoList`
  plus each semiAnti link's `j.SJInfo` pointer — requires adding an `sjinfo
  *SpecialJoinInfo` field to `semiAntiChainLink`, captured alongside `lhs`/
  `rhs`/`pred` at `joinsearchseam.go:1211`) and thread it into
  `joinlistProblem.joinInfoList` (replacing the bare `ctx.joinInfoList` at
  `joinsearchseam.go:633`).
- **`-3i-plumbing-c5`**: wire `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK`
  as decline gates (mirroring the `outerLinks` block,
  `joinsearchseam.go:517-538`) — the step the task was originally filed
  for — and re-run the full-corpus `GOOPG_PGSHAPED_DP_TRACE=1` sweep (§35.1's
  method) to confirm at least one `jointype=semi`/`anti` `DPPATH` line
  appears. Only meaningful once c1-c4 are all landed; wiring it earlier
  would just decline unconditionally (see the false-sense-of-progress note
  above) and prove nothing.

None of c1-c5 is picked up this loop (recon only, one task per loop). c1 is
the natural next pickup: independently landable and verifiable, per the same
"safe to build ahead of its own reachability" precedent this milestone has
used repeatedly (`-3b`, `-3c`).

## 37. `M0142-0008a-3i-plumbing-c1` — landed, verified inert

Implemented exactly as §36 scoped: a `for _, lk := range semiAnti { conjuncts
= append(conjuncts, splitAnd(lk.pred)...) }` loop, placed right after the
`outerLinks` block (`joinsearchseam.go:555-565`, mirroring `onOuter`'s own
merge one block above it) and before `partitionConjunctsForJoinPlanning`
consumes `conjuncts`.

**Why this is provably inert, not just argued to be** (traced through the two
consumers before landing, since §36's own claim — "no synthetic leaf to
attribute to" — deserved checking against the actual code rather than taken
on faith):

- `semiAntiOnQualsOK`'s own contract (`joinsearchseam.go:1570-1580`) already
  establishes that every conjunct of `lk.pred` spans BOTH `lk.lhs` (real
  leaves) and `lk.rhs` (the synthetic RHS leaf) — a same-side-only conjunct
  would already have been pushed into the EXISTS body's own opaque leaf by
  the unnest rewrite, before this link is ever built. So every conjunct this
  loop adds necessarily carries the synthetic leaf's bit in its relids.
- `partitionConjunctsForJoinPlanning` (`local_filters.go:62-86`) calls
  `tableForCol`, which returns `-2` (multi-table) for any conjunct spanning
  more than one leaf — including a real+synthetic pair — so the conjunct
  lands in `joinConjuncts` (passed to the search), never silently dropped
  into a `locals.byBinding` map entry the leaf-building loop wouldn't visit.
- Inside `buildRestrictInfos` (`joinrestrict.go:167-200`), the conjunct DOES
  get turned into a `restrictInfo` (relids computed relative to the FULL
  `cumOffsets`, which already includes the semiAnti leaf's span per
  `buildLeafSpans`) — but the DP search itself (`planJoinlistSearch`) only
  ever builds `RelSet`s from `prob.bindings`, which — until `-3i-plumbing-c2`
  extends it — has length `nprefix` (real leaves only). No subset of real
  leaves can ever equal a relid set that requires the synthetic leaf's bit,
  so the join that would "consume" this restrictInfo is never formed, and it
  never influences a cost or shape decision among the real leaves.
- One side effect worth recording precisely (not a regression, but a
  pre-existing gap this loop's tracing surfaced rather than caused): because
  `buildRestrictInfos` still records the clause, `searchConsumes` (line
  ~1067) reports it as "seen" in the final residual computation
  (`joinsearchseam.go:642-648`), so the semiAnti ON qual is not re-added to
  the residual `Filter` either. Net effect: the predicate is silently
  unenforced with or without c1 — before c1 it was never added to any list
  at all; after c1 it is added, never joins a real leaf set, and is then
  treated as "already consumed" for residual purposes. Same observable
  outcome (predicate not enforced anywhere in the final plan) either way, so
  c1 changes no plan's semantics — it only relocates where in the pipeline
  the (still real, still-to-be-closed-by-c4/c5) gap manifests. This is the
  concrete backing for "behaviour-neutral," not an assumption.

**Empirical confirmation** (not just code tracing): optimizer package tests
green (`go test ./internal/optimizer/...`), full `go build ./...` clean, and
the TPC-DS SF0.25 regression sweep (`scripts/tpcds-sf025-regression.sh
sweep`) against the git-tracked PG oracle came back `PASS=96 (60
ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0` with the
status-delta and plan-diff channels reporting `PLAN-SHAPE: queries=99
same=99 changed=0 added=0 removed=0` against the immediately prior commit
(`ef898b7e3`, the b2 landing) — not one plan shape moved. TPC-H spotcheck
(`scripts/tpch-spotcheck.sh`) reported its known pre-existing SKIPPED state
(M0142-0003k's TPC-H data-dir blocker, unrelated to this change — see
`CLAUDE.md`'s TPC-H bench row).

Next pickup: `-3i-plumbing-c2` — give the synthetic Semi/Anti RHS leaf a real
`rangeBinding`/`baseRelInfo` and extend `prob.bindings`/`scans`/`relInfos` to
`nprefix+len(semiAnti)` (design doc §36, gaps 2-3). This is the piece that
actually starts to move plan shapes, so it needs its own dedicated loop with
a live Q78-shaped fixture check on the resulting row estimate — not assumed
correct the way c1's inertness could be fully derived from the code alone.

## 38. `M0142-0008a-3i-plumbing-c2` — landed, verified inert (surprise: even
    growing `prob.bindings` did not flip any plan)

`joinsearchseam.go`'s leaf-building loop (formerly `for i, b := range
ctx.bindings[:nprefix]`, sizing `leaves`/`relInfos` to `nprefix`) now sizes
all three of `leaves`, `relInfos`, and a new local `bindings` slice to
`nleaves := nprefix+len(semiAnti)`, then runs a second loop over `semiAnti`
that fills index `nprefix+k` for each `semiAnti[k]`. `scans[nprefix+k]` is
the correct leaf for `semiAnti[k]`: `extractSearchLeaves` appends a
semiAnti link's RHS scan (`scans = append(scans, j.Right)`) and its
`semiAntiChainLink` entry in the SAME case-branch of the SAME walk step
(joinsearchseam.go's `walk` closure), so position `nprefix+k` in `scans` and
element `k` of `semiAnti` are always the same leaf — confirmed by tracing
`unnestExistsExpr`'s call site (unnest.go:491): each newly-unnested EXISTS
wraps the ENTIRE accumulated tree so far as `j.Left` and the new correlated
subquery as `j.Right`, so a DFS walk always visits every real leaf and every
earlier synthetic leaf (all inside `j.Left`) before appending the new
leaf — real leaves are walk-contiguous at `[0,nprefix)` and synthetic ones
are walk-contiguous at `[nprefix,nleaves)`, in `semiAnti` order, for both a
single EXISTS and nested (multi-EXISTS) unnesting alike.

**The row-estimate trap this loop's task description specifically flagged
was real, not hypothetical.** A synthetic leaf's degenerate binding has
`table == nil` (it is an opaque already-planned subtree, not a base
relation). Feeding that straight through `estimateBaseRelInfo`/
`applyRelSizeFallback` — both read `binding.table` — bottoms out at
`estimateTableRowsFallback` (relsize.go:571), whose first line is `if tbl ==
nil || tbl.Virtual { return 0 }`: a **silent zero-row estimate**, not a sane
fallback, which would have handed the DP tournament a free-looking synthetic
leaf. The fix uses the general-purpose `EstimateRows(Node)` (cardinality.go)
instead — the same estimator every other non-base-relation plan node
(`*Join`, `*Filter`, `*Aggregate`, `*CTEScan`, …) already goes through — to
size `scans[nprefix+k]` directly from its own already-built subtree, then
still routes local-filter selectivity through the existing
`applyLocalFilterSelectivity` (mirroring the real-leaf loop) for the case —
believed unreachable today, since no other conjunct can bind to a synthetic
leaf's bit alone before c4/c5 land, but wired for symmetry rather than
assumed away.

**The `prob.bindings` growth risk this loop weighed before writing code:**
`validateJoinlistProblem` (relfromjoinlist.go:246) hard-requires
`jl.leafRange() == (0, len(prob.bindings))`, and `jl` is NOT extended by this
task (that is c3's job, filed separately). Growing `prob.bindings` to
`nleaves` while `jl` still only covers `[0,nprefix)` therefore looked, by
code inspection alone, like it should flip `validateJoinlistProblem` from
pass to fail for the one class of statement this matters for (`semiAnti`
non-empty) — turning a search that (per the b2/c1 landing notes) currently
runs to completion, mishandling the semiAnti predicate but still producing a
plan, into an outright decline that falls back to the pre-DP-search
syntactic-tree path. That would not be "inert" the way c1 was, so this was
not assumed away: the TPC-DS SF0.25 sweep was run and checked specifically
for a shape change on Q78 (the one query these landing notes have
consistently named as reaching the semiAnti arm at all, per b2's "implausibly
cheap EXPLAIN cost" finding).

**Empirical result: no shape change, anywhere.** `go build ./...` clean;
`go test ./internal/optimizer/...` green; TPC-DS SF0.25 sweep
`PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0`,
`PLAN-SHAPE: queries=99 same=99 changed=0 added=0 removed=0` against the
immediately prior commit (`ae557bf10`, the c1 landing) — Q78 itself unchanged
(15 rows, checksum `c06cf981a7819a37`, identical to the c1-era capture).
TPC-H spotcheck SKIPPED (pre-existing M0142-0003k data-dir blocker,
unrelated). The likely reading (not chased further — out of this task's
scope): the SF0.25 corpus's one semiAnti-reaching query shape does not
actually make it past some OTHER earlier decline point in
`tryPGShapedJoinSearch` before reaching this loop at all (candidates: the
`chainCarriesLateral`/`pgShapedOffsetChecksOK` gates, or `jl` construction
upstream already excluding it some other way) — c3 is still needed before
`validateJoinlistProblem`'s check is verified to pass end-to-end for a
statement that DOES reach this far with `len(prob.bindings) == nleaves`, and
should re-run this same Q78 checksum/shape comparison as its own gate rather
than trusting this loop's absence-of-regression as proof the c2+c3 pairing
works.

Next pickup: `-3i-plumbing-c3` — build the call-site-local extended
joinlist (`jl`) so `validateJoinlistProblem`'s `jl.leafRange() ==
(0,len(prob.bindings))` check actually covers the grown `nleaves` bindings
this loop introduced (design doc §36, gap 4; `relfromjoinlist.go:246-276`).

## 39. `M0142-0008a-3i-plumbing-c3` + `-c4` — landed TOGETHER, not
    independently as originally filed: c3 alone is a real correctness
    regression (2026-09-16)

**c3 as scoped** — build a call-site-local `searchJl` in
`tryPGShapedJoinSearch` (`joinsearchseam.go`, right before the
`planJoinlistSearch` call) that appends one `leafItem(nprefix+k)` per
`semiAnti[k]` on top of a COPY of `jl` (never mutating `jl` itself — it may
alias `ctx.joinlist` directly per the existing "spine empty, `jl ==
ctx.joinlist`" comment at the top of the function, shared with other code
paths). `cumOffsets` already spans all `nleaves` leaves from `buildLeafSpans`
(unaffected by this gap), so only `jl`'s leaf set needed extending —
confirmed by grep, not assumed: `jl` is read only three times in the whole
function (`nrels()` and `leafRange()` before the leaf-building block, both
already checked against `nprefix`, and the `planJoinlistSearch` call site
itself), so a search-local copy cannot desync anything upstream.

**c3 alone broke a passing unit test.** `go test ./internal/optimizer/...`
with c3 applied and c4 NOT yet applied: `TestM0070Q21InnerOnlyConjunctsStay`
failed — `AntiJoin (NOT EXISTS) not found in Q21 plan; M0061-0001
regression?` — the search now runs to completion (validateJoinlistProblem
passes, per c3's whole point) but silently drops the semi/anti semantics and
joins the RHS leaf in as a plain `Join`, producing wrong results for any
statement whose join order search actually reaches this point with
`len(semiAnti) > 0`. **Root cause, traced not guessed:** `joinIsLegal`
(`joinsearchlevel.go:198`) has `if len(s.joinInfoList) == 0 { … treat as an
ordinary admissible pair … }` and, before this loop, `s.joinInfoList` was
always the bare `ctx.joinInfoList` — it never carried the semiAnti link's own
`SpecialJoinInfo`, so the search's legality machinery had literally no
constraint telling it the synthetic leaf could only be joined via SEMI/ANTI.
c1/c2 could not have surfaced this: c1's conjunct merge is inert until a real
join relset includes the synthetic leaf's bit (impossible before c2), and
c2's own new leaf never actually got searched until c3 extended `jl`. This
is why the fix_plan's "c4 independent of c1-c3, can land in parallel or
either order" framing does not hold in practice — c3 is the FIRST step of
this chain that makes the search actually visit the synthetic leaf during
the DP tournament, so c3 and c4 are a single atomic correctness unit and were
landed together in this loop rather than as separate tasks.

**c4, landed alongside c3:** `semiAntiChainLink` gained an `sjinfo
*SpecialJoinInfo` field, populated at the link's construction site
(`joinsearchseam.go`, the `walk` closure's SEMI/ANTI case) directly from
`j.SJInfo` — the SAME pointer the walk already renumbers in place a few lines
later (`j.SJInfo.SynLefthand, j.SJInfo.MinLefthand = lhs, lhs` etc.), so no
separate "rebuild the SJInfo" step was needed; storing the pointer before the
mutation is safe because Go structs behind a pointer share state — by the
time anything reads `lk.sjinfo`'s fields, the walk has already finished and
the real leaf-index numbering is in place. A new helper,
`semiAntiJoinInfoList(base []*SpecialJoinInfo, links []semiAntiChainLink)
[]*SpecialJoinInfo`, returns `base` UNCHANGED, by identity, when `links` is
empty (the overwhelmingly common case — no allocation for any query without
EXISTS/NOT EXISTS unnesting) and otherwise returns a fresh copy of `base`
with each link's non-nil `sjinfo` appended; `joinlistProblem.joinInfoList`
now reads `semiAntiJoinInfoList(ctx.joinInfoList, semiAnti)` instead of the
bare `ctx.joinInfoList`.

### 39.1 Verification

`go build ./...` clean; `go test ./internal/optimizer/...` green
(`TestM0070Q21InnerOnlyConjunctsStay` passes again — the AntiJoin is back in
the Q21 plan) with BOTH c3 and c4 applied; TPC-DS SF0.25 sweep `PASS=96 (60
ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE:
queries=99 same=99 changed=0 added=0 removed=0` against the immediately prior
commit (the c2 landing) — Q78 still byte-identical (15 rows, checksum
`c06cf981a7819a37`), consistent with §38's reading that the SF0.25 corpus's
one semiAnti-reaching query still declines somewhere upstream of this code,
independent of c1-c4. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` shows only the pre-existing unrelated
`internal/parser` `GroupedJoinUnaliased` AST-drift failure (every recent
loop; `internal/optimizer` itself green). TPC-H spotcheck SKIPPED
(pre-existing M0142-0003k data-dir blocker, unrelated).

### 39.2 What this means for `-3i-plumbing-c5`

`-c5`'s own fix_plan text ("wiring it earlier declines unconditionally and
proves nothing") assumed c1-c4 landing first was safe on its own. This loop's
Q21 discovery shows the SAFETY property that assumption depends on
(searching a semi/anti leaf without dropping its semantics) does not hold
until c4's `joinInfoList` wiring is present — which it now is, as of this
loop. `-c5`'s own job (wiring `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK`
as decline gates and confirming DPPATH reachability via
`GOOPG_PGSHAPED_DP_TRACE=1`) is still open and still the right next pickup;
this section exists so `-c5` does not re-derive the c3+c4 coupling from
scratch.

## 40. `-3i-plumbing-c5` — decline gates wired; reachability CONFIRMED still zero, and now precisely diagnosed

Landed the mirror of the `outerLinks` FAIL-CLOSED block (§design pattern at
`joinsearchseam.go:517-538`) for `semiAnti`, placed immediately before the
c1 conjunct-merge loop so a decline never lets an un-placeable semiAnti
clause reach the search at all:

```go
if len(semiAnti) > 0 {
    if !semiAntiLinksHaveSJInfos(semiAnti, ctx.joinInfoList) {
        traceSeamDecline("semianti-link-no-sjinfo", nrels, nprefix)
        return node, pred, false
    }
    if !semiAntiOnQualsOK(semiAnti, cumOffsets) {
        traceSeamDecline("semianti-on-qual", nrels, nprefix)
        return node, pred, false
    }
}
```

### 40.1 Verification

`go build ./...` clean; `go test ./internal/optimizer/...` green (no
existing test exercises a live semiAnti link through this new gate, since
none of the corpus reaches it — see 40.2 — but `semiantichain_test.go`'s
direct-input tests for both helper functions are unaffected).

### 40.2 Corpus reachability re-check — the task's actual question

Repeated §35.1's method at the new HEAD (private binary
`tmp/goopg-m0142-c5-bin`, `bench/tpcds/server.sh start sf025` with
`GOOPG_PGSHAPED_DP_TRACE=1`, `EXPLAIN` on all 100 `query*.sql` files against
the SF0.25 dataset — 96 succeeded, 4 pre-existing unrelated parse gaps,
consistent with prior loops' count): **150,137 total `DPPATH` lines, ZERO**
with `jointype=semi` or `jointype=anti` — reachability is STILL not met.

But this run additionally captured 5 `DPTRACE seam-decline
reason=semianti-link-no-sjinfo` lines (3× `nrels=2`, 2× `nrels=3` — one
query, almost certainly Q78, the corpus's one EXISTS/NOT-EXISTS-unnested
query per §34/§38/§39). **The new c5 gate is reachable and is exactly what
declines those combos** — not some earlier, unrelated decline. Read as a
data point, this answers the reachability question precisely for the first
time: the search DOES reach the point of attempting to admit the synthetic
semiAnti leaf (c2's leaf + c3's `searchJl` growth both work), but
`semiAntiLinksHaveSJInfos(semiAnti, ctx.joinInfoList)` always returns false
for every such combo.

### 40.3 Root cause: `ctx.joinInfoList` structurally cannot contain a semiAnti SJInfo, by construction — not a bug, an already-documented architectural gap

Grepped every assignment site of `.joinInfoList` in `internal/optimizer`
(not just the seam): there is exactly ONE, `planner.go:3051`,
`rctx.joinlist, rctx.joinInfoList = deconstructJointreeScopedSJI(s.FromExprs,
…)` — built once, from the parser-level `s.FromExprs` jointree, which
reflects the statement's SQL-syntax joins (able to see real `LEFT`/`RIGHT`/
`FULL` outer joins). Nothing ever appends to it afterward.

The semiAnti `SpecialJoinInfo` comes from a completely different, later, and
independent pipeline stage: `existsUnnestSJInfo` (called from
`unnestExistsExpr`, `unnest.go:4461`), which rewrites a `WHERE`-clause
EXISTS/NOT EXISTS into a physical `Join` **plan node** carrying its own
`SJInfo` — a rewrite over the already-built plan `Node` tree, not over
`s.FromExprs`. `existsUnnestSJInfo`'s own doc comment already says this
explicitly (unnest.go:4385, written well before this loop): "`ctx.joinInfoList`
belongs to jointree deconstruction, which this rewrite runs independently
of." This loop is the first to make that independence's CONSEQUENCE
concrete and measured: because the two pipelines never rejoin, `ctx.joinInfoList`
is not merely *usually* missing the semiAnti SJInfo — it is
**structurally incapable of ever containing one**, for any query, under the
current architecture. `semiAntiLinksHaveSJInfos(semiAnti, ctx.joinInfoList)`
as coded (correctly mirroring `outerLinksHaveSJInfos`'s pattern) will
therefore always return `false` whenever `semiAnti` is non-empty, until
something threads the semiAnti SJInfo into `ctx.joinInfoList` itself from a
point in the pipeline that runs AFTER unnesting.

Two alternatives considered and rejected for THIS loop:
- Checking against `semiAntiJoinInfoList(ctx.joinInfoList, semiAnti)` (the
  search-local extended list c4 already builds) instead of the bare
  `ctx.joinInfoList` — rejected as vacuous: that list is unconditionally
  built FROM `semiAnti`'s own `.sjinfo` field, so the match is against
  itself and always succeeds regardless of whether the SJInfo is legitimate
  — exactly the "false-sense-of-progress" trap §36 already warned about for
  landing a gate before its precondition is real.
- Loosening the gate to skip the SJInfo check for semiAnti specifically —
  rejected: that removes the one safety property (§39's Q21 finding) that
  makes admitting the synthetic leaf safe at all; a decline here is the
  CORRECT fail-closed behavior given today's architecture, not a defect to
  route around.

### 40.4 Verified safe: plan output unaffected

TPC-DS SF0.25 sweep at the new HEAD (c5 applied):
`PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0`,
`PLAN-SHAPE: queries=99 same=99 changed=0 added=0 removed=0` against the
c3+c4 commit — Q78 still byte-identical (15 rows, checksum
`c06cf981a7819a37`, unchanged across c2/c3+c4/c5). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` shows only the pre-existing unrelated
`internal/parser` `GroupedJoinUnaliased` AST-drift failure. TPC-H spotcheck
SKIPPED (pre-existing M0142-0003k data-dir blocker, unrelated). The decline
this loop's gate adds is a pure safety net on an already-unreachable path
(§40.3), so a plan-shape-identical sweep is the expected and required
result, not a surprise.

### 40.5 What this means for the rest of the `-0008c` family

**`-0008c-3c`/`-3d`/`-4`'s Q10/Q35 acceptance bar is still NOT attemptable**
— reachability is not just "not yet confirmed," it is now known to be
blocked on a specific, nameable prerequisite: something must thread each
semiAnti link's `SpecialJoinInfo` into `ctx.joinInfoList` (or an equivalent
authoritative list the seam's `outerLinksHaveSJInfos`-style check can
legitimately validate against) from a point in the pipeline that runs AFTER
`unnestExistsExpr` creates it — `deconstructJointreeScopedSJI` itself cannot
do this (it runs on `s.FromExprs`, before unnesting exists). Filed below as
`-3i-plumbing-c6`.

## 41. `-3i-plumbing-c6` — real population landed; reachability moves ONE gate deeper, and the next blocker is now precisely diagnosed too

### 41.1 The fix

Populated `ctx.joinInfoList` for real, at the one point in the pipeline
that has both a correctly-renumbered semiAnti `SpecialJoinInfo` (c1-c4's
`extractSearchLeaves` walk already renumbers `j.SJInfo` from
`existsUnnestSJInfo`'s synthetic `synL=1/synR=2` placeholder to real
leaf-index bits — §37/§38's finding, unchanged) and a mutable `ctx`
(`tryPGShapedJoinSearch`'s own `*resolveContext` parameter), immediately
before the c5 gate:

```go
for _, lk := range semiAnti {
    if lk.sjinfo != nil && !joinInfoListHas(ctx.joinInfoList, lk.sjinfo) {
        ctx.joinInfoList = append(ctx.joinInfoList, lk.sjinfo)
    }
}
```

`joinInfoListHas` (relfromjoinlist.go:408, pointer identity) guards a
duplicate append if the seam ever runs more than once against the same
`ctx` for one statement.

**Why this is not §40.3's rejected "vacuous" alternative.** §40.3 rejected
checking `semiAntiLinksHaveSJInfos(semiAnti, semiAntiJoinInfoList(ctx.joinInfoList,
semiAnti))` — a list built FROM `semiAnti`'s own `.sjinfo` field and then
matched against itself, proving nothing about production wiring. This
loop's fix is different in kind, not just in form: it performs a REAL
production write into the actual `ctx.joinInfoList` field — the same field
`deconstructJointreeScopedSJI` populates for outer links, and the same
field any FUTURE consumer of `ctx.joinInfoList` (not just this one gate)
will read. It also still declines correctly whenever `lk.sjinfo == nil` —
the IN/NOT-IN unnesting paths (`unnest.go:3453`, `:3588`, `:4726`) never set
`.SJInfo` on the joins they build, so a semiAnti link sourced from one of
those still fails to find a match and the gate still declines it,
unchanged from before this loop.

This also turns out to match upstream PG's own architecture, not just
goopg's local safety convention: PG's `pull_up_sublinks` (which performs
the EXISTS/NOT-EXISTS → semi/anti-join rewrite) runs BEFORE
`deconstruct_jointree` (which builds `root->join_info_list`) —
`subquery_planner` (postgres/src/backend/optimizer/plan/planner.c) calls
`pull_up_sublinks` during preprocessing, and `query_planner` calls
`deconstruct_jointree` afterward, on the already-rewritten tree. PG's
`join_info_list` is therefore ALWAYS built from a jointree that already
contains every semi-join JoinExpr — there is no second, independent
pipeline to desynchronise from, unlike goopg's `ctx.joinInfoList` (built
pre-unnest) versus `existsUnnestSJInfo` (built post-unnest, over the plan
`Node` tree). This fix does not change goopg's two-pipeline architecture,
but it does make `ctx.joinInfoList` end up in the same POPULATED state PG's
single pipeline would leave it in, for the one purpose (`outerLinksHaveSJInfos`
-style legality checks) that state exists to serve.

### 41.2 Verification

`go build ./...` clean; `go test ./internal/optimizer/...` green. Full-corpus
`GOOPG_PGSHAPED_DP_TRACE=1` sweep (private binary `tmp/goopg-m0142-c6-bin`,
built+removed this loop; sf025 log truncated before each pass): 96/100
EXPLAINs succeeded (same 4 pre-existing unrelated parse gaps as every prior
loop), 184,704 `DPPATH` lines, still **ZERO** `jointype=semi`/`anti` — full
reachability is NOT yet achieved — but the `seam-decline` reason
distribution changed in exactly the way the fix predicts:

| reason | before (c5, §40.2) | after (c6, this loop) |
|---|---|---|
| `semianti-link-no-sjinfo` | 5 (3×nrels=2, 2×nrels=3) | 3 (nrels=2 only) |
| `semianti-on-qual` | 0 | 1 (nrels=3) |

A per-query isolation pass (EXPLAIN each of the 6 corpus files containing
`EXISTS`/`NOT EXISTS` — Q10, Q16, Q35, Q69, Q94, `query_0` — individually
against a freshly-truncated log) pinpoints the new `semianti-on-qual`
decline to **Q69** specifically; Q10/Q16/Q35/Q94 all decline earlier
(`leaf-count` or `outer-over-derived`, upstream of the semiAnti gate
entirely — unrelated to this change) and never reach either semiAnti check.
Q69 was previously blocked at `semianti-link-no-sjinfo`
(the exact bug this loop fixes) and now reaches `semianti-on-qual` instead —
direct, per-query evidence that the population is real and not a no-op,
not just an aggregate count shift that could be explained another way.

TPC-DS SF0.25 sweep at the new HEAD: `PASS=96 (60 ck-verified, 36 ck=n/a)
MISMATCH=0 CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE: queries=99 same=99 changed=0
added=0 removed=0` against the c5 commit — Q78 still byte-identical
(15 rows, checksum `c06cf981a7819a37`, unchanged across every c2-c6 loop).
The decline this loop's fix newly PERMITS (progress past the sjinfo gate)
does not flip Q69's plan output because `semianti-on-qual` still declines
it one gate later — a plan-shape-identical sweep is therefore still the
expected and required result, not a surprise.

### 41.3 Root cause of the new blocker, diagnosed via temporary instrumentation (reverted, not landed)

Added throwaway per-conjunct debug prints inside `semiAntiOnQualsOK`
(env-gated, built into the private `tmp/goopg-m0142-c6-bin`, then fully
reverted via `git checkout` before this loop's commit — no debug code
landed) and ran Q69 alone. Q69 is TPC-DS's only chained-multi-EXISTS query:
`customer, customer_address, customer_demographics WHERE … EXISTS(store_sales
JOIN date_dim …) AND NOT EXISTS(web_sales JOIN date_dim …) AND NOT
EXISTS(catalog_sales JOIN date_dim …)`, i.e. THREE semiAnti links chained
over the same 3-relation outer, each an independent correlation on
`c.c_customer_sk`.

The instrumented run showed the FIRST link's (EXISTS store_sales) equijoin
conjunct passing cleanly (`lhs=7` bits 0-2 = customer/ca/cd, `rhs=8` bit 3 =
the store_sales+date_dim opaque leaf, both operands correctly resolve).
The SECOND link's (NOT EXISTS web_sales) equijoin conjunct — `lhs=15` bits
0-3 (customer/ca/cd + the FIRST link's now-spliced-in opaque leaf), `rhs=16`
bit 4 (the web_sales+date_dim opaque leaf) — resolves to `rs=9` = bits {0,3}:
the outer operand (`c.c_customer_sk`) correctly lands in leaf 0 (customer),
but the inner operand lands in leaf **3** (the FIRST link's own opaque
leaf) instead of leaf **4** (this link's own opaque leaf) — `overlapR=false`,
which is what trips the decline.

**Root cause:** `unnest.go:4694`'s `innerKey.Index = outerWidth +
params[0].SubCol.Index` is correct for EXECUTION (the Join operator's own
padded row is always `[this join's outer row][this join's inner row]`,
and `outerWidth = len(outerChild.Output())` is exactly that outer row's
width, unaffected by any semiAnti link nested inside `outerChild` — Semi/
Anti's `Output()` republishes the outer schema only, per plan.go's own
`JoinTypeSemi` doc comment, so chaining never changes what `outerWidth`
reports here). It is WRONG for the search seam's flat leaf-space, which
`extractSearchLeaves`'s walk (joinsearchseam.go, the `rebaseChainQual(pred,
base)` call) tries to convert it into using a SINGLE additive `base`
captured once, at function entry, before recursing into `j.Left`. `base`
is the correct shift for the OUTER operand (LeftKey's raw index is already
0-based from the TRUE original outer schema, so `base` — the true flat
offset of `j.Left`'s first leaf — lands it correctly). It is the WRONG
shift for the INNER operand (RightKey/innerKey's raw index is `outerWidth
+ innerColIndex` in THIS join's own 2-participant coordinate space, so its
correct flat-space shift is not `base` but `base + (columns contributed by
j.Left's ENTIRE subtree)` — the value `width` holds right AFTER `walk(j.Left,
preserved)` returns, which the walk already computes but never captures
under its own name, and never uses for this purpose). Whenever a semiAnti
link is chained BELOW another one (Q69's pattern: link 2 built with link 1
already spliced into its `outerChild`), the two operands need two DIFFERENT
shifts and a uniform `base` can only get one of them right — which is
exactly what the instrumented run measured.

This is a distinct, harder bug from `-3i-plumbing-c1` through `-c6`
(which are all about WHETHER `ctx.joinInfoList` carries an entry at all);
this one is about whether an entry that IS present carries the RIGHT
`RelSet` bits once more than one semiAnti link is chained over the same
outer. Filed below as `-3i-plumbing-c7`; single-EXISTS queries (Q78, and
presumably Q10/Q35 once whatever declines them earlier is separately
fixed) are NOT affected by this specific bug — it only manifests when a
second (or later) chained link's inner key coordinate is computed relative
to an `outerChild` that already has an earlier sibling link spliced into it.

## 42. `-3i-plumbing-c7` landed — chained-link inner-key rebase fixed; the next blocker is a pre-existing guard that is over-broad for a semiAnti RHS leaf (2026-09-17)

### 42.1 The fix

`extractSearchLeaves`'s semiAnti walk arm (joinsearchseam.go, inside
`walk`) now captures two more values right before appending `j.Right` as
the opaque leaf: `outerWidth := len(j.Left.Output())` (the SEMANTIC width
`unnestExistsExpr` used when it built `LeftKey`/`RightKey`/`Predicate` —
unnest.go:4694/4762, always `len(outerChild.Output())` at synthesis time)
and `rightBase := width` (the walk's own flat-leaf-space position where
`j.Right`'s opaque leaf is about to start). The single `rebaseChainQual(pred,
base)` call is replaced with a new `rebaseSemiAntiChainQual(pred,
outerWidth, base, rightBase)`, which rewrites each `ColumnRef` in `pred`
piecewise instead of by one uniform `+= base`:

- `cr.Index < outerWidth` (the OUTER operand, 0-based in the true original
  outer's own schema per `unnest.go`'s convention): shift by `base`, same
  as before — the true original outer's leaves are always the FIRST leaves
  the walk appends under `j.Left`, chained or not, so this half of the old
  code was already correct.
- `cr.Index >= outerWidth` (the INNER operand, encoded as `outerWidth +
  subcol`): shift to `rightBase + (cr.Index - outerWidth)` instead — the
  flat-space position of `j.Right`'s own leaf, plus the inner column's
  local offset.

The two shifts coincide (`rightBase == base + outerWidth`) exactly when
`j.Left` contributes NO extra flat leaves beyond `outerWidth` — the
unchained case, where `rebaseSemiAntiChainQual` is a no-op fast path
(`base == 0 && rightBase == outerWidth`) identical to the old skip-if-`base
!= 0` behavior. They diverge whenever `j.Left` is itself a chained semiAnti
link: the walk recurses into it and appends its own opaque RHS leaf, so the
flat leaf count under `j.Left` (`rightBase - base`) exceeds `outerWidth` by
exactly that extra leaf's width — which is precisely Q69's second (and
third) EXISTS link's situation, and exactly the gap `-3i-plumbing-c6`'s
§41.3 root-caused via throwaway instrumentation.

### 42.2 Verification

`go build ./...` clean; `go test ./internal/optimizer/...` green, including
a NEW regression test,
`TestExtractSearchLeaves_AdmitSemiAnti_ChainedLinksRebaseInnerKeyCorrectly`
(semiantichain_test.go) — a two-link chained fixture (`t1` outer, two
independent single-table `EXISTS` bodies over `t2`/`t3`) that asserts both
links' folded equality resolves within their own `{lhs,rhs}` via
`relidsOfExpr`/`relsOverlap`, then calls `semiAntiOnQualsOK` end to end.
Verified this test is a REAL pin, not a tautology: temporarily reverted
just the `rebaseSemiAntiChainQual` call to the old uniform
`rebaseChainQual(pred, base)` (kept `outerWidth`/`rightBase` declared but
blanked to keep the build compiling) and re-ran — the test FAILS with
`relidsOfExpr = 0x3 (bits {0,1}), want overlap with rhs=0x4 (bit {2})`,
i.e. the second link's inner operand lands in leaf 1 (the first link's own
opaque leaf) instead of leaf 2 (its own) — the same `overlapR=false`
symptom §41.3's Q69 instrumentation measured, reproduced here in a minimal,
permanent, checked-in fixture instead of a throwaway debug print. Reverted
back to the real fix before commit; full suite re-confirmed green.

Full-corpus `GOOPG_PGSHAPED_DP_TRACE=1` sweep (private binary
`tmp/goopg-m0142-c7-bin`, sf025 log truncated before each pass): 96/100
EXPLAINs succeeded (same 4 pre-existing unrelated parse gaps). `DPPATH`
count 150,137 (stable vs the pre-c6 baseline — c6's own +34,567 delta was
specific to reaching `semianti-on-qual` at all; once c7 lets that gate
pass, the combo declines one gate later inside `searchOneProblem` before
generating as many candidate paths, per §42.3, so the aggregate count
alone is not read as a progress signal by itself — the decline-reason
table is). Still **ZERO** `jointype=semi`/`anti` DPPATH lines. Decline
reasons, `seam-decline` lines only:

| reason | before (c6, §41.2) | after (c7, this loop) |
|---|---|---|
| `semianti-link-no-sjinfo` | 3 (nrels=2 only) | 3 (nrels=2 only, unchanged) |
| `semianti-on-qual` | 1 (nrels=3, pinned to Q69) | **0** |

A per-query isolation pass on Q69 alone (fresh-truncated log, single
`EXPLAIN`) confirms `reason=semianti-on-qual` never appears — Q69's chain
now clears BOTH semiAnti gates — and shows exactly one NEW seam-decline,
`reason=outer-over-derived nrels=6`, root-caused in §42.3.

TPC-DS SF0.25 sweep at the new HEAD: `PASS=96 (60 ck-verified, 36 ck=n/a)
MISMATCH=0 CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE: queries=99 same=99
changed=0 added=0 removed=0` against the c6 commit — Q78 still
byte-identical (15 rows, checksum `c06cf981a7819a37`, unchanged across
every c2-c7 loop); Q69 unaffected in output (`ck=n/a`, 100 rows, a
saturated `LIMIT` — this fix cannot yet change Q69's chosen plan since a
later gate still declines it, per §42.3).

### 42.3 Root cause of the NEXT blocker: `problemPairsOuterWithDerived` treats a semiAnti RHS leaf's `nil` `baseRelInfo.table` as "no statistics," which is true of a genuine CTE/subquery but NOT of a multi-relation EXISTS body

Q69's isolated run's one `seam-decline` line reads `reason=outer-over-derived
nrels=6`, traced from `relfromjoinlist.go:665` — inside `searchOneProblem`,
a LATER stage of the same PG-shaped pipeline that runs only after
`tryPGShapedJoinSearch`'s own admission gates (leaf-count,
`semianti-link-no-sjinfo`, `semianti-on-qual`) all pass. This is why it was
never visible before c7: Q69 never reached this far.

`problemPairsOuterWithDerived` (relfromjoinlist.go:564, C-04a's Q78 fix —
see the function's own doc comment) declines a problem whenever an OUTER
hand of some `SpecialJoinInfo` in scope touches an item classified
"derived" by `leafIsDerivedInput` (:523). That classifier descends through
`*Filter`/`*Project` wrappers to the underlying scan node, and returns true
for `*CTEScan`/`*WorkTableScan` OR whenever `info.table == nil`. The
function's `switch sj.Jointype` arm already includes `parser.JoinSemi,
parser.JoinAnti` alongside `Left/Right/Full` — deliberately, per
`TestProblemPairsOuterWithDerivedSemiOverDerived`/`...AntiOverDerived`
(semiantichain_test.go:140/154), which pin a Semi/Anti RHS touching a CTE
as correctly declined, same as an outer join.

The gap: `baseRelInfo.table` is nil not only for a genuine CTE/subquery
(which truly has no base-table statistics, the C-04a hazard) but for ANY
leaf built from more than one base table — including a semiAnti's own
opaque RHS leaf whenever the EXISTS/NOT EXISTS body joins ≥2 relations
(Q69's `store_sales JOIN date_dim` / `web_sales JOIN date_dim` / `catalog_sales
JOIN date_dim`, each wrapped as one opaque `*Project{*Join{...}}` leaf by
the walk — §12.4/§13's finding that this shape is `*Project` over a real
`*Join`, not a CTE). That opaque leaf's row estimate is NOT a defaulted
guess: it comes from the already-cost-estimated join subplan underneath,
the same subplan `unnestExistsExpr` spliced in wholesale. `leafIsDerivedInput`
cannot currently tell the difference — it only asks "is there a single
base table," not "does this leaf's estimate rest on real statistics" —
so it misclassifies every multi-relation EXISTS/NOT-EXISTS body as
`derived`, and every semiAnti chain whose RHS is multi-relation trips this
guard once it reaches `searchOneProblem`.

This also explains why Q78 (single-table EXISTS body — `t2` alone, or
TPC-DS's own single-relation EXISTS shape) stays unaffected end to end:
its RHS leaf descends to an actual base-table scan with a real
`info.table`, so `leafIsDerivedInput` returns false and this specific guard
never fires for it — Q78's continued byte-identical plan is explained by
some OTHER, not-yet-found gate declining it (still open, not this one).

Filed below as `-3i-plumbing-c8`. Candidate fix directions (not
implemented this loop): (a) give `leafIsDerivedInput` a THIRD signal
distinguishing "provably no stats" (CTE/subquery/function-scan) from
"multi-relation subtree with real per-child stats" (a semiAnti RHS leaf,
or any opaque join leaf built by this package's own rewrites) — e.g. read
whether the leaf's `Node.EstimateRows()` traces back to a non-default
value rather than keying on `.table == nil`; (b) narrow
`problemPairsOuterWithDerived`'s Semi/Anti arm to skip touching ITS OWN
RHS hand specifically (the one hand that is ALWAYS opaque-by-construction
for a semiAnti link, as opposed to an outer join's nullable side, which is
a real, independently-searchable subtree) while still declining if a
DIFFERENT derived input (an actual CTE elsewhere in the problem) is
touched. (b) is likely closer to the guard's original C-04a intent and
would not require plumbing a new stats-confidence signal through
`baseRelInfo`.

## 43. M0142-0008a-3i-plumbing-c8: a provenance flag, not (a) or literal (b)

### 43.1 Why neither candidate direction survived as written

(a) was rejected as fragile: `EstimateRows` returns a plain `int64`, and a
"non-default value" test cannot distinguish a genuinely no-stats CTE that
happens to estimate to, say, 40 rows from a real per-child estimate that
happens to be 1 — the C-04a Q78 hazard was specifically a **rows=1**
default, but nothing guarantees every derived input defaults to exactly
that value, and nothing guarantees a real join estimate never lands on a
small number either. Keying a safety guard on a numeric coincidence is the
wrong kind of signal.

(b) read literally — exempt `sj.SynRighthand` from the bitmask `touched`
test for a Semi/Anti `sj` — was checked against the two existing pinned
tests (`TestProblemPairsOuterWithDerivedSemiOverDerived`/
`...AntiOverDerived`, semiantichain_test.go:140/154) and found to BREAK
them: both fixtures place the derived (CTE) item exactly on the semiAnti
sj's own `SynRighthand` (`sjis[0] = {JoinSemi, SynLefthand:1<<0,
SynRighthand:1<<1}`, `items[1] = cteLeaf`), and both assert the problem
MUST decline. Removing `SynRighthand` from the bitmask this sj tests would
make `touched` collapse to `SynLefthand` alone, `derived[1]` would no
longer be reachable through this sj, and the guard would silently stop
declining the exact CTE-on-RHS shape it was built to catch. (b)'s prose
("exempt a link's own rhs hand") turns out to describe a narrower target
than the SJInfo bitmask can express: it means "exempt THIS SPECIFIC LEAF
being classified derived because IT HAPPENS TO BE this link's own
synthetic opaque leaf" — a per-item distinction, not a per-hand one.

### 43.2 The actual fix: mark the leaf's provenance where it is built

`baseRelInfo` gains one new field, `isSemiAntiSyntheticLeaf bool`
(cardinality.go). It is set to `true` at exactly one construction site —
joinsearchseam.go's Semi/Anti synthetic-leaf loop (the `for k := range
semiAnti` block predp.go's c2 added), the same place that already prices
the leaf with `EstimateRows(scan)` over the spliced-in join subtree instead
of a catalog lookup, with a comment explaining why (`scan` is a real,
already-sized plan subtree). `leafIsDerivedInput` (relfromjoinlist.go)
checks the flag FIRST and returns `false` immediately when set, before the
Filter/Project-unwrap loop or the `CTEScan`/`WorkTableScan` type switch ever
run.

This is narrower than either candidate: it does not touch the
`SynLefthand`/`SynRighthand` bitmask logic `problemPairsOuterWithDerived`
uses at all (so the two pinned CTE-on-RHS tests are untouched — their
fixtures build `relInfos` directly in the test, never set the new flag, and
never go through the synthetic-leaf construction site), and it does not
infer anything from a numeric row estimate (so it cannot be fooled by a
coincidental small number). It reads as "was this baseRelInfo built by the
one code path that prices an opaque multi-relation subtree with real
stats," which is exactly the distinction 43.1 needed and the C-04a guard's
comment (relfromjoinlist.go:495-511) already draws in prose ("derived
inputs carry no statistics") — the flag makes that prose machine-checkable
at the one site where it was previously approximated by `table == nil`.

### 43.3 A residual gap, noted rather than fixed here

The flag is leaf-scoped, not subtree-scoped: if a semiAnti RHS body itself
contains a genuine CTE nested inside a multi-relation join (e.g. `EXISTS
(SELECT 1 FROM cte_x JOIN t3 ON …)`), the synthetic leaf's TOP node is
`*Join`, the flag is set, and `leafIsDerivedInput` now returns `false`
without ever seeing the `*CTEScan` one level down — this differs from
BEFORE this fix, where the same case accidentally declined via the
`info.table == nil` fallback (right answer, wrong reason: EVERY
multi-relation semiAnti RHS had `table == nil`, whether or not it
contained a real CTE). Whether this nested shape occurs in the TPC-H/
TPC-DS corpus is not known to be the case today (no corpus query nests a
CTE inside an EXISTS/NOT-EXISTS body), and neither candidate direction in
§42.3 asked for it — the task's own MUST-NOT list is only the two existing
pinned tests plus a new one for "multi-relation EXISTS body must not
decline," which this fix satisfies. Recorded as a deferral-ledger row
rather than silently accepted.

### 43.4 Unit test and controlled revert-and-check

`TestProblemPairsOuterWithDerivedSemiOverMultiRelationRHSDoesNotDecline`
(semiantichain_test.go) is the third test §42's task description asked
for: a Semi sj whose RHS item has `isSemiAntiSyntheticLeaf: true` and no
`table` must NOT decline. Verified to FAIL against the pre-fix code
(temporarily gated the new check behind `if false && …`, confirmed the
exact same "must NOT decline" failure the design predicted, then restored
the real fix) before landing — same discipline c7 introduced. The two
pre-existing pinned tests
(`TestProblemPairsOuterWithDerivedSemiOverDerived`/`...AntiOverDerived`)
were re-run alongside it and still pass unchanged.

### 43.5 Verification

`go build ./...` clean; `go test ./internal/optimizer/...` green (13/13
`TestProblemPairsOuterWithDerived*` cases, including the new one).

Full-corpus `GOOPG_PGSHAPED_DP_TRACE=1` sweep (private binary
`tmp/goopg-m0142-c8-bin`, built+removed this loop; `bench/tpcds/server.sh
start sf025` with the flag exported): 96/100 EXPLAINs succeeded (same 4
pre-existing unrelated parse gaps as every prior loop in this line), 150,255
`DPPATH` lines, still **ZERO** `jointype=semi`/`anti` — full DP-search
reachability is NOT yet achieved. `seam-decline` reason counts corpus-wide:
`semianti-link-no-sjinfo`=3 (unchanged), `semianti-on-qual`=0 (unchanged
from c7), `outer-over-derived`=3 (down from being Q69's own decline
reason at c7 — see below, these 3 are now a DIFFERENT, unrelated query's
pre-existing decline, not Q69's).

Per-query isolation on Q69 alone (freshly truncated log, `EXPLAIN` Q69
only): **ZERO** semiAnti-related or `outer-over-derived` seam-declines of
any kind — Q69 now clears every seam-level gate this milestone has landed
(c5 sjinfo check, c6 population, c7 rebase, c8 derived-input guard). Its
only seam-decline reasons are ordinary per-pair DP-search noise
(`reason=illegal` x22, `reason=no-join-clause` x6,
`reason=strategy-or-mode` x1) — the same `DPTRACE decline` channel every
other query's DP search also emits while exploring join orders, not a
semiAnti-specific gate. `jointype=semi`/`anti` DPPATH is still zero for Q69
specifically too.

TPC-DS SF0.25 sweep (private binary, same run): `PASS=96 (60 ck-verified, 36
ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE: queries=99 same=99
changed=0` against the c7 commit — Q78 still byte-identical (15 rows,
checksum `c06cf981a7819a37`, unchanged since c2). Exactly the expected
result: c8 lets Q69 progress further through admission gates without
changing which plan wins, because costing/DP-search still doesn't reach a
semiAnti path.

### 43.6 Next blocker (not root-caused this loop — filed as c9)

Q69 clearing every seam-level gate and landing on ordinary `illegal`/
`no-join-clause` DP-search noise, with zero `jointype=semi`/`anti` DPPATH
lines, means the next blocker is downstream of the seam entirely — inside
`joinIsLegal`/`makeJoinRel` (joinsearchlevel.go) or the join-path builders
themselves, not `searchOneProblem`'s pre-DP firewalls c5-c8 covered. One
concrete lead, not yet chased: `(*searchCtx).joinIsLegal`
(joinsearchlevel.go:197) already carries a SEMI "unique-ified RHS" admission
arm calling `createUniquePath` (M0142-0008c-1/-2, landed independently of
this milestone's c-series, AFTER the M0142-0008c recon that said this
mechanism did not exist yet — that recon's finding is now stale) — whether
that arm is what Q69's pairing needs, and if so whether
`createUniquePath`'s preconditions (`sjinfo.SemiCanBtree`,
`len(sjinfo.SemiRhsExprs) != 0`, `subpath.Kind == PathPrebuilt`) actually
hold for the leaf-index-rebuilt `j.SJInfo` c7 mutates in place, is
unconfirmed. `buildInitialRels` (joinsearch.go:434) does wrap every leaf —
including the synthetic semiAnti one — in a `PathPrebuilt`
(`newPrebuiltPath`), so that specific precondition looks satisfied by
inspection, but this was not traced end to end with instrumentation. Start
there before assuming a NEW mechanism is needed.

## 44. M0142-0008a-3i-plumbing-c9: instrumented `joinIsLegal`/`createUniquePath` for Q69 — one real bug found and fixed, the actual blocker is one level up

### 44.1 Method: throwaway env-gated instrumentation, per this milestone's discipline

c8's static-inspection lead (`(*searchCtx).joinIsLegal`'s SEMI unique-ify
admission arm, `createUniquePath`'s three preconditions) had exhausted what
reading the code could tell us — the working set's own next step said so.
This loop added temporary `os.Getenv("GOOPG_C9DEBUG")`-gated `fmt.Fprintf`
calls to `joinIsLegal` (every SEMI/ANTI `SpecialJoinInfo` considered per
call, the call's eventual match/no-match outcome, and the catchall "illegal"
branch) and to `createUniquePath` (which of its early-return guards actually
fired). Built a private binary (`tmp/goopg-m0142-c9-bin`), ran it against
the SF0.25 TPC-DS cluster, and captured Q69's own `EXPLAIN` with both
`GOOPG_PGSHAPED_DP_TRACE=1` and `GOOPG_C9DEBUG=1`. All of this
instrumentation was **reverted before committing** — `git diff` against
`internal/optimizer/joinsearchlevel.go`, `joinpaths.go`, and
`createuniquepath.go` is empty in the landed commit; only the real fix
(§44.2, in `unnest.go`) and its regression test survive.

### 44.2 Finding 1 (root-caused and FIXED): `SemiRhsExprs`' `SourceTableIdx` was never rebased, so `createUniquePath` declined on every real call

The first run's `createUniquePath` instrumentation showed, for every single
call reaching it (the SEMI link's own RHS leaf, `rel={?3}` i.e. relset bit 3
in Q69's 6-relation problem):

```
C9DEBUG createUniquePath: decline schema drift cr.Index=3 cr.Name=ss_customer_sk cr.SourceTableIdx=1 oc.Name=ss_customer_sk oc.SourceTableIdx=5 rel=0x00000008
```

`cr.Name == oc.Name` (both `ss_customer_sk`) and `cr.Index` correctly
indexed `child.Output()` — but `cr.SourceTableIdx` (1) disagreed with
`oc.SourceTableIdx` (5), tripping `createUniquePath`'s schema-drift guard
(`createuniquepath.go`, "the index no longer names the same column").

Root cause, confirmed by reading `unnestExistsExpr` (`unnest.go`) top to
bottom: `existsUnnestSJInfo` builds `SemiRhsExprs[i]` directly from
`prm.SubCol` — the correlation column as it was harvested from the EXISTS
body **before** that body gets remapped into the outer tree's numbering.
`unnestExistsExpr` already had to solve this EXACT problem once, for a
sibling field: `innerKey` (the join's own `RightKey`, used by the executor's
hash-join operator) is built with `SourceTableIdx:
params[0].SubCol.SourceTableIdx + srcTableOffset` — a comment right there
explains why: *"SubCol was harvested from the PRE-remap EXISTS body, so it
still carries the pre-shift value — without the offset this would disagree
with the scan explainNames now registers under the shifted numbering
(M0142-0008e)."* `SemiRhsExprs` is built from the exact same `prm.SubCol`,
a few lines below `innerKey`, but was never given the same `+srcTableOffset`
treatment — an oversight from when `SemiRhsExprs` was first populated
(M0142-0008c-1), which pre-dates any live caller ever reaching this code
path (the file's own comment: *"this gate never actually declines a real
query today"* — true until this very call, the first time `createUniquePath`
was ever exercised against a real, spliced multi-relation semiAnti leaf
rather than a hand-built unit-test fixture).

This is **not** the same class of bug as c6/c7's qual-rebase work (those
rebased `.Index` inside the join predicate via `rebaseChainQual`/
`rebaseSemiAntiChainQual`, run by the search-seam walk at splice time).
`SemiRhsExprs` lives on the `SpecialJoinInfo`, not inside any `Expr` tree the
seam walks, and its `.Index` was **already correct** (confirmed: the
`cr.Name == oc.Name` match at `cr.Index=3` above) — only the cosmetic-looking
but load-bearing `SourceTableIdx` field had drifted, and it drifted at
`unnestExistsExpr`'s OWN remap point (`remapSourceTableIdx`, called on
`innerPlan` a few lines before `existsUnnestSJInfo`), not at the seam.

**Fix**: `existsUnnestSJInfo` gained a fourth parameter, `srcTableOffset
int16`, and `SemiRhsExprs[i]` is now built as a fresh `*ColumnRef` (Index/
Name/Type copied from `prm.SubCol`, `SourceTableIdx: prm.SubCol.SourceTableIdx
+ srcTableOffset` — the identical expression `innerKey` already uses) instead
of assigning `prm.SubCol` verbatim. The one production call site
(`unnest.go`, `unnestExistsExpr`) passes its own `srcTableOffset`; the two
unit-test call sites that pass `params: nil` (`semiantichain_test.go`,
`m0142_0008a_3i_plumbing_probe_test.go`) pass `0` since the loop never
executes for them.

**Regression test**: `TestExistsUnnestSJInfoSemiHashKey`
(`exists_unnest_sjinfo_test.go`) gained an assertion that
`sj.SemiRhsExprs[0].SourceTableIdx == j.RightKey.SourceTableIdx` — the two
fields are built from the same `prm.SubCol` and must always agree. Verified
FAILS on pre-fix code (temporarily reverted just the `+srcTableOffset`
addition, confirmed `SemiRhsExprs[0].SourceTableIdx = 1, want 3`, restored
the fix) before landing.

**Verification this loop**: `go build ./...` clean; `go test
./internal/optimizer/...` green (includes the fail-then-pass check above,
plus the full targeted SEMI/ANTI/`createUniquePath`/`existsUnnestSJInfo`
test set). Re-ran the same Q69 `EXPLAIN` against the fixed build:
`createUniquePath` now logs `SUCCESS rel=0x00000008 keyCols=[3]` — the very
first time this mechanism has ever actually succeeded for a live query,
confirmed by the file's own "never actually declines a real query today"
comment becoming false as of this fix. Row-count check against the
git-tracked SF0.25 oracle (`bench/tpcds/runtime_goopg/tpcds-results-sf025/oracle.txt`):
Q69 still returns exactly 100 rows (unchanged — it still plans via the
syntactic fallback, see §44.3) and the pinned Q78 still returns exactly 15
rows with checksum `c06cf981a7819a37` (byte-identical to the oracle,
unaffected as expected since Q78's EXISTS body is single-relation and never
reaches `createUniquePath`'s multi-relation domain). The full
`scripts/tpcds-sf025-regression.sh sweep` gate could not be run this loop —
`ci/batch`'s nightly run held the shared SF0.25/SF1 lanes for the whole
session (its own FATAL guard: *"the nightly CI batch is running ... would
contaminate these timings"*) — the two spot-checks above are the
substitute evidence; a full sweep should still be run at the next
opportunity per this milestone's standing gate list.

### 44.3 Finding 2 (root-caused, NOT fixed — filed as c10): fixing createUniquePath was necessary but not sufficient — the ANTI links have no equivalent escape valve, and their `MinLefthand` is over-broad by the same "atomic-LHS" convention

Even with §44.2's fix landed, Q69's `jointype=semi`/`anti` DPPATH
reachability is **still zero** and the DP search still fails with `"join
search: failed to build any 4-way joins"` (unchanged from every prior c-series
loop). Root cause, read directly off the corpus trace's `DPTRACE
problem`/`pair`/`decline` lines for Q69's own top-level 6-relation search
(`problem nrels=6 rels=c,ca,customer_demographics,?3,?4,?5` — `?3` is the
SEMI leaf, `?4`/`?5` the two ANTI leaves):

- **The SEMI leaf (`?3`) now admits at level 2** against `{c}` alone via the
  fixed `createUniquePath` unique-ify fallback (confirmed directly: the
  `C9DEBUG joinIsLegal` instrumentation showed `rel1=customer(0x1)
  rel2={?3}(0x8)` matching via that branch, no error). This is real
  forward progress: before this loop, the branch's condition
  (`createUniquePath(...) != nil`) was NEVER true for a live query.
- **Both ANTI leaves (`?4`, `?5`) still decline as `reason=illegal` at
  EVERY level** they are tried at (2, 3, and 4, plus phase 3's last-ditch
  pass) — confirmed via a second instrumentation pass (a debug print at
  `joinIsLegal`'s final `else { return nil, false, err }` catchall)
  showing every one of these declines names an ANTI `SpecialJoinInfo`,
  never the (now-fixed) SEMI one.

The reason is structural, not a small bug: `?4`'s `SpecialJoinInfo` carries
`MinLefthand = 0x0f` (bits 0-3 — **all of** `{c, ca, customer_demographics,
?3}`) and `?5`'s carries `MinLefthand = 0x1f` (bits 0-4 — those four PLUS
`?4`). Since `jointypeForDirection`'s ANTI arm (`joinpaths.go`) has **no**
unique-ify fallback (correctly — PG's own `create_unique_path` call sites
are SEMI-only; ANTI cannot be legally reordered past an arbitrary other rel
by de-duplicating its RHS, since ANTI's "no match" semantics do not survive
naive unique-ification the way SEMI's "first match" semantics do), an ANTI
link's ONLY admission route is the direct `MinLefthand`/`MinRighthand`
containment check. With `MinLefthand` this broad, `?4` cannot be admitted
against anything smaller than the FULL `{c,ca,customer_demographics,?3}`
composite — and the DP search never successfully builds that composite as
ONE rel at level 4 before phase 1/3 both exhaust themselves (all `illegal`),
so the whole 6-way problem fails and the query falls back to the syntactic
join order (which the executor still plans and executes correctly — Q69's
row count is right — just not via a genuine DP-costed plan).

**Why `MinLefthand` is this broad — the SAME "atomic 2-bit convention"
already named as a limitation in `existsUnnestSJInfo`'s own doc comment**:
`existsUnnestSJInfo` (`unnest.go`) builds every semiAnti link's
`SpecialJoinInfo` using a **self-contained 2-bit scheme** — `SynLefthand =
1` (the WHOLE outer side, as ONE opaque participant) and `SynRighthand = 2`
(the WHOLE EXISTS/NOT-EXISTS body, also ONE opaque participant) — because at
the point `unnestExistsExpr` runs, the outer side has not yet been
decomposed into its real base relations from the search's point of view.
Since `len(params) > 0` for every semiAnti link in Q69 (each has a real
equijoin correlation column), `existsUnnestSJInfo`'s own narrowing logic
sets `clause = synL | synR` (unconditionally — "any param/residual makes the
clause span both sides", per its own comment) and therefore `minL = clause &
synL = synL` — i.e. **`MinLefthand` always equals the WHOLE outer side,
never narrowed to the actual referenced relation(s)**, for every live
producer of a semiAnti `SpecialJoinInfo`, not just Q69's chained ones. This
is intentional and documented as a KNOWN gap at construction time ("-3
recomputes real bits once the RHS actually joins the search" — but nothing
in the c5-c9 series has actually done that narrowing yet; c6/c7/c9 all
narrowed or fixed OTHER fields — `ctx.joinInfoList` population, the join
QUAL's `.Index` rebase, and now `SemiRhsExprs`' `SourceTableIdx` — but never
`MinLefthand`/`MinRighthand` themselves).

**Confirmed this is NOT merely a "chained sibling" artifact of Q69
specifically**: all three of Q69's semiAnti links correlate on the exact
same single outer column, `c.c_customer_sk` (`store_sales`/`web_sales`/
`catalog_sales` each join `customer` on that one column, confirmed by
reading `/home/ryo/work/TPC-DS-QUERIES-FOR-PG/query69.sql` directly) — so
the TRUE minimal `MinLefthand` for ALL THREE links is just `{customer}`
(one bit), not `{customer,ca,cd}` (the SEMI link, bits 0-2) or
`{customer,ca,cd,?3}`/`{customer,ca,cd,?3,?4}` (the two ANTI links, which
additionally and artificially inherit every EARLIER semiAnti leaf's bit
purely because `unnestExistsExpr` processes WHERE-clause conjuncts one at a
time and nests each new join around the accumulated tree — a
processing-order artifact, not a real data dependency between the three
conjuncts, which are independent ANDed predicates at the SQL level). Real
PG's planner is not subject to this: its `min_lefthand` is computed from
`pull_varnos(clause)` against the REAL base-relation numbering PG already
has at deconstruction time, so PG's own chosen plan for Q69 (§44.4) applies
the SEMI join right after `customer` alone (via its OWN unique-ify, a
`HashAggregate GROUP BY ss_customer_sk`) and defers both ANTI joins to the
very end — a COST decision PG's planner was free to make either way, not a
legality constraint forced by an inflated `min_lefthand`.

### 44.4 What c10 needs to do

Narrow `MinLefthand`/`MinRighthand` for a semiAnti link's `SpecialJoinInfo`
from "the whole atomic outer/inner participant" down to "the real base
relation(s) the correlation clause(s) actually reference," analogous to
what `makeSpecialJoinInfoScoped` (`specialjoin.go`) already does for
ordinary FROM-clause outer joins via `sjiClauseRelids`. Candidate approach:
since `params[i].OuterRef` is a real, resolvable `ColumnRef` into the outer
side's schema, the narrowing has to happen **at splice time** (the same
search-seam code c6/c7/c8 already touch, `joinsearchseam.go`'s semiAnti
synthetic-leaf construction loop, ~line 665-686) rather than at
`existsUnnestSJInfo` construction time (unnest.go, before the outer side has
been decomposed into real search leaves) — resolve each `OuterRef`/residual
correlation column against the outer's REAL flat-leaf numbering (the same
numbering the seam already computes for the qual rebase) and intersect
against that, instead of leaving `MinLefthand = SynLefthand` unconditionally.
Must NOT regress `TestExistsUnnestSJInfoSemiHashKey`/`AntiHashKey`/
`KeylessSemi` (which pin the CURRENT unnarrowed `Min == Syn` behavior at
`existsUnnestSJInfo`'s own construction boundary — those stay correct
statements about what `existsUnnestSJInfo` itself produces; the narrowing is
a SEPARATE, later transformation the seam applies, the same relationship c7's
`rebaseSemiAntiChainQual` already has to `existsUnnestSJInfo`'s un-rebased
qual). Also confirm whether `MinRighthand` needs any equivalent narrowing (a
priori no — `SynRighthand`/`MinRighthand` already correctly names exactly one
atomic leaf, the whole EXISTS/NOT-EXISTS body, and PG's own plan treats that
whole body as one unit too, e.g. the `Gather`/`HashAggregate` subtree over
`store_sales`+`date_dim`).

### 44.5 What Q69's real PG plan tells us about the target shape

`bench/tpcds/plans-pg/Q69.txt` (real PG 18.3): innermost, `customer` joins
directly to a `HashAggregate(GROUP BY ss_customer_sk)` over the
`store_sales`⋈`date_dim` composite (PG's own unique-ify SEMI mechanism) via
Nested Loop — BEFORE joining `customer_address` or `customer_demographics`
at all. `ca`/`cd` join in next (both Nested Loop, both trivial single-row
index lookups). Only at the very TOP does PG apply BOTH anti joins, as `Hash
Right Anti Join` against the `web_sales`/`catalog_sales` composites, spanning
the entire `{customer,ca,cd,semi}` composite by then. This is the shape
c10's narrowing needs to make REACHABLE (not necessarily the shape goopg's
own cost model will PICK — that is a separate, later question once the DP
search can enumerate it at all).

## 45. c10 — MinLefthand/MinRighthand narrowed at splice time; reachability STILL zero, root cause pinned to a `ctx.joinInfoList` duplication bug (next: c11)

### 45.1 Landed: the narrowing itself, exactly as c9 filed it

`joinsearchseam.go`'s semiAnti synthetic-leaf splice site (the walk's
`JoinTypeSemi`/`JoinTypeAnti` admission arm, where `j.SJInfo.SynLefthand,
MinLefthand = lhs, lhs` used to fire) now computes a narrowed `minL`/`minR`
separately from the un-narrowed `lhs`/`rhs`: `buildLeafSpans(widths, nil)`
(the "no semiAnti links" branch — a plain cumulative sum) reconstructs the
SAME per-leaf column-index table `rebaseSemiAntiChainQual` just wrote
`pred` into (both are the walk's own naive running-cumulative space, NOT
`buildLeafSpans`'s canonical post-walk "real-then-synthetic-out-of-band"
space — passing the REAL `semiAnti` list here would have been wrong,
exactly the trap §44.4 flagged). `relidsOfExpr(pred, spans)` then resolves
`pred`'s ColumnRefs back to real leaves; intersecting with `lhs`/`rhs`
gives the narrowed bits, with a safe fallback to the old un-narrowed value
whenever the clause can't be resolved. `SynLefthand`/`SynRighthand` are
left untouched at the full `lhs`/`rhs` (deliberately — see the unnest.go
comment cited in §44.4).

New unit test `TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation`
(semiantichain_test.go) proves this positively, which none of the prior
tests could: `TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo`'s
own `MinLefthand == wantLHS` assertion passes whether or not narrowing
happens at all, because its fixture's outer side is a SINGLE relation (`t1`
alone) — `lhs` is already a single bit there. The new fixture gives EXISTS
a TWO-relation outer (`t1, t3`, joined `t1.x = t3.a`, correlated to `t1`
only) so `lhs` (2 bits) and a correctly-narrowed `MinLefthand` (1 bit) are
distinguishable; the fixture also had to select every outer column
(`t1.x, t3.a, t3.b`) — selecting fewer triggers goopg's narrowing PASS
(unrelated to this task, same name coincidence) to insert a `*Project`
between `t1 JOIN t3` and the Semi join, which opaques the whole composite
into ONE leaf and defeats the test (`extractSearchLeaves` only flattens
`*Join` chains, not `*Project`-wrapped ones). Confirmed fail-then-pass:
reverting just the narrowing computation (keeping `minL, minR := lhs, rhs`
with no override) reproduces `MinLefthand = 0x3, want 0x1`.

### 45.2 Confirmed correct in production, via a live SF0.25 trace — and still not enough

A private-binary SF0.25 server (`tmp/goopg-m0142-c10-bin`, built and
removed at the end of this loop) with `GOOPG_PGSHAPED_DP_TRACE=1` and a
temporary (reverted before commit) `C10DEBUG` print inside `joinIsLegal`
confirmed the fix reaches production exactly as intended: all three of
Q69's semiAnti `SpecialJoinInfo`s report `MinLefthand=0x00000001`
(`{customer}` alone) while `SynLefthand` correctly stays broad (`0x7`,
`0xf`, `0x1f`) — §44.3's predicted values, exactly.

Despite that, `GOOPG_PGSHAPED_DP_TRACE=1`'s own `DPTRACE end top={}
... status=join search: failed to build any 4-way joins` line is
UNCHANGED — the DP search for Q69's 6-relation problem still fails
completely, and `jointype=semi`/`anti` still never appears in a `DPPATH`
line. **c10 alone does not make Q69 reachable.**

*Method note, since this took several iterations to pin down correctly*:
the first read of `bench/tpcds/runtime_goopg/goopg.sf025.log` for this
investigation was contaminated by STALE content — `server.sh`'s log
redirect is `>>` (append), so a `C9DEBUG`-tagged trace left over from c9's
own (already-reverted-from-source) investigation was still sitting in the
file and got mixed in with this loop's fresh output. Truncating the log
(`: > .../goopg.sf025.log`) before each restart is necessary for a clean
read; do not trust a `grep` across this file without first confirming
which run's output you're looking at.

### 45.3 Root cause, pinned precisely: `ctx.joinInfoList` gets Q69's three semiAnti `SpecialJoinInfo`s TWICE, as pointer-distinct duplicates

Tracing WHY the exactly-correct pairing — `joinIsLegal(rel1={customer,ca,
customer_demographics}=0x7, rel2={?3 store_sales-EXISTS leaf}=0x8)` — still
declines `reason=illegal`, with two more temporary (reverted) debug prints:

- `len(s.joinInfoList) == 6`, not the expected 3 (one `*SpecialJoinInfo`
  per semiAnti link — SEMI for the `EXISTS store_sales`, ANTI for each
  `NOT EXISTS`).
- For the `(0x7, 0x8)` pair specifically, the search finds **two**
  candidate matches — both `SEMI`, both `MinLefthand=0x1`/
  `MinRighthand=0x8` (identical values, i.e. this loop's narrowing landed
  correctly on BOTH copies), but at **different pointer addresses**
  (`0x...c0e0` vs `0x...c2a0`, confirmed via `matchSJInfo == sj` printing
  `false`) — so `joinIsLegal`'s own "matches multiple SpecialJoinInfos —
  invalid" guard (correct in intent: a pair must match at most ONE SJ)
  fires and declines a pairing that is legitimately legal exactly once.

`joinInfoListHas` (`relfromjoinlist.go:407`) — the only guard standing
between `ctx.joinInfoList` and a duplicate append — dedupes by POINTER
IDENTITY only. Two structurally-identical-but-distinct `*SpecialJoinInfo`
objects sail straight past it.

`ctx.joinInfoList` has exactly two writers in the whole codebase:
`planner.go:3051` (`deconstructJointreeScopedSJI`, ONE-TIME initialization
from the FROM clause's ordinary outer joins) and the monotonic `append` at
`joinsearchseam.go:595` inside the semiAnti-population loop — **there is no
third site that ever clears or rolls back `ctx.joinInfoList`.** Meanwhile
`tryPGShapedJoinSearch` (the function whose call reaches that append) has
**two live call sites for the very same statement**, both in `predp.go`,
and the comment on the second one is now STALE:

- **Phase A** — `predp.go:148`, `tryJoinSearch(f.Child, f.Predicate, ctx,
  cat)` — the ordinary top-level search over the Filter's whole child tree.
- **Phase B** — `predp.go:179`, `tryPGShapedJoinSearch(spineJoins[0], nil,
  ctx, cat)` directly — "a second, additive join-order search rooted at
  the outermost pinned Semi/Anti join," explicitly run AFTER Phase A. Its
  own doc comment (§32.3, written when `admitSemiAnti` was still the
  literal `false`) says *"`used` is therefore false on every production
  call today"* — **no longer true**: `admitSemiAnti` was flipped to the
  literal `true` at the one production call site by an earlier loop in
  this same c-series (b2, `joinsearchseam.go:313`), so Phase B's call now
  genuinely walks and populates `semiAnti`/`ctx.joinInfoList` too, not just
  Phase A's.

The semiAnti-population loop (`joinsearchseam.go:593-597`) runs
UNCONDITIONALLY early in `tryPGShapedJoinSearch`, before any of the later
checks that can still cause the OVERALL call to return `used=false` (a
leaf-count mismatch, an on-qual failure, etc. — see `tryJoinSearch`'s own
"declined → falls back to syntactic order" contract, §package-header
comment). So even a call whose search ultimately fails/declines has
ALREADY appended its freshly-built `*SpecialJoinInfo` pointers to the
shared, never-rolled-back `ctx.joinInfoList` as a side effect. If Phase A's
attempt over the WHOLE tree declines for an unrelated reason (very
plausible — Phase A's `chain` covers all 6 relations at once, a much
larger/pickier `nprefix`/leaf-count surface than Phase B's narrower
`spineJoins[0]`-rooted retry) while still reaching the population loop
before declining, and Phase B then independently re-walks
`spineJoins[0]`'s own (structurally identical) semiAnti chain and appends
ITS freshly-built SJInfo pointers too, `ctx.joinInfoList` ends up with two
generations of the same three links — exactly the observed `len == 6`.
This mechanism (a discarded/declined search attempt's side effects
surviving in shared context state) is the leading hypothesis; the exact
point where Phase A's `*SpecialJoinInfo` objects diverge from
`spineJoins[0]`'s own (same node reference vs. a clone somewhere in the
pipeline) was not traced further this loop and is c11's first step.

### 45.4 What c11 needs to do

Stop `ctx.joinInfoList` from accumulating duplicate/stale `*SpecialJoinInfo`
entries across the two `tryPGShapedJoinSearch` call sites for one
statement. Two shapes of fix, in ascending order of invasiveness:

1. **Structural dedup at append time.** Extend `joinInfoListHas` (or add a
   sibling check called alongside it) to also treat two SJInfos as
   duplicates when they are STRUCTURALLY identical for the fields
   `joinIsLegal` actually reads (`Jointype`, `MinLefthand`, `MinRighthand`,
   `SynLefthand`, `SynRighthand`) rather than requiring pointer identity —
   cheapest, most self-contained, but treats a symptom (the shared list
   ending up "morally deduplicated" already) rather than the cause (why
   two independent builds happen at all).
2. **Stop Phase A and Phase B from both reaching the population loop for
   the same links.** Trace whether Phase A's attempt over the whole tree
   is EXPECTED to decline for Q69 (if so, is skipping its semiAnti
   population loop on the declining path enough — i.e. defer the
   `ctx.joinInfoList` append until the call is about to return
   `used=true`, matching a transactional "commit on success only"
   discipline PG's own single-pass `deconstruct_jointree` never has to
   solve because it only builds `join_info_list` once). This is the
   higher-value fix: it also plugs the same latent hazard for OTHER
   two-call-site combinations of `tryJoinSearch`/`tryPGShapedJoinSearch`
   that don't involve semiAnti at all, not just this one.

Either way, re-verify with the SAME method §45.2 used (private SF0.25
binary + `GOOPG_PGSHAPED_DP_TRACE=1`, log TRUNCATED before each restart)
that `len(s.joinInfoList) == 3` for Q69 post-fix, then re-check for the
`jointype=semi`/`anti` `DPPATH` line this whole c-series has been chasing
since c5. Also update `predp.go:159-176`'s Phase B doc comment — its
"`used` is therefore false on every production call today" claim is now
false and actively misleading to the next reader.

## 46. c11 — root cause CONFIRMED live; the one-line fix unblocks the DP search but surfaces two further, pre-existing defects (both now REQUIRED before landing)

### 46.1 Method

Same private-binary method as c10 (§45.2), extended: a private SF0.25 data
directory copy (`tmp/c11-sf025-data`, removed at the end of this loop) run
WITHOUT the cgroup wrapper (`scripts/goopg-test-run.sh` does not forward
env vars through `systemd-run --user --scope` unless `--setenv` is passed,
so `GOOPG_PGSHAPED_DP_TRACE=1` silently failed to reach the server under
the wrapper — confirmed by an all-zero-byte trace log; running the private
binary directly, with `GOMEMLIMIT=4GiB` set by hand, fixed it). Temporary
(reverted before commit) pointer-identity prints were added at three
points: `tryPGShapedJoinSearch`'s entry, its semiAnti population loop
(joinsearchseam.go:593-597), and `joinIsLegal`'s two error-return arms
(joinsearchlevel.go).

### 46.2 Root cause: NOT two independently-built clones — a single, unconditional double-append

c10 (§45.3) hypothesised "two independently-built pointer-distinct
`*SpecialJoinInfo` copies," provenance untraced. Live tracing this loop
found the true mechanism, and it is simpler and fully explained by reading
the code — no clone theory needed:

`tryPGShapedJoinSearch`'s semiAnti population loop
(joinsearchseam.go:593-597, landed by c6) already threads each semiAnti
link's `*SpecialJoinInfo` into `ctx.joinInfoList`, deduplicated by pointer
via `joinInfoListHas`. A live trace confirmed this leaves `ctx.joinInfoList`
holding exactly 3 unique pointers for Q69, immediately after the loop.

Twenty-odd lines later, the SAME function builds the search's own local
`joinInfoList` field with:

```go
joinInfoList: semiAntiJoinInfoList(ctx.joinInfoList, semiAnti),
```

`semiAntiJoinInfoList` (pre-c11) unconditionally appends `semiAnti`'s own
`.sjinfo` pointers onto `base` (== `ctx.joinInfoList`) with NO dedup at
all. Since `semiAnti`'s `.sjinfo` values are — by construction — the EXACT
SAME pointers c6's loop had already folded into `ctx.joinInfoList` two
dozen lines earlier, this appends each of the 3 links a SECOND time. The
list `planJoinlistSearch` actually searches with therefore has 6 entries:
`[A, B, C, A, B, C]`. `joinIsLegal` (joinsearchlevel.go:239-259) iterates
the WHOLE list per candidate pair and its own "matches multiple
SpecialJoinInfos" guard does not special-case re-visiting the identical
pointer — it fires on the second occurrence of `A` exactly as it would for
a genuine second, different SJInfo. Confirmed live:
`joinIsLegal-multi rel1=0x1 rel2=0x8 prev=0x...dc00 cur=0x...dd50` — two
DIFFERENT addresses in THIS particular run's numbers, which momentarily
looked like it disproved the "same pointer twice" theory, until cross-
checked against the population loop's OWN trace from the SAME run
(`sjinfo=0x...ab0/b20/b90`) — a THIRD, still different set of addresses.
This is explained by `semiAntiJoinInfoList`'s doc comment (pre-c11): it
returns a COPY (`out := make(...); out = append(out, base...)`), so the
`s.joinInfoList` slice `joinIsLegal` iterates over is a freshly-allocated
array holding COPIES OF THE SAME POINTER VALUES twice — the pointer
VALUES stored at index 0 and index 3 of that array are identical (both
equal `ctx.joinInfoList[0]`), but printing `%p` of the loop variable `sj`
mid-iteration for two DIFFERENT slice INDICES holding the SAME pointer
value should still print the SAME address twice, not two different ones —
so the address mismatch was a genuine puzzle, not just an artefact of
copying the slice header. It was never resolved to the byte: two
consecutive server rebuild-and-restart cycles between the three capture
points (population-loop trace, then a rebuild adding the joinIsLegal
prints, then a further rebuild) each reallocate the whole heap from
scratch, so ASLR/allocator layout differs per PROCESS run and the three
address groups (`ab0`-group, `dc00`-group, `dd50`-group) were captured
across what turned out to be non-comparable process instances in the
first two capture attempts — only the FINAL, single-process capture (one
truncated log, one `psql -f -` invocation, both `joinIsLegal-multi` and
the population trace lines within it) is the trustworthy one, and in that
capture the `joinIsLegal-multi` pair's `prev`/`cur` were still two
different addresses from EACH OTHER within the same run. That residual
puzzle does not change the fix or its verification, though: regardless of
the precise identity of the two colliding entries, reading
`semiAntiJoinInfoList`'s call site shows it is handed a `base` that ALREADY
contains what it is about to append again, so the list is a mechanical
double-count by construction, and the fix (drop the second append) is
correct independent of resolving exactly which two entries `joinIsLegal`
printed.

### 46.3 The fix landed, then reverted this same loop

The fix: `joinInfoList: ctx.joinInfoList` (no wrapper call at all) — since
`ctx.joinInfoList` is already complete by the time this struct literal is
built, thanks to c6's population loop. `semiAntiJoinInfoList` becomes
unreferenced and was deleted (`safe_delete_symbol`, zero other call sites
in production or test code).

Verified live via the SAME private-binary method: `ctx.joinInfoList` (and
therefore `s.joinInfoList`) now holds exactly 3 entries; the
`joinIsLegal-multi` decline for `(0x1, 0x8)` and its siblings disappeared;
Q69's DP search — for the FIRST time in this entire c-series (c5 through
c11) — produced `jointype=semi`/`jointype=anti` `DPPATH` lines and
completed the full 6-relation problem (`DPTRACE end
top={?3+?4+?5+c+ca+customer_demographics} ... status=ok`), where every
prior loop's trace ended in `failed to build any 4-way joins`.

**This success immediately exposed two further, pre-existing, entirely
separate defects — both now on the critical path before this fix can
ship:**

1. **`createPlanAtSearchRootRange`'s boundary-totality panic
   (createplanroot.go:264, via `boundaryMap`).** Once the DP search
   actually PICKS a winning 6-leaf tree and tries to materialise it, it
   panics: `"search root does not publish binding coordinate(s)
   [91 92 ... 214]"` — 124 missing coordinates, a suspiciously round
   number for "the semiAnti synthetic leaves' own internal columns"
   (3 leaves × roughly 40 columns each, for `store_sales+date_dim`-style
   sub-joins). The boundary's own doc comment (§21.1-ish region,
   createplanroot.go:196-222) states its contract as "the root's layout
   must be a PERMUTATION of `[0, bindingWidth)`" — i.e. EVERY leaf's
   columns, including a semiAnti synthetic leaf's, must be individually
   traceable to an output column of the searched root. That is almost
   certainly the wrong contract for a semiAnti leaf specifically: a
   SEMI/ANTI join never projects its RHS columns above itself by
   definition (the RHS exists only to be tested, not read), so nothing
   above the join should ever need to "publish" a semiAnti leaf's
   internal columns at all — the boundary's totality check was written
   before any leaf could BE a semiAnti synthetic leaf (S5b was always
   out of scope until this c-series) and was never taught to exempt one.
   This reads as a real, scoped, fixable gap — likely "treat a semiAnti
   leaf's own coordinate range as always-fillable/prunable, the same way
   `fill` already licenses a narrowed index-only leaf's dropped columns"
   — but it is its own task, not a one-line change discovered in passing.
2. **A genuinely different statement (TPC-DS Q10) that ALSO reaches the
   newly-unblocked search regresses from PASS to a hard error**:
   `outer column ref c_current_cdemo_sk/level=1 out of range (depth=0)`.
   Q10's WHERE is `EXISTS(...) AND (EXISTS(...) OR EXISTS(...))` — an
   OR-combined pair of EXISTS clauses, structurally different from Q69's
   pure AND-chain of three EXISTS/NOT EXISTS. The error is a normal
   returned error (not a panic; the server and connection survive), most
   likely raised later than planning — during correlated-subplan
   execution setup — which means the earlier `len(semiAnti) > 0`-scoped
   `recover()` guard this loop first tried (to neutralise defect 1) does
   NOT also neutralise defect 2: they are different failure modes at
   different pipeline stages, not two symptoms of one bug. Confirmed via
   `scripts/tpcds-sf025-regression.sh sweep` (run with a private
   `GOOPG_BIN` to avoid the shared nightly binary): `PASS -Q10` /
   `ERROR +Q10` in the status-delta, with Q69 correctly flipping to
   `PASS  100 rows` (matching the git-tracked oracle) in the same sweep.

Given a CONFIRMED regression on a currently-passing gate query
(`scripts/tpcds-sf025-regression.sh sweep` is one of this project's
required gates for planner/executor changes — see AGENT.md "Plan-parity
harness" and CLAUDE.md's test-gate section), the fix was **reverted in
full this same loop** (`git checkout -- internal/optimizer/joinsearchseam.go`,
confirmed `git diff` empty for both touched files, `go build ./...`
clean) rather than shipped with only a partial safety net. The double-
append bug, its exact one-line fix, and the full live-trace evidence
above are preserved here so the NEXT loop does not have to re-derive any
of it — see c11 in `.ralph/fix_plan.md` for the concrete next steps.

### 46.4 What lands c11 for real

In order, since (a) is required before (b) can even be evaluated safely:

1. **Fix `createPlanAtSearchRootRange`/`boundaryMap`'s totality contract
   for a semiAnti synthetic leaf** (§46.3 item 1) — teach it that a
   semiAnti leaf's own coordinate range is never a real "must-publish"
   requirement, the same way a narrowed index-only leaf's dropped columns
   already get a `fill`-licensed pass. Re-run this loop's exact repro
   (private SF0.25 binary + `GOOPG_PGSHAPED_DP_TRACE=1`, apply ONLY the
   `joinInfoList: ctx.joinInfoList` one-line fix from §46.3, run Q69) and
   confirm the panic is gone and the `EXPLAIN` plan now shows a REAL
   reordering (not just "search succeeded, still fell back").
2. **Root-cause Q10's `outer column ref ... out of range (depth=0)`**
   (§46.3 item 2) — first determine whether Q10's OR-combined EXISTS
   pair should ever have been admitted into the semiAnti chain in the
   first place (PG's own SEMI/ANTI unnesting only ever applies to a
   TOP-level AND-connected EXISTS; an OR-combined EXISTS is a shape PG
   itself keeps as a correlated SubPlan, never flattens into a Join) —
   if `unnestExistsExpr`/`whereEligibleForPreDPUnnest` is over-admitting
   Q10's OR shape, the right fix may be to correctly DECLINE it upstream
   (matching PG's own scope) rather than debug what happens once it is
   wrongly admitted.
3. Re-apply the §46.3 one-line `joinInfoList: ctx.joinInfoList` fix
   (delete `semiAntiJoinInfoList`) and re-run the FULL
   `scripts/tpcds-sf025-regression.sh sweep` (not just Q69/Q10 in
   isolation) to confirm zero verdict regressions before landing.

### 46.5 Item (a) LANDED: boundaryMap now exempts a semiAnti synthetic leaf's own coordinate range — and item (b)'s scope was narrower than filed

**Landed this loop** (production diff is `internal/optimizer/relfromjoinlist.go`
only): `searchOneProblem`'s hole-filler closure — the `fill` callback passed to
`createPlanAtSearchRootRange` (§46.4 item 1) — now checks
`infos[i].isSemiAntiSyntheticLeaf` (the marker §42.4/c8 already established,
`cardinality.go`/`relfromjoinlist.go:leafIsDerivedInput`) BEFORE the
`neededColsKnown`/`outputCols`/`neededCols` gates that exist for every OTHER
leaf kind. A SEMI/ANTI join never projects its RHS columns above itself by
definition, so a binding coordinate inside a semiAnti synthetic leaf's own
range is always licensed to be padded — independent of whether the
statement's needed-column set was computed at all (§46.3 item 1's own framing:
"never a real must-publish requirement"). The three existing failure modes
(hole with no filler, out-of-range leaf lookup, empty column name) are
untouched and still panic.

**Verification method**: same private-binary + `GOOPG_PGSHAPED_DP_TRACE=1`
technique as §46.1-46.2, but this time run TWICE:

1. With the §46.3 `joinInfoList: ctx.joinInfoList` one-liner ALSO applied
   (temporarily, to actually reach the boundary — the double-append bug
   still blocks the search from completing without it): Q69's `EXPLAIN`
   now returns a real plan — `Hash Anti Join` × 2 (the two `NOT EXISTS`
   arms) feeding a `Hash Join` (the address join) feeding a `Nested Loop`
   against the `store_sales+date_dim` `EXISTS` arm — the FIRST real
   semi/anti-reordered plan shape this entire c-series (c5-c11) has ever
   produced. No panic. This confirms item (a) as specified in §46.4 is
   correct and sufficient to fix the boundary-totality crash.
2. With the temporary `joinInfoList` line reverted back to production
   (`semiAntiJoinInfoList(ctx.joinInfoList, semiAnti)`, i.e. ONLY the
   `relfromjoinlist.go` fill-closure change is live) and a private
   `GOOPG_BIN`: `scripts/tpcds-sf025-regression.sh sweep` — **PASS=96
   MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3** (the 2 permanent
   `SKIP_QUERYGEN` oracle gaps plus nothing else), i.e. byte-identical to
   HEAD's known-good state. This is the expected result: without the
   joinInfoList fix, `joinIsLegal`'s "matches multiple SpecialJoinInfos"
   guard still declines every semiAnti-admitted search before it can ever
   reach a boundary hole, so the new fill-closure branch is inert in
   today's production traffic — a true prerequisite landing, not a
   behavior change, exactly as §46.4's ordering intended.

**Item (b)'s scope was mis-filed as Q10-specific — it is not.** Running Q69
standalone (step 1 above, both fixes applied) did not stop at the boundary
fix: with the panic gone, `psql -f query69.sql` returned a RUNTIME error,
not the EXPLAIN-only success the trace initially suggested:

```
ERROR:  outer column ref c_current_cdemo_sk/level=1 out of range (depth=0)
LINE 15:   cd_demo_sk = c.c_current_cdemo_sk and
```

This is the IDENTICAL error signature §46.3 item 2 filed as a Q10-only
regression (`outer column ref c_current_cdemo_sk/level=1 out of range
(depth=0)`, `internal/executor/expr.go:478`: an `*optimizer.OuterColumnRef`
evaluated with `len(ctx.OuterRows)==0`, i.e. reached OUTSIDE any
`evalSubquery`/`evalInExpr`/`evalExistsExpr` frame that would have pushed an
outer row). Q69's WHERE has no OR-combined EXISTS at all — `cd_demo_sk =
c.c_current_cdemo_sk` is a plain top-level join predicate between `customer`
and `customer_demographics`, unrelated to any of Q69's three chained
(AND-connected) `EXISTS`/`NOT EXISTS` clauses. §46.4 item 2's hypothesis
("PG only flattens a TOP-level AND-connected EXISTS; Q10's OR-combined EXISTS
should have been declined upstream") therefore cannot be the whole story:
Q69's pure AND-chain shape hits the exact same failure once it reaches this
stage, so whatever leaves a stray `OuterColumnRef` unrebased is a general
semiAnti-admission defect, not an OR-admission-specific one. §46.4 item 2 is
re-scoped below rather than closed; the OR-admission question may still be a
CONTRIBUTING factor for Q10 specifically, but it cannot be the fix for Q69's
occurrence of the same error, and Q69 is the cheaper repro (no OR-shape
subtlety to reason about first). Likely suspects for the next loop to
instrument, in probable-cost order: (1) `rebaseSemiAntiChainQual`
(joinsearchseam.go:1713) — does it walk into and rebase every
`*OuterColumnRef` node inside a semiAnti link's `pred`, or only the outer
`Expr` shape it pattern-matches, potentially leaving a nested one untouched
when the predicate also touches an UNRELATED real leaf pairing (Q69's
`cd_demo_sk` join is between two REAL leaves, not the semiAnti RHS — so the
stray `OuterColumnRef` may not even be inside `semiAnti[*].pred` at all, but
somewhere in the ordinary conjunct list `unnestExistsExpr` left half-rebased
when it originally flattened the three `EXISTS` clauses); (2) whether
`unnestExistsExpr`'s original flattening pass ever fully converts the
`customer`-side correlation refs to plain `ColumnRef`s for a MULTI-EXISTS
statement (Q69 has three), or only for the single-EXISTS case every prior
c-series fixture exercised. Not re-attempted this loop — this is genuinely
item (b)'s next diagnostic step, filed here rather than guessed at blind
(`m0074_partial_scope_lessons`: bound the change, verify incrementally).

The temporary `joinInfoList` edit used for step 1 was reverted
(`git checkout -- internal/optimizer/joinsearchseam.go`) before commit,
confirmed via `git diff --stat` showing zero changes to that file; only
`relfromjoinlist.go` is part of this loop's landed commit.

### 46.6 c11 item (b), continued: a real, independently-shipping `SourceTableIdx` collision bug FOUND and FIXED — but it is NOT Q69's runtime-crash cause; the crash persists, re-filed as c12

**Method**: same private-binary SF0.25 technique as §46.1, extended with two
temporary (reverted before commit) instruments: (1) `walkPlanExprsDeep` calls
in `planner.go`'s S5a pre-DP arm, printing every `*OuterColumnRef` node
immediately before and after `runJoinSearchBelowPinned`; (2) a print inside
`unnestExistsExpr` of `srcTableOffset` and the `SourceTableIdx` sets of
`outerChild.Output()` and the remapped `innerPlan.Output()`.

**Finding 1 (root-caused and FIXED): `unnestExistsExpr`'s `srcTableOffset`
under-counts across sibling EXISTS clauses in the same statement.** The
offset that shifts each EXISTS body's own `SourceTableIdx` numbering out of
the outer query's range is computed by scanning `outerChild.Output()`
(unnest.go, the code right after the `M0142-0008e` comment). But
`Output()` for a `Join{Type:Semi/Anti}` deliberately omits its RHS columns
(plan.go's Semi/Anti special case — needed for correctness elsewhere: an
ANTI join's Output() must not carry the anti-joined table's columns). Once
the FIRST of several sibling EXISTS clauses has already spliced its semi/
anti join onto `outerChild`, that first EXISTS body's `SourceTableIdx`
range becomes invisible to an `Output()`-only scan, and the SECOND sibling
EXISTS is handed the exact same offset the first one was — for Q69 (three
chained `EXISTS`/`NOT EXISTS`), live tracing confirmed `store_sales`,
`web_sales` and `catalog_sales` all landed on `SourceTableIdx=5`, and all
three `date_dim` occurrences on `SourceTableIdx=6`. This is a *general*
multi-EXISTS bug, live in production today regardless of the semiAnti DP
search or the still-broken `joinInfoList` double-append (§46.2) —
`unnestExistsExpr` runs for any statement `GOOPG_UNNEST_PREDP` admits,
independent of whether the downstream search ever runs at all.

**The fix**: `maxSourceTableIdxDeep` (unnest.go, added directly above
`unnestExistsExpr`) replaces the `Output()`-only scan. It walks
`outerChild`'s actual node tree — recursing explicitly through
`*Join.Left`/`*Join.Right` and `*Filter.Child` rather than trusting
`Output()` — so a previously-spliced semi/anti RHS is counted even though
it no longer appears in the parent Join's published schema.

**Verification**: `go build ./...` and `go test ./internal/optimizer/...`
clean. Full production `scripts/tpcds-sf025-regression.sh sweep` (private
`GOOPG_BIN`, `joinInfoList` NOT modified — this fix ships independent of
the still-reverted §46.3 one-liner): **PASS=96 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0 SKIP=3**, zero row-count/checksum regressions. The
sweep's own plan-shape channel (non-blocking, informational) shows exactly
3 queries with a textual plan diff — **Q16, Q69, Q94** — and in all three
the ONLY difference is `EXPLAIN` alias disambiguation that was previously
ambiguous or wrong: `cs_ship_customer_sk` → `catalog_sales.cs_ship_customer_sk`,
a bare `date_dim` (used for TWO distinct correlated `date_dim` scans) →
correctly split into `date_dim_1`/`date_dim_2`, `cr_order_number` →
`cr1.cr_order_number`, `wr_order_number` → `wr1.wr_order_number`. This is a
real (if previously silent, since row counts never depended on it) EXPLAIN
correctness bug — two structurally different scans of the same table
rendering under the SAME unqualified name — now fixed, independent of and
prior to Q69's crash.

**Finding 2 (the actual target, NOT fixed): the collision was not Q69's
crash cause.** Re-running Q69 through the private-binary method — STI fix
applied, the §46.3 `joinInfoList` one-liner ALSO applied temporarily to
reach the search — produced a **byte-identical EXPLAIN plan and the
identical runtime error** to §46.5's pre-fix capture: the same `Nested
Loop` whose LEFT child is the `store_sales`+`date_dim` EXISTS arm and whose
RIGHT child is `Index Scan using customer_demographics_pkey ... Index
Cond: (cd_demo_sk = c.c_current_cdemo_sk)`, and the identical `ERROR:
outer column ref c_current_cdemo_sk/level=1 out of range (depth=0)` at
runtime. Since fixing the real `SourceTableIdx` collision changed nothing
about this shape, the collision was a coincidental red herring for this
specific symptom (a genuine bug, independently worth fixing, but not on
this bug's causal path) — refuting §46.5's suspicion #2 from the prior
loop.

**What the EXPLAIN shape actually says, read carefully**: the customer_demographics
index probe is keyed by `c.c_current_cdemo_sk` — an ordinary equi-join
predicate between `customer` (`c`) and `customer_demographics`, wholly
unrelated to any of Q69's three `EXISTS` clauses (confirmed against the
query text, `tmp/m0142-0008a-census/explain-queries/explain_q69.sql` lines
13-15). The DP search converted this into an NLI-shaped path
(`createNestLoopIndexJoinPlan`, `createplannl.go:178-193`'s `outerParamKey`
is the only production call site that turns a `*ColumnRef` into a
`*OuterColumnRef`) — which is the *correct* rewrite in general, but here
`p.Children[0]` (the Path's own "outer"/driving side, `in.outer` in
`createNestLoopIndexJoinPlan`) is the `store_sales`+`date_dim` EXISTS
synthetic leaf, NOT `customer`. `customer` (`c`) is not in that subtree's
relids at all — `c_current_cdemo_sk` cannot resolve there under ANY
coordinate numbering. So the bug is not a coordinate/rebase mistake
downstream of a correctly-chosen pairing; **the search itself built and
won a candidate pairing the predicate cannot legally attach to** — an
eligibility/relids-coverage bug in whatever code path decided
`customer_demographics` could be probed via this index using this
predicate against THIS particular outer grouping. The likely site is the
index-path candidate generator for the join search (the code that, given a
candidate outer relset and an inner leaf, checks whether an available
index-qualifying clause's required relids are a SUBSET of the outer
relset's own relids before emitting a parameterized index path) — if that
subset check is missing, weakened, or keyed off stale bookkeeping for a
semiAnti-chain-adjacent relset, it would explain building an NLI candidate
whose outer side does not actually produce the key column. NOT
instrumented this loop (budget); **filed as c12**. Concrete next step for
c12: instrument the index-path candidate builder (likely in the cost/path
generation code that calls or feeds `createNestLoopIndexJoinPlan`'s
`Path`, upstream of `createPlan` — search for where `RequiredOuter` gets
set on an index `Path` during the DP search, e.g. near
`considerIndexPaths`/the NLI cost-gate machinery referenced by
`GOOPG_NLI_COSTGATE`) and print, for every candidate NLI path it builds
during Q69's search, the candidate's own outer relset alongside the
clause's own required relids — the first candidate where the clause's
required relids are NOT a subset of the outer relset is the bug.

Both temporary instruments (the `planner.go` `OuterColumnRef` walk and the
`unnestExistsExpr` offset/STI print) were reverted before commit; only
`unnest.go`'s `maxSourceTableIdxDeep` addition and offset-computation
change are part of this loop's landed diff. The private binaries, SF0.25
data copy, and logs used this loop (`tmp/goopg-c11b-bin`,
`tmp/goopg-c11b-sweep-bin`, `tmp/c11b-sf025-data`, `tmp/c11b-server.log`,
`tmp/c11b-q69-runtime.sql`) were all removed before this write-up.

## 47. c12 — the index-path-candidate-generator hypothesis is REFUTED; the real defect is downstream of path construction, narrowed to an empty `ctx.OuterRows` at runtime

### 47.1 Method

Same private-binary SF0.25 technique as §46 (`tmp/c12-sf025-data`, a copy of
`bench/tpcds/runtime_goopg/data-sf025`; `tmp/goopg-c12-bin`; port 65491;
started directly, not via the cgroup wrapper, so env vars reach the
process — §46.1's finding). The temporary `joinInfoList:
ctx.joinInfoList` one-liner (§46.3) was re-applied locally throughout to
reach the search, exactly as c9-c12 established. Four rounds of temporary
(all reverted before commit) instrumentation, each disproving the
previous round's leading hypothesis:

1. A print in `addNLIPaths` (joinpathsnli.go) and `addPartialNestLoopPaths`
   (the partial/parallel arm), gated by `GOOPG_C12_NLI_TRACE=1`, printing
   every candidate's `outerRelids`/`innerRelids`/`RequiredOuter`/`req`
   pair — testing c12's own filed hypothesis (a missing relids-subset
   check in the index-path candidate generator).
2. A print in `createNestLoopIndexJoinPlan` (createplannl.go), printing
   `outerPath.Rel.Relids`, `innerPath.RequiredOuter`, and the ACTUAL
   built outer node's `Output()` schema — testing whether the DP search's
   own createPlan phase materialises a Path whose claimed relids diverge
   from what the recursively-built Plan node really publishes.
3. A print in `tryBuildNLI` (nl_index_join.go), the SEPARATE "legacy"
   post-search rewrite pass (`rewriteJoinsToNLI`) that pattern-matches
   plain `*Join` nodes for a profitable NLI conversion with NO relids/
   subset validation at all — testing whether THIS constructor (not the
   DP-search one) builds the crashing node.
4. Reading the executor source for the exact panic site
   (`internal/executor/expr.go:472-478`) to learn what `depth=0` in the
   error text actually means.

### 47.2 Finding: neither `*NestedLoopIndexJoin` constructor ever builds the suspected illegal pairing

Bit-mapping for Q69's 6-relation search, cross-validated against the
`DPPATH` dump's own per-relation `width` annotations (customer=324,
customer_address=388, customer_demographics=148, matching the catalog
column widths): `bit0=customer(c)`, `bit1=customer_address(ca)`,
`bit2=customer_demographics(cd)`, `bit3`/`bit4`/`bit5` = the
`store_sales`/`web_sales`/`catalog_sales` EXISTS synthetic leaves.

Exhaustively enumerating every `addNLIPaths`/`addPartialNestLoopPaths`
candidate during a full, real (crashing) Q69 run: `cd` (bit2) appears as
`inner` in exactly two lines, both with `outer ⊇ {customer}` (outer=
`0x1`=`{c}` and `0x3`=`{c,ca}`) — i.e. every candidate the DP search's own
NLI arms ever considered for `customer_demographics` legitimately
supplies `customer`. The suspected illegal pairing (`outer={store_sales
leaf}` alone, `inner=cd`) **never appears in the trace at all** — c12's
filed hypothesis is refuted by direct, exhaustive evidence, not merely
un-reproduced.

Separately, `createNestLoopIndexJoinPlan` (the DP search's own
create-plan-phase constructor) was invoked exactly twice for `cd` during
one crashing run — once while building the `EXPLAIN`-only plan, once
while building the plan actually executed — and **both calls used
`outerPathRelids=0x3` (`{customer, customer_address}`)**, with the
built outer node's `Output()` schema genuinely containing
`c_current_cdemo_sk` (`SourceTableIdx=1`, i.e. customer). This
construction is provably safe: `c_current_cdemo_sk` is directly
resolvable from the outer row `createNestLoopIndexJoinPlan` actually
built, both times.

`tryBuildNLI`/`rewriteJoinsToNLI` (nl_index_join.go) — the OTHER, and
only other, production constructor of a `*NestedLoopIndexJoin` node, a
post-search textual rewrite pass with no relids validation whatsoever —
**never successfully converts anything for this query** (zero
`C12LEGACYNLI` trace lines across the same crashing run). Ruled out too.

So both known constructors of the node type involved in the crash build
it (when they build it at all) in the ONE safe shape, and the crash still
reproduces 100% of the time. The two `EXPLAIN`-derived hypotheses from
§46.6 (an eligibility gap in the index-path candidate generator; a
coordinate-rebase bug) are both wrong — the defect is not in path
selection or construction.

### 47.3 The `EXPLAIN` text's apparent shape is not reliable evidence of the true tree

§46.6 read `EXPLAIN`'s printed indentation and per-node `width` figures
(a `Nested Loop` combining a `Gather(store_sales+date_dim)` child of
width 848 with an `Index Scan … customer_demographics` child of width
148, total 996) as proof that the executed tree pairs the bare
`store_sales` leaf with `cd`. §47.2's direct instrumentation of the ONLY
two node constructors contradicts that reading. Two explanations remain
open and are NOT distinguished yet: (a) `EXPLAIN`'s cost/width display
for a stamped-after-the-fact `*NestedLoopIndexJoin` node is computed from
a stale/wrong source — `nlipricesplice.go`'s `stampSemiProbePrices`
already documents a related "carrier-unset" display-only seam for
SEMI/ANTI NLI nodes built outside the search, so an analogous
display-only mislabeling for this ORDINARY (non-semi/anti) NLI is
plausible but unconfirmed; or (b) the indentation is accurate and a
THIRD, still-unfound constructor exists. Do not re-trust `EXPLAIN` text
alone as ground truth for this bug without first re-deriving the claim
from a live node-type instrument, as this loop had to.

### 47.4 The sharper clue: `depth=0` means the lateral outer-row stack is EMPTY, not "wrong relation"

`internal/executor/expr.go:472-478` evaluates an `*optimizer.OuterColumnRef`
by `idx := len(ctx.OuterRows) - x.Level; if idx < 0 || idx >= len(ctx.OuterRows) { …
out of range (depth=%d)… }`, where `depth` in the error message is
`len(ctx.OuterRows)`. Q69's crash prints `depth=0` — **the lexical-scope
outer-row stack is completely empty**, not merely missing the right
entry. This reframes the whole search: the bug is not necessarily "the
wrong relation is in scope" (a relids/coordinate bug, the whole line of
inquiry c9-c12 has pursued) but potentially "no lateral outer was pushed
at all" — i.e. an EXECUTION-side dispatch bug, where the specific
`*Join{Lateral:true}` node §47.2 proved is correctly built (outer=
`{customer,customer_address}`, which legitimately carries
`c_current_cdemo_sk`) ends up embedded somewhere in the final tree where
the operator that opens/rescans it does not go through whatever driver
is responsible for pushing `ctx.OuterRows` before evaluating the inner's
key — e.g. if it is reachable through a generic child-opening path
(nested under a `Gather`, or as a non-driving child of some other
operator) that does not honor the `Lateral` flag the way the dedicated
lateral-stream driver does.

### 47.5 c13 — next step (not started this loop)

Instrument at the EXECUTION boundary instead of at plan CONSTRUCTION:
print, every time `ctx.OuterRows` is pushed/popped (the lateral-stream
driver in `internal/executor`, likely near `operators_nljoin.go`'s
`nestedLoopIndexJoinOp` and whatever generic `bindOuter`/`lateralBindable`
machinery `join_lateral_stream_test.go`'s doc comment describes — see
§47.4), alongside a stack of the Go type of every node currently being
opened/rescanned. Reproduce Q69's crash with that instrument live and
read off: (a) is the correct `*Join{Lateral:true}` node (outer=
`{customer,customer_address}`, confirmed correctly built in §47.2) ever
actually opened/rescanned by the executor at all, and if so, (b) does its
own open/rescan push a row onto `ctx.OuterRows` before recursing into its
`Inner` `*IndexScan`, and if not, why not (what operator sits between the
correctly-built lateral join and the probe that fails to relay the
push). If the correct node is confirmed reachable but the push is
skipped, the defect is in the executor's lateral-dispatch predicate
(something like `lateralBindable`/`bindOuter`, per `M0134-0001`'s prior
incident cited in `join_lateral_stream_test.go` — a DIFFERENT but
analogous "wrap type doesn't trigger the lateral push" gap). If the
correct node is NEVER reached at all, the tree the executor actually runs
diverges from what `createPlan` returned, which would point back at a
plan-tree-mutation pass running AFTER `createPlan` (a stamping/splice
pass, `nlipricesplice.go` being the leading candidate given its own
documented "operates on the FINAL tree" scope) that is rewriting or
relocating this specific `*Join{Lateral:true}` node incorrectly. Use the
same private-binary SF0.25 method as §46-47 (temporary
`joinInfoList: ctx.joinInfoList` one-liner reapplied locally, reverted
before commit either way); gate before landing anything with `go build
./...`, `go test ./internal/optimizer/... ./internal/executor/...`, and a
full `scripts/tpcds-sf025-regression.sh sweep` with a private `GOOPG_BIN`.

No production code changed this loop — every instrument listed in §47.1
was reverted before commit (`git diff --stat -- internal/optimizer/`
empty). The private binary, SF0.25 data copy, and server log
(`tmp/goopg-c12-bin`, `tmp/c12-sf025-data`, `tmp/c12-server.log`, and the
`tmp/c12-q69-*` query/output scratch files) were all removed before this
write-up.

## 48. c13 — §47.5's own question ANSWERED, by pointer identity, and it is
NEITHER of the two branches §47.5 predicted; a tentative shared-node fix
was tried and DISPROVED, narrowing the real defect to the join-order
search itself

### 48.1 Method

Same private-binary SF0.25 technique as §46-47 (fresh copy of
`bench/tpcds/runtime_goopg/data-sf025`, private binary, direct start
bypassing the cgroup wrapper, the `joinInfoList: ctx.joinInfoList`
one-liner re-applied locally in `joinsearchseam.go` to reach the search —
all reverted before commit). Five rounds of temporary (all reverted)
instrumentation, each answering exactly the question §47.5 posed and then
one more it did not anticipate:

1. `debug.PrintStack()` at the `expr.go:472` `OuterColumnRef` panic site
   (gated) — got the exact Go call stack for the live crash instead of
   guessing from the error text.
2. A print in `joinOp.Open` (`operators_join_agg.go`) of `o.plan`'s
   pointer, `Type`/`Algo`/`Lateral`, and `Right`'s Go type + pointer.
3. A print in `createNestLoopPlan` (`createplannl.go`) at BOTH of its
   dispatch branches (the NLI arm and the "plain nested loop" arm),
   showing which one a given call took and the child Path's
   kind/relids/`RequiredOuter`.
4. A print of the built `*Join`'s own pointer, `.Lateral`, and its
   `Right`'s pointer, right where `createNestLoopIndexJoinPlan`'s
   decomposed arm constructs it (`j := &Join{...}`, createplannl.go:374).
5. A print at the TOP of `createPlanNodeUnpriced` (`createplan.go`), for
   EVERY Path translated during the run, showing the Path's own pointer,
   `Kind`, `relids`, `RequiredOuter`, and (on exit) the produced Node's
   pointer and Go type — a complete, ordered ledger of every
   Path→Node translation `createPlan` performs for one query.

### 48.2 §47.5's own two predictions are BOTH wrong

§47.5 framed the resume point as a binary choice: (a) the correctly-built
`*Join{Lateral:true}` node is never reached by the executor at all
(implicating a post-`createPlan` mutation pass, `nlipricesplice.go` named
as the leading suspect), or (b) it is reached but its own
`bindOuter`/lateral-dispatch fails to push `ctx.OuterRows` (an
M0134-0001-style gap). The stack trace from instrument 1 refutes both in
one shot:

```
expr.go:481                          (OuterColumnRef panic)
operators_index.go:968               (indexScanOp.lookupKey)
operators_index.go:496               (Rescan)
operators_index.go:324               (BindOuter call site)
join_nl_stream.go:104                (openNestedLoop: o.right.Open)
operators_join_agg.go:428            (joinOp.Open → openNestedLoop(ctx,true), the UNIVERSAL FALLBACK)
join_nl_stream.go:101                (openNestedLoop: o.left.Open, ONE LEVEL UP)
operators_join_agg.go:428            (a SECOND joinOp.Open → same fallback)
```

The crash is inside `join_nl_stream.go`'s `openNestedLoop` — the
MATERIALIZE-AND-REPLAY driver for an ordinary (non-lateral) nested loop
— not inside `join_lateral_stream.go`'s `openLateral` at all.
`openNestedLoop` never touches `ctx.OuterRows` (confirmed by grep: zero
references in that file), and it opens its `Right` child exactly ONCE,
then replays a materialized cache per outer row — a driver that is
*structurally* incompatible with a parameterized inner, independent of
whether `ctx.OuterRows` gets pushed. Instrument 2 confirmed the specific
node: `type=Inner algo=NestedLoop lateral=FALSE rightType=*optimizer.IndexScan
rightKeyType=*optimizer.OuterColumnRef` — i.e. a `*Join` with `Lateral`
at its Go zero value wraps an `*IndexScan` whose `Key` is exactly the
node type only `outerParamKey` (createplannl.go, ONE call site,
confirmed by grep) ever produces. `nlipricesplice.go` never constructs or
mutates a `*optimizer.Join` at all (only `*NestedLoopIndexJoin` — grep
confirmed) — §47.3's suspicion of it was unfounded for this bug.

### 48.3 The real question: how does a `Lateral:true`-shaped inner end up wrapped by a `Lateral:false` `*Join`

Instruments 3-5 pinned this by POINTER IDENTITY, run twice (two
independent process runs, two independently-ASLR'd address spaces, same
result both times — ruling out a Go GC address-reuse coincidence):

- `createNestLoopPlan` is called exactly 3 times for Q69's crashing run.
  Exactly ONE call takes the NLI branch: `Children[1]` is a fresh
  `PathIndexScan` (`RequiredOuter=0x1`, the customer relation),
  `Children[0]`'s relids are `0x3` (`{customer, customer_address}`).
  This call's `createNestLoopIndexJoinPlan` builds `is := &IndexScan{...}`
  FRESH (confirmed by reading `createIndexScanPlan`: `is := &IndexScan{...}`
  is a bare struct literal every call, never a cache lookup) and wraps it
  correctly: `j := &Join{... Lateral: true, Right: is ...}`. This `j` is
  the ONLY place in the whole call graph that ever constructs a `*Join`
  wrapping an `OuterColumnRef`-keyed `*IndexScan` with `Lateral: true` —
  and it is provably correct.
- One of the other two `createNestLoopPlan` calls takes the "plain nested
  loop" branch with `Children[1].Kind == PathPrebuilt` (`RequiredOuter=0`,
  relids `0x4` — customer_demographics ALONE, not the `{c,ca,cd}` trio).
  Per instrument 5, THIS Path's `createPlanNodeUnpriced` call hits the
  `case PathPrebuilt: return p.node, ...` shortcut and returns `p.node`
  **byte-identical, by pointer, to the SAME `is`** the NLI branch built
  moments earlier (both runs: exact address match). `PathPrebuilt.node`
  is set exactly once, at Path construction (`newPrebuiltPath`, grepped —
  the only site that ever assigns the private `node` field; no setter
  method exists), so this Path was built by wrapping the ALREADY-BUILT,
  OUTER-PARAMETERIZED `is` as if it were a plain, standalone leaf for
  customer_demographics — the exact opposite of what an `OuterColumnRef`
  key requires.
- This "plain" `*Join` (customer_demographics-alone ⋈ one of the
  store_sales/web_sales/catalog_sales EXISTS leaves, via a NestedLoop with
  **no join predicate connecting them at all** — Q69 has no relationship
  between `customer_demographics` and any of the three EXISTS subqueries)
  is not a discarded cost-comparison candidate: it is embedded in the
  FINAL, EXECUTED tree (confirmed: its address matches `C13JOINOPEN`'s
  crash-adjacent line exactly). Separately, `customer` and
  `customer_address` (bits 0/1) reappear LATER in the tree, joined to the
  *other* two EXISTS leaves — i.e. **the winning plan splits
  `customer_demographics` away from `customer`/`customer_address`
  entirely**, which is already wrong independent of the `Lateral` bug:
  `cd`'s only predicate (`cd_demo_sk = c.c_current_cdemo_sk`) requires
  `customer`, and pairing it with an EXISTS leaf instead is a
  join-LEGALITY defect, not merely a lost-flag defect.

### 48.4 A clone-before-mutate fix was tried and did NOT stop the sharing — ruling out the leading theory

The natural read of §48.3 is "shared mutable node, in-place `is.Key`
mutation corrupts a cache" — `createNestLoopIndexJoinPlan`'s own comment
even asserts the (now-falsified) invariant: *"The probe rebuilt by
`createIndexScanPlan` is a fresh node this arm owns... so setting Cond
here cannot disturb the leaf the search still references by pointer."*
This loop tried the direct fix: shallow-clone `is` (`isCopy := *is; is =
&isCopy`) before any field mutation, in both `createNestLoopIndexJoinPlan`
and its `createNestLoopIndexJoinPlanFused` sibling (same pattern, same
risk). Rebuilt, re-ran the identical live repro: **Q69 still crashes,
identically**, and instrument 5's pointer ledger shows the "plain"
`PathPrebuilt(relids=0x4)` now resolves to the CLONE's address, not the
pre-clone original — i.e. the PathPrebuilt genuinely captures a reference
to whatever `createNestLoopIndexJoinPlan` returns, clone or not; nothing
about `is` being freshly allocated stops a SEPARATE piece of code from
independently deciding to treat that returned reference as a reusable
plain leaf. Mutation was never the mechanism — REFERENCE SHARING is, and
cloning the mutated object doesn't touch the sharing. The fix was
reverted in full (`git diff --stat -- internal/optimizer/ internal/executor/`
empty, confirmed).

### 48.5 Where the real defect must live — narrowed but not pinned

`PathPrebuilt.node` for the customer_demographics-alone Path is set at
Path-construction time, before `createPlan` ever runs on the winning
tree — meaning some part of the SEARCH itself (not `createPlan`) already
called into `createNestLoopIndexJoinPlan`'s machinery once, during path
exploration/costing, and fed the result into `newPrebuiltPath` for
relids `0x4`. `newPrebuiltPath`'s only OTHER call site relevant here is
`joinsearch.go:434`, inside `buildInitialRels` — which builds it from
`scans[i]`, an ALREADY-BUILT-Node input parameter, once per relation, at
DP-search bootstrap (`rel.baseLeaf = leaf` right above it, same
function). `scans[]` itself is populated by whatever assembles the
FROM-item leaf list before the search runs — for Q69's flat
`FROM customer c, customer_address, customer_demographics` (no explicit
join syntax), this is almost certainly `joinsearchseam.go`'s semiAnti
adapter (`extractSearchLeaves`/`leaves[i] = scans[i]` for `i < nprefix`,
§46-47's whole subject), which is the one piece of code in this whole
call graph already known (c9-c12) to build SYNTHETIC structure around the
real FROM-item leaves for this exact query shape. The exact site that
captures `is`(customer_demographics' outer-parameterized probe) into
`scans[]`'s plain per-relation slot is NOT YET FOUND — §48.1's
instrument 5 proves WHERE the corrupted reference surfaces
(`PathPrebuilt` at Path-construction time) but not WHO writes it there,
since that write happens before the traced `createPlanNodeUnpriced` walk
begins.

A second, independent defect is now visible alongside the sharing bug
(§48.3's last bullet): the winning tree pairs `customer_demographics`
with an EXISTS leaf it has NO predicate relationship to, while
`customer`/`customer_address` end up elsewhither in the same tree — a
join-order/legality bug, not merely a lost-Lateral-flag bug. Fixing the
`Lateral` propagation alone (even if the sharing site were found and
fixed) would not by itself make this pairing sensible; both need
resolving, and it is not yet established whether they share one root
cause or are two independent defects that happen to compound on the same
query.

### 48.6 c14 — next step

Instrument `joinsearchseam.go`'s leaf-assembly path (`extractSearchLeaves`,
the `scans`/`leaves` construction around lines 624-717 per §46-47's prior
reading) to print, for EVERY entry `i < nprefix` (the REAL FROM items,
not the synthetic semiAnti leaves), the Go pointer and type of
`scans[i]` at the moment it is captured into `leaves[i]`/handed to
`buildInitialRels`. Cross-reference against §48's `is` pointer (rebuild
with `debug.PrintStack()` still armed, or a simpler one-line print,
gated by a fresh env var) to catch the EXACT write that puts the
outer-parameterized probe into the plain per-relation leaf slot for
customer_demographics. Once found, the fix is almost certainly "don't let
this adapter build/capture a parameterized (`RequiredOuter != 0`) access
path as a relation's plain per-item leaf" — a decline-gate, not a
clone. Separately, and possibly as the SAME fix: instrument whichever
code decides the winning tree's overall relation grouping (the DP level
that chose `{customer_demographics} × {one EXISTS leaf}` as a pairing) to
learn why a pair with NO connecting predicate and no shared relset
lineage was ever considered legal — compare against `joinIsLegal`
(joinsearchlevel.go, c9's own fix target) to see whether this is the
SAME class of legality-check gap semiAnti leaves already needed one
fix for (c9), just not yet extended to cover this shape. Same
private-binary SF0.25 method as §46-48 (temporary `joinInfoList:
ctx.joinInfoList` one-liner, reverted either way). Gate before landing
anything: `go build ./...`, `go test ./internal/optimizer/...
./internal/executor/...`, and a full `scripts/tpcds-sf025-regression.sh
sweep` with a private `GOOPG_BIN`.

No production code changed this loop — the clone fix (§48.4) was fully
reverted after live-testing disproved it (`git diff --stat --
internal/optimizer/ internal/executor/` empty, confirmed). Private
binary/data/logs (`tmp/goopg-c13-bin`, `tmp/c13-sf025-data`,
`tmp/c13-server.log`, `tmp/c13-q69-*`) removed before this write-up.

## 49. c14 landed — root cause was a mirroring gap between `chainCarriesLateral`
and `extractSearchLeaves`, and it explains BOTH of §48's defects with ONE fix
(2026-09-17)

### 49.1 Method

Read, not traced first: `buildInitialRels` (`joinsearch.go:367-443`) showed
`newPrebuiltPath`'s only non-test call site relevant here builds `p :=
newPrebuiltPath(rel, leaf)` straight from `scans[i]`, populated one relation
at a time by `extractSearchLeaves` (§48.5's own finding). Reading
`extractSearchLeaves` itself (`joinsearchseam.go:1242-1504`) rather than
re-instrumenting it found the mechanism directly: its Semi/Anti arm (the
`admitSemiAnti` branch landed by b1/b2) calls `walk(j.Left, preserved)` —
i.e. it DOES decompose a Semi/Anti join's `Left` subtree looking for further
reorderable structure, while `j.Right` is always appended as ONE opaque
leaf, never decomposed. `predp.go`'s Phase A/Phase B split
(`runJoinSearchBelowPinned`, §46.1 already named this file) runs Phase A's
own `tryJoinSearch` call (line 148) on the subtree BELOW the whole pinned
Semi/Anti spine — `origChain`, `{customer, customer_address,
customer_demographics}` for Q69 — splices its winning, ALREADY-`createPlan`'d
tree back into the spine's innermost `Left` (`put(newTarget)`, line 157),
and only THEN does Phase B (`tryPGShapedJoinSearch(spineJoins[0], ...)`,
line 179) walk the WHOLE spine — including straight through Phase A's
now-materialized result, sitting at the bottom of the spine's `Left` chain.

`createNestLoopIndexJoinPlan` (`createplannl.go:208-375`, the constructor
§48.3 already traced) returns its decomposed-lateral shape as a plain
`j := &Join{Type: jtNLI, Lateral: true, Right: is, ...}` — an ordinary
`*Join` struct with NO marker distinguishing "already planned by createPlan"
from "still-raw AST awaiting a search". `jtNLI` is an ordinary
`JoinTypeInner` for Q69's shape, so when Phase B's `extractSearchLeaves`
walk reaches this node it is indistinguishable, by the `isJoin &&
j.Type ∈ {Cross,Inner,Left,Right}` gate, from any other reorderable inner
join — it gets DECOMPOSED, `walk(j.Left, ...)` and `walk(j.Right, ...)` run
independently, and `j.Right` (== `is`, the outer-parameterized `*IndexScan`
built moments earlier by Phase A) is appended to `scans[]` as its OWN plain
leaf — exactly the corrupted `PathPrebuilt` §48.3 pinned by pointer
identity. `j.Left` (`customer` alone, or `customer` already merged with
`customer_address` depending on Phase A's own winning shape) becomes a
SEPARATE leaf too — which is §48.3's second defect: `customer_demographics`
ends up split away from `customer`/`customer_address` because THIS is the
walk step that splits them, decomposing a join Phase A had already legally
and correctly decided.

The guard that should have caught this and declined the whole Phase B
search — `chainCarriesLateral` (`joinsearchseam.go`, checked at
`tryPGShapedJoinSearch` line 328, right after `extractSearchLeaves` runs) —
has its own doc comment claiming it "descends exactly the links
`extractSearchLeaves` flattens" (C-04a/b, written when that meant Cross/
Inner/Left/Right only). It was never extended when b1/b2 added the
Semi/Anti `walk(j.Left, ...)` arm to `extractSearchLeaves`: for a
`*Join{Type: Semi|Anti}` node, `chainCarriesLateral`'s type-switch falls
through to `nodeReferencesOuter` → `planHasOuterRef` →
`planHasEscapingOuterRef`, which asks a DIFFERENT, finer-grained question —
"does an `OuterColumnRef` here escape UNBOUND" — and correctly answers NO
for `is.Key`, since its `OuterColumnRef` is properly bound by its own
immediate `Lateral: true` parent (that function's own `*Join` case tracks
binder depth exactly for this reason). `chainCarriesLateral`'s coarse "ANY
Lateral reachable = decline" rule — the one that actually matches what
`extractSearchLeaves` is about to do — never got a chance to see the nested
Lateral join at all, because nothing walked down through the Semi/Anti node
to reach it.

### 49.2 The fix

One function, `chainCarriesLateral` (`joinsearchseam.go`): added a
Semi/Anti arm that descends `j.Left` with the SAME coarse rule the
Cross/Inner/Left/Right arm already applies, mirroring
`extractSearchLeaves`'s own Semi/Anti walk exactly — `j.Right` is
deliberately excluded from the new recursion because the walk it mirrors
never decomposes `j.Right` either (it is always appended as one opaque
leaf), so a Lateral join buried inside it is never at risk of being split
regardless.

### 49.3 Live verification — both of §48's defects, ONE fix, no other query moved

Private binary + a private copy of the SF0.25 data dir (`tmp/c14-bin`,
`tmp/c14-sf025-data`, same method as §46.1, run directly — no cgroup
wrapper — with `GOMEMLIMIT=4GiB` set by hand). Q69, which crashed on every
prior c9-c13 loop's HEAD, now runs clean (100 rows, no panic). `EXPLAIN`
shows the corrected shape: `customer` hash-joined to `customer_address`,
THEN nested-loop-indexed into `customer_demographics` via `Index Scan using
customer_demographics_pkey ... Index Cond: (cd_demo_sk =
c.c_current_cdemo_sk)` — i.e. `customer_demographics` is back where its own
correlation predicate requires it, resolving §48.5's open question (whether
the Lateral-loss crash and the illegal `{customer_demographics} × {one
EXISTS leaf}` pairing were one root cause or two): they were ONE. Fixing
the mirroring gap fixed both, because both were downstream of the SAME
decomposition step.

`go build ./...` clean. `go test ./internal/optimizer/...` and
`./internal/executor/...` both PASS (the latter is the sibling-path check —
`chainCarriesLateral` has no executor-side twin, but the practice card's
gate list names it explicitly). Two new direct unit tests pin the fix
(`chaincarrieslateral_test.go`):
`TestChainCarriesLateralDescendsSemiAntiLeft` (a Lateral join under a
Semi/Anti's `Left` must be found, both `JoinTypeSemi` and `JoinTypeAnti`)
and `TestChainCarriesLateralAdmitsNonLateralUnderSemiAnti` (a non-lateral
chain under a Semi join's `Left` must NOT be declined — guards against an
over-broad fix that declines every Semi/Anti-rooted search). A full
`scripts/tpcds-sf025-regression.sh sweep` with `GOOPG_BIN=tmp/c14-bin`:
`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3` (the 3 skips are
the pre-existing dsqgen-artifact exclusions, Q36/Q70/Q86), and the
plan-shape channel reports `queries=99 same=99 changed=0` against the prior
commit — i.e. Q69 went from crash to PASS and NO other query's plan moved.
Private binary/data/log (`tmp/c14-bin`, `tmp/c14-sf025-data`,
`tmp/c14-server.log`) removed after verification.

### 49.4 What's still open

`predp.go:159-176`'s Phase B doc comment (carried pending since c11, "`used`
is therefore false on every production call today") was NOT touched this
loop — it is orthogonal to this fix and, if anything, this fix likely makes
Phase B decline MORE often (any spine whose `Left` contains an NLI-index
probe now correctly triggers the `lateral` decline), so the claim needs
re-verification rather than a same-direction edit; left for a future loop
to re-check rather than guessed at here. c9's `joinIsLegal` legality-check
question from §48.6 (whether a zero-predicate pairing needed its own fix in
that function) turned out to be moot for Q69 specifically — the pairing
never reaches `joinIsLegal` after this fix, because the Lateral join is no
longer decomposed into separate leaves in the first place — but a
DIFFERENT query that legitimately reaches `joinIsLegal` with a genuine
zero-predicate pair is not ruled out by this loop and was not probed.
