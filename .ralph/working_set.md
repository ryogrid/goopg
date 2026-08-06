(idle — nothing in flight)

Last loop: **M-NIGHTLY / S7-gate loop #3. Selected `TestPort_IsolationEvalPlanQual`
(AI-20260806-011323-001) under carve-out #2 — it is the last genuine blocker to
the clean nightly cycle M0127-P6.1..P6.4 wait on. It did NOT reproduce; a
DIFFERENT row-locking defect was found and FIXED. Committed.**

1. **The recorded diagnosis was wrong.** fix_plan said `partiallock_ext` failed
   to block at L1027. The real 20260806 diff starts at **L1001 on
   `lockwithvalues`** (perm `wx2 lockwithvalues c2 c1 read`, spec line 411):
   no ` <waiting ...>` AND the stale row `checking|600|1200` where PG gives
   `1050|2100`. `partiallock_ext` blocks correctly at L1024 of the same log.
2. **Four reproduction conditions FALSIFIED at HEAD** — isolation (5 runs,
   21–22 s), nightly cgroup env (6G/8G/GOMEMLIMIT=5GiB), synthetic 12-way CPU
   load, and the whole `TestPort_Isolation*` family in nightly order (404 s,
   PASS at 21.44 s). It still fails 6/6 nights in the FULL package run ⇒ the
   trigger is order-dependent on a test OUTSIDE the isolation family.
3. **Fixed instead (root-0038):** `ORDER BY … FOR UPDATE` over a JOIN took **no
   tuple lock at all** — stale row in 4 ms, no block, no EPQ recheck. goopg has
   no resjunk-ctid column and reconstructs the TID from plan shape; both walkers
   correctly return nil at a `sortOp`, and the slot side-channel that covers
   that case (`sortOp.ctids`) only fires if the slot entering the sort has
   `hasCTID` — `seqScanOp` stamps it, a `joinOp` only does when
   `preserveCTIDRel` is set, and `markJoinPreserveCTID` stopped dead at the
   Sort. One arm (`case *sortOp:`) fixes it. A/B, same plan shape both sides:
   4 ms/600/no-block → 4008 ms/1050/blocked.

Files: `internal/executor/operators_lockrows.go` (the arm + why the walkers are
left alone), `internal/testport/lockrows_sort_ctid_test.go` (new guard),
`docs/design/root-0038-lockrows-sort-over-join-ctid.md` + README index,
fix_plan (diagnosis corrected + `[x]` task), 3 ledger rows.

Gates run: UNITS 0 FAIL; SPOT PASS (Q12=2, Q13=35); `TestPort_(Isolation|LockRows)*`
0 FAIL (416 s) post-change; new guard verified NON-VACUOUS (with the arm removed
`sort_over_join` fails at `balance=600`/no block while the `join_no_sort`
control still passes); pgbench smoke via hook. DS05 NOT run — deliberate:
`markJoinPreserveCTID`'s only caller is `lockRowsOp.Open`, so the change is
unreachable without a `LockRows` node and the TPC-DS corpus has no `FOR UPDATE`.

NEXT LOOP (banner: M0124 closed → M0125 → **M0127** → M-NIGHTLY → M0123).
M0127-P6.1 is STILL NOT selectable — it needs `ci/logs/action-items.md` at
`status: pass`, and the newest run is still `fail`. Read the newest one FIRST:
the next nightly is the first to run the new summarizer, so an
`[infra] testport/build-broke-mid-stage` item means "the harness saw a loop edit
the tree", not a regression. If still `fail` on EvalPlanQual, the next step is
the **prefix bisect** of `internal/testport` (regress / pg_dump / pg_basebackup /
pgoutput blocks + EvalPlanQual) to find the predecessor that poisons it — do not
re-attempt an isolation-level repro, that is now falsified four ways.

In-flight: none.
