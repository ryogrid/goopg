# Milestone 0029 — HammerDB TPC-H End-to-End Run on goopg

**Status:** planned
**Depends on:** Milestone 0003 (initial TPC-H workload support), Milestone 0025–0027 (performance optimisations that may affect TPC-H throughput).
**Drives:** First successful end-to-end run of the HammerDB TPC-H power test (Q1–Q22) against goopg at scale factor 1.

## Context

The TPC-H benchmark (22 decision-support queries) has never been
run end-to-end against goopg. Previous work (M0003) validated
individual queries, but a complete power test (schema build +
data load + 22 queries) has not been completed.

The benchmark scripts live under `bench/tpch/` and are structured
for both PostgreSQL (reference) and goopg (target):

| Script | Purpose |
|--------|---------|
| `setup_goopg.sh` | Build goopg binary, init data dir, start server |
| `build_schema_goopg.sh` | HammerDB Tcl → create TPC-H schema + bulk-load |
| `run_power_test_goopg.sh` | HammerDB Tcl → run power test (Q1..Q22) |
| `env_goopg.sh` | Paths, port, credentials for goopg run |
| `stop_goopg.sh` | Shut down the goopg cluster |

## Workflow

Since the TPC-H benchmark can take a long time (especially the
bulk-load phase and individual query execution), the run must
be performed as a **background task** with output redirected to
log files. The operator checks progress by reading those logs.

### Background Execution

```bash
nohup bash bench/tpch/run_all.sh > /tmp/tpch-run-20260501.log 2>&1 &
```

Or step by step for goopg:

```bash
# 1. Start goopg cluster
bash bench/tpch/setup_goopg.sh --reset

# 2. Build schema + bulk-load (background, can take 10+ minutes)
nohup bash bench/tpch/build_schema_goopg.sh \
    > bench/tpch/logs/build_$(date +%s).log 2>&1 &

# 3. Check build progress
tail -f bench/tpch/logs/build_*.log

# 4. Run power test (background, can take 10+ minutes per query set)
nohup bash bench/tpch/run_power_test_goopg.sh \
    > bench/tpch/logs/run_$(date +%s).log 2>&1 &

# 5. Monitor progress
tail -f bench/tpch/logs/run_*.log

# 6. Stop cluster
bash bench/tpch/stop_goopg.sh
```

### Progress Monitoring

Since the benchmark runs in the background with no timeout,
monitor it via:

```bash
# Watch the end of the log file as it grows
tail -F bench/tpch/logs/run_*.log

# Check if HammerDB process is still running
pgrep -a hammerdb

# Check goopg server status
bin/goopg status -D bench/tpch/runtime/pgdata

# Check disk usage (data dir grows during build)
du -sh bench/tpch/runtime/pgdata
```

## Definition of Done

1. The goopg cluster starts successfully with `setup_goopg.sh --reset`.
2. The HammerDB schema build completes without error (all TPC-H
   tables created: customer, lineitem, nation, orders, partsupp,
   part, region, supplier).
3. The bulk-load phase completes successfully (SF=1 data loaded).
4. The power test runs all 22 queries (Q1–Q22) without crashing
   the server.
5. At least 10 of 22 queries return correct results (verified
   against PostgreSQL reference output or with sanity checks).
6. No server panic or unrecoverable error during the run.
7. A brief summary of the results is written to
   `analysis/tpch-hammerdb-run-001.md`.

## Reference

- Benchmark scripts: `bench/tpch/`
- HammerDB TPC-H documentation: `HammerDB-5.0/doc/`
- TPC-H specification: https://www.tpc.org/tpch/
