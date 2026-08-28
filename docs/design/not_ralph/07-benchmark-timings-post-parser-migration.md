# Benchmark timings after the goyacc parser migration

Measured 2026-08-28 on `parse-refac` at `e56d4f4fd`, the commit that closes the
goyacc parser migration (`P7.4`). The point of the run is to record what the
engine does per query now that the migration and its five regress regressions
are behind us — not to compare against PostgreSQL, which is a separate exercise
with its own reference clusters.

## Environment

| | |
|---|---|
| host | AMD Ryzen 7 5700X, 8 cores / 16 threads, 31 GB RAM |
| kernel | Linux 6.18.33.2-microsoft-standard-WSL2 |
| Go | go1.26.3 linux/amd64 |
| goopg | `e56d4f4fd` (`parse-refac`) |
| GC settings | `GOGC=100`, `GOMEMLIMIT=12GiB` on every server |

Both benchmarks ran against goopg only. Servers were started through the cgroup
cap (`scripts/goopg-test-run.sh`), one benchmark at a time — a goopg server that
has just run a heavy query sits near `GOMEMLIMIT` and thrashes GC, so
overlapping runs read as regressions that are not there. That effect is not
hypothetical here: see the note under the TPC-H table.

## TPC-H, scale factor 1

Cluster: `bench/tpch/runtime_goopg/data` on `127.0.0.1:65433`, database `tpch`,
loaded by HammerDB 5.0 (`lineitem` = 6,001,255 rows). Runner:
`cmd/tpch-runner`, per-query timeout 900 s.

| query | result | elapsed | rows |
|---|---|---:|---:|
| Q1 | OK | 7.57 s | 4 |
| Q2 | OK | 3.79 s | 418 |
| Q3 | OK | 12.24 s | 11,415 |
| Q4 | OK | 5.73 s | 5 |
| Q5 | **memory exhaustion** | — | — |
| Q6 | OK | 5.82 s | 1 |
| Q7 | OK | 36.87 s | 4 |
| Q8 | OK | 7.21 s | 2 |
| Q9 | OK | 19.55 s | 175 |
| Q10 | OK | 10.42 s | 20,451 |
| Q11 | OK | 2.43 s | 1,302 |
| Q12 | OK | 16.56 s | 2 |
| Q13 | OK | 8.91 s | 34 |
| Q14 | OK | 6.01 s | 1 |
| Q15 — CREATE VIEW | OK | 0.03 s | 0 |
| Q15a — view body | OK | 6.38 s | 10,000 |
| Q15b — main | OK | 56.58 s | 1 |
| Q16 | OK | 1.10 s | 18,310 |
| Q17 | OK | 5.77 s | 1 |
| Q18 | OK | 67.96 s | 12 |
| Q19 | OK | 6.35 s | 1 |
| Q20 | OK | 3.62 s | 85 |
| Q21 | OK | 30.17 s | 402 |
| Q22 | OK | 1.18 s | 7 |

**21 of 22 queries complete, 0 errors.** Total for the 21: 316 s. The row counts
match the pinned anchors (`bench/tpch/spotcheck_expected.env`: Q12 = 2,
Q13 = 34).

### Q5 does not complete

Q5 was run **alone on a freshly started server** and still could not finish. It
is not a timeout: with `GOMEMLIMIT=12GiB` the server's RSS climbed past 28 GB on
a 31 GB host, at which point the run was stopped to keep the host alive.
`GOMEMLIMIT` is a soft limit, so a live heap that genuinely exceeds it is not
reclaimed — Go keeps allocating.

This is a real regression against the project's own record: `M0077` (2026-05-11,
branch `try-codex`) closed with **Q5 at 26 s / 5 rows** after the four-slice
planner refactor in `docs/design/fix-for-q5/`. Something between that commit and
`e56d4f4fd` reintroduced the build-side memory blowup that refactor fixed. It is
**not** parser-migration debt: the migration is AST-preserving (0 AST divergence
against the legacy parser before it was deleted, and a 1,642-statement golden
corpus pinning the result), and Q5's cost is decided by the planner and the hash
join, not by parsing. It needs its own investigation.

### Why an earlier run reported Q6 at 424 s

