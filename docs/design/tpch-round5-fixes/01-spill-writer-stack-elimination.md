# 01 — Spill Writer `runtime.Stack` Elimination

| field | value |
| --- | --- |
| priority | **CRITICAL** — 3–7× speedup for Q4, Q7, Q13 |
| risk | Very low |
| files | `internal/executor/spill.go` |
| precedent | `internal/gls/` — same anti-pattern fixed for WAL hot path (perf-optimize2) |

## 1. Motivation

Profiling shows `runtime.Stack` called from `spillWriter.WriteRow()` consumes
**69–86% of CPU** for the three TPC-H queries that spill hash tables to disk:

| Query | Wall time | `runtime.Stack` CPU | After fix (est.) | Speedup |
| --- | ---: | ---: | ---: | ---: |
| Q4 | 284.70 s | 78.8% (94.10 s) | ~60 s | **4.7×** |
| Q7 | 158.64 s | 69.6% (83.87 s) | ~48 s | **3.3×** |
| Q13 | 108.87 s | 85.9% (95.49 s) | ~15 s | **7.3×** |

This is a **sibling-code-path regression**: the exact same anti-pattern was
previously fixed for WAL appends in perf-optimize2 via `internal/gls/`, but
`internal/executor/spill.go` was never updated.

## 2. Current state

### 2.1 The bottleneck call chain

```
(*spillWriter).WriteRow                        (spill.go:31)
  → activity.LookupCurrentGoroutine()          (registry.go:832)
    → goroutineID()                            (activity.go:186)
      → runtime.Stack(buf, false)              ← 78.8% of CPU for Q4
```

### 2.2 spillWriter struct and constructor (`spill.go:17-28`)

```go
type spillWriter struct {
    f    *os.File
    path string
    buf  []byte // reusable encode buffer
}

func newSpillWriter(dir string) (*spillWriter, error) {
    f, err := os.CreateTemp(dir, "goopg-spill-*.tmp")
    if err != nil {
        return nil, fmt.Errorf("spillWriter: create temp file: %w", err)
    }
    return &spillWriter{f: f, path: f.Name()}, nil
}
```

### 2.3 WriteRow — the hot path (`spill.go:31-53`)

```go
func (w *spillWriter) WriteRow(row Row) error {
    w.buf = w.buf[:0]
    w.buf = binary.AppendUvarint(w.buf, uint64(len(row)))
    for _, d := range row {
        w.buf = encodeDatum(d, w.buf)
    }
    lenBuf := make([]byte, 4)
    binary.LittleEndian.PutUint32(lenBuf, uint32(len(w.buf)))
    reg, procNum, okReg := activity.LookupCurrentGoroutine()  // ← BOTTLENECK (line 40)
    if okReg {
        reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitBuffileWrite)
    }
    _, err1 := w.f.Write(lenBuf)
    _, err2 := w.f.Write(w.buf)
    if okReg {
        reg.WaitEventEnd(procNum)
    }
    if err1 != nil {
        return err1
    }
    return err2
}
```

### 2.4 spillReader and ReadRowInto — same pattern (`spill.go:62-82, 102-130`)

```go
type spillReader struct {
    f       *os.File
    path    string
    dataBuf []byte
}

func (r *spillReader) ReadRowInto(dst Row) (Row, error) {
    var lenBuf [4]byte
    reg, procNum, okReg := activity.LookupCurrentGoroutine()  // ← BOTTLENECK (line 104)
    if okReg {
        reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitBuffileRead)
    }
    _, errLen := io.ReadFull(r.f, lenBuf[:])
    if okReg {
        reg.WaitEventEnd(procNum)
    }
    // ... decode payload (second WaitEventStart/End pair at lines 124-130
    //     reuses the same reg/procNum/okReg — no second LookupCurrentGoroutine) ...
}
```

### 2.5 The goroutine identity mechanism (`activity.go:186-207`, `registry.go:832-841`)

```go
// activity.go
func goroutineID() string {
    const prefix = "goroutine "
    buf := make([]byte, 64)         // heap allocation per call
    n := runtime.Stack(buf, false)  // stack walk — captures every frame PC
    // ... parse "goroutine N [...]" from header ...
}

// registry.go
func LookupCurrentGoroutine() (*ActivityRegistry, int32, bool) {
    id := goroutineID()                     // expensive
    goroutineActivityMu.RLock()             // global RWMutex
    entry, ok := goroutineActivityMap[id]   // map[string] lookup
    goroutineActivityMu.RUnlock()
    return entry.reg, entry.procNum, true
}
```

