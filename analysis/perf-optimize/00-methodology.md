# 00 — Methodology

## Scope

Side-by-side `pgbench` comparison of `goopg` (Go reimplementation of PostgreSQL, this repo) against upstream PostgreSQL 18.3 (`./postgres/local_install/`). Three canonical workloads × three client/thread counts = nine measurement patterns per system. Goopg side captures all eight available pprof profile types; PG side is unprofiled (TPS-reference only).

## Environment

| Property | Value |
|---|---|
| Host | AMD Ryzen 7 5700X (8 physical / 16 logical), 32 GiB RAM, 64 GiB swap |
| Kernel | `Linux 6.6.114.1-microsoft-standard-WSL2 x86_64` |
| Go toolchain | `go1.25.0` |
| goopg build | `vcs.revision=6774c5388a3ca2c7a83dc780d68a1e35cfb5ce8e` (built 2026-05-18 02:45 UTC; rebuilt locally 2026-05-18 11:50 JST after adding `GOOPG_PPROF_ADDR` env hook) |
| Upstream PG | `pgbench (PostgreSQL) 18.3`, `psql (PostgreSQL) 18.3` from `postgres/local_install/bin/` |
| Storage | `/dev/sdd` ext4 under WSL2 backing store, ~825 GiB free at run start |

## Test configuration

| GUC / env | Value | Rationale |
|---|---|---|
| `shared_buffers` | `2560MB` (= 2.5 GiB) | User specification |
| `wal_buffers` | `134217728` bytes (goopg) / `128MB` (PG) (= 128 MiB) | User specification; goopg's GUC takes bytes (`internal/config/defaults.go:250`), PG accepts unit form |
| `checkpoint_timeout` | `24h` | Suppress checkpoint during the 180 s runs |
| `max_wal_size` | `1024GB` | Suppress WAL-size-triggered checkpoint |
| `min_wal_size` | `1024MB` | Avoid WAL-segment recycle pressure |
| `checkpoint_completion_target` | `0.9` | Default |
| `max_connections` | `200` | Required for `-c 100` headroom |
| `GOMEMLIMIT` | `18GiB` | User specification; soft heap cap |
| `GOGC` | unset → defaults to `200` from `cmd/goopg/main.go:198` (M0098-0007 OLTP tuning) | Preserves project default |
| `GOOPG_MUTEX_PROFILE_RATE` | `1` | Full mutex contention sampling (`cmd/goopg/main.go:160-164`) |
| `GOOPG_BLOCK_PROFILE_RATE` | `1` | Full blocking-event sampling (`cmd/goopg/main.go:166-170`) |
| `GOOPG_PPROF_ADDR` | `127.0.0.1:6160` | New env hook (this commit) — avoids collision with Ralph test loop which uses default 6060 |
| `LD_LIBRARY_PATH` | `postgres/local_install/lib:…` | `pgbench`/`psql` need `libpq.so.5.18` (`PQsendPipelineSync`) |

`shared_buffers = 2560MB` materializes as 327 680 buffer slots (8 KiB pages) in goopg's startup log; matches PG's allocation.

## Workloads

| Workload | pgbench flag | Statement mix |
|---|---|---|
| `standard` | (none — TPC-B-like) | BEGIN; SELECT abalance FROM accounts; UPDATE accounts SET abalance = abalance + :delta; UPDATE branches; UPDATE tellers; INSERT INTO history; END |
| `simple-update` | `-N` | BEGIN; UPDATE accounts; INSERT INTO history; END |
| `select-only` | `-S` | SELECT abalance FROM pgbench_accounts WHERE aid = :aid |

Scale factor: **100** (10 M `pgbench_accounts` rows, ~1.5 GiB raw data plus indexes; fits in 2.5 GiB shared_buffers).

## Run matrix

For each client count `c ∈ {10, 50, 100}` (matched thread count `-j c`):

