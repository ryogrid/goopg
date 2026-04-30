# Milestone 0025 — OLTP Performance Analysis & Bottleneck Identification

**Status:** accepted
**Depends on:** Milestone 0003 (HammerDB TPC-H workload), Milestone 0022 (pg_stat_activity + wait events), Milestone 0023 (syntax test suite for correctness verification).
**Drives:** Evidence-based optimisation roadmap for goopg's short-transaction (OLTP) processing path — primarily the `pgbench -c 1 -t 100` (default, TPC-B-like) workload.

## Context

Early measurements with goopg against the `pgbench` CLI showed a dramatic throughput gap between two workloads:

| Workload | Command | Measured TPS | Notes |
|----------|---------|-------------|-------|
| Select-only | `pgbench -S -c 1 -t 1000` | ~12 000 TPS | Read-only, no WAL writes |
| Default (TPC-B) | `pgbench -c 1 -t 100` | ~3 TPS | INSERT/UPDATE/DELETE + WAL |

The default pgbench workload (`-N` / `-b` / no flag → TPC-B) exercises:
1. `BEGIN`
2. `UPDATE pgbench_branches SET abalance = abalance + :delta WHERE bid = :bid`
3. `SELECT abalance FROM pgbench_accounts WHERE aid = :aid`
4. `UPDATE pgbench_tellers SET abalance = abalance + :delta WHERE tid = :tid`
5. `UPDATE pgbench_branches SET abalance = abalance + :delta WHERE bid = :bid`
6. `INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES (:tid, :bid, :aid, :delta, CURRENT_TIMESTAMP)`
7. `COMMIT`

The 4000× throughput gap (12k → 3 TPS) suggests that one or more write-path components are dominating the latency budget.

## Goals

1. Identify the dominant latency contributors in goopg's write path (WAL flush, buffer-pin waits, lock contention, data-file I/O, planner overhead, protocol serialisation).
2. Produce a reproducible measurement methodology using `pgbench` so future changes can be evaluated against the same baseline.
3. Generate a structured report under `analysis/oltp-performance/` with flamegraphs, latency breakdowns, and prioritised optimisation recommendations.

## Methodology

### 1. Wait-Event Profiling (shell script)

Poll `pg_stat_activity` every 1 second during a `pgbench -c 1 -t 100` run and aggregate the `wait_event_type` / `wait_event` columns. This identifies which blocking operations dominate wall-clock time.

Script sketch:
```bash
#!/bin/bash
# collect-waits.sh — poll pg_stat_activity every second
for i in $(seq 1 30); do
    psql -Atqc "SELECT wait_event_type, wait_event, count(*)
                FROM pg_catalog.pg_stat_activity
                WHERE backend_type = 'client_backend'
                  AND state = 'active'
                GROUP BY wait_event_type, wait_event
                ORDER BY count(*) DESC" >> waits.csv
    sleep 1
done
```

### 2. pprof CPU Profiling

Run `goopg` with `GODEBUG=cpuprofile=/tmp/goopg.pprof` or use `net/http/pprof` (if available) during a pgbench run. Generate:

- `go tool pprof -top /tmp/goopg.pprof` — top CPU consumers
- `go tool pprof -flame /tmp/goopg.pprof > flame.svg` — flame graph

Focus on the write-path goroutines (`serveConn`, `state.loop`, `Checkpointer.Run`).

### 3. Custom Latency Instrumentation

If wait events and pprof do not provide enough granularity, add temporary `log/slog` timing lines at key boundaries:

- WAL `FlushUpTo` latency
- `Pool.Pin` latency
- `Manager.ReadBlock` / `WriteBlock` latency
- `lockmgr.Acquire` latency
- `dispatchSimpleQueryViaExecutor` total time

These can be gated behind an environment variable (e.g. `GOOPG_PROFILE=1`) so they do not affect production measurements when disabled.

### 4. Flame Graph Generation

Use the following toolchain:
```bash
# 1. Capture profile
curl -o /tmp/cpu.pprof http://localhost:8080/debug/pprof/profile?seconds=30 &
pgbench -c 1 -t 100
wait

# 2. Generate flame graph
go tool pprof -raw /tmp/cpu.pprof | stackcollapse.pl | flamegraph.pl > flame.svg
```

(If `debug/pprof` is not wired, use `runtime/pprof.StartCPUProfile` / `StopCPUProfile` wrapped around the pgbench run in a test.)

## Deliverables

All files placed under `analysis/oltp-performance/`:

| File | Content |
|------|---------|
| `README.md` | Executive summary + methodology + key findings |
| `wait-event-breakdown.csv` | Raw wait-event samples from 30s of pgbench default workload |
| `wait-event-breakdown.md` | Analysis of the wait-event distribution |
| `cpu-flame.svg` | Flame graph from pprof during pgbench default workload |
| `cpu-top.txt` | `go tool pprof -top` output (top 20 functions) |
| `latency-trace.md` | Custom latency instrumentation results (if added) |
| `recommendations.md` | Prioritised optimisation opportunities with estimated impact |

## Out of Scope

- Detailed storage-engine analysis (page layout, B-tree internals).
- Multi-client scaling analysis (`-c 2` / `-c 4` / `-c 16`).
- Network / wire-protocol microbenchmarks.
- Memory-allocation profiling (goopg uses Go's GC; alloc-heavy paths are visible in pprof).

## Reference

- pgbench documentation: https://www.postgresql.org/docs/current/pgbench.html
- goopg wait-event taxonomy: `docs/reference/wait-events.md`
- pprof documentation: https://go.dev/doc/diagnostics
- Flame graph tools: https://github.com/brendangregg/FlameGraph
- Existing goopg cluster test harness: `internal/testutil/cluster/`

## Definition of Done

1. `analysis/oltp-performance/README.md` exists with executive summary and methodology.
2. Wait-event polling data from a 30-second pgbench default workload is collected and analysed.
3. pprof CPU profile from the same workload is collected and the top-20 functions are documented.
4. A flame graph SVG is generated from the CPU profile.
5. At least one latency contributor is identified with evidence (wait-event percentage, CPU percent, or custom timing).
6. `analysis/oltp-performance/recommendations.md` lists at least 3 concrete optimisation items with estimated priorities.
7. `go test ./...` remains green (the analysis itself must not break the build).