An earlier sweep of the same 22 queries ran Q5 first, let it burn its 1,200 s
budget, and then measured Q6 at **423.94 s**. On the clean run above Q6 takes
**5.82 s** — a factor of 73. Nothing about Q6 changed; the server was simply
sitting at `GOMEMLIMIT` with a huge live heap and spent the whole query in GC.
This is the "sweep-tail collapse" the repository guidance warns about, and it is
the reason Q5 is measured separately here rather than in query order.

## TPC-DS, scale factor 1

Cluster: `bench/tpcds/runtime_goopg/data` on `127.0.0.1:65436`, database
`postgres`. Runner: `scripts/tpcds-run.sh` with `TPCDS_TIMEOUT=600`. The
benchmark ships 99 numbered queries (Q1–Q99); all 99 were run in one sweep.

| query | result | elapsed | rows | | query | result | elapsed | rows |
|---|---|---:|---:|---|---|---|---:|---:|
| Q1 | OK | 3 s | 100 | | Q51 | OK | 31 s | 100 |
| Q2 | OK | 29 s | 2,513 | | Q52 | OK | 3 s | 100 |
| Q3 | OK | 3 s | 31 | | Q53 | OK | 15 s | 100 |
| Q4 | OK | 60 s | 4 | | Q54 | OK | 33 s | 0 |
| Q5 | OK | 40 s | 100 | | Q55 | OK | 3 s | 73 |
| Q6 | OK | 33 s | 44 | | Q56 | OK | 33 s | 100 |
| Q7 | OK | 22 s | 100 | | Q57 | OK | 16 s | 100 |
| Q8 | OK | 7 s | 0 | | Q58 | OK | 169 s | 0 |
| Q9 | OK | 152 s | 1 | | Q59 | OK | 39 s | 100 |
| Q10 | OK | 33 s | 1 | | Q60 | OK | 33 s | 100 |
| Q11 | OK | 34 s | 95 | | Q61 | OK | 40 s | 1 |
| Q12 | OK | 7 s | 100 | | Q62 | OK | 1 s | 100 |
| Q13 | OK | 19 s | 1 | | Q63 | OK | 16 s | 100 |
| Q14 | OK | 242 s | 100 | | Q64 | **TIMEOUT** | 635 s | 0 |
| Q15 | OK | 3 s | 100 | | Q65 | **ERROR** | 36 s | 0 |
| Q16 | OK | 30 s | 1 | | Q66 | OK | 15 s | 5 |
| Q17 | OK | 13 s | 1 | | Q67 | OK | 32 s | 100 |
| Q18 | OK | 122 s | 100 | | Q68 | OK | 17 s | 100 |
| Q19 | OK | 17 s | 100 | | Q69 | OK | 35 s | 100 |
| Q20 | OK | 10 s | 100 | | Q70 | **ERROR** | 0 s | 0 |
| Q21 | OK | 3 s | 100 | | Q71 | OK | 32 s | 1,129 |
| Q22 | OK | 19 s | 100 | | Q72 | OK | 427 s | 100 |
| Q23 | OK | 174 s | 0 | | Q73 | OK | 18 s | 3 |
| Q24 | OK | 32 s | 0 | | Q74 | OK | 25 s | 100 |
| Q25 | OK | 12 s | 0 | | Q75 | OK | 44 s | 100 |
| Q26 | OK | 16 s | 100 | | Q76 | OK | 32 s | 100 |
| Q27 | OK | 19 s | 100 | | Q77 | OK | 35 s | 44 |
| Q28 | OK | 101 s | 1 | | Q78 | OK | 45 s | 100 |
| Q29 | OK | 19 s | 1 | | Q79 | OK | 16 s | 100 |
| Q30 | OK | 1 s | 63 | | Q80 | OK | 184 s | 100 |
| Q31 | OK | 32 s | 43 | | Q81 | OK | 1 s | 100 |
| Q32 | OK | 12 s | 1 | | Q82 | OK | 43 s | 2 |
| Q33 | OK | 32 s | 100 | | Q83 | OK | 6 s | 22 |
| Q34 | OK | 15 s | 374 | | Q84 | OK | 2 s | 18 |
| Q35 | OK | 39 s | 100 | | Q85 | OK | 20 s | 2 |
| Q36 | **ERROR** | 0 s | 0 | | Q86 | **ERROR** | 0 s | 0 |
| Q37 | OK | 11 s | 0 | | Q87 | OK | 36 s | 1 |
| Q38 | OK | 36 s | 1 | | Q88 | OK | 120 s | 1 |
| Q39 | OK | 50 s | 6 | | Q89 | OK | 18 s | 100 |
| Q40 | OK | 101 s | 100 | | Q90 | OK | 9 s | 1 |
| Q41 | OK | 10 s | 1 | | Q91 | OK | 2 s | 1 |
| Q42 | OK | 4 s | 10 | | Q92 | OK | 6 s | 1 |
| Q43 | OK | 3 s | 6 | | Q93 | OK | 7 s | 0 |
| Q44 | OK | 62 s | 10 | | Q94 | OK | 10 s | 1 |
| Q45 | OK | 7 s | 14 | | Q95 | OK | 60 s | 1 |
| Q46 | OK | 20 s | 100 | | Q96 | OK | 15 s | 1 |
| Q47 | OK | 26 s | 100 | | Q97 | OK | 30 s | 1 |
| Q48 | OK | 23 s | 1 | | Q98 | OK | 15 s | 2,531 |
| Q49 | OK | 38 s | 34 | | Q99 | OK | 4 s | 90 |

