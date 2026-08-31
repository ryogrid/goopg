# 0057-0001 — Background-Worker Activity Logging

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| milestone  | 0057 |

## 1. Problem

There is no visibility during a TPC-H benchmark run into whether
background daemons (bgwriter, WAL writer, checkpointer, autovacuum)
are active, idle, or absent. This makes it impossible to determine
whether mid-benchmark I/O spikes are caused by query execution or by
background flushing.

## 2. Design

### 2.1 Log granularity

Add a single LOG-level line per *logical unit of work* for each daemon:

| Daemon | Trigger | Log message |
|--------|---------|-------------|
| bgwriter | each flush-batch invocation in `bgwriter.run()` | `"bgwriter flush" pages=N elapsed_us=M` |
| WAL writer | each `WAL flush` in `Writer.run()` | `"walwriter flush" lsn=X elapsed_us=M` |
| Checkpointer | checkpoint start | `"checkpoint start" type=scheduled\|immediate\|requested` |
| Checkpointer | checkpoint complete | `"checkpoint complete" dirty_written=N wal_flushed_bytes=M elapsed_ms=K` |
| Autovacuum | vacuum selected | `"autovacuum begin" table=<name>` |
| Autovacuum | vacuum complete | `"autovacuum complete" table=<name> dead_removed=N elapsed_ms=M` |

Not logged per-page or per-WAL-record (would be too noisy under load).

### 2.2 Log level

Use the existing `slog.Logger` passed to each component. Use `Info`
level for checkpoint start/complete (signal events) and for bgwriter
flush (periodic). The lines only appear in the server log file, not
on the wire.

### 2.3 Conditional logging / GUC gate

For bgwriter and WAL writer (which fire multiple times per second),
add a `log_bgwriter_activity` GUC (type bool, default false) and
`log_walwriter_activity` (bool, default false). Checkpoint and
autovacuum events are lower-frequency and log unconditionally at
INFO level.

The benchmark setup script (`setup_goopg.sh`) sets both GUCs to true
when running in benchmarking mode (new `--verbose-background` flag).

### 2.4 Files touched

- `internal/storage/bgwriter.go` — add INFO log in `run()`.
- `internal/wal/writer.go` — add INFO log per flush in the run loop.
- `internal/wal/checkpointer.go` — add INFO log at start+complete of
  each checkpoint pass.
- `internal/autovacuum/launcher.go` — add INFO log at vacuum begin
  and complete.
- `internal/config/defaults.go` — register `log_bgwriter_activity`
  and `log_walwriter_activity` bool GUCs.
- `bench/tpch/setup_goopg.sh` — add `--verbose-background` flag that
  appends the GUC settings to postgresql.conf.

## 3. Tests

- `TestBgwriterFlushLogged` — verify a flush-batch log line appears
  when bgwriter runs on a dirty pool.
- `TestCheckpointerLogs` — verify start+complete log lines appear in
  a checkpointer run.

## 4. Acceptance

See milestone doc M0057-0001 acceptance criterion.

## 5. References

- `internal/storage/bgwriter.go::(*Bgwriter).run`
- `internal/wal/checkpointer.go::(*Checkpointer).runCheckpointCycle`
- `internal/autovacuum/launcher.go::(*Launcher).Run`
