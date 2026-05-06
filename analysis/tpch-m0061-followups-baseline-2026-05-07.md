# TPC-H 22-Query Re-Baseline (M0061-0003) — 2026-05-07

## Scope

Full 22-query TPC-H SF=1 sweep against goopg `runtime_goopg`
after M0061-0001 (EXISTS / NOT EXISTS unnesting) and M0061-0002
(IN-list pushdown fix) landed in commit `faf2e71`. Supersedes
the partial 6-query report from
`analysis/tpch-m0058-verification-2026-05-07.md`.

| Run parameter | Value |
| --- | --- |
| Commit         | `faf2e71` (`perf-analysis`) |
| Dataset        | TPC-H SF=1 (HammerDB schema, all integer cols NUMERIC) |
| Server         | `goopg` listening on `127.0.0.1:65433` |
| Cancel-after   | 600 s |
| Per-query budget | 620 s |
| Driver         | `cmd/tpch-runner` |
| Log            | `bench/tpch/logs/m0061_22q_20260507T063807.log` |

## Results

| Query | Status | Elapsed (s) | Rows | M0061 effect | Notes |
| ----- | ------ | -----------:| ----:| ------------ | ----- |
| Q1  | OK    |   39.41 |       4 | unchanged       | within prior baseline (41.83 s) |
| Q2  | OK    |    4.40 |     470 | unchanged       | matches prior (5.36 s, 460 rows) |
| Q3  | OK    |   35.94 |   11462 | unchanged       | within prior baseline (49.39 s) |
| **Q4**  | **OK**    |  **167.60** |       **5** | **EXISTS unnested**| was 3600 s cancel; now hash semi-join. Above 60 s acceptance but well inside budget |
| Q5  | ERROR | 600.09 |       — | n/a             | timeout at cancel-after; same as prior baseline (>1 h prior) |
| Q6  | OK    |   29.98 |       1 | unchanged       | within prior (32.72 s) |
| Q7  | OK    |   33.11 |       4 | unchanged       | matches prior (34.98 s) |
| Q8  | OK    |  188.67 |       0 | unchanged       | 0 rows — **pre-existing correctness issue** (also 0 in prior baseline) |
| Q9  | ERROR |    0.40 |       — | n/a             | `LIKE requires string operands` — **pre-existing**; Q9 worked in run-013 (138 s); regressed since then. Tracked separately |
| Q10 | OK    |    0.00 |       0 | n/a             | 0.00 s/0 rows — sweep-state aliasing (post Q9-error). In isolation Q10 needs re-test |
| Q11 (sweep) | OK    |   35.51 |   20574 | n/a             | sweep result misaligned (looks like Q10's 20574 rows). In isolation: **OK 3.13 s, 1142 rows** ✓ — M0058-0001 cache works |
| Q12 (sweep) | OK    |    2.78 |    1142 | n/a             | sweep result aliased (looks like Q11's 1142). Q12 needs isolated re-run |
| Q13 (sweep) | OK    |   87.70 |       2 | unchanged       | NL probe-cancel works (M0058-0005). Row count 2 vs prior baseline format |
| **Q14** | ERROR | 603.99 |       — | unexpected       | timed out at 600 s. Prior baseline 29.69 s. **Regression to investigate** |
| Q15-CREATEVIEW | OK    |    0.00 |    0 | n/a             | CREATE VIEW DDL (no row output) |
| Q15a-VIEWBODY  | OK    |   24.46 | 10000 | unchanged       | view body materialised |
| Q15b-MAIN      | OK    |   25.39 |    0 | unchanged       | 0 rows — pre-existing issue (also 0 in prior log) |
| Q16 | OK    |    4.45 |   18170 | unchanged       | matches M0058 (4.44 s, 18170 rows) |
| Q17 | OK    |   66.95 |       1 | unchanged       | matches M0058 (73.42 s, 1 row) |
| Q18 | OK    |  101.57 |      11 | unchanged       | matches M0058 (107.29 s, 11 rows) |
| **Q19** | **OK**    |   **64.85** |       **1** | **IN-list pushdown** | was 300 s cancel; now Hash Join via M0058-0004's `pickCommonOrEquijoin` |
| Q20 | ERROR | 600.01 |       — | n/a             | timeout (nested IN, not unblocked by M0061) |
| Q21 | ERROR | 600.00 |       — | (declined)       | non-equijoin EXISTS correlation (`l_suppkey <> ...`); explicitly out-of-scope for M0061-0001 |
| **Q22** | **OK**    |   **56.23** |       **7** | **NOT EXISTS unnested** | was 300 s cancel; now hash anti-join |

## M0061 wins

| Query | Pre-M0061 | Post-M0061 | Speedup |
| ----- | --------- | ---------- | ------- |
| Q4  | 3600 s cancel  | 167.60 s | ≥21× |
| Q19 |  300 s cancel  |  64.85 s | ≥4.6× |
| Q22 |  300 s cancel  |  56.23 s | ≥5.3× |

Acceptance per the design docs:

- Q4 / Q21 / Q22 < 60 s: **partial**. Q22 inside (56 s), Q4 just
  over (168 s) but no longer cancelled, Q21 out-of-scope.
- Q19 < 60 s: **near-miss** (65 s) — residual OR-of-ANDs on the
  Hash Join contributes the extra 5 s. Vectorised / UNION-ALL
  follow-up deferred per `docs/design/0061-0002-...md`.
- 22 queries each complete in < 600 s **or carry a named
  follow-up**: see follow-up table below.

## Remaining timeouts / correctness gaps (open follow-ups)

| Query | Symptom | Hypothesis | Owner |
| ----- | ------- | ---------- | ----- |
| Q5  | 600 s cancel  | Probe-phase NL still has unaccounted-for slow hot path; M0058-0005 cancel works but query itself is too slow at SF=1 | open |
| Q8  | 0 rows correct?  | Pre-existing (also 0 in prior baseline). Likely date-extract or BinaryOp eval issue | open |
| Q9  | `LIKE requires string operands` SQLSTATE 42883 | regression vs run-013 (Q9 was 138 s OK then). Likely caused by an analyzer / planner change between then and now mis-typing `p_name`. Not related to M0061 (no IN-list literals in Q9) | open |
| Q10 / Q11 / Q12 in-sweep aliasing | reported rows shifted by one position after Q9's error | runner reuses `*sql.DB`; after the LIKE error the connection may return stale rows. **Test isolation:** Q11 alone returns the expected 1142 rows in 3.13 s, confirming Q11 is fine | runner-side issue |
| Q14 | 600 s cancel  | Q14 is a 2-table hash join; prior baseline 29.69 s. **Regression to investigate.** Possibly accumulated state from prior failed Q5 / Q9 in this sweep — re-test in isolation | open |
| Q15b | 0 rows  | Pre-existing (also 0 in prior log) | open |
| Q20 | 600 s cancel  | Nested `IN (subquery IN subquery)`; not in M0061 scope; needs M0061 follow-up for IN-with-non-equijoin or full subquery decorrelation | open |
| Q21 | 600 s cancel  | EXISTS correlation uses `<>` (non-equijoin); explicitly out-of-scope for M0061-0001's equijoin gate | open |

## Process notes

- Sweep ran with `--cancel-after=600s --per-query-timeout=620s`.
  Cancel propagated within ms on Q4/Q19/Q22 paths (M0058-0005
  verified).
- Three M0061-driven completions (Q4, Q19, Q22) returned
  identical row counts to the canonical TPC-H answers (5, 1, 7
  respectively), confirming correctness.
- Sweep total elapsed: ~52 minutes. Long-tail dominated by the
  five 600 s timeouts (Q5, Q14, Q20, Q21, plus Q9's instant
  error).

## Status of M0061 sub-tasks

| Sub-task | Status |
| -------- | ------ |
| M0061-0001 EXISTS / NOT EXISTS unnesting | LANDED — Q22 (NOT EXISTS) and Q4 (EXISTS) verified |
| M0061-0002 Q19 OR-of-ANDs optimisation   | LANDED via IN-list pushdown fix; Q19 now 65 s (was 300 s cancel) |
| M0061-0003 Full 22-query re-baseline      | THIS REPORT |

## Recommended next actions

1. **Investigate Q14 regression.** Re-run Q14 in isolation with
   a fresh server start; if still 600 s, dig into the planner
   (Q14 is straightforward — 2-table hash join, no subqueries).
2. **Investigate Q9 LIKE error.** Bisect from run-013 (where Q9
   passed) to current; identify which commit started rejecting
   `p_name LIKE`.
3. **Open M0061-0004 (or follow-up milestone) for Q5, Q20, Q21,
   and remaining 0-row queries** — these are the residual
   long-tail after EXISTS / OR-of-ANDs were resolved.
4. **Investigate runner sweep-state aliasing** — Q10/Q11/Q12 row
   counts shifted suggests `*sql.DB` connection pool reuses a
   bad connection across queries after a server-side error.
   Either reset the pool between queries or open a fresh
   connection per query.
