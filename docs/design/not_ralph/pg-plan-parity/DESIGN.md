# PG plan parity — what is actually missing

**Status:** ten fixes landed and gated. **Index Only Scan and Bitmap Heap Scan
are both off zero** — Q22 emits PG's index-only scan (§10), and Q8/Q17 emit
parameterised bitmap scans roughly 9x faster than before (§11). The Q2
regression is fixed (§9.5). Q13, Q16 and four of PG's six bitmap scans remain.
**Date:** 2026-08-30 (rewritten after adversarial review; §8)
**Branch:** `perf-opt-take6`
**Baseline:** `edfca5d43`
**Oracle:** PostgreSQL 18.3, `postgres/local_install`, TPC-H SF=1 on :65432
**Subject:** goopg, same data, :65433

---

## 1. The access methods exist

The task was framed as "implement the missing index types". They are not
missing — every one exists in the executor, and the planner generates paths for
them:

| capability | executor | planner |
|---|---|---|
| Seq Scan | `operators_storage.go` | ✔ |
| Index Scan | `operators_index.go` | ✔, incl. parameterised (`pathparamindex.go`) |
| Index Only Scan | `operators_indexonly.go` | `IndexOnlyScan`, **5** construction sites |
| Bitmap Heap / Index Scan | `operators_bitmap.go`, `tidbitmap.go` (+parallel) | `costbitmap.go`, `pathbitmap.go`, `createplanbitmap.go` |

Reachability is real, not dead code: `addBaseRelIndexPaths` →
`addBaseRelBitmapPaths` (`pathindexordered.go:56`), reached from
`searchOneProblem` (`relfromjoinlist.go:331`), with `GOOPG_PGSHAPED_DP` ON by
default.

And goopg **does** emit index-only scans:

```
goopg=# EXPLAIN SELECT o_orderkey FROM orders WHERE o_orderkey = 5;
 Index Only Scan using public.orders_pk on public.orders
```

**The visibility map is deliberately NOT in this table.** An earlier draft
listed it as a planner capability; that is inverted. `relsize.go:424` states
the opposite: *"allvisfrac … is omitted: goopg has no visibility map for the
planner to read, so there is nothing to report and no consumer to report it
to."* `grep -ri allvisfrac internal/optimizer/` returns that comment and nothing
else. VACUUM populates `relallvisible`, and no planner code can see it.

## 2. Statistics changed which plans win — they did not end a blackout

The goopg cluster had never been ANALYZEd or VACUUMed; the PG one had. But an
earlier draft's explanation of what that cost — *"no selectivity and no relation
size … choosing in the dark"* — **is wrong in both halves**, and the correction
matters because it is the doc's central claim.

*Relation size was never missing.* `GOOPG_RELSIZE_FALLBACK` defaults to stage 2
(`relsize.go:66`), and `estimateTableRowsFallback` reads the **live smgr block
count** (`relsize.go:567`, `im.RelationBlocks(tbl)`) — not `pg_class.relpages`.
So `relpages = 0` in the catalog says nothing about what the planner knew. The
pre-ANALYZE captures prove it: `q12.goopg.txt` carries
`Seq Scan on public.orders rows=483395` and `q09.goopg.txt` carries
`rows=1909502`. Those are block-derived estimates, not a blind planner's floor.

*And "no selectivity ⇒ no index path" is false of PostgreSQL too.* PG applies
`DEFAULT_EQ_SEL`/`DEFAULT_INEQ_SEL` to a never-analysed relation and picks index
scans routinely. A missing histogram makes an estimate wrong, not absent.

**Corrected:** at S-cold goopg had a good relation-size estimate and default
selectivities. ANALYZE changed which plans won; it was not the difference
between costing and darkness.

| | goopg (before)¹ | PostgreSQL |
|---|---:|---:|
| `lineitem` `reltuples`/`relpages` | 0 / 0 | 5,998,856 / 129,346 |
| `pg_stats` rows | 0 | populated |
| `relallvisible` cust/ord/part | 0 / 0 / 0 | 3,662 / 27,814 / 4,198 |

¹ **not reproducible** — the cluster has since been VACUUMed and ANALYZEd, and
no capture of the "before" catalog state was taken. The PG column reproduces
exactly. The asymmetry is stated rather than presented as symmetric evidence.

Two further corrections about that state:

- **VACUUM's relation statistics do not survive a restart.** VACUUM updates them
  in memory only (`operators_vacuum.go:268` → `UpdateRelStats`, a map mutation);
  the durable sidecar is written **only by ANALYZE**
  (`operators_analyze.go:400-411`) and re-read at startup (`initdb/open.go:3885`).
  An earlier draft claimed both persist across a restart, "verified by
  restarting" — but the same cluster had been ANALYZEd, so that experiment could
  not distinguish the two. (`relallvisible` does survive; it is read live from
  the VM fork.)
- **The S-cold state is a goopg defect, not a harness quirk.** An earlier draft
  cited `CLAUDE.md` for "not a goopg defect"; `CLAUDE.md:34` actually says
  *"per-DB scoping gap in the ANALYZE path — see the deferral ledger row
  `bench-reorg ANALYZE-scope`"*. That gap has since been closed — `pg_stats` is
  populated in db `tpch` on :65433 today — so `CLAUDE.md` is stale on this
  point.

### 2.1 What ANALYZE did change

Q12, on cost alone, no code change (both plans verified against the captures and
live):

```
before:  Hash Join  -> Parallel Seq Scan lineitem
                    -> Seq Scan orders            (rows=483395 estimated)
after:   Merge Join -> Index Scan using orders_pk
                    -> Index Scan using idx_lineitem_orderkey_fkidx
```

The canonical anchors still hold (Q12 = 2 rows, Q13 = 34). Structurally, this is
the only query ANALYZE moved.

## 3. The census, and why "Index Scan parity" was an illusion

Node counts, both engines VACUUMed + ANALYZEd:

| node | goopg | PG | verdict |
|---|---:|---:|---|
| Index Scan (total) | 25 | 24 | *see below* |
| — of which **parameterised** | **2** | **24** | **gap** |
| — of which plain | 23 | 0 | |
| Nested Loop (plain) | 1 | 25 | **gap** |
| Hash Join | 44 | 26 | mirror of the above |
| Merge Join | 5 | 0 | goopg-only |
| Index Only Scan | 0 | 3 | **gap** |
| Bitmap Heap Scan | 0 | 6 | **gap** |
| Bitmap Index Scan | 0 | 6 | **gap** |

An earlier draft read the 25-vs-24 total as **parity** and presented it as the
survey's one success. It is not. Classifying each scan by whether its
`Index Cond` references an outer var: **all 24** of PG's are nested-loop or
correlated-subplan inner probes; **23 of goopg's 25** are unparameterised
full-or-range scans feeding merge joins. The totals coincide; the shapes do not
overlap. That row was the same gap as "Nested Loop 1 vs 25" counted twice.

> **Census caveat.** The two sets are not identical: the sweep script skips Q6
> and Q15, the goopg re-sweep includes Q6 and the PG capture does not, and two
> Q15 fragments are errors on the PG side. So this is ~20–21 real queries per
> side, not 22, and the Seq Scan row in particular is not a like-for-like
> total. Only PG was *not* re-swept after ANALYZE (it was already analysed), so
> "the fair comparison" is half re-captured.

**The census set is NOT the gate set.** Two different sets appear in this
document with confusingly similar counts, so, explicitly:

| | queries | what it is |
|---|---|---|
| census (§3, above) | ~20-21 per side, and the two sides differ | the plan-shape comparison against PG, captured before this work |
| **gate, items 1-3** | **21: Q1-Q22 minus Q15** | the byte-for-byte result comparison |
| **gate, item 0** | **24** | 21 + Q15's three fragments, which item 0 DID compare |

**Item 0's gate was therefore stricter than items 1-3's, and §7 declares "all 22
queries" as the bar — so items 1, 2 and 3 were gated three files short of the
document's own stated requirement.** That is stated rather than smoothed over;
re-running those three fragments against the landed build is an open item in
TODO.md.

The 21-query gate excludes Q15 alone, and for a mechanical reason rather than a
convenient one: Q15 is not a single runnable statement in this query set — it
ships as `q15_create` / `q15_viewbody` / `q15_main` fragments that create and
drop a view around the query, so a one-file-per-query sweep has nothing to run.
Q6 IS in the gate set (it is missing only from the older census capture).

