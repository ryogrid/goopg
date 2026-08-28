Task just completed: M-NIGHTLY AI-20260822-001356-003 —
`testport/TestSyntax_AdvisoryLock_SessionUnlockAcrossBeginBoundary`, the only
nightly regression that still reproduces at HEAD (failed 6 nights running:
20260822/23/24/25/27 + the in-flight 20260828-235424).

Diagnosis: NOT an engine regression. `pg_advisory_lock()` returns **void**; PG
18.3 sends it as a NON-NULL zero-length value (oracle-verified on a throwaway
PG 18.3 at /tmp: `... IS NULL` -> f, `pg_typeof` -> `void`). goopg's
`evalAdvisoryLock` (internal/executor/expr.go:17924) has returned the same
empty non-NULL datum since M0096-0003. The test scanned it into `sql.NullBool`
and only ever passed because goopg serialized an empty non-null string as NULL
on the wire; `fd24fa33c` (postmaster wire fix, M0134-0070, 2026-08-21) made the
empty value real and `ParseBool("")` then failed. So a PG-parity FIX exposed a
test asserting the old bug. Fix: that one call site now uses `ExecContext`,
matching the two sibling void call sites in the same file (:39 / :105 use
`sql.NullString`, :363 uses `ExecContext`).

Files: internal/testport/advisory_lock_test.go (the fix), .ralph/fix_plan.md
(nightly 20260827 section filed + advisory rows closed), .ralph/deferral_ledger.md
(new row: void RESULT COLUMN TYPE OID gap).

Ledgered discovery: goopg reports `pg_typeof(pg_advisory_lock(0))` = `unknown`
where PG reports `void` (OID 2278) — value bytes match, RowDescription type OID
does not. Applies to every void builtin. Needs a void Datum kind + result-type
inference + wire RowDescription work (pg_proc already has RetType 2278).

M-NIGHTLY filing done this loop: run 20260827-052222 (133 items) filed. Its
132-item testport block sampled a MID-MIGRATION parser sha (846d651d884d, inside
the goyacc rewrite, before e56d4f4fd + the #97 merge) — the failures are that
intermediate grammar's 42601s in test SETUP. 18 verified stale (PASS at HEAD),
110 marked "presumed stale, pending" — **the in-flight nightly 20260828-235424
runs the SAME HEAD sha and adjudicates them: read
ci/logs/20260828-235424/testport/results.csv FIRST next loop and tick them off
in bulk** (at filing time it had exactly ONE --- FAIL, the advisory one, now fixed).

Gates run: TestSyntax_Advisory* (5/5 PASS under cgroup cap), go vet
./internal/testport, make ralph-state-guard, commit-hook pgbench smoke.

NEXT LOOP: adjudicate the 110 pending 20260827 rows from
ci/logs/20260828-235424/testport/results.csv (bookkeeping, cheap), then per the
Current Priority banner work **M0134-0156 (psql_crosstab.sql)**.

In-flight: none of mine. NOTE: the nightly batch run 20260828-235424 was still
executing during this loop (pid ~640236 run-nightly.sh) — do not run a second
`internal/testport` binary concurrently with its testport stage (wedge, ledger
row 2026-08-28 P7.2-testport-concurrency).