### 2.6 Why caching is safe

The spill writer is created by `drainRowsBounded` (line 340) and used exclusively
within the same goroutine that creates it. The connection's `serveConn` goroutine
calls `activity.SetCurrentGoroutine(reg, procNum)` at `server.go:968` **before**
any query executes, so the registry entry is always valid when the spill writer
is constructed. There is no goroutine migration — the cached values are immutable
for the lifetime of the `spillWriter`/`spillReader`.

### 2.7 Existing precedent: the WAL fix (`internal/gls/`)

The WAL append hot path had the same anti-pattern — `LookupCurrentGoroutine` on
every append was 57% of server CPU under pgbench simple-update. The fix
(`internal/gls/gls.go`) uses pprof goroutine labels for cheap backend-ID lookup:

- `gls.SetBackendID(procNum)` — called once at connection startup (`server.go:973`)
- `gls.BackendID() (int32, bool)` — pointer load + one-entry label scan, allocation-free

The spill writer was simply never updated to use this pattern.

## 3. Design

### 3.1 Add cached fields to structs

```go
type spillWriter struct {
    f       *os.File
    path    string
    buf     []byte

    // Cached activity registry reference and procNum.
    // Populated once at construction via LookupCurrentGoroutine;
    // safe because the spillWriter is single-goroutine and the
    // goroutine is registered before any spill writer is created.
    reg        *activity.ActivityRegistry
    procNum    int32
    hasReg     bool  // true when reg/procNum are valid
}
```

Same fields for `spillReader`.

### 3.2 Populate cache at construction

In `newSpillWriter`, call `activity.LookupCurrentGoroutine()` **once** and cache:

```go
func newSpillWriter(dir string) (*spillWriter, error) {
    f, err := os.CreateTemp(dir, "goopg-spill-*.tmp")
    if err != nil {
        return nil, fmt.Errorf("spillWriter: create temp file: %w", err)
    }
    w := &spillWriter{f: f, path: f.Name()}
    if reg, procNum, ok := activity.LookupCurrentGoroutine(); ok {
        w.reg = reg
        w.procNum = procNum
        w.hasReg = true
    }
    return w, nil
}
```

Same for `newSpillReader`.

### 3.3 Use cached values in WriteRow

Replace the per-row `LookupCurrentGoroutine` call with cached field access:

```go
func (w *spillWriter) WriteRow(row Row) error {
    w.buf = w.buf[:0]
    w.buf = binary.AppendUvarint(w.buf, uint64(len(row)))
    for _, d := range row {
        w.buf = encodeDatum(d, w.buf)
    }
    lenBuf := make([]byte, 4)
    binary.LittleEndian.PutUint32(lenBuf, uint32(len(w.buf)))

    if w.hasReg {
        w.reg.WaitEventStart(w.procNum, activity.WaitTypeIO, activity.WaitBuffileWrite)
    }
    _, err1 := w.f.Write(lenBuf)
    _, err2 := w.f.Write(w.buf)
    if w.hasReg {
        w.reg.WaitEventEnd(w.procNum)
    }

    if err1 != nil {
        return err1
    }
    return err2
}
```

### 3.4 Use cached values in ReadRowInto

Same pattern — replace the per-read `LookupCurrentGoroutine` call at line 104
with `r.reg`/`r.procNum`/`r.hasReg`:

```go
func (r *spillReader) ReadRowInto(dst Row) (Row, error) {
    var lenBuf [4]byte
    if r.hasReg {
        r.reg.WaitEventStart(r.procNum, activity.WaitTypeIO, activity.WaitBuffileRead)
    }
    _, errLen := io.ReadFull(r.f, lenBuf[:])
    if r.hasReg {
        r.reg.WaitEventEnd(r.procNum)
    }
    // ... decode dataLen, read payload (also using r.hasReg/r.reg/r.procNum
    //     for the second WaitEventStart/WaitEventEnd pair at lines 124-130) ...
}
```

### 3.5 What does NOT change

