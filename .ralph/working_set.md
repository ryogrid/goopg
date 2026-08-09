(idle — nothing in flight)

Last loop: M-NIGHTLY `race/internal/mctx TestMultipleChunks`. COMPLETE,
committed, pushed. Nightly batch 20260810-011258 was already fully FILED (all
6 items present in fix_plan); nothing new to file this loop.

The discovery: this was filed as a load-sensitive TEST flake and was a REAL
ENGINE BUG in the mctx bump allocator. `AllocBytes` encodes
`offset = chunkIdx*c.cs + offsetWithinChunk`, and `Bytes` inverts with `/c.cs`
and `%c.cs` — invertible only while EVERY chunk has cap exactly `c.cs`.
`growChunk` deliberately makes oversized chunks (`make([]byte,0,n)`, n > cs),
which is harmless in-context because such a chunk is created full — but
`Release` handed every chunk to `putChunk(c.cs, …)`, filing the oversized one
into the `cs` size pool. An unrelated later `Acquire` drew a `cap > cs` chunk,
and the first allocation reaching in-chunk offset `c.cs` reported `chunkIdx+1`,
so `Bytes` resolved into a nonexistent chunk and returned nil. Silent: no
panic, no error, just `""`.

The filed hypothesis (recycled context with a grown `c.cs`) is REFUTED —
`Acquire` allocates a fresh `Context` and derives `cs` from `Kind` alone; only
chunks are pooled, never contexts. That cross-test hand-off through a shared
`sync.Pool` is precisely why it needed full-package load and passed 100/100 in
isolation.

Fix: `putChunk` pools only buffers whose cap is exactly the pool's size class.
Files: `internal/mctx/mctx.go` (guard) + `internal/mctx/mctx_test.go` (2 tests).
Design: addendum in `docs/design/0107-0001-mctx-memory-context-substrate.md`
(+ README row amended). Ledger: 1 new row (adjacent unguarded entrance —
`growChunk` inserts after `head` and memmoves the tail, renumbering chunks past
`head`; unreachable today only as an emergent property of `head` bookkeeping).

Gates run: mutation-verified BOTH directions (guard reverted → white-box test
reports `cap 65636`, black-box fails at block 64 `Bytes returned 0 bytes`);
`go test -race ./internal/mctx/` PASS; **`make race-gate` GREEN** (it had been
red on this test for several loops); units precommit PASS; `tpch-spotcheck`
PASS (Q12=2, Q13=35) since this is the row-data allocation path; pgbench commit
hook PASS.

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
M0130 all `[x]`, so M-NIGHTLY stays the top selectable milestone. 4 items
remain unchecked, all from batch 20260810-011258:
AI-...-002 `TestE2E_FailoverGoopgToPG` (subtest `sync_remote_apply`),
AI-...-003 `TestE2E_PGStandbyFullCycle`, AI-...-004
`TestPort_IsolationMergeUpdate` (LIKELY STALE — re-run at HEAD first; the
cross-partition cmax fix landed after the nightly sha), AI-...-005
`TestPort_PublicationSurvivesRestart`. Cheapest first move: re-run -004 at HEAD
to close it. Highest-value engine item remains AI-...-006 pgbench/nightly —
79 aborted clients whose ORIGINATING error is not in the log, and the run still
prints `0 failed`.

In-flight: none.
