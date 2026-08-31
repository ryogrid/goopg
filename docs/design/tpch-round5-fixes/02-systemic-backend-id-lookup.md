# 02 — Systemic Backend-ID Lookup

| field | value |
| --- | --- |
| priority | MEDIUM — eliminates remaining `runtime.Stack` callers; enables future hot-path safety |
| risk | Low |
| files | `internal/activity/registry.go`, `internal/server/server.go` |
| depends on | `internal/gls/` (already exists) |

## 1. Motivation

While Fix 01 eliminates the per-row `LookupCurrentGoroutine` in the spill writer,
there are still callers that use the expensive function. These are not performance-critical
cold paths but represent **technical debt and a risk surface** for future hot-path
regressions:

| Call site | File:line | Path temperature |
| --- | --- | --- |
| `SET application_name` GUC hook | `server.go:403` | Cold (SET only) |
| `SET track_io_timing` GUC hook | `server.go:414` | Cold |
| `SET backend_flush_after` GUC hook | `server.go:426` | Cold |
| `LookupTrackedGoroutine` (IO timing hooks) | `registry.go:634` | Hot (per buffer pin/flush/extend) but gated by `trackIOTimingFastPath` |
| `BackendFlushAfterOverride` (flush-after hooks) | `registry.go:670` | Warm (per backend write) — no fast-path gate exists |
| `OnFlushAll` assertion | `initdb/open.go:855` | Cold (assertion only) |

The `LookupTrackedGoroutine` path is gated by an atomic load (`trackIOTimingFastPath`)
that is false when `track_io_timing = off` (the default). But `BackendFlushAfterOverride`
has no such gate — it always calls `LookupCurrentGoroutine()` → `runtime.Stack`.

Replacing these with `gls.BackendID()` + array-indexed registry lookup:
1. Eliminates the last `runtime.Stack` calls from production code paths.
2. Provides a safe, allocation-free pattern for any future goroutine-identity lookups.
3. Makes `LookupCurrentGoroutine` itself fast for all callers, not just those that cache.

## 2. Current state

### 2.1 The goroutine map (`registry.go:807-841`)

```go
type goroutineActivityEntry struct {
    reg     *ActivityRegistry
    procNum int32
}

var (
    goroutineActivityMu  sync.RWMutex
    goroutineActivityMap = make(map[string]goroutineActivityEntry)
)

func SetCurrentGoroutine(reg *ActivityRegistry, procNum int32) {
    id := goroutineID()             // runtime.Stack (cold path — once per connection)
    goroutineActivityMu.Lock()
    goroutineActivityMap[id] = goroutineActivityEntry{reg: reg, procNum: procNum}
    goroutineActivityMu.Unlock()
}

func LookupCurrentGoroutine() (*ActivityRegistry, int32, bool) {
    id := goroutineID()             // runtime.Stack (hot path — every call)
    goroutineActivityMu.RLock()
    entry, ok := goroutineActivityMap[id]
    goroutineActivityMu.RUnlock()
    if !ok {
        return nil, 0, false
    }
    return entry.reg, entry.procNum, true
}
```

### 2.2 The gls fast path (`gls.go:40-45`, `gls_linkname.go:87-92`)

```go
// gls.go — called once per connection at startup (server.go:973)
func SetBackendID(id int32) {
    pprof.SetGoroutineLabels(pprof.WithLabels(
        context.Background(),
        pprof.Labels(labelKey, strconv.Itoa(int(id))),
    ))
}

// gls_linkname.go — allocation-free, safe on hot paths
func BackendID() (int32, bool) {
    if !glsUsable {
        return 0, false
    }
    return readLabelID()  // pointer load + single label scan
}
```

### 2.3 Registration order (`server.go:968-973`)

```go
// 1. Register the goroutine in the activity map (for legacy callers)
activity.SetCurrentGoroutine(reg, procNum)
// 2. Stamp the pprof label (for gls.BackingID callers)
gls.SetBackendID(procNum)
```

Both are called once at connection startup, before any query executes. The `gls`
label is inherited by any goroutines spawned from this one (pprof label inheritance
at `go` time).

### 2.4 Why BackendID alone is insufficient

