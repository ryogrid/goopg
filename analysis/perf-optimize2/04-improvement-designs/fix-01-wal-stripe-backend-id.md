# fix-01 — Eliminate `runtime.Stack` from WAL stripe selection (P0)

## Problem (evidence)

`internal/wal/writer.go:1870` `(*state).stripeNum()` calls
`activity.LookupCurrentGoroutine()` on **every WAL append** to pick the
per-backend WAL-buffer stripe. That helper derives the goroutine ID via
`runtime.Stack(buf[:64], false)` (`internal/activity/activity.go:186-207`)
and then does a `map[string]` lookup under `sync.RWMutex`.

Measured (run 20260712_114859, c=50 simple-update):
- `runtime.Stack` cum **199.2 s of 352.6 s CPU = 56.5 %**, 100 % via
  `activity.goroutineID ← LookupCurrentGoroutine ← wal.stripeNum`.
- 28.5 % of mutex-wait time rides the same path (registry RWMutex).
- The same storm is **~60 % of CPU during COPY** (`copydiag/`:
  `runtime.Stack` cum 60.6 %, `stripeNum` chain 61.2 %), because COPY
  appends WAL per row.

Root cause of the expense: `runtime.Stack` formats the **entire** call stack
(symbol + file/line per frame) even into a 64-byte buffer, and executor
stacks are deep. This is a per-append cost that PostgreSQL pays **zero** for.

## PostgreSQL approach (03 §1)

A backend's identity is the global `MyProcNumber` (slot index, computed once
in `InitProcess`); `WALInsertLockAcquire` caches
`lockToTry = MyProcNumber % NUM_XLOGINSERT_LOCKS` in a function-local static.
Selection is a memory load.

## Design

### Recommended: goroutine-local backend ID via pprof labels (Option A)

Go offers exactly one supported goroutine-local storage mechanism: profiler
labels (`runtime/pprof.SetGoroutineLabels`), which children inherit at
`go` time and which are readable in-process via the runtime's
`runtime_getProfLabel` (accessed with `//go:linkname`, same technique the
practice doc sanctions for `nanotime`).

1. New tiny package `internal/gls` (goroutine-local session id):
   - `gls.SetBackendID(id int32)` — wraps
     `pprof.SetGoroutineLabels(pprof.WithLabels(ctx, pprof.Labels(...)))`
     storing the id; called **once per connection goroutine** at the point
     `server.serveConn` already registers the session in the activity
     registry.
   - `gls.BackendID() (int32, bool)` — reads the current goroutine's label
     set via `//go:linkname runtime_getProfLabel runtime/pprof.runtime_getProfLabel`
     and parses the id. Cost: pointer load + scan of a 1-entry label list
     (~tens of ns; no lock, no allocation). Note the concrete layout: in
     current Go the label store is a slice-backed `labelMap{list []label}`
     (it was a `map[string]string` in older releases and changed around
     Go 1.23/1.24) — only the *symbol* has been stable since Go 1.9, so
     `internal/gls` must pin the layout per Go version behind the canary
     test below.
2. `wal.(*state).stripeNum()` becomes:
   ```go
   func (s *state) stripeNum() int32 {
       if id, ok := gls.BackendID(); ok { return id }
       return 0 // initdb, checkpointer, tests — unchanged fallback
   }
   ```
3. `activity.LookupCurrentGoroutine` keeps its current implementation for
   its remaining callers, but migrate those (wait-event start/end, backend-
   type lookup) to `gls` in the same loop if cheap — the registry's
   goroutine-keyed map then becomes registration-time-only.

Encapsulation rule: the `//go:linkname` and label-map layout knowledge live
only in `internal/gls`, with a unit test that fails loudly if a Go upgrade
changes `runtime_getProfLabel`'s shape (the symbol has been stable since
Go 1.9; degrade to fallback stripe 0 + logged warning rather than crashing).

### Long-term alternative: explicit backend number threading (Option B)

PG-faithful: thread the session's proc number through the WAL append API
(e.g. an `Append` variant taking `stripe int32`, carried by
`executor.Context` / the storage-layer WAL hooks). Cleanest semantics, no
runtime tricks — but the WAL hooks are invoked from `internal/storage`
(buffer-pool dirty hooks) with no session context today, so the plumbing
touches many signatures. Do this when the storage hooks next get an
interface revision; A is compatible with (and a stepping stone to) B.

### Rejected: assembly/`getg()` goid extraction (Option C)

Fragile across Go releases (offset-of-goid in `runtime.g`), duplicates what
labels already provide, and still needs a goid→backend map (the thing that
made the registry hot). No advantage over A.

## Expected lift (arithmetic)

Removing 56.5 % of CPU bounds CPU headroom at ×2.3 (Amdahl). TPS is co-gated
by the commit flush pipeline, so the realistic single-fix expectation is
×1.5–2.0 at c=50 (1,269 → ~1,900–2,500 with profiling on), plus ~60 % CPU
back on the COPY path, plus reduced GC pressure (64 B + string alloc per
append disappears; also less runtime-lock traffic feeding profiling
overhead).

## Risks

- Label inheritance: any goroutine spawned per-query inherits the parent's
  label (correct); pooled workers (`internal/aio`) never had a session
  identity and keep the stripe-0 fallback (today's behavior).
- pprof labels become visible in CPU profiles (they already are, harmless —
  arguably a diagnostic improvement).
- Go-version coupling isolated in `internal/gls` with a canary test.

## Verification plan

1. Unit: `internal/gls` round-trip + inheritance test; `internal/wal` stripe
   distribution test (50 registered goroutines → 50 distinct stripes mod N).
2. `make race-gate` (wal + activity are concurrency-critical).
3. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` + pgbench
   smoke hook.
4. Perf acceptance: re-run `run_su50.sh`; CPU profile must show
   `runtime.Stack` < 1 % (was 56.5 %); TPS ≥ 1,900 at rate=1 profiling.
5. `scripts/tpch-spotcheck.sh` (WAL path feeds all DML; row counts are the
   gate).
