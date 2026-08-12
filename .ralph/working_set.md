(idle — nothing in flight)

M-NIGHTLY `AI-20260813-005117-012` (`TestPort_IsolationInsertConflictDoUpdate4`)
fixed and committed — and the same one-line-class fix also closed
`AI-20260813-005117-009` (`TestPort_IsolationEvalPlanQual`).

**The finding worth carrying: "REOPENED for the 3rd time" was a false story, and
believing it would have sent the loop down the wrong path.** The fix_plan framed
EvalPlanQual as two earlier fixes that "did not hold". They held fine. The
`TM_SelfModified` guard from `408a3962` (M0131-S32.1) landed 2026-08-12 and this
nightly is the FIRST run after it — a brand-new regression wearing an old
symptom. Date the last change to the code path before accepting a reopen
narrative; `git log -S` on the guard text settled it in one command.

Second finding: the spec's own shape was a decoy. `insert-conflict-do-update-4`
is the PARTITIONED upsert spec, so partitioning and ON CONFLICT were the two
obvious suspects — both wrong. Bisecting by SQL shape (partitioned vs plain,
key vs non-key column, index vs seq scan, FOR UPDATE present vs absent) on a
throwaway 5533 server reduced it to a repro with neither feature in it:
`BEGIN; SELECT * FROM t WHERE i=1 FOR UPDATE; UPDATE t SET i=i+10 WHERE i=1;`
→ `UPDATE 0`. Reduce to the minimal shape before reading the failing test's
subject matter as a hint.

Third: the bug was narrow because of the HOT split — a non-key update takes
`tryApplyHOTUpdate` and never reaches the guard, so ONLY a HOT-ineligible
key-column change falls through to it. That is also why a whole class of
FOR UPDATE tests kept passing while these two failed.

Root cause: the guard was a bare `Xmax == myXID`. Upstream
`HeapTupleSatisfiesUpdate` (`heapam_visibility.c`) reaches that comparison only
after excluding `HEAP_XMAX_IS_MULTI` (raw MultiXactId vs TransactionId are
disjoint id spaces) and `HEAP_XMAX_IS_LOCKED_ONLY` (returns TM_BeingModified —
a row you only LOCKED is still yours to update). Both write-phase arms now share
`isSelfModifiedWrite`; sharing was deliberate, since a duplicated inline guard is
how one sibling gets fixed while the other rots.

Ledger row filed: the guard still has no cmax/curcid arm. Probed it rather than
asserting it — the obvious two-UPDATEs-in-one-txn shape does NOT diverge (the
second command scans the new version), so no failing shape is in hand.

Next candidates (all M-NIGHTLY, selectable): PredicateHash / ReceiptReport (open
since 2026-08-11, 3 AI-ids each); `TestE2E_FailoverPGtoGoopg` subtest `async`;
`TestPort_IsolationMultipleCic`; the 11 regress normalization cases. Worth
re-running the whole testport isolation set first — this fix may have cleared
more than the two items it was aimed at.

Gates: `go build ./...` clean; `go test ./internal/executor/` PASS (6.0 s);
`TestIsSelfModifiedWrite` PASS; 9 neighbouring isolation specs PASS (68 s) incl.
EvalPlanQual individually (24.25 s); target spec PASS (3.9 s);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 canonical); pgbench smoke PASS
via the commit hook; `make ralph-state-guard` OK.

In-flight: none.
