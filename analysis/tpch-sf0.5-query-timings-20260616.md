# TPC-H SF≈0.5 — 22-query runtime under cgroup memory cap (2026-06-16)

## Summary (headline)

- **Scale:** SF ≈ 0.500 — 750,000 orders (lineitem ~3.6M; suppliers 5,000 /
  customers 75,000 / parts 100,000 / partsupp 400,000).
- **Data load:** **6m20s**.
- **Queries:** **21 of 22 completed OK.** **Q9 did NOT finish** — it hit the
  20-minute per-query timeout and was canceled (SQLSTATE 57014).
- **Time to run the 21 completing queries:** **≈ 10m18s** (sum of per-query
  wall-clock = 618.2 s).
- **Time to run all 22 incl. the Q9 20m timeout:** ≈ 30m18s of query time.
- **Total sweep wall-clock** (load + all 22 + cluster setup/teardown): **36m54s**
  (test wall-clock 2213.9 s).

> Caveat — this is a **concurrent / reference run**: the Ralph autonomous loop was
> running on the same host throughout (`loop_at_start=ALIVE`), so CPU/IO contention
> inflates these timings somewhat. It is **not** a PGO-optimized build (compiled via
> `go test`). Treat the numbers as a ballpark for "how long the 22-query set takes at
> SF≈0.5 under a memory cap", not as a peak-throughput benchmark.

## Environment

| Field | Value |
|-------|-------|
| Date | 2026-06-16 16:56:39 – 17:35:36 JST |
| Branch / HEAD | `align-data-structure-with-pg` @ `1f013863` (DU-002 slice 67) |
| Host | WSL2, 16 cores, 31 GiB RAM, kernel 6.18 |
| MemAvailable at start | 24 GiB (concurrent Ralph loop also running) |
| Build | `go test` default (NOT PGO / GOAMD64=v3 optimized) |
| Harness | `internal/testutil/tpch` → `TestTPCHScaleLoadAndQueryRun` |
| cgroup cap | scope `tpch-sf05-bench`: **MemoryHigh=20G, MemoryMax=24G, MemorySwapMax=0, GOMEMLIMIT=18GiB** (via `scripts/goopg-test-run.sh`) |
| Per-query timeout | 20 min (harness-enforced) |

## Per-query results (Q1..Q22)

| Query | Status | Elapsed (s) | Rows |
|-------|--------|------------:|-----:|
| Q1  | OK   |     10.047 |     6 |
| Q2  | OK   |      1.455 |   251 |
| Q3  | OK   |      8.871 | 40290 |
| Q4  | OK   |    110.851 |     5 |
| Q5  | OK   |     10.362 |     5 |
| Q6  | OK   |      6.965 |     1 |
| Q7  | OK   |     61.770 |     4 |
| Q8  | OK   |      9.067 |     2 |
| Q9  | **FAIL (timeout 20m)** | 1200.007 | — |
| Q10 | OK   |      9.328 | 18193 |
| Q11 | OK   |      0.876 |  3771 |
| Q12 | OK   |     10.885 |     2 |
| Q13 | OK   |      1.897 |    24 |
| Q14 | OK   |      8.546 |     1 |
| Q15 | OK   |      0.001 |     0 |
| Q16 | OK   |      1.901 | 11562 |
| Q17 | OK   |     20.568 |     1 |
| Q18 | OK   |     17.501 |    12 |
| Q19 | OK   |     11.223 |     1 |
| Q20 | OK   |      9.392 |   198 |
| Q21 | OK   |    304.928 |   183 |
| Q22 | OK   |      1.716 |     1 |

**Totals:** 21/22 OK; sum of the 21 completing queries = **618.2 s (≈10m18s)**.
Slowest completing: Q21 (5m05s), Q4 (1m51s), Q7 (1m02s). Sub-second/very fast:
Q11, Q15, Q22, Q2, Q13, Q16.

## Notes

- **Q9** is the lone non-completer — it exceeded the 20-minute budget and was
  canceled. Q9 (the part/supplier profit query) has historically been the hardest
  TPC-H shape for goopg's planner; see prior analyses
  (`analysis/m0077_q5_unlocked_4_slice.md`, `M0072-0002` Q9 rebind hang). At SF≈0.5
  it still does not finish within 20m.
- **Q15** ran via the harness's split view path (CREATE VIEW `revenue0` → body →
  main SELECT); the reported 1 ms / 0 rows is the main SELECT against the view.
- The harness reported `--- FAIL` overall purely because it asserts all 22 execute
  without error; the single failure is Q9's timeout. All timing data above is valid.
- Q13 returned 24 rows here (load-dependent; the spotcheck canonical pins Q13 to a
  different HammerDB dataset — not comparable to this generated SF0.5 set).

## Reproduce

```bash
GOOPG_CG_UNIT=tpch-sf05-bench scripts/goopg-test-run.sh \
  go test -v -run TestTPCHScaleLoadAndQueryRun -timeout 180m \
  ./internal/testutil/tpch/
```

Override scale via `GOOPG_TPCH_ORDERS` (default 750000 ⇒ SF≈0.5) and the query set
via `GOOPG_TPCH_QUERIES` (e.g. `9` to isolate Q9). For uncontended numbers, run with
the Ralph loop stopped.
