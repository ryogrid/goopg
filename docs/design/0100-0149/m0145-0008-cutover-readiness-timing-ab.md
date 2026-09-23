# Cutover readiness: the arm-vs-arm timing A/B (M0145-0008)

Status: measurement landed 2026-09-21; BOTH named blockers are now FIXED the
same day — the semijoin one (see "The fix, measured") and Q17 (see "Q17 FIXED").
Q17's attribution was also CORRECTED in the process: the defect is route-borne,
not arm-borne, and is reachable on the DEFAULT arm.
Task: `.ralph/fix_plan.md` M0145-0008. Parent: M0145-0007 (whose NLI census
raised the question). Kind: recon.

## Why this measurement exists

M0145-0007's census established that the jointree pipeline hands pulled-up
semijoins to the search, that the search files an NLI path for them
(`gate=filed`), and that `add_path` then out-costs it. The remaining question
was whether that cost preference is RIGHT — and the ledger recorded that
settling it needs runtime, not another plan census.

Every gate this milestone runs compares VALUES, and the two arms are
value-identical (acceptance arm 24/24 on both). A cost misjudgement is
therefore invisible to every gate in the harness. Runtime is the only channel
that can see it.

## Method

Two `scripts/tpch-acceptance-arm.sh` runs, TPC-H SF1, serial, per-query cap
600s, each on its own fresh memory-capped server — the arm's own design, which
holds server age at zero for both (the "sweep-tail collapse" hazard in
CLAUDE.md). The ONLY difference between them is `GOOPG_JOINTREE_PIPELINE`:

```
engine-id     d71748906848b967c1fdf1fe611581228e569e40 …  (identical)
engine-binary on-disk=3e61809f585fd51c                    (identical)
host load     1.86 / 2.02                                 (comparable)
```

## Result: the pipeline is timing-neutral except on sublink queries

| query | default | knob | ratio |
|---|---|---|---|
| **Q4** | 0.37s | 12.98s | **35.1x** |
| **Q20** | 0.13s | 3.58s | **27.5x** |
| **Q17** | 0.38s | 7.61s | **20.0x** |
| **Q21** | 2.63s | 18.94s | **7.2x** |
| Q22 | 0.51s | 0.43s | 0.8x |
| every other label (19 of them) | — | — | 1.0x (±10%) |
| **TOTAL** | 62.43s | 102.46s | 1.64x |

Nineteen of twenty-four labels land inside ±10%, so the jointree pipeline
itself costs nothing measurable. The entire 1.64x total is four queries, and
all four are sublink queries:

- **Q4, Q20, Q21** are the semijoins M0145-0007 traced: the pull-up hands them
  to the search, the search files an NLI path and out-costs it, and the plan it
  prefers instead runs 7-35x slower. That answers the ledgered cost question:
  the preference is not merely unvalidated, it is wrong on every one of the
  three queries where it fires.
- **Q17** is a correlated SCALAR subquery, which the pull-up does not touch —
  PG does not convert `EXPR_SUBLINK` either. Its 20x is therefore a DIFFERENT
  mechanism and must not be folded into the semijoin story; it is recorded here
  because the same A/B surfaced it, and it is unattributed.
- **Q22** is a `NOT EXISTS` that does not regress, which is the useful control:
  being a sublink query is not sufficient to regress.

## Attribution: Q4's semijoin DISAPPEARS on the knob arm (2026-09-21)

The obvious next step was "log the filed NLI cost against the winning path's
cost and see which term is wrong". `noteSemiJoinrelPaths` (nlicensus.go) dumps
every path filed for a semi/anti joinrel with its kind and cost, once per
`addPathsToJoinrel`, so the winner is the minimum total and the NLI's margin of
loss is readable. What it found is that the premise was wrong again.

Q4 alone, same binary, same clone, only the knob differing:

