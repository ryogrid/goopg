# M0145-0004 — UNION ALL subqueries as appendrel leaves

Status: landed (partial-path hoist arm) / gates in §"Measurement".
**`tlist_same_datatypes` landed 2026-09-22** — see the final section; it also
records an over-refusal the measurement caught before it shipped.
Task: `.ralph/fix_plan.md` M0145-0004. Parent: M0145-0001 (IR contract),
M0145-0002 (harness), M0144-0003b-1 (the `setOpBranchTag` carry this
slice consumes). Kind: impl.

## What this is

The jointree pipeline's second divergence point. In PG,
`pull_up_simple_union_all` (prepjointree.c:1617) flattens a FROM-clause
`(... UNION ALL ...) alias` into an **appendrel**: the subquery's
`RangeTblRef` stays a single leaf in the parent jointree with
`rte->inh = true`, the member leaf queries are copied into the parent's
rtable as `AppendRelInfo` children, and `add_paths_to_append_rel`
(allpaths.c:1321) files the leaf's pathlist from the members' pathlists —
including the parallel `Append` that TPC-DS Q5's
`Parallel Hash Join → Parallel Append(ParSeqScan store_sales, ParSeqScan
store_returns)` shape is built from.

goopg does not give members parent-level rtable entries — the union's
members are planned inside the leaf's own nested scope and stay there.
What this slice lifts across the boundary is the thing PG's appendrel
exists to produce: **the partial UNION ALL path**, re-targeted onto the
parent leaf rel so the outer join search can place it the way it places
any base rel's partial path.

Concretely: the nested scope's `createSetOpPaths` already runs
`addPartialSetOpPath` for a non-top-level UNION ALL (`topLevel` reads
`ParallelStatementOK`, false inside every subquery scope) and already
files the pure and mixed Parallel Append arms on its SETOP rel —
`setOpRel.PartialPathlist` holds them. M0144-0003b-1's `setOpBranchTag`
already carries that SETOP rel out on the node the leaf is built from.
The missing step is one translation: file copies of those partial paths
with `Rel` re-targeted from the nested SETOP rel to the parent leaf rel.
The parent search then sees a leaf offering a parallel-aware emission —
`try_partial_hashjoin_path` can build `Parallel Hash Join` over it, where
today the leaf can only be a serial prebuilt (opaque `*SetOp`) or carry
a nested `Gather` that a join cannot use as a partial input.

## Oracle anchors

`is_simple_union_all` (prepjointree.c:2214) ported as
`subqueryChainIsSimpleUnionAll` (internal/optimizer/jointreeappendrel.go):

| PG check | goopg gate |
|---|---|
| `subquery->setOperations` exists | `rv.Subquery.SetOp != nil` |
| no `sortClause` | `len(s.OrderBy) == 0` — the parser's trailer lift already moved an unparenthesised member's ORDER BY onto the chain head, so this one check covers `A UNION ALL B ORDER BY 1` |
| no `limitOffset`/`limitCount` | `s.Offset == nil && s.Limit == nil` |
| no `rowMarks` | `len(s.Locking) == 0` |
| no `cteList` | `s.With == nil` |
| every setop node `SETOP_UNION && all` | chain walk: `cur.SetOp.Type == parser.SetOpUnion && cur.SetOp.All` |
| `is_simple_union_all_recurse` over the setop tree | parenthesised compound members (`right.Parenthesized && right.SetOp != nil`) recurse into the member's own chain; `SetOpOperand` grouping nodes decline |
| `tlist_same_datatypes` per leaf | **not ported** — goopg's bound AST carries no types and the produced node's member schemas are post-cast; see §"Deferrals" |

`pull_up_simple_union_all`'s two member-side properties that this slice
does NOT reproduce (both ledgered):

- Member RTEs become parent rtable entries, so
  `distribute_qual_to_rels` can push parent quals into member children
  and each member's rel gets independent path generation. goopg's member
  plans are finished Nodes inside the leaf's nested scope; parent quals
  cannot reach them (same boundary the leaf always had).
- `is_safe_append_member` pulls a single-RTE member further up; goopg
  members stay unitary inside the leaf.

The appendrel is ONE leaf in PG too — members are `AppendRelInfo`
children under the leaf's relid set, not join siblings — so "one leaf
with a lifted append path" is the structurally faithful piece to land
first; member-level entries belong with the fuller IR work (M0145-0005).

## The mechanism

Five pieces, each fail-closed:

