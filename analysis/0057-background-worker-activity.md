# Background-Worker Activity Logging (M0057-0001)

**Date:** 2026-05-06
**Author:** goopg perf-analysis (M0057)

## 1. Goal

Add visibility into background-daemon activity during TPC-H benchmark
runs so it is possible to confirm (a) which daemons are active,
(b) when they fire relative to query execution, and (c) whether any
daemon fires during the power-test window.

## 2. Changes made

### 2.1 bgwriter (`internal/storage/bgwriter.go`)

Added `logger *slog.Logger` field and `SetLogger()` setter. The
`run()` loop now logs after each `WriteDirtyPages()` call that
returns `n > 0`:

```
"bgwriter flush" pages=N
```

If no pages are dirty, no log line is emitted (avoids noise during
idle periods). Default logger is `slog.Default()`.

### 2.2 WAL writer (`internal/wal/writer.go`)

Added `Logger *slog.Logger` to `Config`. The `flushUpTo()` function
now logs after each successful fdatasync sequence:

```
"walwriter flush" lsn=X segments_fsynced=N
```

This fires once per transaction commit that flushes WAL, which for
TPC-H typically means per-INSERT commit in the build phase and
per-query in the power test.

### 2.3 Checkpointer (`internal/wal/checkpointer.go`)

The checkpointer already had a logger (`cfg.Logger *slog.Logger`).
Added two log points in `runCheckpoint()`:

At entry:
```
"checkpoint start" type=scheduled|requested
```

At exit:
```
"checkpoint complete" type=scheduled|requested lsn=X elapsed_ms=M
```

These are the most important lines for benchmark analysis: a
`"checkpoint start"` appearing between the pre-test CHECKPOINT and
the end of the run indicates the benchmarker configuration is
insufficient (see M0057-0002 for the fix).

### 2.4 Autovacuum (`internal/autovacuum/launcher.go`)

No changes needed — full logging was already present:
- `"autovacuum launcher starting"`
- `"autovacuum: running vacuum" table=<name>`
- `"autovacuum: vacuum done" table=<name> dead_removed=N live=M`
- `"autovacuum: running analyze" table=<name>`
- `"autovacuum: analyze done" table=<name> rows=N`

## 3. What to look for in the server log

During a SF=1 power test, the expected log signature is:

```
# Pre-test CHECKPOINT (manually issued before run):
2026-...: level=INFO msg="checkpoint start" type=requested
2026-...: level=INFO msg="walwriter flush" lsn=X segments_fsynced=1
2026-...: level=INFO msg="checkpoint complete" type=requested lsn=X elapsed_ms=M

# During the power test — SHOULD NOT appear (with M0057-0002 config):
2026-...: level=INFO msg="checkpoint start" type=scheduled   ← ALARM if seen

# bgwriter activity between queries (acceptable):
2026-...: level=INFO msg="bgwriter flush" pages=N

# Per-query WAL flush (one per query that writes WAL):
2026-...: level=INFO msg="walwriter flush" lsn=X segments_fsynced=1
```

The critical invariant: **no `"checkpoint start"` should appear
between the pre-test CHECKPOINT and the last query's completion.**

## 4. Acceptance status

- [x] `internal/storage/bgwriter.go` — flush log added.
- [x] `internal/wal/writer.go` — flush log added.
- [x] `internal/wal/checkpointer.go` — start + complete logs added.
- [x] `internal/autovacuum/launcher.go` — no change needed (already logged).
- [x] `go test ./internal/storage/... ./internal/wal/...` PASS.

## 5. Empirical verification status

Empirical verification (running a Q14 + checking the log) requires
a fresh SF=1 database to be loaded. The code changes are correct per
code review; the log-excerpt evidence will be added after the next
schema build run under M0054-0007.

**M0057-0001 is LANDED (implementation).**
Empirical evidence will be appended here after the first
benchmark run with this binary.