| | default (1.49s) | knob (16.00s) |
|---|---|---|
| `SEMICOST` (semi joinrel candidates) | one path: `kind=nli total=502253.16` | **no semi joinrel at all** |
| `NLIGATE` | `gate=filed` | none |
| `NLICENSUS` (node built) | `route=rewrite jointype=semi probe=idx_lineitem_orderkey_fkidx` | **none** |
| `SUBLINKCENSUS` (route taken) | — | **neither route fired** |

So on the knob arm Q4's `EXISTS` never becomes a semijoin at all. It is not
out-costed — no semi joinrel is ever built, no NLI path is ever filed, and
neither the jointree pull-up nor the legacy pinned-spine route runs. The
correlated subquery stays a per-row subplan, which is the 10x.

Note also what the default-arm dump shows: the semi joinrel there has exactly
ONE filed path, the NLI. So even on the default arm the NLI is not competing
against a hash semijoin and losing — it wins its own joinrel uncontested.

**This retires the "add_path out-costs the NLI" reading** that the previous two
loops recorded (including this document's own first section). The A/B numbers
stand; the mechanism behind Q4's share of them does not.

### CORRECTION (same day): both routes DO fire — the joinrel never forms

The section above reported that neither sublink route fired for Q4 on the knob
arm. That was read off a run which also had `GOOPG_PGSHAPED_DP_TRACE=1` set,
and it was wrong. Re-running Q4 on the knob arm without the trace flag, twice,
gives:

```
PULLUPCENSUS decline=(pulled)
SUBLINKCENSUS route=jointree-pullup
SUBLINKCENSUS route=pinned-spine
```

The pull-up fires and the `EXISTS` **is** pulled up. What does not happen is
anything after that: widening `noteSemiJoinrelPaths` from semi/anti to EVERY
join type produced **zero** lines for Q4, so `addPathsToJoinrel` is never
reached at all — the search forms no joinrel of any kind. The DPPATH trace
agrees: it shows `relids={0}`, a single-relation problem.

So the pulled body's leaves never enter the search, the `EXISTS` stays a
per-row subplan, and Q4 runs 16.0s against the default arm's 1.5s.

This is the failure mode M0145-0003's own notes predicted: `pulled` marks the
conjunct, which SUPPRESSES the legacy pre-DP arm for that scope, and if the
seam then declines the pulled bodies there is no semijoin left from either
route. The note put it exactly: "without `exprListHasLocalAndLevel1Ref`,
`pulled` would mark bodies the seam declines anyway and wrongly suppress the
pre-DP arm".

### What was ruled out

- **The NOT NULL reduction wired into the generic WHERE arm** (M0145-0005, same
  day) is NOT involved: disabling its block and re-running Q4 on the knob arm
  gives 17.78s against 16.03s with it — no routing change, no timing change.
- **The cost comparison** is not involved either: no joinrel is formed, so
  nothing is costed and nothing is out-costed.

### ROOT CAUSE (same day): a body-local qual can never be "consumed"

The seam trace names the class, and a refined census names the refusal inside
it:

```
PULLUPCENSUS     decline=(pulled)                      the EXISTS is pulled up
PULLUPCLASSIFY   refusal=body-qual-not-consumable      classifyPulledQuals refuses
seam-decline     reason=pullup-classify nrels=1 nleaves=2
```

Q4's `EXISTS` body is
`… FROM lineitem WHERE l_orderkey = o_orderkey AND l_commitdate < l_receiptdate`.
`classifyPulledQuals` sorts each rebased body qual into three buckets:
spanning (becomes the link predicate), RHS-only, emitting-only. The correlation
clause is spanning and fine. `l_commitdate < l_receiptdate` is RHS-only, and
that branch requires `searchConsumes(rebased, spans)` — which asks whether
`buildRestrictInfos` yields this exact clause.

It never can. `buildRestrictInfos`' `add` closure drops any clause with
`relLevel(relids) < 2` (joinrestrict.go): the restrictInfo list holds JOIN
clauses only, by design — base restrictions live elsewhere. So a
**single-relation body qual fails `searchConsumes` structurally**, and
`classifyPulledQuals` refuses the whole body.

That is the defect: the RHS-only branch validates a BASE restriction with a
JOIN-clause test. PG has no such step — `distribute_qual_to_rels` puts a
single-rel qual on that rel's `baserestrictinfo`
(`postgres/src/backend/optimizer/plan/initsplan.c`) and the pull-up proceeds.

The consequence is the whole 10x: `pulled` has already suppressed the legacy
pre-DP route for the scope, so when the seam refuses, **no route produces a
semijoin at all** and the `EXISTS` runs as a per-row subplan. Any pulled body
whose WHERE carries a body-local qual — an extremely common shape — is
affected.

### The fix, and why it was not in the diagnosis loop

The RHS-only branch must PLACE the qual as a base restriction on the pulled
leaf instead of demanding it be a join clause. The pulled leaves come from
`flattenPulledBodyTree` as bare `*SeqScan`s with the body's WHERE held
separately in `pb.quals`, so placing it means attaching the qual to its leaf
(or threading a base-restriction list the search consumes) — a change in the
pulled-leaf construction path, on the knob arm only. It belongs to M0145-0003,
it is ledgered with that resume point, and it wants its own loop and its own
knob-arm sweep rather than being appended to a diagnosis.

## The fix, measured (2026-09-21)

The refusal is now conditioned on the qual's rel count, which is what decides
which of the two downstream placement mechanisms owns it:

```go
if relLevel(rs) >= 2 && !searchConsumes(rebased, spans) {
        notePullupClassify("body-join-qual-not-consumable")
        return false
}
*searchQuals = append(*searchQuals, rebased)
```

- **two or more rels** — the qual is a JOIN clause, `buildRestrictInfos` files
  it as a restrictInfo, and `searchConsumes` is the right test. Unchanged.
- **exactly one rel** — the qual is a BASE restriction. It rides the conjunct
  pool into `partitionConjunctsForJoinPlanning`, which runs on this very pool
  immediately after `classifyPulledQuals` returns (joinsearchseam.go:856 then
  :862) and routes a single-rel conjunct to `locals.byBinding[leaf]`; the
  seam's pulled-leaf loop then wraps the leaf in a `LeafLocal *Filter` and
  prices it through `estimateBaseRelInfo`. Nothing further was needed — the
  placement machinery already existed and the refusal was the only thing
  standing in front of it.

So the fix is a one-condition change, not the pulled-leaf-construction change
the ledger's resume point anticipated: the leaves did not need the qual
attached to them, because the seam attaches it.

### Result: the semijoin blocker is gone

Full 22-query knob-arm run, same harness, same pinned binary and engine-id as
the A/B above:

| query | default | knob before | knob after | knob-after vs default |
|---|---|---|---|---|
| **Q4** | 0.37s | 12.98s | **1.02s** | 2.8x |
| **Q21** | 2.63s | 18.94s | **2.12s** | 0.8x |
| Q20 | 0.13s | 3.58s | within 0.4s of before | — |
| Q17 | 0.38s | 7.61s | 8.52s | **22.4x (untouched)** |
| **TOTAL** | 62.43s | 102.46s | **77.37s** | 1.24x |

Q4 is 12.7x faster on the arm and Q21 8.9x; Q21 is now marginally FASTER than
the default arm. Values are unchanged (rows 5 / 412 / 101 / 7 on Q4/Q21/Q20/Q22).

A caution worth recording for whoever re-measures: an earlier `QUERIES=4,20,21,22`
subset run read Q20 at 5.59s and Q22 at 1.01s and looked like a regression on
both. The full run shows neither moves. Per-query timings are not comparable
across runs of different length — a 4-query run warms the cache far less than a
22-query one — so the arm must be run whole to be compared.

### What the residual 1.24x is

14.94s of gap remains, and **8.14s of it is Q17 alone** — the correlated
SCALAR subquery, a different mechanism that the pull-up does not touch and
which this fix does not address. The other 21 labels account for the rest
inside run-to-run noise. Q17 is therefore the single remaining named blocker.

### Sibling-path audit

`searchConsumes` has one structural sibling with the same shape: the legacy
arm's body-qual gate (`joinsearchseam.go:819`), whose own comment says "a
body-local restriction **or** an intra-RHS join clause" while the code requires
`searchConsumes` for both. It was **measured, not assumed**: planning
`EXISTS (SELECT 1 FROM i WHERE j = k AND v < j)`, its `NOT EXISTS` twin, and a
constant-compare variant on the DEFAULT arm all decorrelate to a semi/anti
join, so that gate does not refuse this class — the legacy arm reaches its
semijoin by a route that never presents the body-local qual to :819. No sibling
fix is owed.

## What this means for the cutover

M0145-0008 flips `GOOPG_JOINTREE_PIPELINE` to on. Flipping it today makes TPC-H
1.64x slower in total and Q4 35x slower, with every value gate still green.
That is a hard blocker, and unlike the two blockers M0145-0007 retired, this one
is measured rather than inferred.

Two prerequisites, both filed:

1. ~~the semijoin NLI cost comparison~~ — ANSWERED below, and differently than
   this section first framed it: for Q4 the semijoin is never built on the knob
   arm, so there is no cost comparison to correct. The open question is why
   neither sublink route fires;
2. Q17's 20x, mechanism unknown, scalar-subquery route.

## Note for whoever re-runs this

The comparison is only meaningful with the binary and engine-id pinned, as
above — the arm prints both, and a run whose `engine-binary` differs from its
counterpart is measuring two trees, not two arms.


## Q17 ATTRIBUTED (2026-09-21): the probe-cheap guard is fed an un-optimized body

The previous note recorded Q17's 20x as "a DIFFERENT mechanism … unattributed",
and the baton's working guess was that the arms should be plan-identical for a
shape neither sublink route rewrites. **Both were wrong.** The arms diverge,
and they diverge inside the correlated scalar subquery itself.

### The measurement

One private clone (`tmp/goopg-spotcheck-tpch-data`), one capped server per arm,
serial (`max_parallel_workers_per_gather = 0`), only `GOOPG_JOINTREE_PIPELINE`
differing. Both arms return the identical value `310077.312857142857`.

| | default (**1021 ms**) | knob (**11155 ms**) |
|---|---|---|
| top shape | `Nested Loop`, `Filter: l_quantity < (SubPlan 1)` | `Hash Join`, `Filter: l_quantity < (0.2 * avg)` |
| the sublink | correlated `SubPlan 1`, bitmap probe per outer row | **decorrelated** into `HashAggregate` over a full `Seq Scan` of lineitem (6,001,988 rows) |
| estimated cost | **32301** | **224656** |

Note the costs: the knob arm elects a plan its OWN model prices at 7x the
default arm's. The cheap correlated plan is therefore not out-costed — it is
never generated. That is the same failure class as Q4, reached by a different
road.

### Which arm is right: PG keeps the SubPlan

PG 18.3 on the same data plans Q17 as `Hash Join` with
`Join Filter: (l_quantity < (SubPlan 1))` and `SubPlan 1` = `Aggregate` over a
`Bitmap Heap Scan` on `lineitem_part_supp_fkidx`. PG does not convert
`EXPR_SUBLINK` at all. So the **default arm is PG-faithful and the knob arm
diverges** — the regression is a fidelity defect as well as a timing one.

### Root cause: the guard is intact, its INPUT is not

`canUnnestSubquery` (`internal/optimizer/unnest.go`) carries the S6/D6.2
selectivity-aware policy whose own comment cites this very query — "decorrelating
a scalar whose inner plan is already an index-probe shape is a LOSS … Q17
58.27 s -> 86.65 s". It enforces that with `innerPlanIsIndexProbeCheap(sub.Plan)`.

Instrumenting the guard (temporary probe, both arms, same statement):

```
jt=0  shape=Project(Aggregate(BitmapHeapScan(BitmapIndexScan)))  probeCheap=true   -> refuses
jt=1  shape=Project(Aggregate(Filter(SeqScan)))                  probeCheap=false  -> unnests
```

The guard is correct and unchanged; the two arms hand it **different plans for
the same subquery body**. On the jointree arm the body arrives with the
correlation predicate still sitting in a `Filter` over a `SeqScan` — it has not
been turned into an index probe — so a shape test that means "is this body
already cheap to rescan?" answers "no" about a body that is demonstrably cheap
to rescan (the default arm proves the index probe exists for it).

This is the structural lesson of M0145-0003 repeating in a second place: **a
decision taken on a provisional body shape is wrong whenever the provisional
shape is not the final one.** `innerPlanIsIndexProbeCheap` is a shape predicate
being used as a cost proxy, and it is only sound once index selection has run
on the body.

### The fix is NOT in this loop, deliberately

`unnest.go` is shared by BOTH arms — it is the default (shipping) pipeline's
code. Any change to the guard (for instance, deciding probe-cheapness from
whether the correlation qual is index-supported in the catalog, rather than
from whether the node is already an `*IndexScan`) changes a predicate the
default arm consults for every correlated scalar in the corpus. That earns its
own loop and its own full value gates, not an append to an attribution.

Two candidate directions, in preference order:

1. **Make the knob arm hand the guard the same body the default arm does** —
   i.e. run the body's index selection before the unnest decision on the
   jointree route. This leaves the shared guard untouched and is therefore the
   lower-blast-radius option; it is also the one that makes the arms agree by
   construction rather than by a second heuristic.
2. **Make the guard independent of the body's current shape** — ask the catalog
   whether the correlation column is indexed. Higher fidelity, but it changes
   default-arm behaviour and needs the full TPC-H/TPC-DS value + timing set.

Direction 1 is recommended: it is the arm-local fix, and the M0145-0003
precedent is that the placement/selection machinery already exists and the
divergence is in what reaches it.


## Q17 FIXED (2026-09-21) — and the attribution above was too narrow

The section above attributed Q17 to `canUnnestSubquery`'s guard being fed an
un-optimized body, and proposed an "arm-local" fix. The first half is right.
The second half was **wrong about ownership**, and the correction matters more
than the fix.

### The body is BORN without its probe

Instrumenting two points — where the body is planned (`planSubqueryExpr`, right
after `planSelectWithParent`) and where the guard reads it — shows no drift
between them:

```
jt=0  bodyPlanned Project(Aggregate(BitmapHeapScan(BitmapIndexScan)))
      atGuard     Project(Aggregate(BitmapHeapScan(BitmapIndexScan)))  probeCheap=true
jt=1  bodyPlanned Project(Aggregate(Filter(SeqScan)))
      atGuard     Project(Aggregate(Filter(SeqScan)))                  probeCheap=false
```

So nothing degrades the body between planning and the guard. The body is *born*
without its index path on the jointree arm. That relocates the defect from the
unnest pass to body planning.

### It is NOT the jointree pipeline — it is the one-relation search route

`planSelectImpl` has a rule-based bypass for single-relation scopes, gated on

```go
isSimpleSingle && !oneRelSearchEnabled() && !appendrelMember && !jointree
```

and that bypass is the **only** producer of an index path driven by a
correlated (outer-reference) restriction: it calls `planIndexScanFromWhere`.
Three routes skip it. `jointree` is only one of them — and the skip is
deliberate and PG-faithful (`make_one_rel` runs `set_base_rel_pathlists`
unconditionally, so a single-FROM-item statement should route through the
search). What is missing is that the search's base-rel pathlist has no
equivalent producer, so the scope comes out as a bare `Filter{SeqScan}`.

The decisive test: run the **DEFAULT** arm with `GOOPG_ONEREL_SEARCH=on`.

```
default, bypass            Filter: l_quantity < (SubPlan 1)   1021 ms
default, ONEREL_SEARCH=on  HashAggregate over full Seq Scan  10625 ms   <- reproduced
jointree                   HashAggregate over full Seq Scan  11155 ms
```

The defect is **route-borne, not arm-borne**, and it is reachable today on the
shipping arm behind a documented flag. Calling it a jointree-pipeline
regression would have filed it against the wrong component and left the
default-arm exposure unrecorded.

This is the same family as the ledgered
`c07-single-rel-never-reaches-ordered-index-producer`: the one-relation search
route lacks producers the bypass has.

### The fix

At the end of the generic arm, for a single-relation scope that skipped the
bypass, offer the bypass's producer:

```go
if isSimpleSingle && (jointree || oneRelSearchEnabled()) &&
    whereQual != nil && planIsBareSeqScanTree(node) {
    onlyFrom := len(s.From) == 1 && s.From[0].Only
    whereForIndex := injectLikeRangePredicates(whereQual)
    if idxNode, ok, err := planIndexScanFromWhere(whereForIndex, ctx, cat, !onlyFrom); err != nil {
        return nil, err
    } else if ok {
        node = idxNode
    }
}
```

Two properties make this safe to land on a route the default arm can take:

- **Strictly narrower than the bypass it restores.** It fires only when the
  search elected no index path at all (`planIsBareSeqScanTree`), so it can
  never displace a costed index choice — it only fills the hole where this
  route produces none. The bypass, by contrast, runs for every
  `isSimpleSingle` scope.
- **`planIsBareSeqScanTree` is fail-closed.** It admits only a `*SeqScan` under
  recognised, index-neutral wrappers; any shape it does not recognise makes the
  producer stand down. An unreadable tree counts as "not a hole".

`appendrelMember` — the third route that skips the bypass — is deliberately NOT
included, and is ledgered. M0145-0004 forces member scopes through the search
for its own reasons and no witness was measured there; widening the gate on an
unmeasured route is how a narrow fix becomes an unattributable plan change.

### Result

All three routes now produce PG 18.3's shape (`Filter: l_quantity < (SubPlan 1)`
over the bitmap probe) with identical values (`310077.312857142857`):