1. **Mark** — `planSubqueryRangeVar` sets `rangeBinding.appendrel`
   when `jointreePipeline && lateralCtx == nil &&
   subqueryChainIsSimpleUnionAll(rv.Subquery)`, and sets
   `ps.appendrelMember` on the settings handed to the subquery's
   planning scope. The binding flag rides through `planFromClause`
   offset-shifting and the seam's `estimateBaseRelInfo` into
   `baseRelInfo.appendrel`. The legacy arm never sets either, so the
   whole slice is inert off-knob by construction — the same
   fail-closed shape `jtPullup` uses.

   LATERAL refuses: PG propagates `rte->lateral` to the pulled-up
   members because a member may cross-reference earlier FROM items;
   a hoisted one-shot partial path cannot express per-outer-row
   re-evaluation.

2. **Member-scope search forcing** — the discovered blocker. A member
   like `SELECT a, v FROM store_sales` is a simple single-table
   statement: `isSimpleSingle` bypasses join search entirely, so the
   member produced no searched rel, no `PartialPathlist`, and the
   SETOP rel's `LeftBranchRel`/`RightBranchRel` read nil — the whole
   downstream mechanism had nothing to hoist. Two bounded changes fix
   it without touching any other scope:

   - `PlannerSettings.appendrelMember` (unexported) is set only on
     the settings object passed into the marked subquery's scope;
     `planSelectImpl` reads it once, clears it, and (a) skips the
     `isSimpleSingle` rule-bypass arms, (b) joins the WHERE-less
     `joinTreeHasOuterLink` arm so a filterless member still reaches
     `tryJoinSearch`, and (c) stamps `ctx.appendrelMember` on the
     scope's resolveContext. Nested subqueries inside a member do
     not inherit it — the flag is consumed at exactly one level.
   - `tryPGShapedJoinSearch` lowers its one-relation floor from
     `minSearchRels()` (=2 without `GOOPG_ONEREL_SEARCH`) to 1 when
     `ctx.appendrelMember` — at BOTH checks (the `nrels` gate and
     the post-spine `nprefix` gate). Upstream has no such floor at
     all: `set_base_rel_pathlists` runs for every base rel
     (allpaths.c:221) before `make_rel_from_joinlist` declines to
     search a one-item joinlist. The member then gets a searched
     rel with base-rel parallel eligibility and a partial path —
     exactly what `setOpBranchRelOf` needs to find.

   `is_safe_append_member` is NOT needed in this model: PG's
   restriction exists because members are promoted to parent rtable
   entries whose quals get distributed; goopg's members stay inside
   the leaf, so a member WHERE qual rides its own `Filter` through
   `spliceBranchEmission` — partitioned per worker, semantically
   exact.

