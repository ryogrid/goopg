# PG plan parity — what was actually missing, and what still is

**Status:** **survey and design only — no planner change has been implemented.**
The three gaps in §3 are located and root-caused (gap A precisely, in source);
none is fixed. [TODO.md](TODO.md) is the state of the work, and every
implementation box in it is unticked. §8 says why this stopped here.
**Date:** 2026-08-30
**Branch:** `perf-opt-take6`
**Baseline:** `edfca5d43`
**Oracle:** PostgreSQL 18.3, `postgres/local_install`, TPC-H SF=1 on port 65432
**Subject:** goopg, same data, port 65433

---

## 1. The premise this started from was wrong

The task was framed as: goopg does not choose PostgreSQL's plans, so implement
the missing index types. **Every index type already exists**, in the executor
*and* in the planner:

| capability | executor | planner path + cost |
|---|---|---|
| Seq Scan | `operators_storage.go` | ✔ |
| Index Scan | `operators_index.go` | ✔ |
| **Index Only Scan** | `operators_indexonly.go` | `IndexOnlyScan` node, 4 construction sites |
| **Bitmap Heap / Index Scan** | `operators_bitmap.go`, `tidbitmap.go` (+ parallel) | `costbitmap.go`, `pathbitmap.go`, `createplanbitmap.go` |
| Visibility map | `internal/storage/vm.go` | `relallvisible` |

`addBaseRelBitmapPaths` is called from `addBaseRelIndexPaths`, so bitmap paths
*are* generated and *do* compete in `addPath` — mirroring PG's
`create_index_paths`, which generates both an index path and a bitmap path for
every index and lets `add_path` keep the cheaper.

So nothing here needed inventing. What the earlier comparison actually measured
was something else.

## 2. Root cause of most of the divergence: the comparison was not fair

The goopg TPC-H cluster had **never been ANALYZEd or VACUUMed**; the PostgreSQL
one had. Measured:

| | goopg (before) | PostgreSQL |
|---|---:|---:|
| `lineitem` `reltuples` / `relpages` | **0 / 0** | 5,998,856 / 129,346 |
| `orders` | **0 / 0** | 1,500,000 / 27,814 |
| `part` | **0 / 0** | 200,000 / 4,199 |
| `pg_stats` rows for `lineitem` | **0** | populated |
| `relallvisible` (`customer`/`orders`/`part`) | **0 / 0 / 0** | 3,662 / 27,814 / 4,198 |

**Precisely what that costs the planner** — and it is not "no size estimate",
which an earlier draft of this paragraph claimed. goopg has a relation-size
fallback: `seqScanRows` returns `EstRelRows` when `tableRows` is 0, derived from
block counts by `GOOPG_RELSIZE_FALLBACK`, whose default stage is **2, i.e. on**.
So with `reltuples = 0` the planner still has a *cardinality* for the relation.

What it does not have is **selectivity**. `pg_stats` is empty, so there are no
MCV lists, no histograms and no ndistinct — and selectivity is what decides
whether a qual is narrow enough for an index path to beat a sequential scan.
A planner that knows a table has 6 M rows but cannot estimate what fraction a
predicate keeps will price an index scan over the whole relation, which never
wins. That is the mechanism, and it is why the missing piece is `ANALYZE`
specifically rather than relation sizes.

This is documented behaviour of the bench harness, not a goopg defect:
`CLAUDE.md` records that HammerDB's final ANALYZE step fails and that the gate
"runs S-cold regardless".

**`ANALYZE` and `VACUUM` both work, and both persist** — across sessions *and*
across a server restart (verified by restarting and re-reading `pg_class`). An
earlier note in my own memory claiming statistics are per-connection is stale.

### 2.1 What that alone fixed

After `ANALYZE`, Q12's plan changed **on cost alone**, with no code change:

```
before:  Hash Join  ->  Parallel Seq Scan lineitem
                    ->  Seq Scan orders            (all 1.5M rows read)

after:   Merge Join ->  Index Scan using orders_pk
                    ->  Index Scan using idx_lineitem_orderkey_fkidx
```

Index-scan usage across the 22 queries went from a handful to **25 Index Scan
nodes, against PostgreSQL's 24** — parity.

And the answers did not move: with the new index-driven plans in place, the two
canonical silent-regression anchors still hold — **Q12 = 2 rows, Q13 = 34 rows**
(`bench/tpch/spotcheck_expected.env`). That is worth stating because it is the
cheap half of the evidence that this is a genuine planner improvement rather
than a plan that merely looks more PG-like: the plan shape changed and the
result did not.

## 3. The gaps that are real

