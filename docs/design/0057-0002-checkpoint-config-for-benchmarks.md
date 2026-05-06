# 0057-0002 — Checkpoint Configuration for TPC-H Benchmarks

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| milestone  | 0057 |

## 1. Problem

PostgreSQL's default checkpoint settings fire a checkpoint roughly
every 5 minutes (checkpoint_timeout = 5min, max_wal_size = 1GB).
During a 2-hour TPC-H power test, this fires ~24 checkpoints, each
of which:
- Generates significant I/O (flush of all dirty pages).
- Consumes CPU from the checkpointer goroutine.
- Can skew query latency measurements for queries that coincide with
  the flush window.

goopg mirrors these defaults; a clean benchmark environment should
suppress checkpoints entirely during the measurement window.

## 2. Design

### 2.1 GUC settings for benchmarks

Write the following to postgresql.conf during benchmark setup:

```
# M0057-0002: suppress mid-benchmark checkpoints.
# A manual CHECKPOINT is issued before the power test starts; no
# automatic checkpoint should fire during the 2-hour run.
checkpoint_timeout = 24h
max_wal_size = 1024GB
```

`checkpoint_timeout = 24h` ensures the time-based threshold cannot
trigger during any run. `max_wal_size = 1024GB` ensures the WAL-
accumulation threshold cannot trigger (SF=1 generates far less than
1 TB of WAL in a single power test).

### 2.2 Pre-test CHECKPOINT

Before every power test, issue an explicit CHECKPOINT via:

```
./tmp/goopg-bench-bin checkpoint -D bench/tpch/runtime_goopg/data
```

This flushes all dirty pages to a clean checkpoint, so the run starts
with zero dirty pages and the checkpointer has nothing to do.

### 2.3 Implementation

`bench/tpch/setup_goopg.sh` writes a postgresql.conf during `--reset`
that includes the two GUC lines above. This means every fresh setup
inherits the benchmark-safe checkpoint configuration.

Files:
- `bench/tpch/setup_goopg.sh` — append GUC lines.

### 2.4 Verification

With M0057-0001's background-worker logging enabled, the power-test
server log must not contain `"checkpoint start"` between the pre-test
CHECKPOINT completion and the end of the run.

## 3. Acceptance

See milestone doc M0057-0002 acceptance criterion.

## 4. References

- PostgreSQL docs: `checkpoint_timeout`, `max_wal_size`.
- `bench/tpch/setup_goopg.sh` — current postgresql.conf generation.
- `internal/wal/checkpointer.go` — the Go-side checkpoint timer.
