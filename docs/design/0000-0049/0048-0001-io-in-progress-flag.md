# BM_IO_IN_PROGRESS Flag — M0048-0001

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

When multiple goroutines simultaneously request a page that is not cached,
the original `Pool.Pin` gives each of them an independent slot and lets all
of them issue their own `smgr.ReadBlock` call. The redundant reads waste I/O
bandwidth, increase latency, and can produce torn pages if the data directory
is on an eventually-consistent storage back-end.

PostgreSQL prevents this with the `BM_IO_IN_PROGRESS` flag on the buffer
descriptor: the first accessor sets the flag, reads the page, then clears it
and wakes waiting accessors.

## 2. Design

### 2.1 New Pool fields (`internal/storage/bufpool.go`)

```go
// ioByTag tracks BufferTags currently being read from disk.
ioByTag map[BufferTag]struct{}   // guarded by poolMu
ioCond  *sync.Cond               // broadcast when an in-flight read finishes
```

`ioByTag` is a set of tags whose pages are currently being fetched from disk.
`ioCond` uses `&p.poolMu` as its underlying lock so `Wait` and `Broadcast`
participate in the same critical section as all other pool metadata mutations.

`Pool.OnBufferIOWait func()` is a new optional hook that fires when a goroutine
must wait for an in-flight read (mirrors `OnPinWait` for the BufferIO wait class).

### 2.2 Updated `Pool.Pin` algorithm

```
Pin(tag):
  lock poolMu
  OUTER_LOOP:
    if byTag[tag] → cache hit → return                  // fast path unchanged
    if ioByTag[tag] → ioCond.Wait(); continue           // BM_IO_IN_PROGRESS wait
    break                                               // we win the I/O race

  ioByTag[tag] = struct{}{}                             // mark in-flight
  evict victim, possibly flush dirty victim
  unlock poolMu

  ReadBlock(tag, slot.page)                             // one disk read

  lock poolMu
  delete(ioByTag, tag)
  ioCond.Broadcast()                                    // wake all waiters
  publish slot in byTag
  unlock poolMu
  return slot
```

**Invariant**: `ioByTag[tag]` is set for the entire duration between
"we won the I/O race" and "we published the slot in byTag and broadcast".
Any goroutine calling `Pin(tag)` in that window will wait on `ioCond`.
After the broadcast they re-check the cache and find the newly published slot.

### 2.3 Activity tracking

`WaitBufferIO` (already defined in `activity.go`) is the wait-event name for
goroutines blocked on `ioCond.Wait`. The `OnBufferIOWait` hook is called
before `ioCond.Wait` so `initdb.Open` can record the event in
`pg_stat_activity`.

## 3. Correctness

- **Single read per miss**: `ioByTag[tag]` is set before the eviction decision,
  so any subsequent goroutine that misses the cache sees the in-flight marker
  and waits rather than starting its own read.
- **No deadlock**: `ioCond` is backed by `poolMu`; `Wait` atomically releases
  and re-acquires it. The reader holds neither `poolMu` nor `contentMu` during
  `ReadBlock`.
- **Error propagation**: if `ReadBlock` fails, the reader still removes
  `ioByTag[tag]` and broadcasts. Waiting goroutines wake up, see no cache
  entry, and attempt their own read (which will also fail).
- **Backward compatibility**: the existing fallback at lines 669–685 (checking
  `byTag[tag]` after the read in case `PinNew` raced) is preserved.
- **Race-free**: `ioByTag` and `ioCond` are both guarded by `poolMu`; the race
  detector confirms no data races under 16-goroutine stress (see test).

## 4. Tests (`internal/storage/bm_io_in_progress_test.go`)

| Test | Coverage |
|---|---|
| `TestBMIOInProgressSingleRead` | **DoD**: 64 goroutines Pin same block → `smgr.Read` = 1 |
| `TestBMIOInProgressDistinctBlocks` | 8 concurrent Pins on different blocks → 8 reads |
| `TestBMIOInProgressRaceCondition` | 16-goroutine stress with 4-slot pool, race-detector clean |