`gls.BackendID()` returns `procNum` (the backend's slot index), but callers also
need the `*ActivityRegistry` pointer to call `WaitEventStart` / `WaitEventEnd` /
`TrackIOTiming`. The `ActivityRegistry` is process-wide — there is exactly one
instance per goopg server. We can store it in a global variable rather than
looking it up via the goroutine map.

## 3. Design

### 3.1 Global registry singleton

Add a package-level atomic pointer to `registry.go`:

```go
// globalRegistry is the process-wide ActivityRegistry, set once at server
// startup. Used by LookupByBackendID to avoid the goroutine-map lookup.
var globalRegistry atomic.Pointer[ActivityRegistry]

// SetGlobalRegistry stores the process-wide ActivityRegistry for fast
// gls-based lookups. Called once at server startup, before any connections.
func SetGlobalRegistry(reg *ActivityRegistry) {
    globalRegistry.Store(reg)
}
```

### 3.2 New function: LookupByBackendID

```go
// LookupByBackendID returns the registry and procNum for the calling
// goroutine using gls.BackendID(). This is allocation-free and does not
// call runtime.Stack. Returns (nil, 0, false) if gls is not usable on
// this runtime, the global registry is not set, or the procNum slot is
// not occupied.
func LookupByBackendID() (*ActivityRegistry, int32, bool) {
    reg := globalRegistry.Load()
    if reg == nil {
        return nil, 0, false
    }
    procNum, ok := gls.BackendID()
    if !ok {
        return nil, 0, false
    }
    if procNum < 0 || int(procNum) >= len(reg.slots) {
        return nil, 0, false
    }
    // Verify the slot is occupied (a registered backend).
    if reg.slots[procNum].cold.Load() == nil {
        return nil, 0, false
    }
    return reg, procNum, true
}
```

### 3.3 Update LookupCurrentGoroutine to prefer gls

The existing `LookupCurrentGoroutine` becomes the compatibility wrapper that
tries the fast path first, falling back to the legacy map:

```go
// LookupCurrentGoroutine returns the registry and procNum for the calling
// goroutine. Prefers the gls-based fast path (LookupByBackendID); falls
// back to the goroutine-ID map on unsupported runtimes or for goroutines
// that were registered before the gls path was available.
func LookupCurrentGoroutine() (*ActivityRegistry, int32, bool) {
    // Fast path: gls.BackendID() — no runtime.Stack, no map lookup.
    if reg, procNum, ok := LookupByBackendID(); ok {
        return reg, procNum, ok
    }
    // Slow path: goroutine ID → map lookup (runtime.Stack).
    return lookupCurrentGoroutineLegacy()
}

// lookupCurrentGoroutineLegacy is the original implementation using
// runtime.Stack to extract the goroutine ID. Kept as fallback for
// unsupported runtimes and goroutines without pprof labels.
func lookupCurrentGoroutineLegacy() (*ActivityRegistry, int32, bool) {
    id := goroutineID()
    goroutineActivityMu.RLock()
    entry, ok := goroutineActivityMap[id]
    goroutineActivityMu.RUnlock()
    if !ok {
        return nil, 0, false
    }
    return entry.reg, entry.procNum, true
}
```

### 3.4 What stays the same

- `SetCurrentGoroutine` — still writes to `goroutineActivityMap` (legacy fallback) and is still called from `server.go:968`. Cold path, acceptable.
- `ClearCurrentGoroutine` — still deletes from `goroutineActivityMap`. Cold path.
- `goroutineActivityMap` — retained indefinitely for backward compatibility with tests, background workers that may not call `gls.SetBackendID`, and unsupported Go runtimes.

### 3.5 SetGlobalRegistry call site

In `internal/server/server.go`, after the `ActivityRegistry` is created:

```go
// During server startup, before any connection accept loop:
activity.SetGlobalRegistry(reg)
```

The exact location depends on where `reg` is first available. Look for the
`NewActivityRegistry` call or the first place the registry is assigned to the
server config.

### 3.6 Background workers

Background workers (WAL writer, checkpointer, bgwriter, autovacuum launcher)
have their own goroutines that call `SetCurrentGoroutine`. Some may not call
`gls.SetBackendID`. For these, `LookupCurrentGoroutine()` gracefully falls back
to the legacy map — no functional change.

The per-buffer-pin IO timing hooks (`LookupTrackedGoroutine`) are the warmest
path that benefits from this fix. They are called on every buffer pin/flush/extend,
but only when `track_io_timing = on` (non-default). With the `LookupByBackendID`
path, even when `track_io_timing` is on, the lookup is O(1) with no allocation.

**Note:** Background workers currently pay the `runtime.Stack` cost on every
`LookupCurrentGoroutine` call because they fall through to the legacy map path.
A follow-up optimization (outside the scope of this fix) would be to add
`gls.SetBackendID` calls for each background worker type (WAL writer,
checkpointer, bgwriter, autovacuum launcher) so their `LookupCurrentGoroutine`
calls also use the gls fast path. The per-buffer-pin IO timing hooks are a
particularly notable beneficiary — `BackendFlushAfterOverride` has no fast-path
gate and always pays the lookup cost.

## 4. Implementation steps

1. **Add `sync/atomic` import** to `registry.go` (if not already present).
2. **Add `globalRegistry` variable** and `SetGlobalRegistry` function.
3. **Add `LookupByBackendID` function**.
4. **Rename existing `LookupCurrentGoroutine`** to `lookupCurrentGoroutineLegacy`.
5. **New `LookupCurrentGoroutine`** that tries `LookupByBackendID` first, falls back to legacy.
6. **Add `SetGlobalRegistry(reg)` call** in `server.go` at server startup.
7. **Run all tests** — verify no regressions.
8. **Optional follow-up**: add `BackendFlushAfterOverride` fast-path gate (similar to `trackIOTimingFastPath`) so it skips the lookup entirely when `backend_flush_after = 0` (the default).

## 5. Risk assessment

| Risk | Impact | Mitigation |
| --- | --- | --- |
| `glsUsable == false` at runtime (unsupported Go version) | Falls back to legacy map — identical behaviour to current code | Graceful degradation; no functional change |
| Background worker goroutines without pprof labels | Fall through to legacy map path | `SetCurrentGoroutine` still populates the map; legacy lookup still works |
| Race between `SetGlobalRegistry` and concurrent `LookupByBackendID` | Nil dereference if registry not yet set | `atomic.Pointer` provides safe publication; `LookupByBackendID` checks for nil |
| `procNum` out of bounds for `reg.slots` | Index panic | Bounds check in `LookupByBackendID` (line 10 of function) |
| Slot not occupied (unregistered procNum) | Returns stale or zero data | The `cold.Load() == nil` check verifies the slot is occupied |

## 6. Verification

1. **Existing tests:**
   ```bash
   go test ./internal/activity/ -count=1
   go test ./internal/server/ -count=1
   ```

2. **Add unit test for `LookupByBackendID`:**
   ```go
   func TestLookupByBackendID(t *testing.T) {
       reg := NewActivityRegistry(16)
       SetGlobalRegistry(reg)
       gls.SetBackendID(3)
       reg.slots[3].cold.Store(&activitySlotCold{})
       gotReg, gotPN, ok := LookupByBackendID()
       if !ok {
           t.Skip("gls not usable on this runtime")
       }
       if gotReg != reg || gotPN != 3 {
           t.Errorf("LookupByBackendID = (%v, %d), want (reg, 3)", gotReg, gotPN)
       }
   }
   ```

3. **Verify no `runtime.Stack` on hot paths:**
   ```bash
   grep -rn "runtime.Stack" internal/ | grep -v "_test.go" | grep -v "gls"
   ```
   The only remaining `runtime.Stack` call should be in `goroutineID()` (the legacy fallback), which is only reached when `glsUsable == false`.

4. **Server smoke test:**
   ```bash
   scripts/csq-bench-server.sh start
   psql -h 127.0.0.1 -p 65433 -U postgres -d postgres -c "SELECT 1"
   scripts/csq-bench-server.sh stop
   ```

## 7. Related improvements

- [01-spill-writer-stack-elimination.md](01-spill-writer-stack-elimination.md) — the immediate per-caller fix; Fix 02 is the systemic generalization.
- `internal/gls/gls.go` — the infrastructure Fix 02 builds on (pprof goroutine labels for cheap backend-ID lookup).
- After Fix 02 lands, any new code that needs goroutine identity should use `gls.BackendID()` + `activity.LookupByBackendID()` or simply call `activity.LookupCurrentGoroutine()` which now prefers the gls fast path.