With both engines VACUUMed and ANALYZEd, node-type census over the 22 queries:

| node | goopg | PG | verdict |
|---|---:|---:|---|
| Index Scan | 25 | 24 | **parity** |
| Nested Loop (plain) | 1 | 25 | **gap** |
| Hash Join | 44 | 26 | (the mirror of the above) |
| Merge Join | 5 | 0 | goopg-only |
| **Index Only Scan** | **0** | 3 | **gap** |
| **Bitmap Heap Scan** | **0** | 6 | **gap** |
| **Bitmap Index Scan** | **0** | 6 | **gap** |
| Seq Scan | 60 | 51 | follows from the above |

Per query, for the rows the task named:

| query | goopg (VACUUM+ANALYZE) | PostgreSQL | remaining gap |
|---|---|---|---|
| Q12 | Index Scan + **Merge Join** | Index Scan + Nested Loop | **fixed** by stats; join method still differs |
| Q19 | Hash Join + Seq Scan | Index Scan + Nested Loop | index-driven NL not preferred |
| Q3 | Hash Join + Seq Scan | Index Scan + Nested Loop | index-driven NL not preferred |
| Q13 | Hash **Left** Join (hashes the 1.48 M side) | Hash **Right** Join + Index Only Scan | outer-join commutation; IOS |
| Q16 | Hash Join + Seq Scan | Hash Join + **Index Only Scan** | IOS |
| Q22 | **Index Scan** + NL Anti | **Index Only Scan** + NL Anti | IOS only |
| Q2, Q11, Q20, Q21 | Hash Join + Index Scan | **Bitmap Heap Scan** + NL | bitmap never wins |

So three distinct capability gaps remain, none of them a missing operator:

