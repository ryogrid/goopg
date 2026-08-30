# PG plan parity — what is actually missing

**Status:** **survey only — no planner code has been changed.** §7 says why.
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

### C — Nested Loop under-preferred (1 vs 25)

Parameterised inner index paths **are** generated —
`addParameterizedIndexPaths` (`pathparamindex.go:220`), live since M0127-P5.9,
reached from `pathindexordered.go:50`; goopg's Q22 emits one under a Nested Loop
Anti Join. So existence is settled. The open question is why that arm loses to
hash 23 times out of 25, and §4-B's hardcoded 1-row estimate is a prime
suspect: an inner priced at 1 row makes the *loop* look cheap but also makes the
index scan's own cost degenerate, and the same constant feeds join costing.

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

**No planner code has been changed**, and the corrected diagnoses in §4 are why
that is the right call rather than a shortfall to apologise for: the work is
larger than the earlier draft implied, and in three of four cases it is *not*
the work that draft named.

- A is relocating an existing coverage check from a top-of-plan peephole into
  per-relation path generation.
- B is adding parameterised bitmap paths **and** fixing a cardinality constant
  that affects every keyed index scan in the system.
- C shares B's cardinality dependency.

In this repository a planner change carries the project's most expensive failure
mode — silent row-count regressions, 608 anchors, documented multi-loop
bisects — so the gate is all 22 TPC-H queries plus the TPC-DS SF0.5 sweep, not
the query being targeted. Landing any of the above half-gated would be worse
than landing nothing. [TODO.md](TODO.md)'s implementation boxes are all
unticked.

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