Per query:

| query | goopg | PostgreSQL | gap |
|---|---|---|---|
| Q12 | Index Scan + Merge Join | Index Scan + Nested Loop | join method |
| Q19, Q3 | Hash Join + Seq Scan | Index Scan + Nested Loop | parameterised inner |
| Q13 | Hash Left Join + Seq Scans | Hash Right Join + **Index Only Scan** | commutation; IOS |
| Q16 | Hash Join + Seq Scan | Hash Join + **Index Only Scan** | IOS |
| Q22 | **Index Scan** + NL Anti | **Index Only Scan** + NL Anti | IOS only |
| Q2, Q11, Q17, Q20, Q21 | Index/Hash | **Bitmap Heap Scan** | bitmap |

Bitmap is **five** queries and six scans (Q17's is inside SubPlan 1); an earlier
draft said four.

## 4. The three gaps, correctly diagnosed

### A — Index Only Scan: a placement problem, not a missing predicate

An earlier draft said goopg lacks `check_index_only` and that
`pathindexordered.go` hardcoding `index_only_scan == false` was the root cause.
**Both are wrong.** goopg has the coverage predicate — `tryPromoteIndexOnlyScan`
(`planner.go:14355`, coverage at `:14380-14406`, residual-filter closure at
`:14424-14449`), plus `tryPromoteOrderedIndexOnlyScan` (`:14618`),
`groupagg_indexorder.go:269`, and the min/max sites — and §1 shows it firing.

The real defect is **where** it runs: it is a **top-of-plan peephole** over
`Project(Filter?(IndexScan))` at `planner.go:1691-1704` — once per SELECT LEVEL
(every FROM-subquery and sublink inner gets its own call via
`planSelectWithParent`), gated on `len(s.Locking) == 0`, and before several
later rewrites. An earlier draft said "applied once to the finished plan", which
is wrong on both counts. It therefore cannot reach a scan buried inside a join
tree — and all three of PG's TPC-H index-only scans are exactly that (Q13 hash
build side, Q16 parallel-hash-join input, Q22 nested-loop inner).

Note goopg's promotion consults **no visibility map**, which is the opposite of
what an earlier draft assumed. Making this a per-relation path-generation
decision is the work; porting a predicate is not.

**Blocker, sharpened by experiment (2026-08-30).** An earlier draft of this
section said the blocker was the absence of `attr_needed`. That is true of PG's
architecture but it is not what stops goopg, and the difference decides how much
work this is. Two facts, the second found by building the thing and measuring
it:

**(a) `tryPromoteIndexOnlyScan` is SCHEMA-PRESERVING.** The node it returns
always has exactly the `*Project`'s own schema — directly
(`iosSchema = proj.schema`) or by reinstating a narrowing
`Project{schema: proj.schema}` when a surviving Filter pulled in a column
outside the target list. So substituting its result for a `*Project` ANYWHERE in
the tree is invisible to every ancestor. No renumbering, no `attr_needed`: the
Project already *is* the statement of which columns the rest of the plan wants
from below it.

**(b) But in a flat FROM-list join tree there is no `*Project` above the scan.**
(The unqualified form of this claim is false: a FROM-subquery is planned by
`planSubqueryRangeVar` → `planSelectWithParent` and returns its own plan root —
a `*Project` — into the join tree, `planner.go:3990`. TPC-H's queries are mostly
flat FROM-lists, which is why the pass fired zero times.) A
tree-wide pass that offers the promotion at every `Project` was written and
run against all 22 queries. It is correct and it fires **zero times** — the
census does not move by a single node. Reduced to the smallest case:

```
EXPLAIN SELECT o.o_orderkey FROM orders o
        JOIN (SELECT c_custkey FROM customer WHERE c_custkey < 100) c
          ON o.o_custkey = c.c_custkey;

 Nested Loop
   ->  Index Scan using customer_pk on customer     <- covered, needs only c_custkey
         Index Cond: (c_custkey < 100)
   ->  Index Scan using order_customer_fkidx on orders
```

The join reads the scan directly, so the pass has no hook point, and it was
removed rather than left in as dead code.

**Caveat on this repro, and it is a real one:** `c_custkey < 100` is a STRICT
bound, and `tryPromoteIndexOnlyScan` refuses promotion outright for one
(`planner.go:14377`, `LowOp == OpGt || HighOp == OpLt` — the executor's IOS path
has no exclusive-bound support, M0134-0001 S4 class 8). So this query would not
promote even WITH a Project above it, and it cannot by itself distinguish "no
Project" from "exclusive bound". Use `c_custkey <= 100` before drawing
conclusions from it. The zero-fire result over all 22 TPC-H queries is the
evidence that stands; this reduction is illustrative only.

So gap A is not "port a predicate" (draft 1) and not "add `attr_needed`"
(draft 2). It is: **give the promotion something to attach to inside a join
tree**.

**(c) And the path form does not escape the schema problem either.** Tracing the
plumbing: `searchCtx` (joinsearch.go) carries no output-column information at
all, and neither does `joinlistProblem` (relfromjoinlist.go:82) — bindings,
scans, relInfos, conjuncts, cumOffsets, cp, cat, joinInfoList, and nothing about
what the statement selects. So an index-only path needs a needed-columns channel
threaded from `planner.go` down through the joinlist construction. That is
mechanical. But the *consumer* is not: `createIndexScanPlan`
(createplanindex.go) rebuilds the leaf with `schema: id.schema`, the full table
width, and an index-only variant by definition cannot produce the columns the
index does not carry. Narrowing it breaks every positional `ColumnRef` above it
in the join tree — the same wall as (b).

**Why PG does not hit this wall.** PG's plan tree addresses columns with `Var`
nodes carrying `varno`/`varattno` — relation-qualified, so narrowing a scan's
output changes nothing above it. goopg's `ColumnRef.Index` is POSITIONAL into
the node's input schema, and a join's schema is the concatenation
`outer || inner`, so narrowing any input shifts every position above it. That
single representational difference is why `check_index_only` is a local
predicate in PG and a cross-cutting refactor here.