| route | before | after |
|---|---|---|
| jointree | 11155 ms | **881 ms** |
| default + `GOOPG_ONEREL_SEARCH=on` | 10625 ms | **700 ms** |
| default, bypass (untouched control) | 1021 ms | 700 ms — same plan, run-to-run variance |

Pin: `TestOneRelIndexProducerKeepsCorrelatedScalarProbe` drives all three routes
over a miniature of Q17's shape and asserts the correlated scalar SURVIVES as a
subquery expression. It fails on both affected routes without the fix
(`Project(Filter(Join{algo:Hash}))` — the decorrelated tree) and passes on the
bypass control either way, so it pins the defect rather than the code.

## Q20 FIXED (2026-09-23, M0145-0027): the second producer was shut out

The executor-capability inventory (`m0145-0008-executor-capability-inventory.md`)
found Q20 still 29x slower on the knob arm (0.13 s → 3.80 s), hidden inside the
1.06x total: the knob arm decorrelated `ps_availqty > (SELECT 0.5 * sum(…) FROM
lineitem WHERE l_partkey = ps_partkey AND l_suppkey = ps_suppkey AND l_shipdate
…)` into a whole-lineitem `HashAggregate`, while PG 18.3 and the default arm keep
the SubPlan with an index probe (PG never converts `EXPR_SUBLINK` —
`./postgres/src/backend/optimizer/prep/prepjointree.c:652`).

