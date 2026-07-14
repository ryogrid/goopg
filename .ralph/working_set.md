(idle — nothing in flight)

## Loop summary (2026-07-12, loop #90)

**M-NIGHTLY AI-20260712-020530-002 — root-cause CORRECTION (no functional code
change).** The nightly triage for run 20260712-020530 was already complete
(loop #89: 38/39 = stale build break fixed at HEAD; AI-002 = flaky
TuplelockUpgradeNoDeadlock, demoted + deferred). This loop re-diagnosed AI-002
empirically and CORRECTED loop #89's wrong root cause.

**Finding (instrumented, proof):** on the failing perm — `s1_share
s2_for_update s3_for_update s1_rollback s2_rollback s3_rollback` (a FOR UPDATE
case, not the s2_update/s3_update perm loop #89 named) — EVERY tuple-lock
acquire reports `LockMgr==nil`, so `acquireTupleLock`/`tryAcquireTupleLock` are
TOTAL NO-OPS in the server, INCLUDING lockRowsOp's FOR UPDATE ExclusiveLock
(operators_lockrows.go:900). This is by deliberate design
(context.go:863-871): `Context.LockMgr` is nil in production to keep heavyweight
locking off the hot path, so all row-lock serialisation rides
xmax/`WaitForXID`, whose single `commitCond.Broadcast()` wakes every waiter →
non-FIFO race (s3 sometimes beats s2, s2 times out). Loop #89's "FOR UPDATE is
stable / DML epqWait path lacks the tuple lock" was FALSE; its proposed fix
(add acquireTupleLock to epqWait) would ALSO be a no-op.

**Landed:** design doc `docs/design/0021-0012-tuple-lock-fifo-wiring.md` (+README
row); corrected the test doc comment; deferral_ledger.md correction row. Test
stays demoted (`runIsoSpec`). No production code changed. Did NOT edit fix_plan
(driver-churn hazard) — correction lives in ledger+design-doc+test-comment.

Gates: go build ./... clean; go vet testport clean; demoted test 6/6 ok (never
FAIL); ralph-state-guard OK (auto-repaired progress marker).

**Next (deferred slice, HIGH blast radius):** implement 0021-0012 — route
acquireTupleLock/tryAcquireTupleLock to the always-on `tableLockMgr` under a
statement-scoped backend id + per-statement release; keep acquireRelLock on nil
c.LockMgr; then re-promote test to runIsoSpecStrict + CSV `pass`. Validate with
FULL isolation suite (`go test -run TestPort_Isolation ./internal/testport/`) +
pgbench smoke — NOT a spot check (second deadlock domain, NOWAIT/SKIP-LOCKED,
coarse key-share modes).
In-flight: none