The honest scope for gap A is therefore: a column-pruning/remap pass, with the
index-only path costed on top of it. Neither half alone is sufficient —
**except for Q22, which is provably exempt.** For a semi/anti
`NestedLoopIndexJoin` the join's schema is the OUTER's alone
(`nl_index_join.go:677-684`, "the inner side is consumed only for matching,
never projected"), so narrowing its inner is invisible to everything above and
the positional-`ColumnRef` problem does not arise. goopg's Q22 plan is already
structurally identical to PG's — same `Nested Loop Anti Join`, same
`Index Cond: (o_custkey = c_custkey)` — and differs only in `Index Scan` vs
`Index Only Scan`. It is the cheapest census win in this document.
Supporting evidence for how absent the path form is:

```
$ grep -rn "attr_needed\|reltarget\|neededCols\|attrs_used" internal/optimizer/
(no matches)
```

and no `path*.go` file mentions `IndexOnlyScan` at all — the path model has no
index-only path type; IOS exists solely as a post-hoc rewrite. So A is
**building per-relation attribute-usage analysis and an index-only path type**,
not relocating a predicate. Confirmed shapes: Q13's IOS is a hash **build side**
(`Index Only Scan using customer_pk` feeding `Hash`), Q16's is a
`Parallel Index Only Scan` inside a `Parallel Hash Join` — neither is under a
Project.

### B — Bitmap: never parameterised, and fed a 1-row estimate

Answering the question an earlier draft left open: `buildOneBitmapPath` returns
nil only for a nil index, an unresolvable partial predicate, and
`Unique && relTuples <= 1`. It otherwise **generates** — but never the path PG
chose, for two independent reasons:

1. **`pathbitmap.go:177` hardcodes `RequiredOuter: 0`.** goopg's bitmap paths
   are unconditionally unparameterised, so one can never sit as a nested-loop
   inner. All six of PG's TPC-H bitmap scans have an outer-var `Index Cond`
   (`s_nationkey = nation.n_nationkey`, `l_partkey = part.p_partkey`).
2. **`matchBitmapIndexQuals` matches only `col = const`** (`pathbitmap.go:234-286`),
   so a join clause contributes nothing and `selectivity` stays 1.0 — a
   whole-relation bitmap that must lose.

And the plain, unparameterised case that this code *was* written for fails too,
for a third reason that is worse than either:

```
goopg:  EXPLAIN SELECT * FROM supplier WHERE s_nationkey = 5;
        Index Scan using public.supplier_nation_fkidx   rows=1
PG:     Bitmap Heap Scan on supplier                    rows=378
```

`cardinality.go:294-297` — `indexScanRows` returns a **hardcoded 1** for any
keyed index scan, statistics or not. Every keyed index scan in goopg is priced
as a unique probe. That single line poisons every comparison a bitmap path could
win, and it is arguably the most consequential defect this survey found.

So B is **not** a cost-calibration item. Comparing `costBitmapHeapScan` against
`cost_bitmap_heap_scan` would find nothing, because the formulas are being fed
1 and 1.0.

Reason 3 is now fixed — see **item 0** below. Reasons 1 and 2 remain, and behind
them sits a harder blocker:

**Blocker, both halves verified (2026-08-30).** Gap B does NOT inherit gap A's
schema problem — a `BitmapHeapScan` fetches whole heap rows, so it produces the
same schema as the `IndexScan` it would replace and nothing above it needs
renumbering. That makes B the better-shaped of the two. But it has two blockers
of its own, one planner-side and one executor-side.

*Executor side:* `bitmapHeapScanOp` (operators_bitmap.go) has **no `Rescan` and
no `BindOuter`** — compare `indexScanOp`, which has both
(operators_index.go:353, :362). A nested-loop inner is re-probed once per outer
row with a new bound key; a bitmap inner would have to rebuild its TID bitmap
from the index each time. That path does not exist, so even a correctly costed
parameterised bitmap path could not be executed.

*Executor interface, the good news:* the executor is ALREADY generic here. It
builds the inner through an `nliInner` interface (operators_nljoin.go:101 —
`Schema/openPrep/Next/BindOuter/Rescan/Close`) which both `indexScanOp` and
`memoizeOp` satisfy. So nothing in the executor's join driver hard-codes an
index scan; the typing blocker is the PLAN node's field alone.

What each candidate inner still lacks is the interface's rescan half:
`indexOnlyScanOp` and `bitmapHeapScanOp` implement neither `BindOuter` nor
`Rescan`. That is the same missing piece for gap A's Q22 (PG's index-only scan
there is a nested-loop ANTI-join inner, where the inner's columns are never
read at all, so narrowing it would be invisible) and for all six of gap B's
bitmap scans. It is one shape of work serving both gaps, which makes it the
highest-leverage next step in this document.

*Planner side:* a parameterised bitmap path would have **no
consumer**. `NestedLoopIndexJoin.Inner` is typed `*IndexScan`
(`plan.go:822`), and `pathparamindex.go` already documents the NLI constructor
as the *only* consumer of a parameterised path. Emitting a parameterised bitmap
path before that type is generalised would let the DP price a plan the plan
builder must refuse — the precise failure that file warns against. B therefore
needs a join-node change (plus confirmation that the bitmap executor rescans per
outer row) *before* `RequiredOuter` and `matchBitmapIndexQuals` are touched.

### C — Nested Loop under-preferred (1 vs 25)

Parameterised inner index paths **are** generated —
`addParameterizedIndexPaths` (`pathparamindex.go:220`), live since M0127-P5.9,
reached from `pathindexordered.go:50`. An earlier draft cited "goopg's Q22 emits
one under a Nested Loop Anti Join" as the proof; **that citation is wrong**. The
search's NLI arm hard-codes `Type: JoinTypeInner` (createplannl.go:274-276), so
a Semi/Anti `NestedLoopIndexJoin` comes from the legacy rule-based rewrite
`rewriteJoinsToNLI` (nl_index_join.go), which runs after the search and is not a
costed path at all. Q22's artefact settles the existence of the legacy rewrite,
not of the path. The open question is why that arm loses to
hash 23 times out of 25, and §4-B's hardcoded 1-row estimate is a prime
suspect: an inner priced at 1 row makes the *loop* look cheap but also makes the
index scan's own cost degenerate, and the same constant feeds join costing.

**Update (item 0 landed).** The hardcoded 1 is gone; `indexScanRows` now returns
selectivity x reltuples (`var_eq_non_const` per equality column, `DEFAULT_INEQ_SEL`
per range bound), with a unique index fully bound by equality still returning 1.
`s_nationkey = 5` moved rows=1 -> rows=400 (PG: 378). The census response was
Index Scan 25 -> 16 — index scans stopped being artificially cheap — but **0
bitmap, 0 IOS, 1 nested loop is unchanged**, which retires the "prime suspect"
hypothesis above: the constant was a real defect, and it was not what was
suppressing the three shapes. All 24 TPC-H result sets stayed byte-identical.

**B and C likely share one root cause with A's sibling problem** — the reachable
path space lacks parameterised shapes, and cardinality for keyed scans is a
constant. That is a different, larger project than "port three access methods".

## 5. Constraint: no query-specific forcing

Every change must be in path generation, cost inputs, or a cost formula aligned
with its PG counterpart. None may test a relation name, query shape, or
benchmark identity. Acceptance for each: the plan changes **and** the reason is
expressible as "PG's `costsize.c`/`indxpath.c` does X, and goopg now does X".

## 6. PG oracle references (all verified present)

`check_index_only` indxpath.c:2229 · `cost_index` costsize.c:560 ·
`estimate_rel_size` plancat.c:1075 · `create_index_paths` indxpath.c:241 ·
`choose_bitmap_and` indxpath.c:1786 · `cost_bitmap_heap_scan` costsize.c:1023 ·
`compute_bitmap_pages` costsize.c:6514 · `initial_cost_nestloop` costsize.c:3267 ·
`final_cost_nestloop` costsize.c:3349 · `initial_cost_hashjoin` costsize.c:4160.

## 7. Why this stops at the design

**Item 0 shipped; A, B and C did not.** The corrected diagnoses in §4 are why,
and the reason is not "this is large" in the abstract — each gap turned out to
be gated on a concrete piece of infrastructure that does not exist yet:

- **Item 0** (the cardinality constant) — **DONE**, `9db8a0970`, fully gated.
  It was a prerequisite for B and C and a real defect in its own right.
- **A** needs a column-pruning/remap pass AND an index-only *path* type (§4-A).
  It also overlaps B: PG's Q22 index-only scan is a NESTED-LOOP INNER, and
  `NestedLoopIndexJoin.Inner` is `*IndexScan` with `createNestLoopIndexJoinPlan`
  panicking on anything else (createplannl.go:224-226) — so one of A's three
  target queries needs B's blocker cleared as well. An earlier draft's clean
  per-gap split understated A.
- **B** needs `NestedLoopIndexJoin.Inner` generalised beyond `*IndexScan`, or a
  parameterised bitmap path has no consumer and the DP prices unbuildable plans.
- **C** overlaps B rather than being independent: **six of** PG's 25 nested
  loops here are the *same* plans as its six bitmap scans (bitmap-heap inners in
  Q2, Q11, Q17, Q20, Q21), so that part of C cannot be reproduced without B. An
  earlier draft wrote "PG's six nested loops", which contradicts §3's census row
  of 25 and made C look four times smaller than it is.

Item 0 also settled an open question the other way: fixing the cardinality
constant did **not** move IOS/bitmap/nested-loop counts, so the remaining gaps
are structural (missing path shapes), not mis-costing.

In this repository a planner change carries the project's most expensive failure
mode — silent row-count regressions, 608 anchors, documented multi-loop
bisects — so the gate is all 22 TPC-H queries plus the TPC-DS SF0.5 sweep, not
the query being targeted. Landing any of the above half-gated would be worse
than landing nothing. Item 0 cleared that bar: 24/24 result sets compared with
`cmp`, `tpch-spotcheck` PASS, units 43/43, `-race` clean. See
[TODO.md](TODO.md).

## 8. Review record

Adversarial agent review against the first draft: **4 critical, 10 major,
6 minor**, verified against the live clusters. Three of the four criticals
invalidated a diagnosis, not a sentence. This document is a rewrite, not a
patch.

