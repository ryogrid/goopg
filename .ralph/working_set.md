Task: M0131-S32.2 — FIXED and committed. Successor filed: **M0131-S32.3**.

Root cause (loop #146), both in the SCAN-phase EPQ loop inside
`updateOp.Next`'s `scanMatching` callback (S32.1 only hardened the two
WRITE-phase loops):
(C) `return nil // row deleted by concurrent tx` on a failed chain-follow —
    the same not-found-means-deleted skip S32.1 fixed one loop later. The row
    never entered `pending`, so the statement reported `UPDATE 0` and no
    write-phase counter fired; that is why S32.1 mis-filed the residue as an
    `mvcc.TupleVisible` hole. Fix = `epqChainPendingWriter` + `epqWait` +
    retry (scan_epq_notfound 20708 -> 4852).
(D) `epqWait` refreshes `ctx.Snap`, and `scanMatching` uses that SAME snapshot
    for visibility. Refreshing mid-scan let the rest of the scan see
    post-statement commits, so one logical row was handed to the callback
    twice (two DIFFERENT physical tuples ⇒ the S32.1 TM_SelfModified guard
    cannot fire) and forked: 130 live rows in a 1-row table. Fix = save/restore
    `ctx.Snap` around the loop. PG keeps the scan snapshot fixed
    (execMain.c EvalPlanQual*).

Files: `internal/executor/operators_storage.go` (seq arm scan-phase loop),
`internal/executor/s321_probe.go` (+2 counters, +`GOOPG_S322_NOWAIT` A/B
switch — temporary, delete with S32.3),
`docs/design/0131-0026-concurrent-hot-row-lost-updates.md` §7,
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

Next step: work **M0131-S32.3** — pgbench control still 0.25% short on
`pgbench_branches` (-966698 vs -969135) while the isolated repro is exact in
every config. The harnesses differ by the multi-statement 4-table
BEGIN/COMMIT, so FIRST extend `analysis/concurrent-hotrow.sh` with a TXN mode
that writes a second table inside the same transaction; then re-run with
`GOOPG_S321_PROBE=1` and read `epq_chain_pending_wait`/`scan_epq_pending_wait`
against `epqRetryLimit`/`maxEPQRetriesRC` (suspicion: retry ceiling reached
under longer xmax lifetimes).

Gates run: `analysis/concurrent-hotrow.sh` PASS 1600/1600, count(*) exact,
0 client errors, all four configs (NOIDX ROWS=1/5, index arm ROWS=1/5, TXN
on/off); `RUNS=1 LOADSEC=30 analysis/atomicity-nocrash-control.sh` —
abalance/tbalance EXACT, bbalance -966698 vs -969135, OVERALL FAIL by design
until S32.3; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS; `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35).
NOT run: TPC-DS SF0.5 gate (~1 h).

In-flight: none.
