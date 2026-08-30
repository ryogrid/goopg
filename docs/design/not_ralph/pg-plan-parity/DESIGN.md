# PG plan parity — what is actually missing

**Status:** three fixes landed and gated (`9db8a0970`, `80a5e334d`); a fourth
(`cost_index`'s `loop_count` arm) was implemented, measured, and **rejected** —
§9. Gaps A and B remain blocked on missing infrastructure. §7 says what and why.
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
`Project(Filter?(IndexScan))`, applied once to the finished plan
(`planner.go:1691-1704`). It therefore cannot reach a scan buried inside a join
tree — and all three of PG's TPC-H index-only scans are exactly that (Q13 hash
build side, Q16 parallel-hash-join input, Q22 nested-loop inner).

Note goopg's promotion consults **no visibility map**, which is the opposite of
what an earlier draft assumed. Making this a per-relation path-generation
decision is the work; porting a predicate is not.

**Blocker (verified 2026-08-30).** Even that framing understates it. The
peephole derives coverage from the **top-level `Project`'s target list** — that
is the only place it can learn which columns are still wanted. Inside a join
tree no such Project exists, so the question has nothing to read. PG answers it
from `rel->reltarget`, populated by `build_base_rel_tlists` /
`add_vars_to_targetlist` out of `baserel->attr_needed`. In goopg:

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

**Blocker (verified 2026-08-30).** A parameterised bitmap path would have **no
consumer**. `NestedLoopIndexJoin.Inner` is typed `*IndexScan`
(`plan.go:801`), and `pathparamindex.go` already documents the NLI constructor
as the *only* consumer of a parameterised path. Emitting a parameterised bitmap
path before that type is generalised would let the DP price a plan the plan
builder must refuse — the precise failure that file warns against. B therefore
needs a join-node change (plus confirmation that the bitmap executor rescans per
outer row) *before* `RequiredOuter` and `matchBitmapIndexQuals` are touched.

### C — Nested Loop under-preferred (1 vs 25)

Parameterised inner index paths **are** generated —
`addParameterizedIndexPaths` (`pathparamindex.go:220`), live since M0127-P5.9,
reached from `pathindexordered.go:50`; goopg's Q22 emits one under a Nested Loop
Anti Join. So existence is settled. The open question is why that arm loses to
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
- **A** needs per-relation attribute-usage analysis (PG `attr_needed` /
  `reltarget`) and an index-only *path* type. Neither exists; grep returns
  nothing for either. Not a relocation.
- **B** needs `NestedLoopIndexJoin.Inner` generalised beyond `*IndexScan`, or a
  parameterised bitmap path has no consumer and the DP prices unbuildable plans.
- **C** overlaps B rather than being independent: PG's six nested loops here are
  largely the *same* plans as its six bitmap scans (bitmap-heap inners in Q2,
  Q11, Q17, Q20, Q21), so C cannot be reproduced without B.

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

**Q2 went 2.0 s -> 87.3 s.** Not a costing artefact — the plan it switched to is
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

### 9.4 Decision

Reverted. Shipping a 43x regression on a benchmark query to gain plan-shape
parity on the others is the wrong trade to make silently, and the fix is not a
tweak to the arm — it is subplan cost placement, a separate piece of work.

The resurrection order is: (1) charge SubPlan evaluation over its row count in
`subplan_cost.go`, re-check that Q2 keeps its de-correlated plan or gets PG's
placement; (2) re-apply the `loop_count` arm; (3) re-run the 21-query byte gate
AND a timing pass — the byte gate alone would have passed this change.

**That last point is the transferable lesson.** All 21 result sets were
byte-identical and every unit test passed. A row-count gate cannot see a 43x
regression; only timing the changed queries caught it. Any future plan-shape
change needs both.
