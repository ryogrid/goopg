# 0049-0001 — Query cancellation (`CancelRequest`)

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0049 — Protocol parity
**Supersedes:** —

## Context

A long-running query in goopg cannot be interrupted from psql's Ctrl-C.
The client tears down the connection instead. Upstream multiplexes
cancellation onto the same listener using a magic protocol code: a
secondary TCP connection sends `CancelRequest(pid, secretKey)`, the
postmaster looks up the backend, and signals it. The executor's
critical loops poll a flag at safe points.

## Plan

1. **BackendKeyData population.** At session start, generate a random
   non-zero `(pid uint32, secretKey uint32)`. `pid` is goopg's
   monotonically increasing backend id (it's not a real OS pid; it
   doesn't have to be). Store the pair in a process-wide map keyed by
   pid.
2. **Cancel-request listener.** The wire-protocol entry-point already
   reads the startup-packet length-and-protocol-version preamble. When
   the version is `1234.5678` (`80877102`), parse the cancel body
   `(pid, secretKey)` and:
   - Look up the backend.
   - If found and key matches, set `backend.cancelled = true`.
   - Close the cancel connection (no response on cancel).
3. **Cancellation flag.** New `Backend.cancelled atomic.Bool` on the
   per-session struct.
4. **Poll points.** Add `if checkCancellation() { return ErrCancelled }`
   to:
   - `executor.Operator.Next` tight loops (SeqScan, IndexScan, Sort,
     HashBuild, MHJ probe).
   - WAL waiter (`flushedLSN >= commitLSN`) wake-loop.
   - Lock-acquisition wait-loop (M0008's lockmgr already has a hook
     point).
5. **SQLSTATE 57014.** New error `query_canceled` with the upstream
   text `"canceling statement due to user request"`. Dispatcher converts
   `ErrCancelled` to this SQLSTATE and resumes the per-connection
   command loop.
6. **psql interaction.** `psql` already sends the right packet on
   Ctrl-C; goopg only needs to handle it correctly server-side.

## Definition of Done

- `psql` Ctrl-C against `SELECT pg_sleep(60)` returns within 200 ms with
  SQLSTATE 57014.
- Cancel during a TPC-H query returns control to the dispatcher cleanly;
  subsequent simple queries on the same session work.
- Wire test: send a cancel with the wrong secretKey — no effect.

## Upstream reference

- `postgres/src/backend/postmaster/postmaster.c` —
  `processCancelRequest`.
- `postgres/src/backend/tcop/postgres.c` —
  `ProcessInterrupts`, `CHECK_FOR_INTERRUPTS`.
- `postgres/src/backend/utils/error/elog.c` —
  `query_canceled` SQLSTATE handling.

## goopg references

- `internal/server/dispatch.go` — startup-packet preamble reader.
- `internal/protocol/` — message codecs.
- `internal/executor/` — operators that need a poll-point.