- The `drainRowsBounded` function (lines 314-387) — no changes needed since `newSpillWriter`/`newSpillReader` handle caching internally.
- The `spillOp` struct and its `Next()` method — delegates to `ReadRowInto`, which uses the cached values.
- Any other callers — `newSpillWriter`/`newSpillReader` are only called from `drainRowsBounded`.

### 3.6 API stability

This fix makes **zero API changes**. The `newSpillWriter` and `newSpillReader`
signatures are unchanged. All caching is internal to the struct.

## 4. Implementation steps

1. **Add `reg`, `procNum`, `hasReg` fields** to `spillWriter` struct (after `buf`).
2. **Populate in `newSpillWriter`**: after creating the temp file, call `activity.LookupCurrentGoroutine()` once and cache the result.
3. **Modify `WriteRow`**: replace `reg, procNum, okReg := activity.LookupCurrentGoroutine()` with `w.hasReg`/`w.reg`/`w.procNum`.
4. **Add `reg`, `procNum`, `hasReg` fields** to `spillReader` struct (after `dataBuf`).
5. **Populate in `newSpillReader`**: after opening the file, call `activity.LookupCurrentGoroutine()` once and cache the result.
6. **Modify `ReadRowInto`**: replace the `LookupCurrentGoroutine` call at line 104 with `r.hasReg`/`r.reg`/`r.procNum`. The second WaitEventStart/WaitEventEnd pair at lines 124-130 already reuses the `reg`/`procNum`/`okReg` variables — update those to use `r.hasReg`/`r.reg`/`r.procNum` as well.
7. **Verify**: `grep -n "LookupCurrentGoroutine" internal/executor/spill.go` should return zero results.

## 5. Risk assessment

| Risk | Impact | Mitigation |
| --- | --- | --- |
| `reg == nil` in unit tests (no server context) | Wait events silently skipped — IO timing not recorded | `hasReg == false` guard — the `if w.hasReg` check ensures WaitEventStart/End are never called with nil reg. This is the correct behaviour for test contexts. |
| Goroutine migration (if spill writer is passed across goroutines in the future) | Cached `procNum` points to wrong backend's slot | Not possible in current architecture. The single-goroutine constraint is documented in the struct comment. If multi-goroutine use is ever added, switch to `gls.BackendID()` + registry lookup by backend ID (see [02-systemic-backend-id-lookup.md](./02-systemic-backend-id-lookup.md)). |
| `LookupCurrentGoroutine` fails at construction time (goroutine not yet registered when `drainRowsBounded` runs) | `hasReg == false`, wait events skipped | `SetCurrentGoroutine` is called at connection startup (`server.go:968`) before any query executes. The spill writer is created during query execution, well after registration. This cannot fail in practice. |

## 6. Verification

1. **Existing tests must pass:**
   ```bash
   go test ./internal/executor/ -count=1
   ```
   The `reg`/`procNum` will be nil/zero in unit tests (no server context), so `hasReg` stays false and wait-event hooks are safely skipped.

2. **Profile Q4 before/after:**
   ```bash
   # Before: build server from HEAD, run Q4 with pprof
   # After:  build server with fix, run Q4 with pprof
   go tool pprof -top -nodecount=10 bench/tpch/pprof/q4_cpu.pb.gz
   ```
   Expected: `runtime.Stack`, `runtime.step`, `runtime.pcvalue`, `runtime.(*moduledata).textAddr` all drop out of top 10. `Syscall6` (actual I/O) becomes the top syscall consumer.

3. **Wall-clock improvement:**
   - Q4: ~285 s → ~60 s (4.7×)
   - Q7: ~159 s → ~48 s (3.3×)
   - Q13: ~109 s → ~15 s (7.3×)

4. **Regression check:**
   ```bash
   # Run all 5 profiled queries; verify output row counts match baseline
   tmp/tpch-runner --port=65433 --queries=1,4,7,9,13 --parallel-workers=0
   ```

## 7. Related improvements

- [02-systemic-backend-id-lookup.md](./02-systemic-backend-id-lookup.md) — the systemic fix that adds `LookupByBackendID()` using `gls.BackendID()`, eliminating all remaining `runtime.Stack` callers and providing a safe pattern for future hot-path goroutine-identity lookups.
- The WAL perf-optimize2 fix (`internal/gls/gls.go`) is the direct precedent — same anti-pattern, same fix shape, validated in production-like benchmarks.