### Measured, not guessed

The inventory's guess — "the scalar sits inside a pulled-up `IN` body, so it is
planned on a route the Q17 fix does not cover" — was WRONG. A throwaway probe
(`canUnnestSubquery`'s input shape, the Q17 rule's inputs, the bypass producer's
verdict, and the scan-input pass's before/after, on both arms) showed:

| step | default arm | knob arm |
|---|---|---|
| rule-based producer `planIndexScanFromWhere` on the 4-conjunct WHERE | **declines** (bypass) | **declines** (Q17 rule fires, same verdict) |
| tree entering `rewriteScanInputsWithSingleTablePredicates` | `Filter{corr,corr,range,range}(SeqScan)` | `Filter{corr,corr}(Filter_searched,leafLocal{range,range}(SeqScan))` |
| that pass's output | **`Filter(IndexScan)`** — probe on `l_partkey` | unchanged |
| body seen by `canUnnestSubquery` | `Aggregate(Filter(IndexScan))` → probe-cheap → SubPlan kept | `Aggregate(Filter(Filter(SeqScan)))` → decorrelated |

So the bypass arm's correlated probe never came from the rule-based producer at
all — for a multi-conjunct WHERE it comes from the SECOND producer, the
scan-input pass, which absorbs the equality out of `Filter{SeqScan}`. On the
jointree/one-rel routes the one-relation search puts the constant quals into a
SEARCHED leaf Filter and holds the correlated ones above it, because
`conjunctIsLocalEligible` (`local_filters.go`) refuses any conjunct holding an
`OuterColumnRef` as a leaf qual; the scan-input pass returns at `isSearchedTree`
(P5.9-b), so the equality is never absorbed. Q17 escaped this only because its
WHERE is the one equality the rule-based producer reads.

### Fix

`flattenCorrelatedSeqScanFilters` (planner.go), called from the same M0145-0008
restoring rule when its producer declines: a chain of Filters directly over one
SeqScan (no Project, no second relation — all predicates already address the
SeqScan's output, so no rebase) that carries a correlated conjunct is merged into
the ONE unsearched `Filter{SeqScan}` the bypass builds, and the scan-input pass
then does exactly what it does on the default arm. Fail-closed: no correlated
conjunct, a single Filter, or any other node in the chain → the search's
election stands. Gated like the rule (`jointree || GOOPG_ONEREL_SEARCH`), so the
default arm is untouched.

### Result

| | before | after |
|---|---|---|
| knob Q20 plan | decorrelated `HashAggregate` over `Seq Scan on lineitem` | `SubPlan 1` = `Aggregate` over `Index Scan using lineitem_part_supp_fkidx`, `Index Cond: (l_partkey = partsupp.ps_partkey)` |
| knob Q20 time (full 22-query arm) | 3.80 s | **0.18 s** (default 0.15 s) |
| knob/default TPC-H total | 1.06x | **1.01x** |

Values 24/24 identical. Blast radius: TPC-H knob capture — only Q20 changed;
TPC-DS SF0.25 knob capture — only Q41 changed (its correlated `item` body now
prints ONE Filter holding the correlation and the constant OR tree, as PG
prints it; `qual-placement` left Q41's divergence list). Default arm: the SF0.25
sweep's plan channel `same=99 changed=0`. Gates: units, tpch-spotcheck,
acceptance arm, SF0.25 sweep, fire set (25 fires, SF0.25+SF1) — all PASS.

Residual PG divergence (ledgered): PG plans the correlation as an index qual of
a PARAMETERISED base-rel path — the outer reference is a `PARAM_EXEC`
(`./postgres/src/backend/optimizer/util/paramassign.c:121` `replace_outer_var`),
which `match_clause_to_indexcol`
(`./postgres/src/backend/optimizer/path/indxpath.c:2712`) accepts as a
pseudo-constant (`is_pseudo_constant_for_index`, `indxpath.c:4596`), so it
probes the composite index on BOTH columns. goopg's search refuses outer
references as leaf quals, so both arms still reach the probe through a rule
(one column, `l_suppkey` as a filter). Pin:
`TestOneRelIndexProducerKeepsMultiConjunctCorrelatedProbe` fails on the
jointree and one-rel routes with the fix disabled.
