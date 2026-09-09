# R31 — parallelism is the dominant axis, and it is blocked on heap density

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
INVESTIGATION round: no code change. It ends in a decision that is the
owner's to take (§6), so it is filed as findings rather than a design
whose implementation I could start.*

## 1. Why parallelism, not K34 (join-method)

R30 filed join-method as the top blocker on category count (TPC-DS 79).
Before designing for it, the parallel structure was censused across all
99 TPC-DS queries against the PG fixtures:

| node | goopg (default) | PG 18.3 |
|---|---|---|
| `Gather` | 42 | 184 |
| `Gather Merge` | 4 | 85 |
| `Parallel Seq Scan` | 41 | 348 |
| **`Parallel Hash`** | **0** | **314** |
| `Partial` / `Finalize` | 16 / 16 | 49 / 49 |

**66 of 99 queries: PG produces a parallel plan and goopg does not.
Zero go the other way.** That single divergence necessarily shows up as
`join-method` (Parallel Hash Join vs Hash Join), `scan-type` (Parallel
Seq Scan vs Seq Scan), `aggregation-strategy` (Partial/Finalize) and
`sort-strategy` (Gather Merge) at once — so the four largest categories
after join-order are substantially ONE root cause, not four.

## 2. The machinery exists and is switched off

`gatherpaths.go` / `joinpathsparallel.go` / `parallel_hash_build.go`
implement `generate_useful_gather_paths` and parallel hash. Admission is
`GOOPG_GATHER_PATHS`, default `off`. Measured at `all`:

| node | off | all | PG |
|---|---|---|---|
| `Gather` | 42 | 104 | 184 |
| `Parallel Seq Scan` | 41 | 103 | 348 |
| `Parallel Hash` | 0 | **167** | 314 |

So C-19f landed: joinrels do get partial paths, and parallel hash joins
appear. But parity does **not** improve — `parallelism` stays at exactly
90, `join-method` worsens 79 -> 82, `qual-placement` 13 -> 16. Flipping
the flag is not the win on its own, and this round does not propose it.

## 3. The worker counts are wrong, and the ladder is not why

| Workers Planned | goopg (`all`) | PG |
|---|---|---|
| 1 | 9 | 35 |
| 2 | 0 | 2 |
| 3 | 46 | 147 |
| **4** | **49** | **0** |

PG never plans 4 workers on this dataset; goopg does, 49 times.

Both of goopg's `compute_parallel_worker` transcriptions were read
against `allpaths.c:4300-4322` and both are faithful — the post-pass
(`parallel.go:computeParallelWorkers`) and the path-model twin
(`considerparallel.go:parallelWorkerLadder`) implement the same log3
ladder. The ladder is correct. Its **input** is not.

PG's rung for 4 workers is `pages >= 27648` (`min_parallel_table_scan_size`
1024, x3 three times):

| | relpages | rung |
|---|---|---|
| goopg `store_sales` | **29761** | >= 27648 -> **4** |
| PG `store_sales` | 25928 | < 27648 -> 3 |

Same rows (1,439,608), same schema, different physical size.

## 4. Root cause: the benchmark heap is stale, not the code

goopg's `store_sales` holds exactly **48 tuples per page**, on every
page; PG holds 55-56. Four hypotheses were tested and three were
refuted by measurement:

- **Tuple encoding** — refuted. A 4-column `numeric(7,2)` table packs
  **136 rows/page in BOTH** engines; a 4-column `int` table packs
  **185 in BOTH**. goopg's numeric/int on-disk layout is PG-identical.
- **`fillfactor` reloption** — refuted. `(none)` on both; goopg's
  `HeapDefaultFillfactor` is 100, matching `HEAP_DEFAULT_FILLFACTOR`.
- **`COPY` underfills** — refuted. The same 20k rows dumped from PG and
  COPYed into a fresh table give **56 rows/page in BOTH**.
- **NULL handling** — refuted as the explanation of the magnitude:
  ~4.5% of values per column are NULL, far too few for a ~22 byte/row
  gap.

What is true: re-inserting the table's own rows with the CURRENT binary
packs them correctly.

```
goopg store_sales as it sits on disk        relpages = 29761   (48 rows/page)
goopg CREATE TABLE AS SELECT * FROM it      relpages = 25830   (55 rows/page)
PG    store_sales                           relpages = 25928   (55-56 rows/page)
```

Current goopg reproduces PG's physical density to **0.4%**. The
benchmark data was loaded by an OLDER binary whose packing differed,
and `VACUUM FULL` does not rewrite the heap in goopg, so the artifact
is frozen into the cluster.

## 5. Why this matters beyond worker counts

`relpages` is not only `compute_parallel_worker`'s input. It is
`cost_seqscan`'s. Every fact-table scan in every TPC-DS plan on this
cluster is priced against a heap **~15% larger than PG's**, and that
error propagates into every join comparison made against it. Every cost
A/B this workstream has run on TPC-DS — including R30's — carries it.

The dimension tables lean the other way (`item` 736 pages vs PG's 1284,
`customer` 2046 vs 2872), so the distortion is not a uniform scale
factor that cancels: it systematically makes goopg's facts look bigger
and its dimensions smaller than PG's.

## 6. The decision this round ends on (owner's call)

The remedy is to **reload the goopg TPC-DS benchmark data with the
current binary**, then re-pin the affected anchors. This was NOT done
unilaterally:

- it rewrites shared benchmark state under `bench/tpcds/`, which the
  SF0.5 gate and its git-tracked oracle depend on;
- the row-count anchors are load-dependent by documented policy
  (`CLAUDE.md`), so a reload obliges re-pinning;
- it costs roughly an hour per cluster and would invalidate every
  stored plan capture in this directory as a comparison baseline.

Recommended sequence once approved: reload SF0.5 -> re-ANALYZE under the
pinned seed -> re-capture both corpora -> re-baseline the category
counts -> only then resume costing rounds. Until then, TPC-DS category
counts should be read as measured against an inflated-fact-table
cluster.

TPC-H should be checked the same way before its numbers are trusted;
its cluster was loaded by HammerDB on an unrelated date.

## 7. Not investigated (recorded, not silently dropped)

- Whether goopg's `VACUUM FULL` is meant to rewrite the heap. It
  accepted the command and changed nothing; that is a defect or a
  documented no-op, and this round did not establish which.
- `round(double precision, int)` resolves on goopg and does NOT exist
  in PG 18.3 (it errored on the oracle). A goopg-only function overload
  is a PG-compat divergence in its own right; filed here because it was
  found here, not because it belongs to this round.
