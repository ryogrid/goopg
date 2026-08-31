# 0045-0004 — Integration Test: Kill-and-Restart Regression

**Status:** landed 2026-05-04
**Parent milestone:** M0045
**Date:** 2026-05-04

## 1. Objective

Lock in the M0045 fix with an integration test that reproduces
the run-007 failure deterministically: load enough data to
trigger at least one full retention cycle, kill the goopg
process, restart against the same data dir, and verify no data
was lost.

The test must:

1. Fail on `master` (pre-M0045) with the
   `wal: first segment is …, expected 000000000000000000000000`
   error from `internal/wal/writer.go:874`.
2. Pass after M0045-0001 (segment-loop fix) lands. The replay
   phase (M0045-0002) and checkpoint discovery (M0045-0003)
   are exercised but in this minimal test their replay set is
   empty or near-empty.
3. Run in CI under a few seconds.

## 2. Test layout

New file: `internal/server/restart_after_retention_test.go`.

```go
func TestRestartAfterRetention(t *testing.T) {
    dir := t.TempDir()
    // Phase 1: bring up goopg, load data, force enough
    // checkpoints to drive retention past segment 0.
    srv1 := startServer(t, dir, withSegmentSize(1<<20))  // small segs
    db1 := connect(t, srv1)
    seedRows(t, db1, 5_000)                              // > 1 MiB WAL
    forceCheckpoint(t, srv1)
    seedRows(t, db1, 5_000)
    forceCheckpoint(t, srv1)                             // 2 ckpts
    forceRetention(t, srv1)                              // unlink seg 0
    db1.Close()
    // Phase 2: hard-kill the server (skip Stop()) — simulates
    // SIGKILL from the orchestration timeout.
    srv1.HardKill()
    requireSegment0Missing(t, dir)                       // sanity
    // Phase 3: restart against the same data dir.
    srv2 := startServer(t, dir, withSegmentSize(1<<20))
    defer srv2.Close()
    // Phase 4: verify all rows are still readable.
    db2 := connect(t, srv2)
    rows := scanRows(t, db2)
    if got, want := len(rows), 10_000; got != want {
        t.Fatalf("after restart: got %d rows, want %d", got, want)
    }
}
```

Helpers (`startServer`, `connect`, `seedRows`, `forceCheckpoint`,
`forceRetention`, `scanRows`, `HardKill`, `requireSegment0Missing`)
live next to the test or under `internal/testutil/server/` if they
are reusable.

## 3. Hard-kill simulation

A real `kill -9` is OS-level and can't be reproduced inside the
Go test runner, but the failure mode we care about is:
"the writer is mid-flush and the process disappears without
running shutdown hooks". Two practical mechanisms inside one
process:

1. **Skip `Server.Close()`** — drop the reference to the
   `*Server` and let GC decide. Forces the test to deal with the
   fact that no checkpoint, no graceful WAL flush, and no
   shutdown record was emitted. This is the closest in-process
   approximation to SIGKILL.
2. **Cancel the writer's context with no flush** — tools like
   `wal.Writer.Stop()` accept a context; cancelling it without
   draining gives a similar shape.

Pick option 1 — it more accurately mirrors run-007. Test helper:

```go
// HardKill abandons the server without invoking Close() or any
// shutdown handler. fsync of the data dir is forced explicitly
// to ensure the OS-level state matches what a SIGKILL would
// leave behind.
func (s *Server) HardKill() error {
    return os.SyscallSyncDirectoryFile(s.DataDir())
}
```

(Implementation detail: the function may need to live on a test
helper in `internal/testutil/server/` so it doesn't pollute the
production API.)

## 4. Forcing retention

`internal/wal/checkpointer.go::CheckpointerConfig.MaxWALBytes`
controls when a size-driven checkpoint fires; the test sets it
small (a few MiB) so two `seedRows + forceCheckpoint` cycles
drive retention. Alternatively the test calls a yet-to-be-added
`forceRetention(srv)` helper that invokes the retainer
synchronously after a checkpoint.

The exact mechanism is a v0 implementation detail to be settled
during M0045-0004 landing.

## 5. Determinism

- `t.TempDir()` ensures a fresh directory per run.
- `withSegmentSize(1<<20)` shrinks segments to 1 MiB so the
  retention pass deletes seg 0 with realistic seed data sizes.
- Row count thresholds (5 000 / 10 000) are sized to fit
  comfortably within reasonable WAL volume.
- No reliance on wall-clock; checkpoints fired explicitly, not
  via timers.

## 6. CI integration

The test lives in `internal/server/restart_after_retention_test.go`
and runs as part of `go test ./internal/server/...`. Total runtime
target: < 3 s. If it exceeds 10 s the design needs revisiting.

## 7. Verification of the test itself

To prove the test catches the bug:

1. On `master` (pre-fix), `go test
   ./internal/server/... -run TestRestartAfterRetention` MUST
   fail with the `wal: first segment is …, expected
   000000000000000000000000` error.
2. After M0045-0001 lands, the same command MUST pass.
3. After all of M0045-0001 .. M0045-0003 land, the test still
   passes (no regression from the replay phase wiring).

These three checkpoints are the milestone's acceptance criteria.

## 8. Out of scope

- Reproducing the exact run-007 TPC-H workload — too heavy for
  CI. The test exercises the same code path with a synthetic
  small workload.
- Multi-process kill (`kill -9` from outside) — not feasible in
  the Go test runner. The in-process approximation captures the
  bug.
