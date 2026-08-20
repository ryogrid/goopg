Task: M0134-0057 (prepared_xacts.sql) — PARTIAL this loop. Landed 1 contained
bucket (SERIALIZABLE-path duplicate-gid check on PREPARE TRANSACTION); case
itself stays `failed`. CSV row unchanged. Next: select M0134-0058
(random.sql).

Files this loop: `internal/postmaster/twophase.go` (`execPrepareTransaction`
duplicate-gid check hoisted above the isolation-level branch + shared nil
marker registered in `s.preparedXactStore` for the SERIALIZABLE keep-open
path; `execFinalizePrepared` frees the marker on same-backend finalise and
guards `px == nil` on the detached-path lookup),
`internal/postmaster/twophase_dupgid_test.go` (new,
`TestPrepareTransactionDuplicateGidSerializable`), `.ralph/deferral_ledger.md`
(new row, M0134-0057 bucket breakdown), `.ralph/fix_plan.md` (M0134-0057
entry rewritten with PARTIAL verdict + next-task pointer).

Key symbols: `execPrepareTransaction`, `execFinalizePrepared`,
`preparedXactStore.put/has/take` (internal/postmaster/twophase.go).

Hypothesis/Findings: prepared_xacts.sql's diff (234 lines, 12 `^+ERROR`/7
`^-ERROR`) breaks down as: (3, landed) duplicate-gid check ran only on the
RC/RR detach path, never the SERIALIZABLE same-backend keep-open path, so a
second SERIALIZABLE PREPARE TRANSACTION of an in-use gid silently succeeded;
(1, LARGE, dominant ~90%) SERIALIZABLE PREPARE TRANSACTION keeps the
transaction open on the SAME connTx handle instead of truly dissociating the
backend (PG's PrepareTransaction releases the PGPROC + gives a fresh txn
state) — statements on the originating connection between PREPARE and
finalise still see the prepared txn's own uncommitted state; needs
DetachToDedicatedSlot extended to SERIALIZABLE with SSI predicate-lock state
re-keyed off the Handle, design-doc scale; (2, LARGE) `pg_prepared_xacts` is
a permanent 0-row stub, same pattern class as the already-ledgered
`pg_cursors` stub (M0134-0056); cascade (not independent) — write-skew
section fallout of bucket 1's wrong-visibility state.

Next step: select **M0134-0058 (random.sql)** per the fix_plan
task-ID-ascending selection rule. Size it via `scripts/pg-regress-runner.sh
--verbose random` (delegate to researcher) before deciding fix/split/park,
same pattern as M0134-0049..0057.

Gates run this loop: `go build ./...` PASS; `GOOPG_CG_UNIT=... go test
./internal/postmaster/...` PASS (52.7s, no new panics);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (via
tester, ~71s dominated by cmd/goopg cold run, rest cache-warm); `make
ralph-state-guard` to be run before status block; pre-commit pgbench smoke
PASS (382/727/13013 TPS across the 3 builtin scripts, no failed
transactions).

Delegation: researcher agent `ae667fda4087304f7` (1 round, sizing, found
diff 234 lines/12+ERROR, 2 LARGE + 1 CONTAINED bucket, recommended landing
bucket 3 only — accepted). implementer agent `a39ddac082d726b9a` (1 round,
landed the duplicate-gid fix cleanly per brief, DONE — no follow-up round
needed; flagged one edge-case deferral candidate re: cross-backend
COMMIT/ROLLBACK PREPARED racing a live SERIALIZABLE marker, noted in the
ledger row's bucket-1 text, not separately actioned since it requires
cross-backend finalisation which is already out of scope).

In-flight: none. Commit `94207664` pushed to `regress-renumbering`. No
server left running (regress runner + pgbench smoke + postmaster tests all
self-start/stop their own throwaway goopg instances via the cgroup wrapper).
