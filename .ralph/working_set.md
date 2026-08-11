Task: M0131-S32.1 — FIXED for the index-driven UPDATE arm and committed.
Successor filed: **M0131-S32.2** (SeqScan arm, scan-phase loss).

Root cause (loop #145), two defects with opposite signs:
(A) The EvalPlanQual retry loop read a failed chain-follow as "the row is gone"
    (`if !chainFound { epqSkip = true; break }`) and skipped the row SILENTLY
    while still reporting `UPDATE 1`. Under contention the chain tip is
    routinely owned by a transaction that has not committed yet, so the write
    is dropped: 1112 of 1946 attempts. PG blocks on that updater
    (XactLockTableWait) and re-fetches. Fix = `epqChainPendingWriter` +
    `epqWait` + RC snapshot refresh + retry, in BOTH EPQ loops.
(B) Retrying exposed convergence: two `pending` entries of one statement reach
    the same physical tuple; `isConcurrentlyUpdated` ignores our own xmax, so
    the second re-stamped a tuple we had already killed and inserted a SECOND
    live version (1720 live rows in a 1-row table) — the pgbench_branches ~10x
    over-application. Fix = PG's `TM_SelfModified` skip at both EPQ loop tops.

Files: `internal/executor/operators_storage.go`, new
`internal/executor/s321_probe.go` (env-gated counters, `GOOPG_S321_PROBE=1`,
plus `GOOPG_S321_NOWAIT=1` A/B kill switch — both temporary, delete with S32.2),
new `analysis/concurrent-hotrow.sh` (minimal repro + gate),
`docs/design/0131-0026-concurrent-hot-row-lost-updates.md`,
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

Next step: work **M0131-S32.2**. Gate `NOIDX=1 ROWS=1 CLIENTS=8 N=200 bash
analysis/concurrent-hotrow.sh` lands 1063/1600. Write-phase classes are CLOSED
there (`count(*)` correct, `epq_self_modified=0`, `epq_chain_notfound=0`) — the
statement finds NO row and reports `UPDATE 0`, so start in the SCAN phase:
instrument the already-declared `s321ScanNoRow` counter, then diff
`mvcc.TupleVisible` against `HeapTupleSatisfiesUpdate`
(`postgres/src/backend/access/heap/heapam_visibility.c`). Hypothesis: goopg
treats a version as dead the moment its xmax COMMITS regardless of
statement-snapshot visibility, so a hot row briefly has no visible version.
pgbench_branches (5 rows) is planned as a SeqScan — it is the last table
diverging in the S32.1 gate.

Harness trap hit this loop: an orphaned server from an earlier probe still
listening on the port absorbs the whole run (fresh server fails to bind, psql
still connects, numbers come from the wrong cluster). concurrent-hotrow.sh now
preflights the port; atomicity-nocrash-control.sh does NOT.

Gates run: concurrent-hotrow.sh exact 1600/1600 at ROWS=1 and ROWS=5, with and
without BEGIN/COMMIT (was 470/1600); `RUNS=1 LOADSEC=30
analysis/atomicity-nocrash-control.sh` — sum(tbalance) now EXACT (-68881, was
-14938), sum(bbalance) -54588 (was -309543 ~10x over), still OVERALL: FAIL by
design until S32.2; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS (rc=0); `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35).
NOT run: TPC-DS SF0.5 gate (~1 h).

In-flight: none.