| # | finding | resolution |
|---|---|---|
| **C1** | "Index Scan 25 vs 24 — parity" is a coincidence of totals. PG's 24 are **all** parameterised inner probes; 23 of goopg's 25 are unparameterised. The row was gap C counted twice, and it was the draft's only claimed success. | §3 split by parameterisation; "parity" withdrawn. §2.1 and §7 no longer lean on it. |
| **C2** | The root cause was wrong in both halves: `GOOPG_RELSIZE_FALLBACK` (default on) supplies a **live smgr block count**, and the pre-ANALYZE captures show real estimates (`rows=483395`). "No selectivity ⇒ no index path" is false of PG too, which uses `DEFAULT_EQ_SEL`. | §2 rewritten: statistics changed which plans won; there was no blackout. (My own earlier correction — "it has size, it lacks selectivity" — was also too strong.) |
| **C3** | Gap A's diagnosis was wrong: goopg **has** `check_index_only` (5 sites) and emits index-only scans; the defect is that it is a top-of-plan peephole that cannot reach scans inside join trees, where all 3 of PG's live. | §4-A rewritten. The `pathindexordered.go` "hardcoded false" line I cited is one comment in one function, not the answer. |
| **C4** | Gap B's diagnosis was wrong: paths are generated but `RequiredOuter` is hardcoded 0, `matchBitmapIndexQuals` matches only `col = const`, and — the big one — `indexScanRows` returns a **hardcoded 1** for any keyed index scan (goopg 1 vs PG 378 rows on the same predicate). | §4-B rewritten. Not a calibration item. |
| M1 | Bitmap is 5 queries / 6 scans (Q17 omitted). | Fixed. |
| M2, M9 | The census compares two different query sets (~20–21, not 22; Q6 on one side only; Q15 fragments error on PG), and only goopg was re-swept. | Caveat added to §3. |
| M3 | VACUUM's relstats are in-memory only; only ANALYZE writes the durable sidecar, so the restart experiment could not distinguish them. | §2 corrected. |
| M4 | "not a goopg defect" mis-cited `CLAUDE.md`, which attributes S-cold to a per-DB ANALYZE scoping gap with a ledger row — since closed, so `CLAUDE.md` is stale. | §2 corrected. |
| M5 | The visibility map was listed as a *planner* capability; `relsize.go:424` says the planner cannot read it. | Row removed from §1 with the inversion called out. |
| M6 | The "goopg (before)" column is unreproducible and was presented as symmetric evidence. | Footnoted. |
| M7 | §7's retraction overcorrected — 5 of 8 census rows remain total gaps. | Rewritten; the original claim was wrong about the executor and about *why*, right that goopg emits neither on TPC-H. |
| M8, M10 | Sequencing rested on the misdiagnoses; C's TODO asked a question already answered in-tree. | §7 and TODO reordered. |
| m1–m6 | 5 IOS sites not 4; `relallvisible == relpages` on every goopg relation here, so an `allvisfrac` test on this cluster cannot distinguish correct from hardcoded 1.0; Q13's build side is not readable from goopg's EXPLAIN; a runtime fact was spliced into a plan excerpt; Q12's "fixed" scoring is inconsistent with counting Merge Join as goopg-only. | All corrected or footnoted. |

Verified **correct** by the review: every file in §1 and its reachability; all
ten PG oracle references; §2's PG column; both censuses arithmetically; §2.1's
Q12 before/after; the Q22/Q13 spot-checks; that the "statistics are
per-connection" memory note is genuinely stale; and that §5's constraint is
sound — both real gaps land in path generation, its first permitted category.


## 9. The `loop_count` arm: implemented, measured, rejected

This is recorded in full because the result is counter-intuitive and the code is
worth resurrecting once §9.3 is fixed — not because it was a dead end.

### 9.1 What it was

`cost_index` (costindex.go) reproduced only PG's `loop_count == 1` arm.
Parameterised paths were priced by a separate flat-probe function that charged a
full `random_page_cost` per fetched row **on every rescan** — a repeated inner
scan costed as if nothing were ever cached. PG spends its entire `loop_count > 1`
arm (costsize.c:598-640) amortising exactly that: it runs Mackert-Lohman over
the tuples ALL the scans fetch and pro-rates back to one scan, so the total
grows sublinearly in the loop count. The loop count itself is `get_loop_count`
(indxpath.c:3266) — the SMALLEST row count among the relations supplying the
parameter, not the product.

Implementing it removed the second cost model 04 §1 forbids, which was a
structural improvement independent of the numbers.

### 9.2 What it did — the numbers are not the problem

TPC-H Q3's inner probe of `lineitem`, across the three fixes in order:

| | inner rows | inner cost | NLI path cost |
|---|---:|---:|---:|
| before anything | 30,006 | 120,479 | 36,233,884,802 |
| after the numdistinct fix (`80a5e334d`) | 4.9 | 23.9 | 7,284,735 |
| after the `loop_count` arm | 4.9 | **4.9** | 1,603,379 |

PG's own figure for that probe is `cost=0.43..4.01`. The amortised arm lands on
it. Across the 22 queries the census moved decisively toward PG's shapes, and
**all 21 result sets stayed byte-identical**:

| node | before | after | PG |
|---|---:|---:|---:|
| Nested Loop | 5 | **13** | 25 |
| Index Scan | 16 | **24** | 24 |
| Hash Join | 44 | **35** | 26 |
| Seq Scan | 68 | 60 | |

Q11 got 4.5x faster (0.9 s -> 0.2 s) and picked up PG's exact inner probe,
`Index Cond: (ps_suppkey = s_suppkey)`.

### 9.3 Why it was rejected anyway

**Q2 went 2.0 s -> 87.3 s** in the first measurement and 1.6 s -> 84.4 s in the
re-measurement below; the multiplier is ~43x and ~53x respectively. Both pairs
are reported because they are separate A/B runs on separate builds, not two
readings of one run. Not a costing artefact — the plan it switched to is
close to PG's own. PG's Q2 is `Nested Loop -> Nested Loop -> Hash Join(nation,
region)` with `Index Scan using partsupp_supplier_fkidx` on
`ps_suppkey = supplier.s_suppkey`, and goopg now produces that shape. The
difference that costs 43x is **where SubPlan 1 is evaluated**:

```
PG:     Hash Cond: ((part.p_partkey = partsupp.ps_partkey)
                    AND ((SubPlan 1) = partsupp.ps_supplycost))
        ... over Gather -> Parallel Seq Scan on part (rows=670)

goopg:  Hash Join
          Filter: (partsupp.ps_supplycost = (SubPlan 1))
        ... over a 160,000-row join output
```

PG evaluates the correlated subplan ~670 times, as part of the hash condition on
the small side. goopg evaluates it as a filter above the large join — on the
order of 160,000 times. Before this change goopg avoided the question entirely by
de-correlating the subquery into a `HashAggregate`; making the parameterised
inner cheap is what made the correlated form win the comparison.

So the defect the change EXPOSED is real and is not in `cost_index`: goopg does
not charge a SubPlan's evaluation cost against the number of rows it will be
evaluated over (PG does, in `cost_qual_eval`). Until it does, making
parameterised inners cheaper will keep pulling plans toward correlated
subplans that goopg then executes badly.

### 9.3b The unexplained fact that decided it

Re-applied and re-measured to find the Q2 fix, the decorrelation path was
instrumented — and it reports SUCCESS on the very subquery that ends up as a
SubPlan:

```
UNNEST scalar ACCEPTED: *optimizer.Project
UNNEST apply: params=1 residuals=0
(no BAIL from any of: planHasOuterRefRemaining, no-Filter-contains-subquery,
 not-AND-reachable, buildUnnestedSubquery-returned-nil)
```

`canUnnestSubquery` accepts, `unnestSubquery` runs to completion and returns a
rewritten tree — and `EXPLAIN` still shows `Filter: (partsupp.ps_supplycost =
(SubPlan 1))`. So the SubPlan in the final plan is not the one the unnester
declined to remove; something downstream re-creates or re-plans it. That is a
bug in its own right and it is NOT understood.

`innerPlanIsIndexProbeCheap` (unnest.go) was the expected culprit — it is the
S6/D6.2 policy that keeps a SubPlan when the inner is probe-shaped, and it names
Q2 in its own comment. It is not: it returned false (accept) in this build.

**Draft 3 said "the DP search discards the unnester's rewrite". That is WRONG,
and the call order alone disproves it:** `tryJoinSearch` runs at
`planner.go:1206` and `unnestSubqueriesInPlan` at `planner.go:1237` — the search
runs FIRST and the unnester decorates its output. `unnestSubquery` finishes with
`filter.Child = join; return outer` (unnest.go:2208), mutating in place the
`*Filter` the search left behind; it never builds a competing plan for anything
to discard. (Found by adversarial review, 2026-08-30.)