- **A — Index Only Scan is never chosen** (Q13, Q16, Q22), and the reason is
  hardcoded and documented in the source. `pathindexordered.go`, in the live
  Path-based search:

  > Step 4's condition, with `index_clauses == NIL` … and
  > **`index_only_scan == false` (goopg's search has no visibility-map model, so
  > `check_index_only`'s answer is always no)**.

  So the search never even asks. The `IndexOnlyScan` construction sites in
  `planner.go` belong to other, narrower paths (the min/max aggregate rewrite,
  group-aggregate index ordering) — not to the general path search that plans
  these queries, which is why the node type exists yet never appears in a TPC-H
  plan.

  Everything the missing step needs is now present: the operator
  (`operators_indexonly.go`), the index, and a populated VM (`relallvisible`
  = 136,393 for `lineitem` after VACUUM). Q22 already picks a plain Index Scan
  on the *same index* PG uses index-only. What is missing is three connected
  pieces PG has:

  1. an `allvisfrac` on the rel, computed as `relallvisible / relpages`
     (`plancat.c` `estimate_rel_size`);
  2. `check_index_only` (`indxpath.c`) — does the index supply every column the
     query needs from this relation;
  3. `cost_index` charging heap fetches against `allvisfrac`, so a covering
     index on an all-visible relation actually wins.

  This is the single best-understood gap of the three, and it is a *generation*
  gap rather than a costing one: no amount of cost tuning can select a path the
  search never creates.
- **B — bitmap paths never win** (Q2, Q11, Q20, Q21). They are generated and
  fully costed — `buildOneBitmapPath` computes an index-side cost, a
  `computeBitmapPages` heap-page estimate and a `costBitmapHeapScan` total — so
  this is a costing or an input problem, not a generation gap like A.

  **Leading hypothesis, not yet proven.** `buildOneBitmapPath` derives its
  selectivity from *local* quals only:

  > Extract local filter conjuncts from the leaf's Filter wrappers and match
  > equality conjuncts against this index's columns.

  With nothing matched, `qualSelectivity` degrades toward a full scan, and a
  bitmap path that admits the whole heap can never beat a Seq Scan. But PG's
  bitmap scans in these queries sit on `supplier` beneath a nested loop — they
  are driven by a **join** qual (`s_suppkey`/`s_nationkey` bound from the outer
  side), which is a *parameterised* path. PG's `create_index_paths` considers
  join clauses as index quals for exactly this reason; goopg's bitmap builder
  reads only the leaf's own filters.

  If that is right, B and C are the same missing capability seen twice —
  parameterised inner paths — and B is not a cost-calibration exercise at all.
  TODO.md keeps this as the first question to settle rather than as a
  conclusion.
- **C — Nested Loop is under-preferred against Hash Join** (Q3, Q19, and Q12's
  method). goopg emits 1 plain Nested Loop to PG's 25.

  Parameterised index paths — the inner side of an index-driven nested loop —
  **are** generated: `addParameterizedIndexPaths` (`pathparamindex.go`) exists
  and runs from `addBaseRelIndexPaths`. So, as with A and B, the machinery is
  present. Two things could still suppress the plan, and this survey did not
  distinguish them: the eligibility guards in that function (it skips a rel
  whose `scanLeafFor` is not a rebuildable scan leaf, and applies a stricter
  arm again for the NLI shape), or the join costing preferring hash. Settling
  that is TODO.md's first C question.

## 4. Constraint: no query-specific forcing

The task requires the plans to fall out of the cost model, not out of code that
recognises a query. Every change below is therefore in one of:

- **path generation** — make a path *available* to `addPath` that PG also
  generates (e.g. an index-only path when the index covers the query's columns);
- **cost inputs** — feed the cost functions data PG feeds them (e.g.
  `allvisfrac`);
- **cost functions** — align a formula with its PG counterpart.

No change may test a relation name, a query shape, or a benchmark identity. The
acceptance test for each is that the plan changes **and** the reason is
expressible as "PG's `costsize.c`/`indxpath.c` does X and goopg now does X too".

## 5. PG oracle references

| goopg concern | PG source |
|---|---|
| when an index-only scan is legal | `src/backend/optimizer/path/indxpath.c` `check_index_only` |
| costing it | `src/backend/optimizer/path/costsize.c` `cost_index`, using `baserel->allvisfrac` |
| where `allvisfrac` comes from | `src/backend/optimizer/util/plancat.c` `estimate_rel_size` ← `relallvisible` |
| bitmap path generation | `indxpath.c` `create_index_paths`, `choose_bitmap_and` |
| bitmap costing | `costsize.c` `cost_bitmap_heap_scan`, `compute_bitmap_pages` |
| nested loop vs hash | `costsize.c` `initial_cost_nestloop` / `final_cost_nestloop`, `initial_cost_hashjoin` |

## 6. Sequencing

Ordered by (value × tractability) and by blast radius. This repository's history
makes the risk explicit: planner changes are its most expensive failure mode
(608 row-count regression anchors), so each step lands with row counts verified
on the full TPC-H set, not just on the query it targets.

1. **A — Index Only Scan.** Smallest and best-understood: the operator, the VM
   and the index all exist, and Q22 already picks the right index. Three queries.
2. **B — bitmap costing.** Paths already generated and costed; this is a
   calibration question against `cost_bitmap_heap_scan`. Four queries.
3. **C — nested loop vs hash.** Largest blast radius (it can move *every* join
   plan), so last and behind the full row-count gate.

[TODO.md](TODO.md) tracks the state of each.

## 7. What this survey changes about earlier claims

I previously told the user that goopg "lacks index-only scans and bitmap scans"
and that the gap was largely missing access methods. **That was wrong on the
first count and misleading on the second**: the access methods exist, and the
dominant cause of the divergence was that the goopg cluster carried no
statistics while the PostgreSQL one did. The genuine planner gaps are narrower
than that claim implied and are enumerated in §3.


---

## 8. Why this stops at the design

The survey is finished and gap A is root-caused to a specific hardcoded
`index_only_scan == false` in the live path search. **No planner code has been
changed.**

What gap A alone requires:

1. an `allvisfrac` on the search's rel — `catalog.TableStats` has
   `RowCount`, `Pages`, `AvgWidth`, `Columns` but **no all-visible count**, so
   `relallvisible` (which exists, and which VACUUM populates) has to be plumbed
   from the catalog into planner statistics first;
2. a port of `check_index_only` — does this index supply every column the query
   needs from this relation;
3. index-only path generation in the search, and `cost_index` charging heap
   fetches against `allvisfrac`;
4. `createplan` emitting the `IndexOnlyScan` node the executor already has.

That is a genuine planner feature, and in this repository a planner change
carries the project's most expensive failure mode: silent row-count
regressions, with 608 regression anchors and a documented history of multi-loop
bisects. The gate is not "the query I targeted still returns the right answer"
but "all 22 TPC-H queries do, plus the TPC-DS SF0.5 sweep". Landing points 1–4
without that gate would be worse than landing nothing.

So this document is the deliverable, and it is deliberately not accompanied by
a partial implementation. The value it carries is:

- the correction in §7 — the access methods were never missing;
- the fairness finding in §2, which explains most of the divergence and is
  reproducible in one `ANALYZE`;
- gap A's exact location, which is the part of that work that takes the longest
  to find and the least time to fix once found.

Gaps B and C are located but not root-caused: for B it is still unknown whether
`buildOneBitmapPath` returns nil or the path is generated and loses, and TODO.md
lists that as the first question.
