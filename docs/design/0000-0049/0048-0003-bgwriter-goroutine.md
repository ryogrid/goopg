# bgwriter Goroutine — M0048-0003

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

Without a background writer, dirty buffer-pool pages are only written to disk
when the eviction clock-sweep (evictLocked) needs to reuse a slot. That
synchronous I/O in the eviction hot path increases foreground latency and,
on a pool with many dirty pages, can push the "dirty victim rate" above 50%.

PostgreSQL's `bgwriter` process proactively writes dirty pages between
checkpoints so that eviction usually finds clean victims ready to reuse.

## 2. Design

### 2.1 Bgwriter goroutine (`internal/storage/bgwriter.go`)

```go
type Bgwriter struct {
    pool     *Pool
    delay    time.Duration  // inter-tick interval (bgwriter_delay GUC)
    maxPages int            // max writes per tick (bgwriter_lru_maxpages GUC)
    stop     chan struct{}
    done     chan struct{}
}
```

`Bgwriter.Start()` launches a goroutine that ticks every `delay` and calls
`Pool.WriteDirtyPages(maxPages)`. `Stop()` closes the stop channel and waits
for the goroutine to finish.

### 2.2 Pool.WriteDirtyPages (`internal/storage/bufpool.go`)

Scans the pool's slot array from `bgwriterHand` (a cursor independent of the
eviction `clockHand`), collecting up to `maxPages` dirty, unpinned slots.
For each collected slot it:

1. Takes `contentMu.RLock()` (shared — read only, no exclusive needed).
2. Re-verifies under `poolMu` that the slot is still valid, dirty, and unpinned.
3. Calls `flushSlot` (WAL FlushUpTo + Manager.WriteBlock — no fsync).
4. Clears `slot.dirty` under `poolMu` if the tag is unchanged.
5. Releases `contentMu.RUnlock()`.

Returns the number of pages written.

The bgwriter does **not** call `Pool.FlushAll` or `Pool.FlushAllPaced` —
those are reserved for the checkpointer and protected by the `OnFlushAll`
assertion.

### 2.3 Dirty-victim instrumentation (`internal/storage/bufpool.go`)

`evictLocked` now increments:
- `Pool.totalVictimCount` on every eviction.
- `Pool.dirtyVictimCount` when the evicted slot is dirty.

`Pool.DirtyVictimRate() float64` reports the fraction;
`Pool.ResetVictimStats()` resets both counters (used in DoD tests).

### 2.4 GUCs (`internal/config/defaults.go`)

| GUC | Default | Meaning |
|---|---|---|
| `bgwriter_delay` | 200 (ms) | Inter-tick interval |
| `bgwriter_lru_maxpages` | 100 | Max pages written per tick; 0 disables |

### 2.5 Wiring (`internal/initdb/open.go`, `cmd/goopg/main.go`)

`Open` creates and starts a `Bgwriter` when both `BgwriterDelay > 0` and
`BgwriterMaxPages > 0`. `Runtime.Close()` calls `bgwriter.Stop()` before
draining the pool.

`cmd/goopg/main.go` reads `bgwriter_delay` and `bgwriter_lru_maxpages` from
the GUC registry and passes them to `initdb.Open` as `BgwriterDelay` and
`BgwriterMaxPages`.

## 3. Correctness

- The bgwriter holds only `contentMu.RLock()` during writes, so readers and
  writers continue unimpeded.
- It re-checks slot identity under `poolMu` before and after the write to
  handle races where the slot was reused for a different block mid-flush.
- WAL-before-data ordering is preserved: `flushSlot` calls
  `wal.FlushUpTo(page.LSN)` before `Manager.WriteBlock`.
- No fsync — the checkpointer is responsible for durability.

## 4. Tests (`internal/storage/bgwriter_test.go`)

| Test | Coverage |
|---|---|
| `TestBgwriterFlushesPages` | `WriteDirtyPages` clears dirty flags on flushed pages |
| `TestBgwriterGoroutine` | Goroutine starts, proactively flushes, and stops cleanly |
| `TestBgwriterDoDDirtyVictimRate` | **DoD**: bgwriter running → 0% dirty-victim rate (≤5% required) |
| `TestBgwriterMaxPagesLimit` | maxPages cap is respected per tick |
</content>
</invoke>