Two further hypotheses were tested and are also wrong:

- **A missing `*NestedLoopIndexJoin` arm in the walk.** The walk
  (unnest.go:513-528) has arms for only `*Filter`, `*Join`, `*Project`,
  `*Aggregate`, `*Sort`, `*Limit` — no NLI arm, and the `loop_count` arm's whole
  effect is to make the search emit NLI nodes. Plausible, and false: adding the
  arm leaves Q2 at 2 SubPlans and 83.6 s.
- **The sublink sitting in `Join.Predicate`,** which the walk never inspects.
  Also false — instrumenting both the `*Filter` and `*Join` arms produced NO
  hits at all.

**What the evidence shows, now MEASURED rather than inferred.** The earlier
version of this paragraph read two debug lines and guessed which SELECT level
each belonged to. Re-instrumented to print the level's own identity — its base
tables and its sublink count — the answer is the opposite of the guess:

```
POSTDP preDPUnnested=true  root=*Filter tables=[nation region supplier partsupp]      sublinks=0
POSTDP preDPUnnested=false root=*Filter tables=[nation region supplier partsupp part] sublinks=1
```

The `true` line is Q2's INNER subquery (`select min(ps_supplycost) from partsupp,
supplier, nation, region …`), which has no sublinks and for which skipping the
walk is correct. Q2's OUTER level is the second line — it includes `part`, it
carries the one sublink, and **`preDPUnnested=false`, so the post-search walk
DOES run on it.**

That retires the "the walk never runs" story as well. It also undermines the
refutation of hypothesis 3 above: that refutation rested on instrumenting the
walk's `*Filter` and `*Join` arms and seeing no hits, but the walk demonstrably
runs on a tree whose root IS a `*Filter` and which contains a sublink, so the
probe that reported "no hits" cannot be trusted (most likely it was inserted at
the wrong one of two `case *Filter:` sites). **Hypothesis 3 is therefore OPEN
again, not refuted.**

Standing at the end of this session:

| # | hypothesis | status |
|---|---|---|
| 1 | the DP search discards the unnester's rewrite | **refuted** — search runs first (planner.go:1206 vs :1237) |
| 2 | missing `*NestedLoopIndexJoin` walk arm | **refuted by measurement** — adding it leaves Q2 at 83.6 s |
| 3 | sublink unreachable in the walk's visited arms | **refuted, correctly this time** — see below |
| 4 | Q2's post-search walk never runs | **refuted by measurement** — it runs |

Hypothesis 3's probe was then re-done with its insertion point ASSERTED — there
are **15** `case *Filter:` sites in unnest.go, not two, and the original probe's
`count=1` replacement had landed on the first (line 270) instead of the walk's
(line 432). At the right site:

```
WALK Filter: sublinkInPred=false pred=*BinaryOp
WALK Filter: sublinkInPred=true  pred=*BinaryOp     <- the walk DOES see it
WALK Filter: sublinkInPred=false pred=*BinaryOp
```

So the walk reaches a `*Filter` whose predicate contains the sublink. Combined
with the earlier instrumentation — `canUnnestSubquery` ACCEPTS, `unnestSubquery`
runs with `params=1 residuals=0` and takes **none** of its four bail paths — the
question is now as narrow as it can get without a fix:

> **`unnestSubquery` is called on the right `*Filter`, reports success, and the
> sublink is still a `SubPlan` in the finished plan.**

Two more measurements close it further.

**The walk does not remove the sublink**, so no later pass re-introduces it:

```
UNNESTWALK sublinks 1 -> 1
```

counted over the whole plan tree immediately before and after
`unnestSubqueriesInPlan` at Q2's outer level. That eliminates "a later pass
rebuilds from a stale node".

**And the rewrite's substitution is by POINTER IDENTITY.** `unnestSubquery`
ends:

```go
newConjunct := replaceExprInConjunct(conjunct, sub, replacement)
conjuncts := splitAnd(filter.Predicate)
for _, c := range conjuncts {
        if c == conjunct { … newConjunct … } else { … c … }   // pointer compare
}
filter.Predicate = combineAnd(newConjuncts)
…
filter.Child = join
return outer, nil
```

If `findFilterContainingSubquery` hands back a `conjunct` pointer that is not
one of `splitAnd(filter.Predicate)`'s top-level elements — because the sublink
sits inside a nested conjunct, or under an `OR`, or because the two functions
flatten the predicate differently — then **every conjunct is copied unchanged**,
`filter.Predicate` keeps the sublink, and `filter.Child = join` installs the
join anyway. No bail is taken and the function returns success.

That reproduces every observation exactly: accepted, `params=1 residuals=0`, no
bail, sublink survives, count 1 -> 1. It is a HYPOTHESIS, not a demonstrated
mechanism — the fifth on this list, and the first four were wrong — so the next
session should test it before acting on it. The test is one print:
`conjunct` versus the elements of `splitAnd(filter.Predicate)` at that line,
compared by pointer.

### 9.4 Decision — LANDED (`07f4f7814`), regression and all

Withheld twice, then landed. The bar set the first two times was "do not ship on
top of an unexplained observation" — §9.3b was that observation, and resolving
it removed the bar. What is left is a trade with a known mechanism, which
belongs in the history where it can be read and reverted, not in a patch file
where it is invisible.

Re-measured on a fresh server per arm before landing: **Q2 1.6 s -> 84.4 s**,
Q11 1.0 s -> 0.2 s, Q5 and Q8 unchanged. All 21 result sets byte-identical.

`07f4f7814` is a single self-contained commit; reverting it restores the old Q2
and nothing else depends on it.

Removal order for the regression: (1) make the DP search either model the
decorrelated form or decline when a rewrite it cannot represent is in play
(§9.3b) — this is the real fix; (2) charge SubPlan evaluation over the row count
it is evaluated over (PG `cost_qual_eval`), `subplan_cost.go`; (3) re-run the
21-query byte gate AND a timing pass.

**That last point is the transferable lesson.** All 21 result sets were
byte-identical and every unit test passed. A row-count gate cannot see a 43-53x
regression; only timing the changed queries caught it. Any future plan-shape
change needs both.


## 9.5 The Q2 regression: RESOLVED (`f95d85ae2`)

**84.4 s -> 1.9 s, result set byte-identical, and none of §9.2's plan-shape
gains given back.**

The cause was not costing, not the DP, not the unnester's logic, and not any of
the five mechanisms proposed above. `clonePlanReplacingOuter` (unnest.go)
enumerates the plan node kinds a correlated subquery's body may contain — Join,
Filter, Project, Aggregate, Sort, Limit, SeqScan, IndexScan, Values, CTEScan,
MaterializedCTEScan — and had **no `*NestedLoopIndexJoin` arm**, because until
`cost_index` learned its `loop_count` amortisation the search never produced one
inside a subquery body. Once it did, Q2's inner plan became
`Aggregate -> NestedLoopIndexJoin -> …` and the cloner returned
`unsupported plan node`.

**And the failure is silent by construction.** `unnestSubqueriesInPlan`'s driver
loop ends `if err != nil || newOuter == nil { break }` — an error and a decline
are the same thing to it — so the sublink simply stayed a correlated SubPlan,
evaluated per row of a 160,000-row join instead of being decorrelated into a
hash aggregate. Nothing anywhere reports it.

### 9.6 Why five hypotheses in a row were wrong

Worth recording, because the pattern is more transferable than the bug:

| # | hypothesis | how it died |
|---|---|---|
| 1 | the DP discards the unnester's rewrite | call order — search runs first |
| 2 | missing `*NestedLoopIndexJoin` walk arm | measured: Q2 stays at 83.6 s |
| 3 | sublink unreachable in the walk | measured (after fixing a probe that had hit the wrong 1 of 15 `case *Filter:` sites) |
| 4 | the post-search walk never runs | measured: it runs |
| 5 | pointer-identity conjunct substitution | measured: the code never reaches that line |

Every one of the five reasoned from plan shape, call structure or cost. The
answer was a missing switch case behind a swallowed error, and it was found the
moment the probe stopped asking "which of my theories is right" and started
asking "where does this function actually return". The general lesson: when a
rewrite reports success and does nothing, instrument its EXIT PATHS before
theorising about its inputs — and treat `if err != nil { break }` in a driver
loop as a place where evidence goes to die.

The five-hypothesis table is left in §9.3b deliberately. A future reader
comparing it with this section learns more from the sequence than from the
answer.

## 10 Index-only scans: Q22 landed (`20495f11e`, `4bb67d06b`, `38f37863a`)

**Index Only Scan 0 -> 1.** goopg's Q22 emits
`Index Only Scan using order_customer_fkidx on orders`, which is what PG emits
for that anti-join probe, and the query is FASTER for it: 1.4 s -> 0.9 s.

### 10.1 What made it reachable when §4-A said it was not

§4-A is still right about Q13 and Q16. Q22 is different, and the difference is
one line of `nl_index_join.go`:

```go
if j.Type == JoinTypeSemi || j.Type == JoinTypeAnti {
        joinedSchema = append(Schema(nil), outerNode.Output()...)
} else { /* outer ++ inner */ }
```

A semi/anti join **does not publish its inner's columns**. So narrowing the
inner cannot renumber any `ColumnRef` above it, and the positional-addressing
problem that makes index-only scans a cross-cutting refactor here simply does
not arise. Coverage is not tested because there is nothing to cover: the heap
tuple was wanted for VISIBILITY alone, which is exactly what the visibility map
supplies.

Three stages, each gated on its own:

| stage | commit | what |
|---|---|---|
| 1 | `20495f11e` | `indexOnlyScanOp.Open` split into `openPrep` + `Rescan` + `BindOuter`, mirroring `indexScanOp`. The four probe helpers moved off a NIL ROW to `evalExprSlot(expr, o.outerSlot, …)` — they were constant-only, and would have probed silently wrong as an inner |
| 2 | `4bb67d06b` | `NestedLoopIndexJoin.Inner` widened to `Node`; `nliInnerProbe` is the one place enumerating legal inner kinds |
| 3 | `38f37863a` | the promotion, with four declines (scan-level `Cond`, exclusive bound, residual reading an inner column, outer/subquery reference) |

### 10.2 What is left, and an estimate to distrust

Q13 and Q16 are NOT reachable this way — their index-only scans are hash-join
inputs, which DO publish their columns — so they still need the column-pruning
pass §4-A describes. The six bitmap scans need the rescan chain
(`bitmapHeapScanOp` -> `bitmapIndexScanOp` -> And/Or) plus the planner half.

**A methodological note that cost this document real time.** Stage 2 was
estimated at "52 references across 26 files" by grepping for `.Inner`. The
actual coupling, obtained by changing the field type and letting the compiler
enumerate dependents, was **four production files** — `joinlayout.go`,
`memoize.go`, `subplan_cost.go`, `executor.go` — plus eight test files. Grep
counted the name; the compiler counted the coupling, and they differed by an
order of magnitude in the direction that discourages starting. When a type
change is the question, ask the type checker, not `grep`.


## 11 Bitmap scans: Q8 and Q17 landed (`94fd0c9b3`, `b4bd87a1a`)

**Bitmap Heap Scan 0 -> 2, and both queries got ~9x faster: Q8 10.0 s -> 1.1 s,
Q17 3.4 s -> 0.4 s.** goopg now picks a bitmap scan as a nested-loop inner,
which is the shape all six of PG's TPC-H bitmap scans have.

Three things had to be true. Only one of them was the planner.

### 11.1 A pre-existing EOF-contract bug that only this consumer could expose

`bitmapHeapScanOp` signalled exhaustion with `(nil, nil)` instead of
`(nil, EOF)`. Every operator in the package returns `EOF` and every consumer
tests `err == EOF`, so a nil slot with a nil error passes that test and arrives
as a REAL row that happens to be nil. A top-level pull tolerated it because it
stops on a nil slot anyway — which is exactly why it survived — but a
`NestedLoopIndexJoin` inner following the contract does `slotRow(nil)`, gets a
zero-length row, and panics with `index out of range [7] with length 0` from
inside the join's predicate evaluation, on a stack naming neither the bitmap
scan nor that return.

### 11.2 The census had been measuring the labeller, not the planner

`EXPLAIN` had **no arm for `BitmapHeapScan` at all**; it printed the Go type
`*optimizer.BitmapHeapScan`. So the first two census runs after bitmap paths
started winning still read **zero**, and would have kept reading zero
indefinitely. A census that greps for a label is only as good as the labeller.

### 11.3 Five wrong hypotheses, then one measurement

The crash in 11.1 was chased through five hypotheses formed by reasoning —
merged-schema width mismatch (disproved by adding the assertion, which never
fired), the parallel hash-build path, the bitmap emit path, `slotRow`, and a
missing walk arm. All five were wrong.

It was settled in ONE run by printing `len(outerMS.row)`, `len(innerMS.row)`,
`outerWidth` and `innerWidth` at the crash site:

```
outerRow=9 innerRow=16 outerW=9 innerW=16 schema=25   <- fine
outerRow=9 innerRow=0  outerW=9 innerW=16 schema=25   <- crash
```

Widths consistent, inner row EMPTY — which points at the callee's return, not at
any of the five theories. This is the second time in this document that a
multi-round reasoning chain was ended by one instrumented run (§9.6 is the
first). The lesson is the same and is now stated twice on purpose: when a
mechanism resists explanation, print the state at the failure and stop
theorising.

### 11.4 What is left, measured rather than guessed

Four of PG's six bitmap scans (Q2, Q11, Q20, Q21) and Q13/Q16's index-only
scans.

The filtered-leaf restriction was named here as "the most likely next unlock".
It was lifted in `01b9a9686` and **unlocked nothing** — zero plans changed.
Instrumenting the producer instead of guessing again shows why: those relations
already GET bitmap paths, including the exact one PG uses for Q2.

```
BM supplier.supplier_nation_fkidx req=0x0002 built=true cost=2588.7
```

PG prices that same scan at **43.46**. So bitmap is not BLOCKED for those four
queries — it is **mispriced by roughly 60x** and loses to the plain index probe.
Two terms are missing from `costBitmapHeapScan`, and the first is most of the
gap:

1. PG blends random toward SEQUENTIAL page cost as the touched fraction grows —
   `cost_per_page = spc_random_page_cost - (spc_random_page_cost -
   spc_seq_page_cost) * sqrt(pages_fetched / T)` (`cost_bitmap_heap_scan`,
   costsize.c:1023). goopg charges full random cost per page, so a bitmap scan
   touching most of a relation is priced as if every page were a random seek —
   precisely the case bitmap exists to make cheap.
2. PG's is a nested-loop inner, so its pages are amortised by `loop_count`, the
   same arm `07f4f7814` added to `cost_index`. `costBitmapHeapScan` has no
   `loop_count` notion at all.

That is a cost-formula gap with a named PG oracle on both terms, which is a very
different remaining task from the "missing infrastructure" this document opened
with.

### 11.5 Both terms fixed (`94ef875ab`) — and the first was inverted, not missing

`costBitmapHeapScan` did have an interpolation; it ran **backwards**. goopg
computed `sqrt*random + (1-sqrt)*seq`, which moves toward RANDOM as more of the
relation is touched. PG's is

```c
cost_per_page = spc_random_page_cost
              - (spc_random_page_cost - spc_seq_page_cost) * sqrt(pages_fetched / T);
