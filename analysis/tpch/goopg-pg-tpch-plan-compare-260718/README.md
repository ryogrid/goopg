# goopg vs PostgreSQL 18.3 — TPC-H (SF1) Query-Plan Comparison

**Date:** 2026-07-18
**goopg commit:** `701a5f57` (`wal-body-and-ddl-log-pg-compatible`, built from a
clean worktree off HEAD)
**PostgreSQL oracle:** 18.3 (`postgres/local_install`, symlinked read-only)
**Scale factor:** 1 (HammerDB TPC-H, single power-test stream Q1–Q22)

This document records the execution plans that **goopg** and **vanilla
PostgreSQL 18.3** produce for the 22 HammerDB-style TPC-H queries at scale
factor 1, after loading the data, building the standard indexes, and running
`ANALYZE` on both engines. For PostgreSQL the queries were additionally
**executed**, and their wall-clock execution time and returned row counts are
recorded. goopg was inspected in **plan-only** mode (`EXPLAIN`), as requested.

> **Scope note.** This is a *plan-shape* study, not a benchmark. PostgreSQL
> timings are single warm-cache samples (see [Methodology](#methodology)); do not
> read them as throughput numbers. The authoritative goopg TPC-H benchmark
> harness remains HammerDB (`bench/tpch/run_power_test_goopg.sh`).

---

## 1. Environment and configuration

Both servers were configured with **identical, comparable GUCs** — the only
knobs deliberately set were `shared_buffers` and `work_mem` (per request);
everything else was left at each engine's default.

| Parameter | goopg (65433) | PostgreSQL 18.3 (65432) |
|---|---|---|
| `server_version` | `18.3 goopg compatible` | `18.3` |
| `shared_buffers` | **2GB** (256 K × 8 KiB slots) | **2GB** |
| `work_mem` | **512MB** | **512MB** |
| `effective_cache_size` | 4GB (default) | 4GB (default) |
| `random_page_cost` | 4 (default) | 4 (default) |
| `max_parallel_workers_per_gather` | 2 (accepted, **but not honored** — see §5) | 2 (default, honored) |
| Go runtime | `GOMEMLIMIT=12GiB`, `GOGC=off` | — |

**Configuration lineage / references.** The goopg `shared_buffers` value and the
Go-heap-arena model come from the established TPC-H bench harness and prior
records:

- [`analysis/tpch-shared-buffers-2000m-run.md`](../tpch-shared-buffers-2000m-run.md)
  — origin of the `shared_buffers≈2 GB` Go-heap arena (M0032-0001).
- [`bench/tpch/env_goopg.sh`](../../bench/tpch/env_goopg.sh) — goopg bench
  environment (`GOMEMLIMIT=12GiB`, `GOGC=off`, checkpoint suppression).
- [`bench/tpch/setup_goopg.sh`](../../bench/tpch/setup_goopg.sh),
  [`bench/tpch/setup_pg.sh`](../../bench/tpch/setup_pg.sh) — cluster provisioning
  templates (the stock `setup_pg.sh` uses `shared_buffers=512MB`; here it was
  raised to 2 GB to match goopg).
- Prior Q1–Q22 sweeps:
  [`analysis/m0093-q1-q22-regression-sweep.md`](../m0093-q1-q22-regression-sweep.md),
  [`analysis/tpch-power-test-0039-final.md`](../tpch-power-test-0039-final.md).

**Build isolation.** A concurrent code change was in flight in the main working
tree, so the goopg binary was built from a **git worktree checked out at the
clean HEAD `701a5f57`** to guarantee the captured plans reflect HEAD and not any
uncommitted work. The read-only `postgres/` submodule and `HammerDB-5.0/` were
symlinked into the worktree; goopg ran under the mandatory cgroup memory cap
(`scripts/goopg-test-run.sh`, `GOOPG_CG_UNIT=goopg-tpch-plan`).

---

## 2. Data load

Both databases were loaded with HammerDB TPC-H at SF1, then `ANALYZE`d and
`CHECKPOINT`ed. HammerDB generates data non-deterministically per virtual user,
so `lineitem` differs by ~0.007 % between the two independently-generated data
sets (benign for plan comparison).

| Table | goopg rows | PostgreSQL rows |
|---|---:|---:|
| region | 5 | 5 |
| nation | 25 | 25 |
| supplier | 10,000 | 10,000 |
| customer | 150,000 | 150,000 |
| part | 200,000 | 200,000 |
| partsupp | 800,000 | 800,000 |
| orders | 1,500,000 | 1,500,000 |
| lineitem | 5,997,678 | 5,998,089 |

**Indexes (identical 16 on both engines):** the 8 primary keys
(`region_pk … lineitem_pk`) plus the HammerDB foreign-key indexes
(`customer_nation_fkidx`, `idx_lineitem_orderkey_fkidx`,
`lineitem_part_supp_fkidx`, `nation_regionkey_fkidx`, `order_customer_fkidx`,
`partsupp_part_fkidx`, `partsupp_supplier_fkidx`, `supplier_nation_fkidx`).

---

## 3. Methodology

- **goopg plans** — captured with `cmd/tpch-runner --explain` (one fresh
  connection per query; Q15's `CREATE VIEW`/main-`SELECT` split handled
  specially). Raw: [`raw/goopg_explain.txt`](raw/goopg_explain.txt).
- **PostgreSQL plans** — plain `EXPLAIN` per query. Raw:
  [`raw/pg_explain.txt`](raw/pg_explain.txt). Instrumented
  `EXPLAIN (ANALYZE, BUFFERS)` in [`raw/pg_explain_analyze.txt`](raw/pg_explain_analyze.txt).
- **PostgreSQL execution** — each query run to completion (rows streamed to
  `/dev/null`) with `\timing`; row counts counted authoritatively from the
  result stream. Raw timing: [`raw/pg_exec_timing.txt`](raw/pg_exec_timing.txt);
  merged: [`raw/pg_time_rows.tsv`](raw/pg_time_rows.tsv).
- Query texts are byte-identical on both engines, dumped from the single
  canonical source `internal/testutil/tpch` into [`queries/`](queries/).
- **Timing caveat:** each `\timing` run was immediately preceded by an
  `EXPLAIN ANALYZE` of the same query, so shared buffers were warm — the numbers
  are indicative single samples, not benchmark measurements.

---

## 4. Per-query comparison

`Rows` and `Time` are PostgreSQL's executed results. goopg was not executed.
"‖" in the parallel column marks a PostgreSQL parallel plan (`Gather` +
`Workers Planned: 2`). Full plans: [`raw/`](raw/).

| Q | PG rows | PG time (ms) | PG ∥ | goopg plan (top → leaves) | PostgreSQL plan (top → leaves) |
|---|---:|---:|:--:|---|---|
| 1 | 4 | 1749 | ‖ | Sort → GroupAggregate(2) → **Seq Scan** lineitem | Finalize GroupAggregate → Gather Merge → Partial HashAggregate → **Parallel Seq Scan** |
| 2 | 474 | 256 | ‖ | Sort → Hash Join *(=subquery)* → NL chain region→nation→supplier→partsupp (index) + Seq Scan part | Sort → Hash Join (correlated **SubPlan** min-cost) → NL, Gather part, bitmap supplier |
| 3 | 11,707 | 364 | ‖ | Sort → GroupAggregate(3) → **Multi-Way Hash Join (3)** seq scans | Sort → HashAggregate → Gather → NL(Parallel Hash Join orders×customer, index lineitem) |
| 4 | 5 | 188 | ‖ | Sort → GroupAggregate → Seq Scan orders *(Exists filter)* | Finalize GroupAggregate → Gather Merge → **Nested Loop Semi Join** (parallel orders + index lineitem) |
| 5 | 5 | 340 | ‖ | Sort → GroupAggregate → NL/Hash tree, seq lineitem+orders, index nation/supplier/customer | Sort → Finalize GroupAggregate → Gather Merge → Hash Join tree, parallel customer, index scans |
| 6 | 1 | 341 | ‖ | Aggregate → **Seq Scan** lineitem (range filter) | Finalize Aggregate → Gather → Partial Aggregate → **Parallel Seq Scan** |
| 7 | 4 | 429 | ‖ | Sort → GroupAggregate(3) → NL/Hash, Multi-Way Hash Join(3), index supplier/nation | GroupAggregate → Gather Merge → Hash Join(nation-pair join filter), NL, parallel customer |
| 8 | 2 | 205 | ‖ | Sort → GroupAggregate → deep NL/Hash tree, index scans, Seq Scan part | GroupAggregate → Gather Merge → Hash Join, deep NL, parallel part, index scans |
| 9 | 175 | 907 | ‖ | Sort → GroupAggregate(2) → Hash/NL → **Multi-Way Hash Join (4)**, index partsupp, Seq Scan part(LIKE) | Sort → HashAggregate → Gather → NL, parallel part(LIKE), index partsupp/lineitem/orders |
| 10 | 20,005 | 670 | ‖ | Sort → GroupAggregate(7) → **Multi-Way Hash Join (4)** seq scans | Sort → HashAggregate → Gather → **Parallel Hash Join** chain + nation hash |
| 11 | 1,119 | 84 | — | Sort → GroupAggregate *(sum>subquery)* → Multi-Way Hash Join(3) seq scans | Sort → **InitPlan** agg → HashAggregate *(>InitPlan)* → NL, bitmap supplier, index partsupp |
| 12 | 2 | 897 | ‖ | Sort → GroupAggregate → NL(Seq orders + index lineitem) | Finalize GroupAggregate → Gather Merge → **Parallel Hash Join** (parallel lineitem+orders) |
| 13 | 35 | 1511 | — | Sort → GroupAggregate → GroupAggregate → **Hash Join (LEFT)** seq scans | Sort → HashAggregate → HashAggregate → **Hash Right Join** (seq orders + index-only customer) |
| 14 | 1 | 260 | ‖ | Aggregate → NL(Seq lineitem + index part) | Finalize Aggregate → Gather → **Parallel Hash Join** (parallel lineitem+part) |
| 15a (view) | 10,000 | 887 | ‖ | GroupAggregate → Seq Scan lineitem | Finalize GroupAggregate → Gather Merge → Partial HashAggregate → Parallel Seq Scan |
| 15b (main) | 1 | 1790 | ‖ | Sort → Hash Join *(=subquery max)* → Seq supplier + GroupAggregate→Seq lineitem | Nested Loop + **InitPlan** max-revenue → Finalize GroupAggregate *(=max)* → index supplier |
| 16 | 18,223 | 397 | ‖ | Sort → GroupAggregate(3) → Hash Join → NL(Seq partsupp+index part), Seq supplier | Sort → GroupAggregate → Gather Merge → **Parallel Hash Join** (parallel index-only partsupp + hashed NOT-IN SubPlan) |
| 17 | 1 | 1503 | ‖(sub) | Aggregate → NL *(l_qty<subquery)* (Seq lineitem+index part) | Aggregate → Hash Join + correlated **SubPlan** (bitmap lineitem), Gather part |
| 18 | 13 | **3739** | ‖ | Sort → GroupAggregate(5) → Hash Join → Multi-Way Hash Join(3) + GroupAggregate *(sum>313)* | Sort → HashAggregate → Gather → NL → **Parallel Hash Join** + sub-HashAggregate(>313) |
| 19 | 1 | 59 | ‖ | Aggregate → NL(Seq lineitem + index part) | Finalize Aggregate → Gather → Partial Aggregate → NL(parallel part + index lineitem) |
| 20 | 90 | 209 | — | Sort → Hash Join *(n_name=CANADA)* → NL(Seq supplier+index nation), Hash Join *(ps_availqty>subquery)* | Sort → **Hash Right Semi Join** → NL(Seq part forest% + index partsupp, correlated SubPlan), bitmap supplier |
| 21 | 445 | 859 | ‖ | Sort → GroupAggregate → **Multi-Way Hash Join (4)** *(Exists / NOT Exists filter)* | Sort → GroupAggregate → NL → **NL Semi Join** → Gather → **NL Anti Join** → Hash Join, bitmap supplier |
| 22 | 7 | 58 | ‖ | Sort → GroupAggregate → Seq Scan customer *(In + acctbal>subquery + NOT Exists)* | GroupAggregate → **InitPlan** (parallel) → Gather Merge → **NL Anti Join** (parallel customer + index-only orders) |

*Canonical silent-regression tripwires confirmed on PostgreSQL: **Q12 = 2 rows**,
**Q13 = 35 rows** (matches `bench/tpch/spotcheck_expected.env`).*

Total PostgreSQL wall time for the 22-query stream (warm, single sample):
**≈ 17.7 s**.

---

## 5. Key structural findings

1. **goopg `EXPLAIN` reports plan structure but no cost/cardinality estimates.**
   Every goopg node prints `(cost=0.00..0.00 rows=1 width=0)`; the operator tree,
   scan methods, join order, and index choices are shown, but no populated cost
   model or row estimates are surfaced. PostgreSQL prints full cost and row
   estimates. Consequence: plan *shape* is comparable; relative cost/estimate
   quality is not observable from goopg's `EXPLAIN` alone.

2. **goopg emits no parallel plans.** Although goopg accepts
   `max_parallel_workers_per_gather = 2`, **not one** of the 22 plans contains a
   `Gather`/`Workers Planned` node — goopg executes single-threaded. PostgreSQL
   parallelizes **19 of 22** query slots (all except Q11, Q13, Q20). This is the
   single largest structural difference and an inherent goopg capability gap, not
   a configuration artifact.

3. **goopg has a native "Multi-Way Hash Join (N tables)" operator.** For
   star/snowflake shapes (Q3, Q7, Q9, Q10, Q18, Q21, and Q11's 3-table join)
   goopg collapses several joins into a single multi-way hash-join node.
   PostgreSQL always builds a left-deep/bushy tree of **binary** joins. Both are
   valid; goopg's form is a distinct executor construct worth noting when reading
   the two plan trees side by side.

4. **goopg does not decorrelate/unnest subqueries into semi/anti joins.**
   goopg keeps `EXISTS` / `NOT EXISTS` / `IN` / scalar subqueries as opaque
   correlated filter placeholders (`<*planner.ExistsExpr>`, `<*planner.InExpr>`,
   `<*planner.SubqueryExpr>`) attached to a scan or join. PostgreSQL rewrites the
   same predicates into **`Nested Loop Semi Join`** (Q4), **`Anti Join`** (Q21,
   Q22), **`Hash Right Semi Join`** (Q20), or hashed-`SubPlan` NOT-IN (Q16). This
   is the clearest planner-maturity gap: on the correlated-subquery queries
   goopg's plan is structurally simpler (and likely to execute the subquery
   per-row) where PostgreSQL flattens to a set-oriented join.

5. **Scan-method choices are broadly aligned on the point-lookup paths.** Both
   engines use index scans on PK/FK columns inside nested loops for the small
   dimension tables (nation, region, supplier, customer, part), and both fall
   back to sequential scans over the `lineitem`/`orders` fact tables when a large
   fraction of rows qualifies. PostgreSQL additionally uses **bitmap index
   scans** (Q2, Q11, Q20, Q21), **index-only scans** (Q13, Q16, Q22), and
   **parallel index-only scans** (Q16) that goopg's plans do not exhibit here.

6. **Join operator for `LEFT JOIN` (Q13) matches in spirit.** goopg picks a
   `Hash Join (LEFT)`; PostgreSQL picks a `Hash Right Join` (the mirror image
   after commuting the outer side). Both produce the canonical 35-row result.

---

## 6. Estimated processing-time difference (plan-shape only)

This section estimates, per query, **how much more processing the goopg plan
would perform than the PostgreSQL plan**, judged **purely from plan shape**.

**Model / assumptions.**
- Per-operator, per-row unit costs are assumed **equal** across the two engines
  — i.e., the Go-vs-C constant-factor "implementation difference" is deliberately
  excluded, as requested. Differences below come only from *what the plan tells
  the engine to do*: join order, access method (seq/index/bitmap), subquery
  decorrelation, and parallelism.
- Two independent components are reported:
  - **Work volume `V` (処理量 / CPU work)** — total rows processed across the
    plan, *independent of parallelism*. Driven by join order, access method, and
    subquery handling.
  - **Parallelism `P` (wall-clock only)** — PostgreSQL's 2-worker `Gather` plans
    divide their parallel portion's wall-clock by up to ≈1.8× (Amdahl-limited by
    the serial Gather/merge/final-aggregate tail); goopg is always serial. This
    reduces *time* but **not** *work volume*.
  - **Net wall-clock estimate ≈ V × P** (goopg ÷ PostgreSQL).
- Cardinalities are grounded in PostgreSQL's `EXPLAIN ANALYZE` actuals
  ([`raw/pg_explain_analyze.txt`](raw/pg_explain_analyze.txt)). These are
  **order-of-magnitude** estimates, not measurements — goopg itself was not
  executed. Reading guide: for **処理量 (CPU work)** look at the `V` column
  alone; for **処理時間 (wall-clock)** use the net column.

| Q | Work `V` (goopg÷PG) | PG ∥ | Net wall-clock est. (goopg÷PG) | Dominant driver of the difference |
|---|:--:|:--:|:--:|---|
| 1  | ≈1.0 | ‖ | **≈1.7–1.9×** | Identical `lineitem` scan+aggregate; entire difference is PG's 2 workers. |
| 2  | ≈1.0–1.2 | ‖ | ≈1.5–1.8× | Small; both use the min-cost subplan. PG parallelizes the 200 K `part` scan. |
| 3  | ≈1.5–2.5 | ‖ | **≈3–4×** | goopg hash-joins the full 6 M `lineitem`; PG index-probes `lineitem` only for qualifying orders **and** parallelizes. |
| 4  | ≈1.0 | ‖ | ≈1.7–1.9× | Same per-order index probe (semi-join ≡ EXISTS filter); difference is parallelism. |
| 5  | ≈1.0–1.3 | ‖ | ≈1.8–2.2× | Comparable index-join tree; difference mostly parallelism. |
| 6  | ≈1.0 | ‖ | **≈1.7–1.9×** | Pure `lineitem` scan+aggregate — the cleanest "parallelism-only" case. |
| 7  | ≈1.0 | ‖ | ≈1.7–2.0× | Comparable join tree; parallelism-driven. |
| 8  | ≈1.0 | ‖ | ≈1.6–1.9× | Comparable deep index NL; PG parallelizes the `part` scan. |
| 9  | ≈2–4 | ‖ | **≈4–7×** | goopg full-scans `orders`(1.5 M)+`lineitem`(6 M) in a 4-way hash; PG drives from `part LIKE '%green%'` via indexes + parallel. |
| 10 | ≈1.3–2 | ‖ | ≈2.5–3.5× | goopg 4-way hash over full tables vs PG parallel hash-join chain. |
| 11 | ≈2–4 | — | **≈2–4×** | goopg seq-scans 800 K `partsupp`; PG uses the index (~32 K rows via 400 German suppliers). **Neither parallel**, so this is pure work volume. |
| 12 | ≈3–6 | ‖ | **≈5–10×** | goopg's NL puts `orders`(1.5 M) on the outer and index-probes `lineitem` **1.5 M×**; PG does one parallel hash-join. |
| 13 | ≈1.0–1.3 | — | **≈1.0–1.3×** | Near-identical (`Hash Left` ≡ `Hash Right` join, twin aggregates); neither parallel → **closest to parity**. |
| 14 | ≈1.0–1.2 | ‖ | ≈1.8–2.0× | Similar hash/NL; parallelism dominant. |
| 15a| ≈1.0 | ‖ | ≈1.7–1.9× | View body = `lineitem` scan+group; parallelism only. |
| 15b| ≈1.0–1.3 | ‖ | ≈1.8–2.2× | Both recompute the grouped revenue for the `max` subquery; parallelism. |
| 16 | ≈1.2–1.8 | ‖ | ≈2–2.8× | goopg seq-scans `partsupp`; PG uses a parallel index-only scan + hashed NOT-IN subplan. |
| 17 | ≈3–6 | ‖(sub) | **≈4–8×** | goopg's NL puts `lineitem`(6 M) on the outer and index-probes `part` **6 M×**; PG hash-joins to ~200 parts, subplan run only ~6 K×. |
| 18 | ≈1.5–2.5 | ‖ | ≈2.5–4× | goopg 4-way hash + a second full `lineitem` scan for the `>313` group; PG parallelizes both. |
| 19 | ≈10–100 | ‖ | **≈10–100× (worst case)** | goopg drives from `lineitem`(6 M) and index-probes `part` per row; PG drives from the ~516 filtered `part` rows and probes `lineitem` **516×** — a ~10,000× gap in join probes (bounded by goopg's unavoidable 6 M base scan). |
| 20 | ≈3–10 | ~— | **≈3–10×** | goopg seq-scans 800 K `partsupp` and evaluates the `0.5·sum` subquery per row; PG drives from `part LIKE 'forest%'` (~2 K) via a semi-join. Largely serial on both. |
| 21 | ≈2–4 | ‖ | **≈3–6×** | goopg 4-way hash over full tables with EXISTS/NOT-EXISTS as per-row filters; PG uses set-oriented semi/anti joins + parallel. |
| 22 | ≈1.0 | ‖ | ≈1.5–2.0× | Comparable (both probe `orders` per customer); small 150 K `customer` scan; parallelism. |

**Buckets (by net wall-clock estimate).**

- **≈ parity (~1×):** Q13 — nearly identical plans, neither parallel.
- **Parallelism-bound (~1.7–2×, equal work `V≈1`):** Q1, Q4, Q5, Q6, Q7, Q8,
  Q14, Q15a, Q15b, Q22, and Q2. Here goopg's *CPU work* (処理量) is essentially
  equal to PG's; the wall-clock gap is almost entirely PG's 2-worker
  parallelism. If parallelism is excluded, these are ~1×.
- **Moderate extra work (~2–4×):** Q3, Q10, Q11, Q16, Q18 — goopg's multi-way
  hash over full fact tables (vs PG's targeted index access) does more real work,
  compounded by parallelism.
- **Large extra work from join-order / non-decorrelation (~4–10×):** Q9, Q12,
  Q17, Q20, Q21 — goopg drives joins from the large table (`lineitem`/`orders`/
  `partsupp`) or keeps correlated subqueries as per-row filters.
- **Severe / order-of-magnitude (≥10×):** Q19 — goopg's join order forces a
  6 M-row driving side where PG's forces ~516 rows.

**Takeaways.** For roughly half the workload (the aggregate/scan-bound and
well-indexed star-join queries) the two plans are equivalent in **CPU work**, and
the expected time gap is essentially PostgreSQL's parallelism (~1.7–1.9×). The
large gaps are **not** parallelism — they come from two plan-shape properties
seen in §5: (a) goopg driving a join from the big fact table instead of a small
filtered dimension (Q3, Q9, Q12, Q17, Q19, Q20), and (b) goopg not decorrelating
correlated subqueries into set-oriented semi/anti joins (Q17, Q19, Q20, Q21).
Q19 is the outlier where plan shape alone implies an order-of-magnitude
difference.

---

## 7. Reference: measured goopg SF1 runtimes and the actual goopg ÷ PG ratio

§6 estimated the difference from **plan shape only** (implementation-neutral).
This section adds **actually measured** goopg execution times from the most
recent all-pass SF1 record in the repository, and computes the real
goopg ÷ PostgreSQL time ratio against the PG SF1 numbers measured in this study
(§4).

**Source (goopg, reference values).**
[`analysis/tests-overview-260706/04-performance-baselines.md`](../../tests-overview-260706/04-performance-baselines.md)
§B — "the newest full 22/22-pass power-test record": run log
`run_goopg_20260526-135117.log`, goopg commit `26cf58d`
(branch `align-data-structure-with-pg`), **HammerDB TPC-H, SF=1**
(lineitem ≈ 6.0 M — the same scale as this study's PG run), 2026-05-26,
**FINISHED SUCCESS, all 22 queries, zero errors**, total 1469 s, geomean 36.3 s.

**Caveats (read before using the ratios).**
- **Same scale, clean ratio:** goopg here is SF1 (lineitem ≈ 6.0 M), matching the
  PG SF1 run, so goopg ÷ PG is a like-for-like *scale* comparison.
- **Different goopg build:** these times are from commit `26cf58d` (2026-05-26),
  **not** the `701a5f57` build whose plans are shown in §4–§6. They also
  **predate mid-June query optimizations** — the later SF≈0.5 run
  ([`analysis/tpch-sf0.5-query-timings-20260616.md`](../../tpch-sf0.5-query-timings-20260616.md))
  shows some queries dropped sharply by then (e.g. Q22 ~85 s → 1.7 s, Q7 ~123 s →
  62 s even at half scale), so the SF1 ratios for **Q4, Q7, Q22** in particular
  likely **overstate** current goopg. Treat as the newest *all-pass SF1 record*,
  not as current HEAD.
- **Actual measured = implementation-inclusive:** unlike §6, these numbers
  include goopg's real per-operator cost (single-threaded Go executor, non-PGO
  `go` build, HammerDB single-VU client timing incl. round-trips) versus PG's
  parallel release build. That is exactly why these ratios are far larger than
  §6's plan-shape-only estimates.
- **Q15** is approximate: goopg's HammerDB Q15 slot (36.7 s) bundles
  `CREATE VIEW` + body + main + `DROP`; the PG figure (2.677 s) is view-body +
  main only.

| Q | goopg SF1 (s) | PG SF1 (s) | goopg ÷ PG |
|---|---:|---:|---:|
| Q1  | 20.036 | 1.749 | **11×** |
| Q2  | 59.078 | 0.256 | **231×** |
| Q3  | 16.789 | 0.364 | 46× |
| Q4  | 217.190 | 0.188 | **1156×** |
| Q5  | 18.603 | 0.340 | 55× |
| Q6  | 13.116 | 0.341 | 38× |
| Q7  | 122.899 | 0.429 | **286×** |
| Q8  | 171.430 | 0.205 | **837×** |
| Q9  | 56.059 | 0.907 | 62× |
| Q10 | 18.524 | 0.670 | 28× |
| Q11 | 2.409 | 0.084 | 29× |
| Q12 | 100.535 | 0.897 | 112× |
| Q13 | 84.864 | 1.511 | 56× |
| Q14 | 20.728 | 0.260 | 80× |
| Q15 | 36.701 *(slot)* | 2.677 *(body+main)* | ~14× |
| Q16 | 2.904 | 0.397 | **7×** |
| Q17 | 45.209 | 1.503 | 30× |
| Q18 | 36.773 | 3.739 | **10×** |
| Q19 | 24.503 | 0.059 | **414×** |
| Q20 | 19.451 | 0.209 | 93× |
| Q21 | 295.057 | 0.859 | **344×** |
| Q22 | 84.918 | 0.058 | **1452×** |
| **Total** | **≈1468** | **≈17.7** | **≈83×** |

**Interpretation.**

- **Whole workload:** goopg's 22-query stream takes ≈ 1468 s vs PG's ≈ 17.7 s at
  SF1 — an **≈83× overall** wall-clock gap on this hardware.
- **The ratio is largest where PG is sub-second** (efficient parallel + decorrelated
  plan) while goopg's plan degrades: **Q4 (1156×), Q22 (1452×), Q8 (837×),
  Q19 (414×), Q21 (344×), Q7 (286×), Q2 (231×)** — every one of these is a
  correlated-subquery query and/or one where goopg drives the join from the large
  fact table (the exact plan-shape gaps identified in §5/§6).
- **The ratio is smallest where PG itself does heavy work** (large result sets):
  **Q16 (7×), Q18 (10×), Q1 (11×), Q15 (~14×)** — here PG's own runtime is not
  tiny, so the relative gap compresses.
- **Relation to §6:** the plan-shape estimate (§6) correctly ranks the *worst*
  offenders (Q4, Q9, Q17, Q19, Q20, Q21) but massively under-predicts the
  *magnitude*, because it excludes implementation cost by design. Even a plan-shape
  near-tie like Q6 (pure scan+aggregate) is ≈38× in practice — that residual is
  goopg's per-row execution overhead plus PG's parallelism, i.e. precisely the
  "implementation difference" §6 sets aside. The two sections are complementary:
  §6 = *what the plan shape alone would cost*; §7 = *what was actually measured*.

---

## 8. Executor-layer performance difference (measured ÷ plan-shape)

The measured gap (§7) is the product of **two** independent factors: *how much
work the plan asks for* (plan quality) and *how fast that work is executed*
(executor layer). §6 already isolates the first as the work-volume ratio `V`.
Dividing it out leaves the second:

```
E (executor-layer factor) = R_measured (§7)  ÷  V_plan-shape (§6 work-volume)
```

`E` is the **performance difference of the machinery that runs the plan**, with
plan quality (join order, access method, subquery decorrelation — i.e. the
number of rows the plan asks to process) removed. It bundles what is genuinely
executor-layer: PG's parallel execution (goopg has none), goopg's per-operator
Go-executor speed (single-threaded, non-PGO) vs PG's C, and per-invocation
operator overhead. It does **not** include plan-shape/work-volume differences.

| Q | Measured `R` (§7) | Plan `V` (§6) | **Executor `E = R ÷ V`** |
|---|---:|---:|---:|
| Q1  | 11×   | ~1.0× | **11×** |
| Q2  | 231×  | ~1.1× | **211×** |
| Q3  | 46×   | ~1.9× | **24×** |
| Q4  | 1156× | ~1.0× | **1156×** |
| Q5  | 55×   | ~1.1× | **48×** |
| Q6  | 38×   | ~1.0× | **38×** |
| Q7  | 286×  | ~1.0× | **286×** |
| Q8  | 837×  | ~1.0× | **837×** |
| Q9  | 62×   | ~2.8× | **22×** |
| Q10 | 28×   | ~1.6× | **17×** |
| Q11 | 29×   | ~2.8× | **10×** |
| Q12 | 112×  | ~4.2× | **26×** |
| Q13 | 56×   | ~1.1× | **49×** |
| Q14 | 80×   | ~1.1× | **73×** |
| Q15 | 14×   | ~1.0× | **14×** |
| Q16 | 7×    | ~1.5× | **5×** |
| Q17 | 30×   | ~4.2× | **7×** |
| Q18 | 10×   | ~1.9× | **5×** |
| Q19 | 414×  | ~31.6× | **13×** |
| Q20 | 93×   | ~5.5× | **17×** |
| Q21 | 344×  | ~2.8× | **122×** |
| Q22 | 1452× | ~1.0× | **1452×** |

**Result — the executor gap is bimodal.**

- **Bulk-data operators** (sequential/index scan, hash join, sort, aggregate,
  index nested-loop — Q1, Q3, Q5, Q6, Q9–Q14, Q16, Q18–Q20): executor factor
  **≈ 5–73×, geometric mean ≈ 19×**. This is goopg's *baseline* executor-layer
  overhead: a single-threaded Go executor with no parallel query, not
  PGO/`GOAMD64`-optimized, versus PG's parallel C executor. Removing PG's ~1.8×
  parallelism from the cleanest plan-equivalent cases (Q1, Q6) leaves a pure
  single-thread per-operator gap of only **~6–20×**.
- **Correlated-subquery queries** (Q2, Q4, Q7, Q8, Q21, and Q22): executor factor
  **≈ 120–1450×, geometric mean ≈ 256×**. Here the residual is dominated by
  goopg's **per-row `SubPlan` open/close overhead** — each `EXISTS`/`IN`/scalar
  subquery is re-instantiated per outer row, and the fixed per-invocation cost
  (operator Build/Open/Close) dwarfs the actual probe, which is the exact
  behaviour documented in
  [`analysis/tpch-runner-measurement-report-2026-05-06.md`](../../tpch-runner-measurement-report-2026-05-06.md)
  ("operator Build/Open/Close overhead dominates").

**Two useful reads of this table.**

- **goopg's executor is ~1 order of magnitude (≈19×) slower on bulk operators**,
  and that is the number to attack for the scan/join/aggregate workload
  (parallel query + per-operator constant-factor / PGO would close most of it).
- **The catastrophic queries are not mainly a plan problem or a bulk-executor
  problem — they are a `SubPlan`-execution problem.** Q19 is the mirror image:
  its huge *measured* 414× gap is almost entirely **plan** (`E` only ~13×), i.e.
  a planner/join-order fix would help Q19 far more than an executor speedup,
  whereas Q4/Q8/Q22 need the executor's per-row-subquery path fixed (or the
  planner to decorrelate so the SubPlan disappears).

**Caveats (this is an order-of-magnitude decomposition).**
- `E` inherits **all** the uncertainty of §6 and §7: `R` is from an older goopg
  build (`26cf58d`) than the `701a5f57` plans, and `V` is a mid-range estimate
  from plan shape, not a measurement. Treat single-query `E` values as ±1
  bucket, and rely on the grouped geometric means.
- **Attribution boundary.** Whether goopg running a correlated subquery per-row
  (instead of decorrelating to a semi/anti join) is charged to the *planner*
  (higher `V`) or the *executor* (higher `E`) is a modelling choice. §6 charged
  most of it to the executor (`V≈1` for Q4/Q8/Q22), which is why their `E` is
  huge; charging non-decorrelation to the planner instead would move that mass
  from `E` back into `V`. The **bulk-operator ≈19×** figure is unaffected by this
  choice and is therefore the most robust executor-layer estimate here.

---

## 9. Selected plan pairs (full text)

Full plans for every query are in [`raw/`](raw/). Two representative contrasts:

### Q6 — simple range aggregate (parallelism, nothing else differs)

```
goopg:
  Aggregate
    -> Seq Scan on lineitem
         Filter: (l_shipdate >= … AND l_shipdate < … AND l_discount in [0.04,0.06] AND l_quantity < 24)

PostgreSQL (341 ms, 1 row):
  Finalize Aggregate
    -> Gather (Workers Planned: 2)
         -> Partial Aggregate
              -> Parallel Seq Scan on lineitem
                   Filter: (same predicate)
```

### Q21 — correlated EXISTS/NOT EXISTS (the decorrelation gap)

```
goopg:
  Sort
    -> GroupAggregate (1 key)
         -> Multi-Way Hash Join (4 tables)
              Filter: (o_orderstatus='F' AND l_receiptdate>l_commitdate
                       AND <*planner.ExistsExpr> AND NOT <*planner.ExistsExpr>
                       AND n_name='SAUDI ARABIA')
              -> Seq Scan orders / supplier / nation / lineitem l1

PostgreSQL (859 ms, 445 rows):
  Sort
    -> GroupAggregate
         -> Nested Loop
              -> Nested Loop Semi Join
                   -> Gather -> Nested Loop Anti Join
                        -> Hash Join (Parallel Seq Scan lineitem l1 + bitmap supplier)
                        -> Index Scan lineitem l3   (NOT EXISTS → Anti Join)
                   -> Index Scan lineitem l2        (EXISTS → Semi Join)
              -> Index Scan orders
```

---

## 10. Reproduction

```bash
# 1. Worktree off clean HEAD; symlink the read-only oracle + HammerDB.
git worktree add -b <br> <WT> 701a5f57
ln -s <main>/postgres <WT>/postgres ; ln -s <main>/HammerDB-5.0 <WT>/HammerDB-5.0
cd <WT> && go build -o tmp/goopg-bench-bin ./cmd/goopg && go build -o /tmp/tpch-runner ./cmd/tpch-runner

# 2. PostgreSQL 18.3 on :65432 (shared_buffers=2GB, work_mem=512MB, rest default), HammerDB SF1 load.
# 3. goopg on :65433 via scripts/goopg-test-run.sh (shared_buffers=2GB, work_mem=512MB), HammerDB SF1 load.
#    (Both: ANALYZE; CHECKPOINT after load.)

# 4. Capture:
/tmp/tpch-runner --port 65433 --db tpch --user tpch --password tpch --explain   # goopg plans
psql -p 65432 -d tpch -c 'EXPLAIN <q>' ; psql -p 65432 -d tpch -c 'EXPLAIN (ANALYZE,BUFFERS) <q>'   # PG
```

Query texts: [`queries/`](queries/). Raw captures: [`raw/`](raw/).
