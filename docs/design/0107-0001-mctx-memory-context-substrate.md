# 0107-0001 — `mctx`: PG-style Memory-Context Substrate (Phase A)

**Status**: accepted  
**Milestone**: M0107-0001  
**Date**: 2026-05-20

## Problem

The executor used `internal/executor/Arena` (64 KiB bump-allocator) for
per-page varchar/text/bytea payloads backing `KindStringArena` and
`KindBytesArena` Datums. The Arena had no parent/child hierarchy, no
pooling, and its `*Arena` pointer in `Datum.arena` was a GC-traced field.
A global `arenaRegistry` array provided forward-compat plumbing that was
dormant in production (`M0074-0003`).

## Solution — Phase A

### New package: `internal/mctx`

A hierarchical bump-allocator with explicit lifetime boundaries that
mirror PostgreSQL's `MemoryContext` discipline:

```
SessionContext  (per-connection; Acquire at serveConn, Release at disconnect)
└── TxnContext  (future; per-transaction)
    └── StmtContext  (per-statement; Acquire in dispatchSimpleQueryViaExecutor)
        └── ExprContext  (per-operator; Acquire in seqScanOp.Open)
```

Key design points:
- Each `Context` owns `[]chunk`, each chunk is a `[]byte` slab pooled
  via `sync.Pool` per size-class (4 KiB / 64 KiB / 256 KiB).
- Allocations bump `len(chunks[head].buf)`.
- `Reset()` rewinds all chunks to len=0 and cascades to children; backing
  arrays are retained for the next allocation cycle.
- `Release()` returns slabs to the pool and removes the context from the
  global `ctxRegistry [65536]*Context`.
- Offset encoding: `chunkIdx * defaultChunkSize + byteOffsetWithinChunk`
  (uint32) — compatible with `executor.Arena`'s offset encoding so
  KindStringArena/KindBytesArena Datum packing (`Datum.Int`) is unchanged.
- `AllocBytes(b []byte) (offset, length uint32)` copies and returns the
  address pair; `Bytes(offset, length uint32) []byte` resolves it back.
- `AllocFor[T]` / `AllocSlice[T]` provide typed allocation via generics.

### Migration in executor

| Before | After |
|--------|-------|
| `Datum.arena *Arena` | `Datum.mctx *mctx.Context` |
| `newStringArenaDatum(a *Arena, off, len int)` | `newStringArenaDatum(sctx *mctx.Context, off, len uint32)` |
| `DecodeRowIntoArena(…, arena *Arena)` | `DecodeRowIntoMctx(…, sctx *mctx.Context)` |
| `seqScanOp.arena *Arena` (NewArena/Drop/Reset) | `seqScanOp.sctx *mctx.Context` (Acquire/Release/Reset) |
| DDL local `arena := NewArena(0)` | `sctx := mctx.Acquire(o.ctx.Mctx, mctx.KindExpr)` |

`arena.go` and `arena_registry.go` are deleted; zero `executor.Arena`
references remain in the codebase (verified by `grep -RIn 'executor.Arena'`
returning empty).

### Lifecycle wiring

- `internal/server/server.go::serveConn`: `sessCtx := mctx.Acquire(nil, mctx.KindSession)` at entry, threaded into `connTxState.SessCtx`.
- `internal/server/dispatch.go::dispatchSimpleQueryViaExecutor`: `stmtCtx := mctx.Acquire(connTx.SessCtx, mctx.KindStmt)`, deferred `Release()`, set `ectx.Mctx = stmtCtx`.
- Operators: `seqScanOp.Open` acquires `mctx.Acquire(ctx.Mctx, KindExpr)`.

### Files changed

| File | Change |
|------|--------|
| `internal/mctx/mctx.go` | NEW — Context, Acquire/Release/Reset, AllocBytes/Bytes, registry |
| `internal/mctx/mctx_test.go` | NEW — unit tests |
| `internal/executor/arena.go` | DELETED |
| `internal/executor/arena_registry.go` | DELETED |
| `internal/executor/datum.go` | arena→mctx field; updated all methods |
| `internal/executor/codec.go` | DecodeRowIntoArena→DecodeRowIntoMctx; all internals |
| `internal/executor/operators_storage.go` | seqScanOp.arena→sctx; Open/Close/Next |
| `internal/executor/operators_ddl.go` | Two local arena vars → mctx.Acquire |
| `internal/executor/context.go` | Added `Mctx *mctx.Context` |
| `internal/server/server.go` | Acquire sessCtx; runPostStartupLoop signature |
| `internal/server/conn_tx.go` | Added `SessCtx *mctx.Context` |
| `internal/server/dispatch.go` | Acquire stmtCtx; set ectx.Mctx |

