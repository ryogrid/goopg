# Milestone 0057 — TPC-H Measurement Prerequisites

**Status:** planned
**Depends on:** Milestone 0054 (TPC-H perf optimisation), Milestone 0053 (complete-run verification)
**Drives:** M0054-0007 final pass; reliable repeat benchmark infrastructure

## Context

The M0054-0007 HammerDB SF=1 power-test attempts (run-012 through run-015)
were repeatedly disrupted by infrastructure failures unrelated to query
optimisation:

- kill -KILL of goopg during an in-flight query prevented clean restart
  (RDBMS minimum requirement: crash recovery).
- SQL `CHECKPOINT` panicked with M0042-0004 in run-015 (fixed in
  commit f5021c8, but the root cause was the driver issuing CHECKPOINT
  mid-run without goopg supporting it cleanly).
- Background-process activity (bgwriter, WAL writer, checkpointer,
  autovacuum) was invisible during measurement; it was unknown whether
  they fired mid-benchmark or were absent.
- HammerDB build script's CHECKPOINT behaviour was unknown.
- tpch-runner had no per-query cancellation; a hung query required
  server restart to unblock.
- No README existed to guide manual operation of the bench tooling.

Milestone 0057 systematically addresses each of these before the next
M0054-0007 attempt.

## ⚠ NO-DEFERRAL POLICY

**Do NOT close any sub-task silently with a forward reference.**
If a sub-task is blocked, the `fix_plan.md` entry MUST:
1. Name the specific blocker.
2. Carry the marker `BLOCKED: <reason>`.
3. Open a named follow-up sub-task with an explicit acceptance criterion.

A coding agent reviewing `fix_plan.md` must be able to identify
unfinished work without reading commit messages or analysis reports.

## Sub-tasks

### M0057-0001 Background-worker activity logging

**Goal:** Add LOG-level log lines so benchmark runs show whether
bgwriter, WAL writer, checkpointer, and autovacuum are active.

**Scope:**
- `internal/storage/bgwriter.go` — log each flush-batch invocation:
  `"bgwriter flush" pages=N`.
- `internal/wal/writer.go` (WAL writer) — log each WAL flush:
  `"walwriter flush" lsn=X`.
- `internal/wal/checkpointer.go` — log checkpoint start, completion,
  and stats: `"checkpoint start" type=scheduled|requested`,
  `"checkpoint complete" dirty_written=N elapsed_ms=M`.
- `internal/autovacuum/launcher.go` — log when autovacuum selects a
  table, starts, and completes a vacuum pass.

**NOT logging:** every individual page write or WAL record (would be
too noisy). One log line per logical unit of work (flush batch,
checkpoint, vacuum pass).

**Acceptance:**
- After schema-build + `CHECKPOINT` + Q14 single-run via tpch-runner,
  the server log contains:
  - ≥ 1 `"bgwriter flush"` OR explicit confirmation that 0-dirty pages
    triggered 0 flushes (trace in analysis report).
  - ≥ 1 `"walwriter flush"` OR confirmation WAL writer not active.
  - ≥ 1 `"checkpoint complete"` for the manually-triggered CHECKPOINT.
  - Autovacuum lines if ANALYZE ran, or explicit confirmation it's
    disabled for the bench schema.
- Analysis report `analysis/0057-background-worker-activity.md`
  committed with annotated log excerpt.

---

### M0057-0002 Checkpoint suppression during power test

**Goal:** Prevent the checkpointer from firing mid-benchmark by
configuring aggressive time/WAL thresholds.

**Scope:**
- Add to `bench/tpch/setup_goopg.sh`'s postgresql.conf generation:
  ```
  checkpoint_timeout = 24h
  max_wal_size = 1024GB
  ```
  (24 h time threshold, 1 TiB WAL accumulation threshold — neither
  will be hit in a 2-hour run regardless of data written.)
- The explicit `CHECKPOINT` issued before the power test
  (M0054-0007-checkpoint-before-run requirement) remains the clean
  starting point.
- Verify via M0057-0001's logging that no `"checkpoint start"` line
  appears in the server log between the pre-test CHECKPOINT and the
  last query's completion.

**Acceptance:**
- `bench/tpch/setup_goopg.sh` writes the two GUC lines.
- Power-test server log contains NO `"checkpoint start"` lines between
  the pre-test CHECKPOINT and end of run.
- Analysis note in `analysis/0057-background-worker-activity.md`.

---

### M0057-0003 HammerDB build-script CHECKPOINT audit

**Goal:** Determine whether the HammerDB schema-build (`buildschema`)
step issues a `CHECKPOINT` before signalling "FINISHED SUCCESS".

**Scope:**
- Read `HammerDB/src/postgresql/pgolap.tcl` for explicit `CHECKPOINT`
  or `vacuum` calls in the build flow.
- Grep the captured build log for the string `CHECKPOINT` (case-
  insensitive, SQL level) and for `checkpoint` in the server log
  during the build.
