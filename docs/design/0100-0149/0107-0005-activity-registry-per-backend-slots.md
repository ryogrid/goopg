# 0107-0005 — Per-Backend `ActivityRegistry` with Atomic Hot Path

**Status**: accepted  
**Milestone**: M0107-0005 (Phase D2 — per-backend wait\_event\_info)  
**Date**: 2026-05-21

## Problem

`activity.Registry` used a single `sync.RWMutex` gating every
`WaitEventStart` / `WaitEventEnd` call.  At c=100 select-only, the
`activity.(*Registry).WaitEventStart` and `WaitEventEnd` calls
(fired on every client protocol frame from the `server.go` client-I/O
hooks) accounted for:

```
1283 s  53.70 %   via activity.(*Registry).WaitEventStart
 893 s  37.37 %   via activity.(*Registry).WaitEventEnd
```

of all mutex delay — the single largest mutex contention source in the
server.

## Solution

`ActivityRegistry` replaces `Registry`.  The type alias `type Registry =
ActivityRegistry` preserves backward compatibility.

### Per-backend 64 B cache-line-aligned `activitySlot`

```
offset  0: waitInfo    atomic.Uint32  — packed (typeCode<<16|eventCode)
offset  4: _pad0       [4]byte
offset  8: stateChange atomic.Int64   — unix nanos
offset 16: cold        *coldActivity  — stable for slot lifetime
offset 24: _pad1       [40]byte
total: 64 bytes (one cache line)
```

Compile-time assertion: `[0]struct{}` trick in `registry.go`.

### Hot path: O(1) atomic stores

```go
func (r *ActivityRegistry) WaitEventStart(procNum int32, typeStr, nameStr string) {
    s := &r.slots[procNum]
    s.waitInfo.Store(packWaitStrings(typeStr, nameStr))
    s.stateChange.Store(time.Now().UnixNano())
}
```

No mutex.  Per-slot isolation means concurrent backends never contend.

### Wait-event encoding

String constants → uint32 via read-only `map[string]uint32` init'd at
`init()`.  Map lookup is ~15 ns — negligible vs old mutex (~100–1000 ns
contended).  Packed high 16 bits = type code, low 16 bits = event code.

### Background worker slots

```
slots[0 .. 1023]          regular backends (procNum = (PID-1) % 1024)
slots[1024]               WAL writer  (WalWriterIdx = 0)
slots[1025]               Checkpointer (CheckpointerIdx = 1)
slots[1026]               Autovacuum   (AutovacuumIdx = 2)
slots[1027..1039]         reserved
```

### Goroutine-activity map

The old `goroutineEntry{reg *Registry, pid string}` map is replaced by
`goroutineActivityEntry{reg *ActivityRegistry, procNum int32}`.  Only
pool/AIO/spill hooks (called at buffer-miss / sort-spill frequency, not
per-message) use this map.  The hot-path client-I/O closures in
`serveConn` capture `(reg, procNum)` directly and bypass the goroutine
map entirely.

### Call-site changes

| File | Change |
|------|--------|
| `server/server.go` | `reg.WaitEventStart(procNum, ...)` (from `pidStr`) |
| `server/dispatch.go` | `reg.UpdateState(connTx.ProcNum, ...)` |
| `executor/context.go` | `c.Activity.WaitEventStart(c.ProcNum, ...)` |
| `executor/spill.go` | `LookupCurrentGoroutine()` → `reg.WaitEventStart(procNum, ...)` |
| `initdb/open.go` | `walProcNum` captured; `LookupCurrentGoroutine()` for pool/AIO hooks |
| `initdb/backend_goroutine_test.go` | `RegisterBackground` / `SetCurrentGoroutine` |

## Verification

- `go test -race ./internal/activity/...` — PASS (size assertion, pack
  round-trip, hot-path, background worker, goroutine map tests)
- `go test -race ./internal/executor/ ./internal/server/ ./internal/mvcc/
  ./internal/storage/ ./internal/wal/` — all PASS
- `TestBackendGoroutineDoesNotFsync` / `TestCheckpointerFlushAllIsAllowed`
  — PASS (M0042-0004 invariant preserved under new API)
- Expected TPS improvement: c=100 SO ≥ 10 000 (vs 6 400 pre-D2), per
  `docs/design/perf-optimize/05-activity-perbackend.md` §11.

## Invariants preserved

- `type Registry = ActivityRegistry` — all existing call sites unchanged
- `Snapshot() []Backend` return type unchanged — `pg_stat_activity` view
  requires no modification
- `pg_compat` gate: pure in-memory refactor; no on-disk format changes
