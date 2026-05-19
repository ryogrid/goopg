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