```

which moves toward SEQUENTIAL — the entire reason a bitmap scan exists. The
file's own comment described PG's behaviour ("a near-whole-table scan approaches
seq_page_cost") while the expression below it did the opposite, so comment and
code never contradicted each other in review, and the unit test encoded the
inverted form. `compute_bitmap_pages` also gained PG's `loop_count` pro-rating.

**The result reads backwards and is the opposite: Bitmap Heap Scan went 2 -> 1.**
PG's Q8 has ZERO bitmap nodes and PG's Q17 has two. goopg had one on BOTH, so
its Q8 bitmap was a divergence the inverted formula produced; after the fix
goopg's bitmap set on those queries matches PG's exactly. Timed against the TRUE
pre-work baseline rather than the intermediate state: q08 11.7 s -> 11.5 s (its
1.1 s under the buggy formula was never real), q17 2.2 s -> 0.4 s.

This is the third time in this document that a census number moving in the
"wrong" direction was the correct outcome, and the second time a unit test was
found pinning the defect it was meant to guard. Neither is a coincidence: a test
written from the implementation rather than from the oracle will always agree
with the implementation.


## 12 Why bitmap parity is unreachable today: lossy pages are dropped

The remaining four bitmap scans are not blocked by costing, by path generation,
or by the parallel handoff. They are blocked by this, in
`internal/executor/operators_bitmap.go`:

```go
// nextParallel
if entry.isLossy {
        o.lossyPages++
        // … a lossy page emits nothing through this operator.
        // Full lossy-page iteration is deferred (S5.x follow-up).
        continue
}

