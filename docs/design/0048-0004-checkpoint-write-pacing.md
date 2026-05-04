# Checkpoint Write Pacing — M0048-0004

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

Without pacing, the checkpointer flushes every dirty buffer back-to-back. On
a busy workload with many dirty pages, this saturates sequential write
bandwidth for several seconds and spikes foreground query latency.

PostgreSQL paces the dirty-buffer flush over
`checkpoint_completion_target × checkpoint_timeout` seconds. The default
`target=0.9` with `timeout=5min` spreads the flush over 4.5 minutes, making
the foreground impact barely perceptible.

## 2. Design

### 2.1 GUC (`internal/config/defaults.go`)

`checkpoint_completion_target` is a TypeReal GUC with BootVal `"0.9"`. It is
read in `cmd/goopg/main.go` and applied via
`Checkpointer.SetCompletionTarget(t float64)`.

### 2.2 Pacer (`internal/wal/checkpointer.go`)

`buildPacer(ctx, spread, start)` returns a `func(progress float64) error`
closure, or `nil` when pacing is disabled:

```go
func (c *Checkpointer) buildPacer(ctx context.Context, spread bool, start time.Time) func(float64) error {
    if !spread || c.cfg.CompletionTarget <= 0 || c.cfg.Interval <= 0 {
        return nil
    }
    target := time.Duration(float64(c.cfg.Interval) * c.cfg.CompletionTarget)
    return func(progress float64) error {
        if progress >= 1.0 {
            return nil
        }
        deadline := start.Add(time.Duration(float64(target) * progress))
        wait := time.Until(deadline)
        if wait <= 0 {
            return nil
        }
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(wait):
            return nil
        }
    }
}
```

For `N` dirty buffers and progress `i/N`, the pacer sleeps until
`start + target × (i/N)`. The final buffer (`progress=1.0`) returns
immediately. Total flush time ≈ `target × (N-1)/N ≈ target` for large `N`.

### 2.3 Flush dispatch (`flushDirty`)

```go
func (c *Checkpointer) flushDirty(pacer func(progress float64) error) error {
    if pf, ok := c.flusher.(pacedFlusher); ok && pacer != nil {
        return pf.FlushAllPaced(pacer)
    }
    return c.flusher.FlushAll()
}
```

- Timer-driven checkpoints: `runCheckpoint(ctx, spread=true)` → `buildPacer`
  returns a non-nil closure → `FlushAllPaced(pacer)`.
- SQL `CHECKPOINT` / volume-triggered: `spread=false` → `buildPacer` returns
  `nil` → `FlushAll()` (IMMEDIATE speed).

### 2.4 `Pool.FlushAllPaced` (`internal/storage/bufpool.go`)

Iterates the dirty-slot list, calling `pacer(progress)` after each write.
`progress = float64(written) / float64(total)` where `total` is the dirty
count at the start of the scan. The pacer drives the timing; the pool does
not sleep on its own.

### 2.5 Wiring (`cmd/goopg/main.go`)

```go
cp.SetCompletionTarget(gucRegistry.MustGetReal("checkpoint_completion_target"))
```

Called once at startup. The field is read-only during `Run`; a GUC reload
path can call `SetCompletionTarget` between checkpoints.

## 3. Correctness

- `SetCompletionTarget` clamps the input to `[0, 1]` so degenerate inputs
  (negative or > 1) are silently normalized.
- When `CompletionTarget = 0` or `Interval = 0`, `buildPacer` returns `nil`
  and the checkpointer falls through to `FlushAll` — identical to the v0
  behavior.
- Context cancellation inside the pacer's `select` propagates the cancel
  error out of `FlushAllPaced` and up through `runCheckpoint`, causing the
  checkpoint to abort cleanly.
- WAL-before-data ordering is preserved: each `flushSlot` call inside
  `FlushAllPaced` still calls `wal.FlushUpTo(page.LSN)` before
  `Manager.WriteBlock`.

## 4. Tests (`internal/wal/checkpointer_test.go`)

| Test | Coverage |
|---|---|
| `TestCheckpointerDoDWritePacing` | **DoD**: paced flush ≥ target window; IMMEDIATE-speed path skips pacer |
| `TestCheckpointerSpreadPacing` | Pacer invoked once per buffer, monotonically increasing progress, final progress = 1.0; IMMEDIATE path skips pacer |
| `TestCheckpointerSpreadHonoursDeadlines` | Wall-clock delay ≥ target × (N-1)/N |
| `TestCheckpointerVolumeTrigger` | Volume-triggered checkpoint fires and runs at IMMEDIATE speed |
