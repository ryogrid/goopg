(idle — nothing in flight)

Last loop: M-NIGHTLY AI-20260810-011258-001 (`race/internal/wal`). COMPLETE,
committed, pushed. Nightly batch 20260810-011258 (6 items) fully FILED first.

The discovery: the red race-gate was not a data race and not an invariant
violation — it was the race-closure test's own non-vacuity guard firing
(`reader took zero snapshots`). Two hazards, and the guard covered only one:
(1) the reader goroutine was spawned AFTER the writers, whose 8x500
reservations finish in well under a millisecond, so the reader could see
`stop` already closed on its first iteration; (2) `observed > 0` counted loop
iterations, but the assertion only does work on a snapshot catching a stripe
mid-flight — a scheduled-but-starved reader satisfied the guard while
asserting nothing.

Load-bearing detail: it reproduces on 100% of runs under `GOMAXPROCS=1 -race`
but passes 200/200 in isolation at default GOMAXPROCS. That asymmetry is why
it escaped review, and it is the cheapest probe for this whole bug family.
General lesson (recorded in the design doc): a non-vacuity guard must count
executions of THE ASSERTION, not iterations of the loop containing it.

Files: `internal/wal/reserve_emitted_test.go` only — `reserve_emitted.go` is
untouched (test-only fix). Fix is by construction, not by luck: writers wait
on a `readerLive` signal the reader sends after its first completed snapshot,
and `witnessed` counts only snapshots that caught a non-idle stripe, with the
write burst repeating (bounded 200 rounds) until `witnessed > 0`.

Design: addendum in `docs/design/0107-0007aa-wal-reserve-emitted.md` (+ README
row 0107-0007aa amended). Ledger: 1 new row (race-gate still red on a separate
cause — see below).

Gates run: mutation-verified BOTH directions — stripe published at `curr`
=> assertion fires under GOMAXPROCS=1 (where the OLD test took zero
snapshots); `perWorker = 0` => new guard fires (the OLD guard passes this).
`go test -race ./internal/wal/` PASS at default GOMAXPROCS and at
GOMAXPROCS=1; `-count=50` GOMAXPROCS=1 on the target test PASS; units
precommit PASS; pgbench commit hook PASS. `make race-gate` still FAILS — on
`internal/mctx` TestMultipleChunks, NOT on wal.

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
M0130 all `[x]`, so M-NIGHTLY selection is unblocked and is the top milestone.
5 nightly items remain unchecked, plus the newly-filed mctx flake. Strongest
candidate: **race/internal/mctx TestMultipleChunks** — it is what still reds
`make race-gate`, and it is diagnosed to a resume point already
(`Acquire(nil, KindStmt)` assumes a pristine pooled context; assert against
`c.cs`, not `defaultChunkSize`). Note AI-...-004 (IsolationMergeUpdate) is
likely STALE — re-run at HEAD before investigating. The pgbench item is the
highest-value engine item: clients abort with an error that is NOT itself in
the log, and the run still prints `0 failed`.

In-flight: none.