**94 of 99 queries succeeded**, totalling 3,591 s (≈ 1 h 0 m) for the
successful ones. The five that did not are covered below, and only ONE of them
is a genuine goopg limitation.

### Q36, Q70, Q86 — malformed query text, not a goopg failure

All three fail identically:

```
ERROR:  syntax error at or near ";"
LINE 28:   limit 100;
```

`dsqgen` emitted a `limit 100;` **inside a subquery**, so the statement is cut
off before its closing `) as sub`. PostgreSQL rejects these files too; they are
the three permanently-skipped queries in the SF=0.5 oracle
(`bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`).

Worth noting for the migration record: goopg now produces *exactly* PG's
diagnostic here. Before `e56d4f4fd`, `SplitStatements` treated that inner `;`
as a statement boundary and the fragment was cut somewhere else entirely.
Keeping parenthesis nesting and the terminating `;` — the fixes that recovered
`copydml` and `errors` — also made this artefact report the way PG reports it.

### Q65 — a measurement artefact, not a failure

The sweep recorded `Q65 ERROR elapsed=36s`. Run on its own, Q65 **succeeds in
34 s and returns 100 rows**. Q65 runs immediately after Q64, and `tpcds-run.sh`
enforces its budget with `timeout 600 psql` — which kills the *client* only.
The server keeps executing the abandoned Q64, so Q65 starts on a host that is
still working on the previous query. This is the documented orphan trap; the
honest figure for Q65 is 34 s.

Correcting for it: **95 of 99 queries succeed**, and the only genuine engine
limitation among the remaining four is Q64.

### Q64 — slower than the 600 s budget, but it completes

Q64 was recorded `TIMEOUT elapsed=635s`. Re-run alone on a freshly started
server with a 1,800 s budget it **completes in 781 s and returns 8 rows**, with
the server's RSS never above 2 GB. It is not a hang and not a memory problem —
it is simply the most expensive query in the set at SF=1, and 600 s is not
enough for it on this host.

### TPC-DS, corrected

Folding in the three re-measurements:

| | queries |
|---|---:|
| complete | **96 / 99** |
| fail on malformed generated SQL (PG fails too) | 3 (Q36, Q70, Q86) |
| genuine goopg failures | **0** |

Corrected timings for the three: Q64 = 781 s, Q65 = 34 s, and Q36/Q70/Q86 are
not runnable. The slowest completing queries are Q64 (781 s), Q14 (242 s),
Q9 (152 s) and Q18 (122 s); the median is well under 30 s.

## Summary

| benchmark | complete | genuine failures |
|---|---|---|
| TPC-H SF=1 | 21 / 22 | 1 — Q5 exhausts memory |
| TPC-DS SF=1 | 96 / 99 | 0 |

The parser migration itself is invisible in these numbers, which is the
expected result: it is AST-preserving, so it moves no plan and no execution
cost. The one thing it did change is *diagnostics* — Q36/Q70/Q86 now report the
same syntax error PostgreSQL reports, because fragment boundaries follow
gram.y's nesting rules.

The single open item is **TPC-H Q5**, which regressed from a recorded 26 s
(M0077, 2026-05-11) to not completing at all within 31 GB. That is planner /
executor territory, not parsing, and it wants its own investigation.