// nextSerial
if lossy {
        // We'll return one row at a time and save position.
        continue // fall through to lossy handling below
}
```

**A lossy page emits nothing, on both paths.** There is no lossy handling below
the serial branch; its comment describes code that was never written, and the
parallel branch's comment claims to match a serial behaviour that is itself
unimplemented.

A TID bitmap goes lossy once it exceeds `work_mem`, which a scan over 6 M
`lineitem` rows does at once. That is exactly the 94% row loss measured on Q3
(702 rows of 11415) the moment bitmap paths began to win.

### 12.1 Why this took so long to surface

It is the fourth latent bug this work found in the bitmap feature, after the
`EOF` contract violation (`b4bd87a1a`), the `Outer`-shape rewrap
(`d24d9e6be`), and the double-charged heap cost. All four share one cause:
**the planner never selected a bitmap path, so the executor's bitmap code had
never run end to end.** Cost bugs kept the path unreachable, and unreachable
code accumulated correctness bugs that only the cost fixes could expose. Each
fix revealed the next.

That is worth stating as a general risk rather than a bitmap anecdote: in a
cost-based planner, an access method that never wins is not "implemented but
unused" — it is untested, and its cost model is the only thing hiding that.

### 12.2 What gap B actually needs

Implement lossy-page iteration: on a lossy entry, walk every line pointer on the
page and re-check each tuple against `BitmapQual` (PG's `bitgetpage` and the
recheck path in `nodeBitmapHeapscan.c`). Until then, a bitmap scan over any
relation large enough to overflow `work_mem` silently returns a subset, so
bitmap paths cannot be enabled broadly at any cost setting — and the verified
double-count fix (§11.4) must stay unlanded.

This also deserves a regression test independent of plan parity: force a bitmap
scan over a relation big enough to go lossy and compare against the seq-scan
result. Such a test would have caught this long before a plan-shape census did.


## 13 The over-selection has a systematic cause: correlation is 0 everywhere

With the bitmap executor holes fixed (`5341d652a`) and the double-count cost fix
applied, all five of PG's bitmap queries get bitmap scans — but goopg selects
**27** bitmap scans against PG's 6. That over-selection is not a bitmap problem.
It is this:

```
goopg  pg_stats.correlation          PG
  lineitem.l_orderkey   (empty)      0.20565678
  lineitem.l_partkey    (empty)      0.0027776298
  orders.o_custkey      (empty)     -0.008831654
  supplier.s_nationkey  (empty)      0.030710574