3. **Leaf parallel safety** — `setBaseRelConsiderParallel` for a
   marked leaf inherits `setOpRel.ConsiderParallel` (the AND of the
   member rels' flags, computed inside the member scope) instead of
   `relConsiderParallel`'s rtekind verdict, which reads the `*SetOp`
   leaf as opaque `other` and fails closed. This is the same content
   PG's `is_parallel_safe` produces by walking the subquery's
   jointree — member safety IS the appendrel's safety.

4. **Carry** — already landed (M0144-0003b-1): the leaf node IS the
   `*SetOp` or `*Gather`/`*GatherMerge` `createSetOpPaths` returned,
   stamped with its SETOP rel; `setOpBranchRelNode` reads it.

5. **Hoist** — `searchCtx.addAppendRelPartialPaths()` runs in
   `searchOneProblem` immediately after `addBaseRelPartialPaths` (the
   `create_plain_partial_paths` slot, allpaths.c:768-790 ordering). For
   each `relInfos[i].appendrel` leaf it requires:

   - `rel.ConsiderParallel` — the leaf's own `relConsiderParallel`
     verdict (`subtreeHasNoParallelHazard` over the union subtree);
   - the leaf ROOT to be the carrier — `leaf.(setOpBranchRelNode)`
     with a non-nil rel. Any wrapper (`*Filter` leaf-local quals,
     `*Sort`, `*Limit`, `*Project`, `*LockRows`) declines: the hoisted
     emission would drop a row-changing wrapper. PG refuses the same
     shapes at `is_simple_union_all` — a `*Gather` top is admitted
     deliberately, since it is the union's own parallel wrapper and the
     hoisted emission is the partial SetOp BELOW it, not the Gather;
   - then each `setOpRel.PartialPathlist` path is copied with
     `Rel` re-targeted to the leaf rel and filed through
     `addPartialPath` — whose own `ParallelSafe && ConsiderParallel`
     gate is the last refusal. `Children`, `Cost`, `Rows`,
     `ParallelWorkers`, `ParallelAware`, `DisabledNodes` carry over
     unchanged: the path is the same Parallel Append candidate,
     just owned by the leaf relid. `createSetOpPlan` lowers it
     unchanged — `PathSetOp` + member picks + `spliceBranchEmission`
     boundary repair were built for exactly this emission contract.

The serial side is deliberately untouched: the leaf's prebuilt path
(costSubplanLeaf over the finished union node) stays the serial
candidate, matching PG where the appendrel's non-partial Append is just
the member total paths under it — same shape, already landed.

## Downstream: sizing and execution the hoist needs

Two latent gaps had to close for the hoisted candidate to be selectable
AND correct — both in shared (non-pipeline-gated) machinery, so the fix
improves every SetOp-bearing plan, not just marked leaves:

- **Worker sizing through `*SetOp`** — `drivingScan` resolves a SetOp
  driving node (M0140-0006c), but `scanTable` cannot map it to a
  relation, so `computeParallelWorkers` (post-pass) and
  `upperSplitWorkers` (partial-agg producer) returned 0 workers —
  `gate=workers` refusals that kept `Aggregate → SetOp` plans serial
  in BOTH arms. New `drivingScans` (plural) expands a SetOp driving
  node into its streamed member scans — recursively for left-deep
  chains, skipping claimed-whole branches — and both sizers take the
  max over branches, matching `create_append_path`'s
  `parallel_workers = max(child workers)`. `drivingScanCrossesSort`
  gained the mirrored `*SetOp` arm (sibling agreement).
- **Executor claims through a join probe** — the hoist lets a
  `PathSetOp` sit as a join's probe input
  (`Gather → HashJoin → probe SetOp`), a nesting M0140-0006c never
  produced (it only placed setOp at/near the subtree root, or joins
  INSIDE branches). `attachAll`'s `unwrapToSetOp` gained `*joinOp`
  (probe side per plan algo, mirroring `attachParallelScan`'s gates)
  and `*nestedLoopIndexJoinOp` (outer) arms — without it the member
  scans stayed unclaimed and every worker returned the whole union —
  measured: 80/120/200 rows for a 40-row join at workers=1/2/4,
  exactly (workers+1)× — caught by the new
  `TestGatherOverJoinProbeSetOpIdentity`, which passes at 1/2/4
  workers post-fix.
- **Join-input layout** — found by the knob-arm corpus capture:
  `createSetOpPlan` returned a nil `outputLayout` for every caller
  ("no caller above the seam reads one"), which was true until the
  hoist made a `PathSetOp` a legal join input — `joinInputsFor`
  panics on a nil child layout (`EXPLAIN Q5` crashed the backend on
  the knob arm). It now returns `baseRelLayout(p.Rel, out)`: nil for
  upper-rel SETOP paths as before (`baseLeaf == nil`), and the
  leaf's contiguous `[baseOffset, baseOffset+width)` for a hoisted
  path — exact because the emitted `*SetOp` is the leaf's own node
  schema-for-schema.

## Refusal matrix (each returns the leaf to the exact legacy shape)

| case | gate |
|---|---|
| knob off / legacy arm | `jointreePipeline` at mark time — flags never set |
| LATERAL union subquery | `lateralCtx == nil` requirement |
| `ORDER BY` / `LIMIT` / `OFFSET` / `FOR UPDATE` / `WITH` on the union | `subqueryChainIsSimpleUnionAll` |
| `UNION` (dedup) / `INTERSECT` / `EXCEPT` anywhere in the chain | `unionAllChainLinks` (`PartialPathlist` is also only ever populated for streaming UNION ALL — `addPartialSetOpPath`'s `setOpStreams` entry gate — so the node alone would refuse; the AST check makes the intent legible) |
| grouping-node member (`SetOpOperand`) | decline (conservative) |
| member keeps simple-single bypass / member floor stays 2 | member scope only — `plannerSet.appendrelMember` + `ctx.appendrelMember` bound it to exactly one planning level |
| member partial paths unavailable | empty `PartialPathlist` → nothing filed |
| leaf wrapped (`Filter`/`Sort`/`Limit`/`Project`/`LockRows` above the carrier) | direct-carrier check at hoist time |
| members not parallel-safe | `setOpRel.ConsiderParallel` = AND of member rels → leaf inherits 0 → `addPartialPath` gate refuses |
| leaf not parallel-safe / rel not `ConsiderParallel` | `addPartialPath` gate |
| hoisted SetOp under a join probe with unsafe shape | `unwrapToSetOp` join gates mirror `attachParallelScan` — unsupported shapes fail closed (no claim wiring, no partial admit) |
| CTE-inlined union (`WITH x AS (A UNION ALL B) ... FROM x`) | `CTEScan` wrapper hides the carrier → decline (ledger) |
| datatype-coerced members | not detected — ledger |

## Coordinate story

No new coordinates: the leaf is one binding occupying one leaf span,
exactly as today. The hoisted path emits the leaf's own schema
(`inner.Output()` — the union's resolved member-1 schema), which is
what the leaf's serial prebuilt emits; `baseOffset`/span rules are
unaffected. The members' internal layouts never escape the nested
scope — the lifted path's `Children` build the member emissions under
`spliceBranchEmission`, which re-applies each member's boundary
wrappers, so member output schema is the leaf schema by construction.

## Why a hoist and not member-level rtable entries

PG's members live in the parent rtable because parent quals, join
clauses and path generation all address them. goopg's seam consumes
finished leaf Nodes; reproducing member-level entries means rebasing
member Var/ColumnRef spaces into the parent scope, distributing
baserestrict clauses across members, and giving each member its own
search — the M0145-0005 single-pass-DP machinery's job. The hoist
captures the plan-shape payload (Parallel Append under the parent
join) with a five-line owner change and zero coordinate surgery;
everything it cannot express is ledgered rather than approximated.

## Measurement

Witnesses: TPC-DS Q5 (`store_sales UNION ALL store_returns` and two
sibling unions as FROM-clause derived tables — PG: `Parallel Hash Join
→ Parallel Append`), plus the other UNION-ALL-in-FROM corpus queries the
nightly census names (Q2/Q14/Q71/Q76 use CTE-wrapped unions — those
decline this slice; Q5's derived-table form is the landing witness).

- `scripts/jointree-parity-capture.sh` A/B, SF0.25: expect the knob arm
  to diverge from the off arm exactly on queries whose leaves are
  admissible UNION ALL subqueries with parallel-eligible members.
- Value gates are unchanged in kind: the lifted path emits the same
  rows the serial leaf emits (Append = concatenation); SF0.25 sweep
  and the TPC-H spotcheck guard the emission contract.

## Deferrals (ledgered at commit)

- Member rtable entries / parent-qual distribution into members
  (`distribute_qual_to_rels`, allpaths.c `set_append_rel_size`) —
  needs member rels in the parent search; M0145-0005 territory.
- `tlist_same_datatypes` — datatype-coerced unions hoist where PG
  refuses; goopg needs bound member tlists pre-cast to detect it.
- `is_safe_append_member` is inapplicable, not deferred — members are
  not promoted to the parent rtable (mechanism §2), so member WHERE
  quals stay member-local and ride `spliceBranchEmission` per worker.
  The pull-up side of that function (member FROM-list items becoming
  parent entries) IS deferred: LATERAL propagation; CTE-wrapped union
  leaves (`CTEScan` boundary); serial-side member-path competition
  (the leaf's serial path stays prebuilt-over-nested-winner).


---

# `tlist_same_datatypes`: the last gate of `is_simple_union_all` (2026-09-22)

The deferral list said this half was "NOT ported: goopg's parser AST carries no
resolved types at the point this runs and the produced node's member schemas
are post-cast". Both halves of that are true, and together they name where the
answer has to be taken.

## Upstream

`is_simple_union_all_recurse` (`prepjointree.c:2258`) ends each leaf with
`tlist_same_datatypes(subquery->targetList, colTypes, true)`
(`tlist.c:257`): every member's output types must equal the top-level
`colTypes`, position for position. Differing column counts are false too.
Typmods and collations are deliberately not compared — upstream's own note is
*"currently no callers care about comparing typmods"*.

## Where goopg can ask it

Not at MARK time: `subqueryChainIsSimpleUnionAll` runs on the parser AST,
which carries no resolved types. Not after planning either: `setOpUnifyBranches`
(M0145-0004's sibling work on `select_common_type`) coerces both branches, so
by the time a schema exists every branch agrees **by construction** and the
comparison is a tautology.

The one moment both facts are false is inside the fold, immediately before the
coercion. `SetOp.TlistTypesDiffer` records the verdict there and
`addAppendRelPartialPaths` consults it — upstream refuses the flattening
outright; refusing the hoist is that refusal at the granularity this seam has,
and it leaves the exact legacy leaf.

Comparing each link's two branches is equivalent to upstream's
member-vs-`colTypes` comparison for a chain: `colTypes` is the first member's
types, so if every adjacent pair agrees all members agree with the first, and
a single disagreeing pair is a member that differs. A nested link that already
answered false propagates, which is the recursion's `&&` over `larg`/`rarg`.

The field's polarity is deliberate: the zero value means *no known
difference*, so the partition/inheritance fan-outs — which build
`*SetOp{All: true}` as PG APPENDRELS rather than set operations and never set
it — keep their behaviour exactly.

## The over-refusal the measurement caught

The first version compared `Type.Name` directly. That is not what upstream
compares: `exprType()` returns OIDs, where `decimal` **is** `numeric` (1700)
and `int` is `int4`. goopg keeps the spelling the statement used.

TPC-DS Q5's union mixes a table column with `cast(0 as decimal(7,2))`, so the
name comparison reported a difference that does not exist and refused a union
PostgreSQL flattens — **removing the `Parallel Append` shape this very task had
landed.** The knob-arm capture named it precisely: with the naive comparison,
Q5 was the one query that moved; with `ArgTypeDisplayAlias` folding both
spellings first, the only remaining differences are Q36/Q70/Q86, which are the
capture's three pre-existing parse errors whose text embeds the temp filename.

`catalog.ArgTypeDisplayAlias` is the repository's single alias table (its own
doc argues for exactly one, "no second alias source to drift"). Using a
DISPLAY mapping for identity is sound in this direction — distinct types never
share a display name — but it is not an OID, so an OID-level question (domains
over the same base type) is outside it. That error direction is the safe one:
it can only answer "same", i.e. keep a union flattenable, never refuse one PG
would allow.

## Verification

- Three levels, each with its own non-vacuity check: the predicate
  (`TestSetOpBranchTypesDifferMatchesUpstreamComparison`, including the alias
  and typmod pins), the gate (`TestAppendRelHoistRefusesTypeMismatchedUnion`,
  whose matched-types control is load-bearing — without it the change would be
  indistinguishable from "never hoist"), and the WIRING
  (`TestPlannedSetOpCarriesTlistVerdict`, which plans real SQL). The wiring
  test exists because neutralising the capture site left the other two green:
  each end was pinned, the connection between them was not.
- Default arm unchanged, as the design's "legacy is inert by construction"
  requires: SF0.25 sweep `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
  plan channel `same=99 changed=0`; floors held exactly (TPC-DS SF0.25
  `match=2` Q9+Q41, TPC-H parallel `match=1` Q6) with `CATEGORIES-EXCL-MATCH`
  identical to the previous default-arm capture.
- tpch-spotcheck Q12=2/Q13=33; TPC-H acceptance arm 24 MATCH; pgbench smoke.

Artefacts: `tmp/m0145-0004-tlist/` (knob arm at HEAD, knob arm with the naive
comparison, knob arm with the alias fix, and both default-arm floor captures).


---

# The "CTE-wrapped union leaves (Q2/Q14/Q71/Q76)" residual is mis-described (2026-09-22)

The deferral list records the remaining Parallel Append sites as *"CTE-wrapped
union leaves (`CTEScan` hides the carrier — Q2/Q14/Q71/Q76's shapes)"*. Measured
against the corpus, that description does not hold for three of the four, and
the mechanism it names is not what blocks them.

## What the queries actually are

| query | union sits in | CTE refcount |
|---|---|---|
| Q2 | a CTE `wscs` | **1** — single-ref, so PG inlines it and `pull_up_simple_union_all` does apply |
| Q14 | the main body / `x` subquery; its CTEs `cross_items`, `avg_sales` are referenced **6×** each | 6 — PG does **not** inline them, so there is no appendrel to make |
| Q71 | a plain `FROM (… UNION ALL … ) tmp` | **no CTE at all** |
| Q76 | a plain `FROM (… UNION ALL … ) x` | **no CTE at all** |

Q71 and Q76 are exactly the FROM-clause shape this task's mark already
handles, so "CTEScan hides the carrier" cannot be their blocker.

## What the plans show

goopg knob arm vs PG 18.3, SF0.25:

```
Q71  goopg:  Append { Gather { Append { Parallel Seq Scan … } }, … }
     PG:     Gather { Parallel Append { Parallel Hash Join …, … } }

Q76  goopg:  Append { Gather { Parallel Seq Scan … }, Gather { … } }
     PG:     Gather Merge { Parallel Append { Parallel Hash Join …, … } }
```

The difference is not a missing Append — goopg builds one. It is **where the
Gather sits**: goopg gathers inside each member and appends the gathered
results, where PG keeps the members partial and puts ONE Gather above a
parallel-aware Append.

## And the appendrel machinery is not what decides it

Default arm vs knob arm, same capture run: **Q71 and Q76 are byte-identical**
in their Append/Gather structure, and Q2 differs only in cost. So for these
queries the hoist either does not fire or fires without changing the shape —
either way the gather-per-member shape is decided somewhere else.

## Consequence

The residual is re-stated in M0145-0004's own task body with the accurate
description — the gap is gather placement over union members, not CTE carrier
visibility. It is NOT a separate task: filing one was refused by the lineage
guard (root M0145-0001's last five completed descendants all read
`Movement: none`), and the guard's remedy — mark the root `[!]` — would
contradict the owner's 2026-09-22 re-open of exactly this chain. That conflict
is escalated in the task body; it blocks filing ANY new work under this root. Q14 is
removed from the population outright (its union CTEs are 6-referenced, so
upstream does not flatten them either) and Q2 is kept as the one genuine
single-reference CTE case.

No production file was touched: the correct next step depends on which
mechanism holds the Gather down, and this measurement does not yet say.


## Narrowing it further (2026-09-22, loop #85)

The previous section named three candidates for what holds the Gather down —
`addBaseRelGatherPaths`, `upperSplitWorkers`, the member scope's own search
root. **All three are refuted**, and the actual shape is more specific.

### The hoist fires, and its paths are accepted

`GOOPG_PGSHAPED_DP_TRACE=1` over the SF0.25 corpus, knob arm:

```
baserel.appendrel.partial   4 paths, every one verdict=accepted
upper.setop.append.partial  11
upper.setop.append.mixed     3
```

So the mark works, partial SetOp paths exist, and the hoist re-targets them
onto leaf rels successfully. Nothing here is failing to fire.

### Q71's plan says what is actually wrong: the chain stays NESTED

Q71's union has THREE members. goopg produces

```
Append                                              <- outer link, SERIAL
  ├─ Gather { Append { ws member, cs member } }     <- inner link: parallel, hoisted
  └─ Gather { Parallel Hash Join (store_sales) }    <- third member, gathered alone
```

against PG's single `Gather { Parallel Append { …3 members… } }`.

The inner link of the right-leaning chain — `SetOp(A, SetOp(B, C))`'s inner
`SetOp(B, C)` — gets the parallel Append and the hoist. The OUTER link does
not, so its two inputs are each gathered and then appended serially. PG has no
such split because `is_simple_union_all_recurse` walks `larg` **and** `rarg`
and flattens the whole chain into ONE appendrel, where
`add_paths_to_append_rel` builds one partial Append over all three members.

**The divergence is chain flattening depth, not gather placement.** The gather
placement is a symptom of the nesting.

### A stale comment that would have misdirected the fix

`addPartialSetOpPath`'s comment lists *"a nested `*SetOp`"* among the wrappers
that disqualify a branch from offering its partial path. The code below it does
not agree: `setOpBranchPartialChainOK` admits a nested set operation through
its carrier check (`setOpBranchRelNode` with a non-nil rel), which M0144-0003b-1
added precisely so a chain link could reach the previous link's SETOP rel. The
prose predates that and is wrong; a fix guided by it would have gone to the
wrong place.

### The one measurement that is still missing

Why the OUTER link produces no winning partial path. `addPartialSetOpPath`
returns early unless `setOpRel.LeftBranchRel` and `.RightBranchRel` are both
non-nil and both `ConsiderParallel`; for the outer link the right input is the
inner link's SETOP rel, so the question is whether that field is populated for
a nested-`*SetOp` input. Instrument those two fields at the outer link before
changing anything — do not infer it from the shape.

Risk to carry into that fix: admitting a nested streaming Append under one
Gather means two levels of block claim cooperating across workers. This task
has already been bitten there once — an unclaimed `PathSetOp` under a partial
hash join made every worker replay the whole union (80/120/200 rows at
workers=1/2/4 for a 40-row join). Any change here needs the same per-worker
row-identity pin.
