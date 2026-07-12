# C4 — Per-query engine tax (direction doc)

status: direction (not a full design) · date: 2026-07-13 · base: `e453e3f2`

The read path measures the tax: goopg `-S` is CPU-bound at 10.8 cores /
91,783 TPS vs PG's 180,257 (1.96×). The same per-statement overhead applies to
every write-path statement (BEGIN 3.0×, in-txn SELECT 1.25×) but explains only
~2× of the 7.4× write gap — hence direction-level here, full designs for
C1–C3. Every point gained on C4 also raises the write path's commit-arrival
rate, widening emergent groups.

Profile attribution (`../01-results.md`, goopg_S CPU):

| cost | share of `-S` CPU | direction |
|---|---:|---|
| `executeOneSimpleStmt` (parse+plan+build) | 24.9 % cum | D-A below |
| operator-tree construction (`opOpen`) | 16.5 % cum | D-A |
| `WriteReadyForQuery` protocol flush | ~18 % | D-B |
| socket `write(2)` syscalls | ~17 % | D-B |
| snapshot capture (`captureSnapshot`, in `-N`) | 4.4 % | D-C |

## D-A — Per-connection plan/operator cache

Cache key: normalized query text (pgbench repeats 4 shapes verbatim; simple
protocol, no params — exact-text keying suffices initially). Cache value:
parsed tree + plan; rebuild the operator tree per execution first (safer), and
only cache open-state if profiling still shows `opOpen` hot. Invalidation:
any DDL (catalog version bump), `search_path`/GUC changes affecting
resolution, temp-table lifecycle — goopg already has per-connection
virtual-catalog scoping to hook. Risk: the silent-row-count trap — the
Q12/Q13 spot-check and the full TestPort_ suites are the gate; cache must be
bypassable via GUC for bisecting — `plan_cache_mode` already exists
(`internal/config/defaults.go:356`); reuse/extend it (`force_custom_plan` ⇒
bypass) rather than inventing a new knob.

## D-B — Protocol write coalescing

A simple-query cycle currently issues a `write(2)` per message boundary
(RowDescription / DataRow… / CommandComplete / ReadyForQuery flushed
separately — `FrameWriter.Flush` sites). Coalesce to one flush per
ReadyForQuery (the protocol only requires flushing when returning control to
the client). Expected: collapses the ~35 % combined socket/flush share to a
fraction; zero semantic risk if NOTICE/COPY immediate-flush paths keep their
explicit flushes (`NoticeFlush` contract, context.go AddNotice).

## D-C — Snapshot-capture fast path

`captureSnapshot` is 4.4 % in `-N` (per-statement snapshots in
read-committed). Directions: epoch-cached snapshot reuse when no commit
occurred since the last capture (commit-sequence counter check), or
proc-array delta capture. Correctness-sensitive (visibility); needs its own
design if pursued.

## Measurement plan

Re-run `run_rw50.sh` `-S` after each direction lands; the target is closing
toward PG's 0.276 ms/statement. Gate set: G-tpch + full TestPort_ +
G-unit (row counts are the regression currency for D-A).
