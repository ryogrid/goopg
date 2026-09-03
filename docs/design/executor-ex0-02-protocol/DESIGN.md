# EX0-02 Design — Commit the measurement protocol

Item: `TODO_EXECUTOR.md` EX0-02 (13 §2.2; gate: protocol doc + one
conforming artifact). Status: design for review; no behaviour change.

## 1. What this item commits

A single normative protocol document (this design, promoted to
`docs/design/not_ralph/planner_refactor_take3/13 §2.3`'s written form) that
every EX1–EX5 item's numbers are void without. It pins three things 13 §2.3
leaves as prose: the artifact header fields, the run rules, and the
profiler invocations. Sources: take3 09 §6 (timing), 09 §3 (floor),
take3 07-gap-analysis §7 items 1/3 (memory parity, sweep-tail), take6
README/RESULTS + take3 00-methodology (profiler), round5 spill design
(pprof check).

## 2. Artifact header (mandatory, every timing/alloc artifact)

Base skeleton from take3 09 §6 (`09:239-249`; fuller form take2 09 §6.6):
`label / date / goopg commit (+dirty flag) / PG 18.3 @ port / suite /
regime stats=S-cold|WARM + parallel=on|off / per-query timeout /
planner-flags (generated) / host load`. Executor additions (this item):

- `GOGC / GOMEMLIMIT` per arm (09 §6: TPC-H `GOGC=100 GOMEMLIMIT=12GiB`;
  TPC-DS harness `GOGC=off` via `bench/tpcds/env_tpcds.sh`).
- `work_mem` **and** `effective_cache_size` per arm (take3 07-gap-analysis
  §7 item 1: cross-engine comparison states both; bench alignment is
  64 MB / 2 GB — the 8× work_mem confound must never recur silently).
- Alloc arm: profiler + object counts alongside time on every item
  (ground rule 2: a CPU win that doubles allocations is not a win).
- Profiler invocation + symbol list (see §4).

No header instance exists in the repo (the take7 files carry measurements
but no `label:`/`regime`/`planner-flags`/`host-load` header block) — this
protocol's skeleton is normative, and every field must be filled, not `N/A`,
on timing artifacts.

## 3. Run rules (mandatory)

From take3 09 §6 (`09:202-232`) and take3 07-gap-analysis §7 item 3: fresh
server per arm through
the cgroup cap (`scripts/goopg-test-run.sh`, distinct `GOOPG_CG_UNIT`,
`GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G GOOPG_MEM_SWAP_MAX=0`); hold server
age constant (sweep-tail collapse: post-timeout server at GOMEMLIMIT with
GOGC=off thrashes GC — Q6 423.94 s vs 5.82 s clean, 73×); reap orphans
(`timeout N psql` kills the client only — MATERIALIZED victim set before
`pg_terminate_backend`); never `pkill -f goopg`; one benchmark at a time;
throwaway ports 5533/5534 (6543x block per `CLAUDE.md`). Per-query cap
300 s (oracles 600 s, plan capture 180 s); `statement_timeout = 0`.
GC state is set explicitly per arm, never inherited: timing arms use
`GOGC=100 GOMEMLIMIT=12GiB` on TPC-H (overriding the `GOGC=off` env
default in `bench/tpch/env_goopg.sh`) and `GOGC=off GOMEMLIMIT=12GiB` on
TPC-DS (`bench/tpcds/env_tpcds.sh`); profiler arms state their GC state in
the header (profiling under a different GC regime than timing is a
protocol violation unless declared and justified). Parallel-path items add
a serial control arm; serial-only items (EX4-01…03) run serially with the
parallel regime marked not-applicable, not silently defaulted. Regime declared
on every artifact; A/B never mixes regimes (take2 09 §6.5: warm vs S-cold
is −15% by itself). Noise band ±17%; per-query claims above 1.2× or on
repeats only; suite claims on totals; TOTAL arm read with plan attribution
(R6/R7). Attribution: a runtime move is yours only if the plan is
unchanged (pin) and the EX0-04 slice moved (the executor transposition of
09 R7, stated at 13 §2.3 — 09 R7 as written attributes a move to a plan
*change*). Values (R8):
projection/join-adjacent changes gate on `-digest` + `-diff` values, never
counts.

## 4. Profiler invocations (mandatory form)

- Timing/alloc attribution: take6 `perf stat` event set
  (`task-clock, cycles, instructions, branch-misses, cache-misses`) attached
  to the server PID — **no trailing `--`** (with it, perf counts the sleep,
  not the process). `perf record -g --call-graph fp -F 199`, user-mode only
  (`:u` suffix; `perf_event_paranoid=2` keeps the kernel out of `perf
  record` — take3 00-methodology; note its `perfrun.sh` shows record
  without `:u`, so the suffix is required explicitly here).
  Same-window SIGPROF caveat applies (take6 README §2: pprof shares
  unaffected; absolute IPC/cache-miss figures carry the profiler's share).
- Alloc arm (mandatory alongside time, §2): pprof heap deltas (`-base`
  before/after, so allocation figures are window deltas not lifetime) +
  block/mutex deltas + `/debug/pprof/allocs` and `/heap` snapshots before
  and after + `/proc/<pid>/io` deltas — the take6 `profile.sh` /
  take3 `perfrun.sh` capture set. Headline wall times are taken with no
  profiler attached.
- Flamegraph scope honesty: no flamegraph-generation command (stackcollapse,
  inferno, `perf script` folding) exists in the take6, take3-methodology, or
  round5 sources — "flamegraph/perf invocation" as inherited scope resolves
  to `perf record` + pprof CPU only. If an item wants rendered flamegraphs,
  proposing the generator is its own addition, not this protocol.
- Symbol reporting list: the take6 ranked table
  (`sync/atomic`, `evalFastExpr`, `evalBinary`, `runtime.memmove`, decode
  path incl. `decodePhysicalPGValue`, `compareDatum`, `PageGetHeapTupleInto`,
  visibility checks, `cloneRowOwned`, agg apply; `strings.ToLower` recorded
  as absent post-take6). New top entries must be named, not folded into
  "other".
- Spill items add the round5 pprof check: `go tool pprof -top
  -nodecount=10 <cpu.pb.gz>` with the `runtime.Stack` family
  (`runtime.Stack`, `runtime.step`, `runtime.pcvalue`, textAddr) dropout
  expectation stated before the run. Round5 has no separate flamegraph
  command — pprof is its invocation.

## 5. Floor and pin (cited, not redefined)

- Floor is take3 09 §3 verbatim: units clean (never `-count=1`), pgbench
  smoke via hook (never `--no-verify` for code), `scripts/tpch-spotcheck.sh`
  on every planner/executor/cost change, `scripts/tpcds-sf05-regression.sh
  sweep` at `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` **plus** the
  TOTAL arm with plan attribution, `cmd/tpch-runner -diff` → `VERDICT:
  PASS`, `make race-gate` for lock/mvcc/storage/aio/wal/shared-planner-state,
  full regress-port after codec/catalog changes. Protocol violation voids
  the item's numbers.
- Plan-shape pin: planner P0-05/06/07 are all still open, so executor items
  pin goopg-vs-goopg (`make plan-gate` + TPC-DS `plans` channel) and say so
  on the item line, per 13 §2.1 — until P0 lands.

## 6. Gate and conforming artifact

Gate: this protocol document merged + one conforming artifact. The
conforming artifact is a minimal REAL run under this protocol — TPC-H Q6,
serial, S-cold, full §2 header including the alloc arm — executed as the
implementation commit that closes EX0-02 (specified below; EX0-06 later
re-baselines the full Q6 chain + per-query tables both suites). A
transcribed illustration alone cannot conform: §2 requires every field
filled, so the take7 transcription below stays a fillability proof and gap
analysis, explicitly not the artifact.

Conforming artifact spec (EX0-02 close): `label: EX0-02-q6-serial-scold`
on the TPC-H SF=1 bench cluster (port 65433), fresh capped server,
`GOGC=100 GOMEMLIMIT=12GiB`, `work_mem=64MB`/`effective_cache_size=2GB`,
Q6 serial ×3 with headline wall time profiler-detached, one profiled run
(§4 capture set), goopg-vs-goopg pin statement, values N/A (timing-only
shape — no projection/join change). Closes EX0-02 only if every §2 field
is filled and every §3 rule was followed; otherwise the item stays open.

Fillability proof (ILLUSTRATIVE values transcribed from take7, not measured
under this protocol — provenance per line):

```
label: EX0-02-example-take7-q6-chain (ILLUSTRATIVE, not a claim)
date: 2026-08-30 (take7 record) | goopg: perf-opt-take4 @ bbe39dd4c (take6)
pg: 18.3 bench cluster (version@port per 09 header form) | suite: TPC-H SF=1 (Q6 only)
regime: stats=S-cold (asserted, not evidenced in take7), parallel=on|off
timeout: 300s | planner-flags: (predates generated flags)
host-load: (not recorded — unacceptable under this protocol)
GOGC/GOMEMLIMIT: (not recorded; take7 ran GOGC=off per its DESIGN — unacceptable)
work_mem/effective_cache_size: 64MB/2GB (P0-12 alignment, not a take7 record)
alloc arm: (not recorded — unacceptable)
timing: serial 4.563→3.792s, parallel 1.009→0.838s (take7 record)
pin: (no Q6 plan comparison in take7 — identical-plan datum is take2 09:410-411)
```

The example doubles as the gap analysis for EX0-06: three header fields
take7 never recorded are exactly what the re-baseline must supply.

## 7. Verification

- Review checks each normative claim against its cited `file:line`
  (evidence pack in the item's commit message trailer).
- `git diff --stat` on the implementation commit shows only this design
  doc (promoted path) + the TODO_EXECUTOR.md EX0-02 line.
- EX0-06's artifact is rejected at review if any §2 header field is
  missing or any §3 run rule is violated.
