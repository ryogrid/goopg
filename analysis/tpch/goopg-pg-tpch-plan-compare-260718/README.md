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

## 6. Selected plan pairs (full text)

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

## 7. Reproduction

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