## Verification

- `go test -count=1 -race ./internal/mctx/ ./internal/executor/ ./internal/storage/ ./internal/server/ ./internal/mvcc/ ./internal/planner/ ./internal/parser/ ./internal/analyzer/ ./internal/wal/` — all 9 packages PASS.
- `grep -RIn 'executor.Arena\|executor.NewArena' internal/` — zero hits.
- `unsafe.Sizeof(mctx.Context{})` ≤ 96 B (pinned in `TestContextSizeof`).
- `unsafe.Sizeof(Datum{}) == 64` (unchanged; pinned in `TestM0073DatumStructSize`).

## Phase B dependency

Phase B (M0107-0002) will collapse `Datum` from 64 B (with `mctx *mctx.Context` pointer) to 24 B by replacing the pointer with `ContextID uint16` packed into the `Int` field, eliminating the last GC-traced pointer in `Datum`.

---

## Addendum (2026-08-10) — oversized chunks must never enter a size pool

**Status:** accepted. Fixes the load-sensitive `internal/mctx` failure that kept
`make race-gate` red after AI-20260810-011258-001 (`mctx_test.go:139: second
chunk: got ""`). Filed under M-NIGHTLY as a test flake; it was an engine bug.

### The invariant

`AllocBytes` returns an absolute `(offset, length)` pair encoded as

```
offset = chunkIdx * c.cs + offsetWithinChunk
```

and `Bytes` inverts it with `offset / c.cs` and `offset % c.cs`. The encoding is
only invertible while **every chunk of a context has capacity exactly `c.cs`**.
If any chunk is larger, an allocation inside it can start at an in-chunk offset
`>= c.cs`; the division then reports `chunkIdx + 1`, and `Bytes` either resolves
into the *next* chunk's bytes or — when `chunkIdx + 1 >= len(c.chunks)` —
returns nil. Both are silent: no panic, no error, just wrong or empty data.

### How the invariant was broken

`growChunk` deliberately allocates oversized chunks: an allocation larger than
`c.cs` gets `make([]byte, 0, n)`. That is fine while the chunk stays inside its
owning context, because such a chunk is created full and never receives a second
allocation. The leak was in teardown: `Release` handed **every** chunk to
`putChunk(c.cs, …)`, so an oversized chunk was filed into the `c.cs` size pool.
A later, unrelated `Acquire` then drew a `cap > cs` chunk from that pool as an
ordinary chunk and started filling it — and the first allocation to reach in-chunk
offset `c.cs` aliased away.

This is why the symptom was load-sensitive and non-reproducible in isolation:
it requires one test (or query) to perform an oversized allocation and release,
and *another* to draw the poisoned chunk out of the shared `sync.Pool`.

The earlier hypothesis recorded in the fix_plan — that a recycled context could
carry a grown `c.cs` — is **refuted**: `Acquire` allocates a fresh `Context` and
derives `cs` from `Kind` alone; only chunks are pooled, never contexts.

### The fix

`putChunk` now rejects any buffer whose capacity is not exactly the pool's size
class (`internal/mctx/mctx.go`). Oversized chunks are dropped to the GC, which is
what the function's original "non-standard sizes are not pooled" comment already
intended — the size class was just being taken from the caller's `cs` argument
rather than from the buffer itself. Undersized buffers are rejected for the
mirror reason: pooling one would hand out a chunk that cannot hold `cs` bytes.

### Verification

- `TestOversizedChunkNeverEntersSizePool` — white-box: poisons both size pools,
  then asserts `getChunk` only ever returns `cap == cs`.
- `TestAllocBytesRoundTripAcrossChunks` — black-box: poisons the default pool,
  then round-trips 4 chunks' worth of tagged 1 KiB blocks through
  `AllocBytes`/`Bytes`, both immediately and after all allocation completes.
- Mutation-verified in both directions: with the `putChunk` guard reverted, the
  white-box test reports `cap 65636` and the black-box test fails at block 64
  with `Bytes returned 0 bytes` (in-chunk offset 65536 == `defaultChunkSize`).
- `go test -race ./internal/mctx/` PASS; **`make race-gate` green** (it had been
  red on this test).

### Known adjacent sharp edge (not triggered today)

`growChunk` inserts the new chunk *after* `c.head` and memmoves the tail, which
renumbers every chunk past `head`. Any `(offset, length)` already issued against
those chunks would silently shift by one `c.cs`. It is unreachable at present
because `growChunk` always leaves `head` pointing at the last chunk, and the only
path that leaves `head` behind the tail is `Reset`, after which all offsets are
invalid anyway. Recorded in the deferral ledger rather than guarded, so a future
change to `head` bookkeeping does not reintroduce the same aliasing class.