1. Restart goopg (cleans cumulative `mutex`/`block` profile state so each client-count block's profiles are tractable).
2. For each workload `w ∈ {select-only, simple-update, standard}`:
   - Start the pprof collector in the background (`analysis/perf-optimize/scripts/pprof_collect.sh`).
   - Run `pgbench -h 127.0.0.1 -p 5533 -U postgres -c c -j c -T 180 -P 10 <flag> postgres` against goopg.
   - On non-zero pgbench exit *and* `c == 100`: write `SKIPPED_<label>.txt` with the last 20 stderr lines, kill the collector, restart goopg if dead, **continue** (per the user's "do not modify goopg" directive).
   - Run the same against PG on port 5534 (unprofiled).

Total nominal wall-time: `init ≈ 5 min + 9 × (180 s + 180 s + ~5 s scaffold) ≈ 60 min`.

Workload ordering within each client-count block (`select-only` → `simple-update` → `standard`) intentionally runs the read-only workload first to minimise state drift; `pgbench_history` grows monotonically across the write workloads but `pgbench_accounts` row count is invariant.

## Profile capture schedule

Per goopg run (`T = 0` is `pgbench` start; window = 180 s):

| Profile | Endpoint | Window | Rationale |
|---|---|---|---|
| CPU | `/debug/pprof/profile?seconds=120` | T+30 → T+150 | Sample steady state past pgbench ramp |
| trace | `/debug/pprof/trace?seconds=30` | T+30 → T+60 | Scheduler/syscall view; parallel with CPU sample |
| mutex_base | `/debug/pprof/mutex` | snap T+30 | Baseline for `pprof -base` delta |
| block_base | `/debug/pprof/block` | snap T+30 | Same |
| goroutine | `/debug/pprof/goroutine?debug=2` | snap T+~60 (after trace) | Full per-goroutine stacks |
| heap | `/debug/pprof/heap` | snap T+150 | inuse + alloc samples |
| allocs | `/debug/pprof/allocs` | snap T+150 | Alloc rate analysis |
| mutex | `/debug/pprof/mutex` | snap T+150 | Diff vs baseline to scope contention to this run |
| block | `/debug/pprof/block` | snap T+150 | Same |
| threadcreate | `/debug/pprof/threadcreate?debug=1` | snap T+150 | OS-thread growth (Go scheduler M's) |

Profiles are stored at `analysis/perf-optimize/runs/<RUN_ID>/profiles/goopg_c<C>_<wl>.<type>.<ext>` and analyzed via `analysis/perf-optimize/scripts/analyze.sh <RUN_ID>`.

## Profile overhead

- CPU profile: ~5 % overhead during the 120 s window.
- mutex/block sampling at rate=1: ~1–2 % per `cmd/goopg/main.go:150` comment.

Both are applied uniformly across all goopg patterns; the PG side has zero such overhead. The numeric TPS gap in `01-results-matrix.md` therefore slightly **understates** raw goopg/PG ratio; the architectural conclusions in chapters 02–07 are unaffected.

## Ralph isolation

A Ralph autonomous-loop instance is running on the side. Ralph spawns short-lived test goopg instances under `/tmp/TestE2E_*/` that bind hardcoded pprof on port 6060. To keep this measurement's profile capture deterministic:

- Data dirs under `tmp/perf-optimize/<RUN_ID>/{goopg,pg}-data/` (not `tmp/pgbench-compare/` which Ralph's own pgbench-compare loop uses).
- Ports `5533` (goopg) and `5534` (PG) — one decade clear of `bench/pgbench-compare/`'s `5433`/`5434`.
- pprof on `127.0.0.1:6160` via the new `GOOPG_PPROF_ADDR` env hook (`cmd/goopg/main.go`). Ralph's tests still default to `6060` — no collision.

`.ralph/` and `.ralphrc` are not touched. The change to `cmd/goopg/main.go` is additive (env override only; default `:6060` preserved).

## Build artefacts archived per run

`analysis/perf-optimize/runs/<RUN_ID>/` contains:

- `pgbench_<target>_c<C>_<wl>.txt` — raw pgbench stdout (TPS, latency, per-10s progress).
- `SKIPPED_<...>.txt` — present iff `c=100` failed; contains the last 20 lines of pgbench output.
- `init_<target>.txt` — `pgbench -i -s 100` stdout (for sanity / init-time tracking).
- `profiles/<label>.<type>.<ext>` — every pprof artefact (see schedule above) plus `<label>.collector.log` (the pprof collector's own stderr).
- `goopg.bin` — the exact goopg binary used (so `go tool pprof` keeps working even if `bin/goopg` is rebuilt by Ralph mid-analysis).
- `driver.log` — the driver script's own log.
- `pprof_top/<label>.<type>.txt` — `analyze.sh` output: `pprof -top -cum -nodecount=40` plus `-list` on the top ~8 hot symbols.
- `pprof_top/<label>.<type>.delta.txt` — for mutex/block: `-base` diff vs the baseline taken at T+30.
- `results_summary.tsv` — flat TSV `target | clients | workload | tps | lat_avg_ms | lat_stddev_ms | status` (populated by `analyze.sh`).

## Reproducibility

```bash
# Pre-flight: ensure bin/goopg matches the build under measurement
go build -o bin/goopg ./cmd/goopg

# Drive the suite
bash analysis/perf-optimize/scripts/run_perf_suite.sh

# Post-process
bash analysis/perf-optimize/scripts/analyze.sh "$(ls -t analysis/perf-optimize/runs/ | head -1)"
```

Environment overrides honoured by `run_perf_suite.sh`:

| Env | Default | Purpose |
|---|---|---|
| `RUN_ID` | `$(date +%Y%m%d_%H%M%S)` | Deterministic re-runs |
| `DURATION` | `180` | Per-pattern pgbench duration (s) |
| `CLIENT_COUNTS` | `10 50 100` | Override matrix (e.g. `CLIENT_COUNTS=10` for a quick check) |
| `GOMEMLIMIT` | `18GiB` | Soft heap cap |
| `GOOPG_PPROF_ADDR` | `127.0.0.1:6160` | Where goopg exposes pprof |

## Deviations from `bench/pgbench-compare/run_comparison.sh`

| Aspect | `bench/pgbench-compare` | This suite |
|---|---|---|
| Ports | 5433 / 5434 | 5533 / 5534 (Ralph-isolated) |
| Data root | `tmp/pgbench-compare/` | `tmp/perf-optimize/<RUN_ID>/` |
| `wal_buffers` | 100 MB | 128 MB (per user spec) |
| Client counts | 100 only | {10, 50, 100} |
| pprof capture | none | all 8 profile types per pattern |
| goopg restart per block | no | yes (between client counts) |
| Failure handling | abort | SKIPPED + continue |
