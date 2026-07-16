# 0107-0011 — WAL drain/flush test flakiness under scheduling pressure (M-NIGHTLY follow-up)

## Context

M-NIGHTLY triage for nightly run `20260715-010036` (`ci/logs/action-items.md`
`AI-...-005`) flagged `units/internal/wal` as a regression: the package's `go
test` run was `SIGQUIT`-killed at the 33-minute per-package timeout during the
nightly batch. A prior loop's investigation (deferral ledger row, 2026-07-15,
task-id `M-NIGHTLY (run 20260715-010036 triage)`) reran `internal/wal` locally
without reproducing a hang and left it as an open, unconfirmed item — grouped
with three other packages (`cmd/goopg`, `internal/amcheck`, `internal/mvcc`)
that also hit the same 33-minute kill in the same nightly run.

This loop picked up that resume point. Two things distinguish `internal/wal`
from the other three: (1) its nightly log entry was not a bare timeout kill —
it recorded an actual fast (5.4s) test **failure**,
`TestStripeAppendConcurrentDrainConsistency: drain goroutine never ran`; and
(2) `ci/batch/run-nightly.sh` runs `Lane L: units,race` concurrently with
`Lane H: testport,pgbench` — i.e. the nightly `units` run genuinely shares the
host's CPUs with a `-race` build (very CPU-heavy) and live server benchmarks,
unlike a quiet local rerun.

## What was reproduced

Both of the following were confirmed **only** under synthetic host contention
(`for i in $(seq 1 20); do yes > /dev/null & done`, on a 16-core box) with the
full `internal/wal` package test suite (not `-run`-isolated, so all
`t.Parallel()` tests in the package compete for scheduling) — neither ever
reproduced under normal load, and `TestStripeAppendConcurrentDrainConsistency`
in isolation (`-run` alone) never reproduced under any load tested:

1. **`TestStripeAppendConcurrentDrainConsistency`** — `drain goroutine never
   ran`. The test spawns a busy-loop drain goroutine (`for { select
   {case<-done: return; default:}; ...; drainHits.Add(1) }`) concurrently with
   16 producer goroutines, then asserts `drainHits.Load() != 0` after
   `wg.Wait()`. Under heavy scheduling pressure the extra drain goroutine can
   genuinely never get a timeslice before the producers finish and `done` is
   closed — this is a real scheduling possibility given the test's structure,
   not a defect in `stripeAppend`/`publishVisibility` themselves.

2. **`TestDrainSafetyStress`** — `LSN invariant violated: write=N drained=M
   flushed=K` with `M > N`. Its `checkInvariant` closure read
   `w.writeLSNAtomic`, then `w.drainedLSNAtomic`, then `w.flushedLSNAtomic` as
   three independent, non-atomically-combined `Load()` calls. All three
   fields are monotonically non-decreasing in production
   (`writeLSNAtomic` via CAS-max `storeMaxLSN`; `drainedLSNAtomic`/
   `flushedLSNAtomic` only ever advance to a value bounded by an
   already-checked write frontier — see `xlogWrite`'s `rq.write > writtenLSN`
   guard, `internal/wal/writer.go`). Reading `write` **first** left a window
   where a concurrent drain/flush (of which the stress test runs 6 + 1
   background walwriter, continuously) could advance `drainedLSNAtomic` past
   the now-stale, already-captured `write` value before the second `Load()`
   ran — producing `dr > wr` even though the true invariant held at every
   real instant. Under 20-way synthetic CPU contention the gap between the
   two `Load()` calls widened enough (hundreds of appends' worth) to trip
   this reliably; under normal load the window is sub-microsecond and never
   observed.

Neither finding indicates a bug in `stripeAppend`, `publishVisibility`,
`xlogWrite`, or the writeMu/drain locking discipline itself — both are
artifacts of how the *tests* observe concurrent state, not of the state
itself. This matches the nightly batch's actual execution model (Lane
L runs `units` and `race` in parallel with `NIGHTLY_GO_P=4`), which explains
why these only ever surfaced in the nightly window and never in a quiet
local rerun — the same pattern the deferral ledger row already suspected for
the sibling `cmd/goopg`/`amcheck`/`mvcc` timeouts (goroutine dumps showing
near-total scheduler starvation, not blocked-on-lock goroutines).

## Fix

Both fixes are test-only, in `internal/wal/stripe_append_test.go` and
`internal/wal/drain_safety_stress_test.go`; no production code changed.

1. `TestStripeAppendConcurrentDrainConsistency`: added a `ready` channel,
   closed by the drain goroutine immediately after its first successful
   `drainHits.Add(1)`. The main test goroutine now blocks on `<-ready` before
   launching the producer goroutines, deterministically guaranteeing the
   drain goroutine has been scheduled and run at least once. The drain
   goroutine still runs concurrently with the producers afterward — the
   original concurrent-drain-safety exercise is unchanged, only the
   "did it run at all" assertion is de-flaked.

2. `TestDrainSafetyStress`'s `checkInvariant`: reordered the three `Load()`
   calls to `flushed`, `drained`, `write` (increasing-monotonicity order,
   ending on the field that can only have advanced further by the time it's
   read). Since `writeLSNAtomic` is monotonically non-decreasing, reading it
   *last* guarantees it reflects everything already visible to the earlier
   `drained`/`flushed` reads, eliminating the reordering artifact. A genuine
   locking-discipline violation (`drainedLSN` advancing beyond any true past
   write frontier) would still trip the check, since even the final,
   maximally-advanced `write` read would have to catch up to it.

## Verification

- Both failures reproduced reliably (10/10 and multiple runs) under
  `for i in $(seq 1 20); do yes >/dev/null & done` + full-package
  `go test ./internal/wal/...` **before** the fix.
- Both pass 10/10 under the identical contention setup **after** the fix.
- `go test -race -count=1 ./internal/wal/...` clean.
- `go test ./internal/wal/... ./internal/mvcc/...` clean (quiet host).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS (0
  failed, all 3 workloads).

## Remaining open work

The other three packages named by the same nightly run
(`cmd/goopg`/`internal/amcheck`/`internal/mvcc`) were **not** re-run to their
full 33+ minute timeout this loop (time-boxed after this fix + its gates).
They remain an open deferral-ledger row: a dedicated ~2-hour quiet-host
`go test -timeout 40m` run against those three, with a full (non-truncated)
goroutine dump captured if a kill still happens, is the next step to either
confirm the same host-contention explanation or surface a real hang.
