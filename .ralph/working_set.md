(idle — nothing in flight)

Last loop: M-NIGHTLY AI-20260810-011258-006 (pgbench TPC-B false deadlocks)
FIXED and checked off in .ralph/fix_plan.md.

Root cause: `updateOp`'s index-scan collector in
`internal/executor/operators_storage.go` appended one `pendingUpdate` per
scanned index entry. A non-HOT update leaves the superseded index entry live
until VACUUM, so several entries for one key each resolve via `followHOTChain`
to the SAME live tuple — the SET expression and the xmax stamp were applied
once per entry. The duplicate stamps left extra versions in a hot page and two
clients then genuinely waited on each other's leftovers, closing a WFG cycle PG
cannot produce. Fix: skip an entry whose resolved `(blk, actualSlot)` is
already pending (19 lines).

Both earlier hypotheses were implemented, MEASURED at 8/8 cycles unchanged, and
reverted — do not retry them: (1) guarding WFG edge registration on
`TxnMgr.IsXIDActive(xmax)`; (2) `epqResolveHeadBlocker`, redirecting the wait to
the t_ctid chain head (the dumps showed it working and the cycle closing anyway).

Gates run: `SCALE=10 T=120 bash analysis/wfg-tpcb-repro.sh` → 8→0 WFG cycles and
8→0 failed transactions (2× 120 s + 1× 30 s); `go test ./internal/executor/`
PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); commit-hook pgbench smoke;
`make ralph-state-guard`.

Ledger: 1 row — a deterministic regression test is still owed. A sequential
in-process fixture (50 index-scan updates, 2 KB filler to force page overflow,
plan verified `*planner.IndexScan`) passes with AND without the fix, so it was
not committed; the duplicate needs a concurrent non-HOT interleaving. The
cheapest standing gate is the TPC-B balance assertion in
`ci/batch/stages/stage-pgbench.sh` (also the resume point of an older row).

Design: Resolution section in
`docs/design/0099-0003-deadlock-safe-conflict-waiting.md` (+ README row).

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner):
M-NIGHTLY has no open item for this subject; the banner puts M0130 next.

In-flight: none.