```

**Correlation is computed, reaches the planner, and is LOST ACROSS A RESTART.**
That is the corrected form; the first draft of this section said "goopg stores
no correlation statistic at all", which the measurements below disprove.
`indexCorrelationFor` (costindex.go) reads `stats.Correlation` correctly, and on
a restarted server the value it reads is always zero: And `csquared = correlation²` is what `cost_index` interpolates
between its two I/O bounds:

```go
csquared := in.correlation * in.correlation
runCost += maxIOCost + csquared*(minIOCost-maxIOCost)
```

With `csquared = 0` every index scan in goopg is priced at **`max_IO_cost`** —
the perfectly-uncorrelated case, every heap page a random seek. PG prices the
same scans partway toward `min_IO_cost`. So goopg's index probes are
systematically overpriced, and any rival that does not pay per-row random
fetches — a bitmap scan, a seq scan — wins more often than it should.

This is a bigger finding than the bitmap work that led to it: it affects **every
index cost decision in the planner**, not just the bitmap comparison. §1 recorded
"`indexCorrelationFor` returns 0 for every index goopg has today" as a fact
about goopg's indexes; it is actually a missing statistic.

### 13.1 Settled by measurement, in two steps

Both candidates were checked rather than argued.

**The `pg_stats` reading was a red herring.** `pgstats_e2e_test.go` asserts
`correlation` is NULL in that view by design — "a slot goopg does not collect"
— so the table above was measuring the VIEW, not the statistic. That comment is
now stale, per what follows.

**Printing what the planner actually reads** gives `corr=0` for every index on
the running (restarted) server — so the statistic really is zero there, and §13's
conclusion about `max_IO_cost` stands.

**But an in-session `ANALYZE` restores it:**

```
ANALYZE nation; ANALYZE supplier;  -- then EXPLAIN in the SAME session
CORR idx=nation_pk               corr=1
CORR idx=nation_regionkey_fkidx  corr=0.3553846153846154
CORR idx=supplier_nation_fkidx   corr=-0.017069080910690808
CORR idx=supplier_pk             corr=1
```

So ANALYZE computes correlation correctly and it does reach
`indexCorrelationFor` — it is **not persisted or not restored**. That is
precisely the shape of the `NDistinct`-vs-`NDistinctFrac` split in §9, where the
absolute form was lost across a restart and only the scale-free one survived; the
same durable-sidecar path is the place to look.

**Why this matters beyond bitmap.** Every cost comparison in this document was
made on a restarted server, i.e. against index scans priced at `max_IO_cost`.

### 13.2 Fixed (`b48008455`), and it retired the trade

The persisted `pg_statistic` row already had `stakind3`/`stanumbers3` columns,
written as 0/NULL. Correlation now rides there as PG's
`STATISTIC_KIND_CORRELATION` (3) — a one-element `stanumbers` array with no
`stavalues`, which is PG's layout for that slot — with the decoder in
`catalog/codec.go` and the startup reload in `initdb/open.go`.

Verified end to end rather than by inspection: ANALYZE, restart, then print what
`indexCorrelationFor` reads — `nation_pk` 1, `nation_regionkey_fkidx` 0.355,
`supplier_nation_fkidx` -0.017, identical to the in-session values and
previously all zero.

**Result: Bitmap Heap Scan 1 -> 6, matching PG's count**, with four of PG's five
bitmap queries covered (Q11, Q17, Q20, Q21), 21/21 result sets byte-identical,
and **every TPC-H query the same speed or faster** — Q3 7.2 s -> 3.6 s.

And it retired §11.4's trade outright. That section recorded the double-count
cost fix as buying PG's bitmap targets at the price of q21 +37% and q08 2x.
Those measurements were taken against index scans priced at `max_IO_cost`. With
correlation restored, bitmap reaches PG's count **with no cost fix and no
slowdown at all** — so the trade was an artefact of the missing statistic, not a
real choice. The prediction in the first draft of this section, that "the trade
those numbers describe may not exist", held.

That is the strongest form of this document's recurring lesson: a calibration
conclusion is only as good as the statistics underneath it, and four rounds of
bitmap cost analysis were conducted on top of a missing one.

### 13.3 Two errors that cancel

Re-running the double-count fix on top of the restored correlation gives a
result worth stating carefully. It now covers all FIVE of PG's bitmap queries,
Q2 included — but it selects **24** bitmap scans against PG's 6, where leaving
it out selects exactly **6** and covers four of the five.

So the oracle-correct change makes goopg's aggregate behaviour diverge FURTHER
from PG. That is only possible if **another term is under-charging bitmap and
the double charge was compensating for it**. The two errors currently cancel,
which is why today's count matches PG's for the wrong reason.

Removing either alone makes things worse; both have to move together. The places
to look, against `cost_bitmap_heap_scan` (costsize.c): goopg charges
`cpu_tuple_cost` per tuple where PG additionally charges `qpqual_cost` per tuple
and a `cpu_operator_cost` per bitmap entry, and `computeBitmapPages` may differ
from `compute_bitmap_pages` on the `maxentries` / lossy-degradation path.

The general form is worth keeping: **when fixing a verified bug moves a system
further from its reference, look for the second bug it was cancelling** rather
than reverting the fix and calling the reference wrong.

### 13.4 The qpqual term is real but far too small — NEGATIVE RESULT

§13.3 named `qpqual_cost.per_tuple` as the missing counterweight. It is genuinely
missing — PG computes `cpu_per_tuple = cpu_tuple_cost + qpqual_cost.per_tuple`
and goopg charged `cpu_tuple_cost` alone — but implementing it and landing it
together with the double-count removal leaves the census at **24** bitmap scans,
unchanged from removing the double count on its own.

The arithmetic says why: `qualEvalCost` is
`cpu_operator_cost x numQuals x tuples`, and the recheck lists here hold one or
two clauses. Against page costs in the hundreds, one or two `cpu_operator_cost`
units per tuple is noise. So the qpqual gap is worth fixing for fidelity, but it
is **not** what the double charge was compensating for, and §13.3's hypothesis
is only half right: the two errors do cancel, but the second one is still
unidentified and is much larger than this.

The state left on the branch is therefore the double charge RETAINED — which
gives Bitmap Heap Scan 6, PG's count, on four of PG's five queries — with both
the double-count removal and the qpqual term written up but unlanded. Anyone
resuming should look for a term of the same order as `indexTotalCost` itself,
not for more per-tuple CPU.

## 14. Q8 was never a costing bug — it was candidate generation

§13's whole line of enquiry assumed the Q8 bitmap-vs-index mismatch was a cost
calibration problem, and looked for "a term of the same order as
`indexTotalCost`". That assumption was wrong, and it was wrong for four rounds
because every round reasoned about the cost functions instead of printing what
the two producers actually emitted.

Printing both candidates' `Cost{Startup, Total}` for `lineitem` under every Q8
parameterisation settles it in one run:

| parameterisation | bitmap | parameterised index | `addPath` |
|---|---|---|---|
| `{part}` | 4.66..116.49, 30.7 rows | **none generated** | bitmap kept |
| `{part,supplier}` | 4.50..13.01, 1 row | 0.50..8.49, 1 row | index kept, **bitmap rejected** |
| `{part,supplier,orders}` | 4.50..13.01, 1 row | 0.50..8.49, 1 row | index kept, **bitmap rejected** |

At every parameterisation where both producers emitted a path, the index path
was already strictly cheaper on both axes and `addPath`'s dominance pruning
already discarded the bitmap. The bitmap survived in exactly one place: the
parameterisation where the index producer emitted **nothing at all**.

### 14.1 The asymmetry

`addOneParameterizedIndexPath` required that EVERY column of the index be bound,
via `pickIndexCoveringAllLeadingColumns`. `lineitem_part_supp_fkidx` is
`(l_partkey, l_suppkey)`; at `req={part}` only `l_partkey` is bound, so the
index producer declined and the bitmap producer — which never had the
restriction — was the only candidate left.

The restriction was documented, in three places, as an executor limitation:

> a multi-column index qualifies only when EVERY one of its columns is bound,
> because a partial-prefix probe is not a shape goopg's executor emits

That is false, and had been false since M0053-0001. `operators_index.go`'s
single-`Key` branch has always padded a one-column probe on a composite index
with `compositeUpperBound`, turning it into a range over every entry sharing the
prefix — which is precisely a prefix probe. Only the planner refused to ask for
one. `pathindexclauses.go` even anticipated the arm by name ("if a later
producer (PG's `amoptionalkey`/prefix-probe arm) forgets the precondition").

### 14.2 What landed

PG requires only that the LEADING index column have a qual
(`build_index_paths`, indxpath.c:1029-1076; `amoptionalkey`). Five coordinated
changes, since the rule was stated redundantly in five places:

1. **`operators_index.go`** — `lookupKeys` accepts a short key list, and the
   multi-key branch pads `hiBytes` with `compositeUpperBound` when the list is
   shorter than the index. A FULL key must *not* be padded: it is a point
   lookup, and padding it would widen the scan to every duplicate of the whole
   key.
2. **`indexPathClauses`** — TRUNCATES at the first unbound column instead of
   declining the index. A list that binds nothing at column 0 is still declined:
   a btree cannot begin a scan below its leading column, and `Keys[i]` binds
   `Index.Columns[i]` positionally, so a gapped list would probe the wrong
   column.
3. **`pickIndexCoveringAllLeadingColumns` → `pickIndexCoveringLeadingPrefix`** —
   renamed because the old name became a false statement. Its ranking moved from
   *index width* to *bound-column count*; those were the same number under total
   coverage, and ranking on width would now prefer a wide index bound only on
   its leading column over a narrow one bound completely.
4. **`createplanindex.go`** — the `binds N of M ... not a shape the executor
   emits` panic became a `> ncols` guard.
5. **Selectivity split** — the probe's selectivity now comes from the bound
   PREFIX, while `ppi_rows` still comes from every movable clause, which is
   PG's own split (`cost_index`'s `indexSelectivity` vs
   `get_parameterized_baserel_size`). And the `idx.Unique` short-circuit is now
   gated on full binding: a unique index on `(a, b)` says nothing about how many
   rows share one value of `a`, and claiming one row there would have made every
   nested loop above it look free.

Result: Q8's `lineitem` scan is now `Index Scan using lineitem_part_supp_fkidx`
with `Index Cond: (l_partkey = p_partkey)`, rows=30 — PG's plan — and it won on
cost (112.65 vs the bitmap's 116.49), with both candidates generated and
compared. Exactly two plans changed across the 21 queries; 21/21 result sets
stayed byte-identical and both changed queries timed the same.

### 14.3 Q17: the node that changed is not the node PG puts a bitmap on

Q17's raw census reads as a regression — goopg's Bitmap Heap Scan count went
6 → 4 while PG has 6. Comparing the plans NODE BY NODE says otherwise, and the
first framing of this section (a "1% cost near-tie that goopg falls on the wrong
side of") compared two different nodes. Corrected:

Q17 has two `lineitem` scans in each engine.

| position | PG 18.3 | goopg before | goopg after |
|---|---|---|---|
| join input | Seq Scan under a **Hash Join** | **Bitmap Heap Scan** as nested-loop inner, rows=1 | **Index Scan**, `Index Cond: (l_partkey = p_partkey)`, rows=30 |
| `SubPlan 1` | **Bitmap Heap Scan**, `Recheck Cond: (l_partkey = part.p_partkey)` | Index Scan, `l_partkey = $0` | Index Scan (**unchanged**) |

The bitmap this change removed sat at the JOIN INPUT, where PG has no bitmap —
PG hash-joins there. And PG's bitmap, in `SubPlan 1`, faces a goopg Index Scan
both before and after; that mismatch is real, separate, and untouched here.

So the removed bitmap was never a match. It was a *coincidence* of the aggregate
count, which is exactly the failure mode of counting node types instead of
comparing positions — the same lesson as "a row-count gate cannot catch a
plan-shape regression", one level up.

Two things at the join input got strictly better, both visible in the plan text:

- The join predicate `p_partkey = l_partkey` was a **post-join `Filter:`** and is
  now an `Index Cond:`. The nested loop no longer re-checks the join key on
  every probed row; the probe enforces it.
- The inner's estimate moved from **rows=1 to rows=30**. Thirty is right —
  PG independently estimates 31 for the same access. The old `rows=1` came from
  the `idx.Unique` short-circuit firing on a partially-bound index, which §14.2
  item 5 gates.

The genuine open question is therefore the SUBPLAN node, and PG's own numbers
show how fine it is. Asking the oracle to price the alternative there
(`SET enable_bitmapscan=off`):

| PG 18.3, `SubPlan 1` | startup | total |
|---|---|---|
| Bitmap Heap Scan (**chosen**) | 4.67 | **127.62** |
| Index Scan | 0.43 | **128.97** |

PG separates them by 1.0%. goopg picks the index scan there. Whether goopg's
costs for THAT node bracket the same way has not been measured — the `PAIR`
trace in §14 covered the DP's parameterised candidates, and a correlated
SubPlan's scan is parameterised from an outer query level, which is a different
seam. Measuring it is the follow-up, and it must instrument the SubPlan node
specifically rather than reuse §14's numbers, which belong to the join input.

### 14.4 The methodological point

This is the fifth wrong hypothesis in a row about Q8, and the first round that
measured instead of reasoning. Every earlier round asked "which cost term is
wrong?" — a question that presupposes both candidates existed. The one-line
instrumentation that would have refuted the premise (print every path each
producer emits, with its parameterisation) was cheap and available from the
start.

Generalised: **before comparing two candidates' costs, verify both candidates
were generated.** A cost model can only choose among the paths it is offered,
and an absent path is indistinguishable from an infinitely expensive one at
every point downstream. This is the same shape as the census bug in §9 (the
labeller, not the planner) and the memory note "in a cost-based planner, a path
the optimiser never selects has never been executed" — three instances now of a
missing thing being mistaken for a wrong thing.
