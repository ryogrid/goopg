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