- Report in `analysis/hammerdb-checkpoint-audit.md`:
  - **If CHECKPOINT IS issued:** confirm the post-CHECKPOINT state is
    the clean start point for the power test. No further action needed.
  - **If CHECKPOINT IS NOT issued:** verify that clean restart between
    run-014 and run-015 succeeded via WAL replay, and confirm WAL replay
    is complete before the power test. If there is any uncertainty,
    open a named sub-task M0057-0003-wal-replay-gap.

**Acceptance:**
- `analysis/hammerdb-checkpoint-audit.md` committed and referenced from
  the fix_plan entry.

---

### M0057-0004 tpch-runner per-query cancellation

**Goal:** Allow in-flight queries to be interrupted via the PostgreSQL
cancel protocol without restarting the server.

**Problem:** `context.WithTimeout` in tpch-runner closes the TCP
connection when the deadline fires. The server-side query goroutine
continues until it tries to write to the closed socket, which means
the query keeps consuming CPU/memory even after the client has moved on.

**Scope (server side):**
- Confirm whether goopg handles `CancelRequest` (byte-8 cancel startup
  message with PID + secret key). If not, implement:
  1. Track (PID, cancel-key) per backend in `server.go`.
  2. On `CancelRequest`, find the matching backend and call its
     context's cancel function → executor receives `context.Canceled`
     → query surfaces as `"canceling statement due to user request"`
     (SQLSTATE 57014).

**Scope (client side):**
- Add `--cancel-after=<duration>` flag to `cmd/tpch-runner` that
  sends `CancelRequest` on a second TCP connection (as per the wire
  protocol spec) after the timeout fires, instead of closing the
  primary connection.
- The primary connection returns the 57014 error, is kept alive, and
  the next query runs immediately.

**Acceptance:**
- `tpch-runner --queries=9 --cancel-after=5s` completes within ~5s
  and prints `Q9: ERROR elapsed=5.Xs — pq: canceling statement due to
  user request`. Server shows `57014` error in log. Server CPU drops
  immediately.
- `go test ./internal/server/ -run TestCancelRequest` PASS.

---

### M0057-0005 Crash recovery (kill -KILL) verification

**Goal:** Guarantee that kill -KILL during a live query does not
prevent a clean restart. Crash recovery is a minimum RDBMS requirement.

**Scope:**
- Write an automated test `TestKillKillRecovery` in
  `internal/testutil/cluster/`:
  1. Start a fresh cluster via `cluster.New`.
  2. Load N rows into a test table.
  3. Spawn a long-running SELECT (e.g., `pg_sleep(30)`) on a
     background connection.
  4. Kill the goopg process with `os.Process.Kill()` (SIGKILL).
  5. Restart the cluster.
  6. Assert the server starts cleanly (no error in startup log).
  7. Assert the loaded rows are all present (`SELECT count(*)`).
  8. Assert no WAL-replay ERRORs appear in the log.
- If the test fails, the WAL redo path has a bug. Fix it — do not
  defer.

**Acceptance:**
- `go test ./internal/testutil/cluster/ -run TestKillKillRecovery
  -count=1 -timeout 120s` PASS.
- Manual test on the SF=1 dataset (kill -KILL mid-Q9, restart,
  SELECT count(*) on all 8 TPC-H tables returns correct rows)
  documented in `analysis/0057-crash-recovery-test.md`.

---

### M0057-0006 cmd/tpch-runner README.md

**Goal:** A user (the project owner) can follow the README to manually
set up a SF=1 schema, issue queries, and observe results — without
reading the implementation code.

**Scope:**
- `cmd/tpch-runner/README.md` covering:
  1. Prerequisites (goopg binary built, HammerDB checked out,
     bench/tpch/setup_goopg.sh).
  2. Complete manual workflow:
     a. `bash bench/tpch/setup_goopg.sh --reset`
     b. `bash bench/tpch/build_schema_goopg.sh` (wait for FINISHED SUCCESS)
     c. `./tmp/goopg-bench-bin checkpoint -D bench/tpch/runtime_goopg/data`
     d. Run individual queries: `./tpch-runner --queries=9,20 --per-query-timeout=600s`
     e. Run full stream: `./tpch-runner`
     f. Cancel a running query: `./tpch-runner --queries=20 --cancel-after=60s`
  3. Complete flags reference table.
  4. Troubleshooting section (common errors + remedies).

**Acceptance:**
- README exists and is reviewed for completeness.
- A new developer (or the project owner) can follow it cold to
  reproduce a Q14 measurement.

---

## Definition of Done

All six sub-tasks marked `[x]` LANDED in `fix_plan.md` with empirical
evidence links. The M0054-0007 power test can then resume with a clean,
transparent infrastructure.

## Reference

- `analysis/btree-staged-enhancement-results-2026-05-06.md`
- `bench/tpch/setup_goopg.sh`
- `HammerDB/src/postgresql/pgolap.tcl`
- `cmd/tpch-runner/main.go`
- `internal/wal/checkpointer.go`
- `internal/storage/bgwriter.go`
- `internal/autovacuum/launcher.go`
