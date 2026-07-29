(idle — nothing in flight)

Last loop: **`M0125-0004` (Q75) CLOSED.** This loop was a **WIP RECOVERY**: the
previous loop died mid-task (04:18–05:17) leaving the whole implementation
uncommitted and a baton that still said "idle". Nothing was re-derived — the
code, design doc and the 99-query EXPLAIN A/B were already on disk; this loop
verified and landed them.

Fix: `internal/planner/inner_join_qual_pushdown.go`
(`pushSingleSideQualsIntoInnerJoinInputs`) duplicates a single-side conjunct onto
an INNER join's CTE/derived-table input, after `remapWithBindings`, validated
positionally by name. Q75 SF0.5 output is now **byte-identical to PG** (100 rows).

## NEXT (banner order)

1. **`M0125-0003` stage 1** — banner item 1, still unlanded (shape-neutral; land
   it and defer the four-arm TIMED study). Q35 is its acceptance query.
2. Then `M0125-0002 / -0005`, and the `M0125-0013` bookkeeping half (Q47's 8.4x
   runtime verdict — needs a QUIET host).
Owed independently, now **four commits deep**: one full 99-query SF0.5 gate run
on a quiet host (own ledger row, 2026-07-30).

## Facts the next loop should NOT re-derive

- **A live loop can leave WIP while its baton says "idle".** Check `git status`
  for untracked source + an `analysis/<task>/` dir before trusting the baton.
  Confirm it is not a peer: two `ralph_loop.sh` PIDs here were parent+child
  (respawner), NOT concurrent loops.
- **`cd` in a Bash call persists across calls.** A `cd analysis/...` silently
  made `scripts/` and `bench/` "not found" two calls later. Use absolute paths.
- **The A/B that clears a change of causing a timeout** is toggling the single
  call line (`if false { … }`), rebuilding, and re-running at a FIXED budget —
  Q31/Q64 timed out at 332s/333s disabled vs 332s/336s enabled, so they are
  pre-existing. Under nightly contention this verdict is available when a
  *timing* is not.
- The nightly CI batch (`ci/batch/run-nightly.sh`) was running all loop; the
  SF0.5 harness refuses to start and `FORCE=1` overrides — legitimate for
  row-count/value work ONLY, never a timing.
- `ck=n/a` (saturated `LIMIT`) means the gate CANNOT verify value — Q75's real
  evidence is the `diff` against a PG capture, not the gate's PASS.

Gates run: `go build ./...` + `go vet` clean; the WIP's 7 named guards PASS
(real runs, not cached); planner + executor package suites PASS; units suite
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); `make plan-diff` 22/22
MATCH incl. `Q15a-VIEWBODY`; SF0.5 firing-set sweep (Q4/11/31/39/64/74/75)
MISMATCH=0 CKMISMATCH=0 ERROR=0 (TIMEOUT=2 pre-existing, SKIP=1 oracle-timeout);
`make ralph-state-guard`; pgbench smoke via the commit hook.

In-flight: none.
