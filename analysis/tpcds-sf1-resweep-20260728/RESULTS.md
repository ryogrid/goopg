# TPC-DS SF=1 dual-engine re-sweep at HEAD — running results

Protocol: `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md` (M0124-0001).
This file accumulates chunk results; the merged deliverable
`analysis/tpcds-sf1-goopg-20260728.md` (confirm/refute for the 13 §13.3
projections) is written once the sweep reaches Q99.

## Provenance (fixed for the whole sweep)

| field | value |
|---|---|
| engine-id | `bba744a817f7ebdec31fd47edfed40362641dd0c c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc` |
| goopg commit at chunk 1 | `6d6bd1ea` (docs/harness only; engine trees as above) |
| engine binary | `e6774c4f5e22f2fa` (Q1–Q4 up to the Q4 restart), `8f0aac15ce9d2aa9` after — **same source**, see below |
| budget | `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1` |
| goopg | 65436, `-U postgres -d postgres`, `bench/tpcds/runtime_goopg/data` |
| PG 18.3 | 65438, `-U ryo -d tpcds`, `bench/tpcds/runtime/pgdata` |
| GC regime | `GOGC=off`, `GOMEMLIMIT=12GiB` (per `env_tpcds.sh`) |
| state | **S-cold** — `s-cold-proof.txt` (8 relations `reltuples=0 relpages=0`, `pg_stats`=0, `store_sales`=2 880 404) |

**The `*** SWEEP VOID ***` line in `chunk-1-4.txt` is a false positive of the
first-cut provenance guard and does not void the chunk.** The guard compared the
binary's sha256, and `go build` stamps `vcs.revision`/`vcs.time`/`vcs.modified`
into the image, so the docs commit `6d6bd1ea` alone moved it
`e6774c4f → 8f0aac15`. The engine source is identical across the two images:
`git diff --stat 6d6bd1ea^ 6d6bd1ea -- internal cmd` is empty,
`git status --porcelain -- internal cmd` is empty, and `go build -buildvcs=false`
yields one image (`33e7d081…`) for both. The guard now keys on `engine-id`
(committed engine trees + digest of uncommitted engine edits), which is what
`chunk-5-8.txt` prints; see design doc D4a.

## Results

Budget 600 s. "set A" = `analysis/tpcds-sf1-goopg-20260727.md` §5.2, same SF and
**same 600 s budget** — comparable under D2.

| Q | goopg | rows | PG | rows | set A goopg | verdict |
|---|---|---|---|---|---|---|
| 1 | OK 246 s | 100 | OK 206 s | 100 | OK 250 s / 100 | rows = PG; stable |
| 2 | OK 27 s | 2513 | OK 1 s | 2513 | OK 28 s / 2513 | rows = PG; stable |
| 3 | OK 15 s | 31 | OK 0 s | 31 | OK 18 s / 31 | rows = PG; stable |
| 4 | TIMEOUT 622 s | 0 | TIMEOUT 616 s | 0 | TIMEOUT 644 s | times out on **both** engines — excluded from "goopg-only" (D6) |
| 5 | TIMEOUT 621 s | 0 | OK 1 s | 100 | TIMEOUT 649 s | goopg-only timeout; unchanged |
| 6 | OK 57 s | 44 | OK 140 s | 44 | OK 59 s / 44 | rows = PG; goopg 2.5× faster than PG here |
| 7 | OK 64 s | 100 | OK 2 s | 100 | OK 65 s / 100 | rows = PG; stable |
| 8 | **ERROR 26 s** | 0 | OK 0 s | 0 | ERROR 24 s | `ERROR: column ref ca_zip/57 out of MaterializedSlot range 1`; **server survives** (verified `select 1` after) — confirms the §13.3 projection "ERROR, contained, not fixed" |
| 9 | OK 143 s | 1 | OK 2 s | 1 | OK 146 s / 1 | rows = PG; stable |
| 10 | TIMEOUT 624 s | 0 | OK 11 s | 1 | TIMEOUT 649 s | goopg-only timeout; unchanged |
| 11 | OK 79 s | 95 | **TIMEOUT 623 s** | 0 | OK 77 s / 95 | **PG-only timeout** — goopg completes what PG cannot at this budget; unchanged from set A |
| 12 | OK 6 s | 100 | OK 0 s | 100 | OK 6 s / 100 | rows = PG; stable |
| 13 | OK 57 s | 1 | OK 0 s | 1 | OK 62 s / 1 | rows = PG; stable |
| 14 | TIMEOUT 625 s | 0 | OK 37 s | 200 [100+100] | TIMEOUT 647 s | goopg-only timeout; unchanged (PG row count is the summed two-block value — harness fix 2) |
| 15 | OK 17 s | 100 | OK 0 s | 100 | OK 15 s / 100 | rows = PG; stable |
| 16 | OK 48 s | 1 | OK 1 s | 1 | OK 48 s / 1 | rows = PG; stable |

Chunk 1–8 reproduces set A on every cell, so nothing between the two sweeps
changed Q1–Q8 behaviour. `reap_pg_orphans` was **not** idle: PG's Q4 timeout left
one backend running and the reap terminated it (`chunk-1-4.txt`), i.e. every
later PG timing in this sweep is the first that is not contaminated by it.

Chunk 9–16 (`chunk-9-12.txt`, `chunk-13-16.txt`) likewise reproduces set A on
every cell — status, row count and elapsed time all within run-to-run noise
(largest delta 25 s, on the two 600 s-budget timeouts where the excess is
teardown, not query, time). The reap fired once more, on PG's Q11 timeout.
The range was split into two harness invocations (9–12, 13–16) purely to stay
inside the loop's foreground Bash budget; both print the same `engine-id`
baseline, so they are one continuous sweep under D4a.

Running timeout classification (D6), Q1–Q16:

- **both engines** (excluded from "goopg-only"): Q4
- **goopg-only**: Q5, Q10, Q14
- **PG-only** (goopg wins): Q11
- **goopg ERROR**: Q8

## Cursor

`M0124-0001 sweep: 1-16 done; next 17-24.`
