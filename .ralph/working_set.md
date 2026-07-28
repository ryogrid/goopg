(idle — nothing in flight)

Last loop (#49): M-NIGHTLY regress divergences — **`regress/errors` CLOSED**
via root-0031 (`docs/design/root-0031-pg-inherits-restart-persistence.md`).

The "a case mutates the shared test_setup fixtures" hypothesis is REFUTED.
Bisecting the ordering never converges because the trigger is nondeterministic:
root-0029's `clusterPoisoned` recovery restarts the cluster after any 120 s
timeout (frequent under nightly co-load, never in an isolated repro), and
`pg_inherits` was a purely VIRTUAL catalog — written only by `CREATE TABLE …
INHERITS`, reloaded by nothing. Every case after a restart therefore ran with
all inheritance edges gone: `pg_inherits` empty, parent scans returning no child
rows, and `ALTER TABLE emp RENAME COLUMN salary TO manager` SUCCEEDING (two
`manager` columns) because `renameatt`'s child-collision recursion had no
children to walk. Fix = heap-backed pg_inherits (`base/<dbOid>/2611`) written
from the `syncTableToCatalogHeap` funnel + `loadInheritanceFromHeap` in
`open.go`, plus three PG-fidelity bugs the restart had masked: qualified
`RenameTable` message, missing self-relation RENAME COLUMN collision check, and
`DROP AGGREGATE` resolving its arg type after the name lookup (fixing which
exposed the name-keyed registry dropping the wrong overload).

Next M-NIGHTLY step (still open, preempts M0124): re-verify `index_including`,
`portals_p2`, `select`, `select_distinct` at HEAD. Expect at least one distinct
cause — `select`'s fixtures are not inheritance-based. **Method proven this
loop:** run an alphabetical PREFIX of the suite up to the target case
(`-run "TestPort_RegressSuite/^(<case1>|…|<target>)$"`; cases are discovered in
`filepath.Glob` order) with `GOOPG_REGRESS_DIFF_DIR` — 63 cases ≈ 3.5 min vs
~1 h for the full suite — and grep the log for `restarting the cluster` before
reading anything into the ordering. Then M0124 → M0125 per the 2026-07-28
directive.

Gates run: new `TestPort_InheritanceSurvivesRestart` PASS (1.5 s) + negative
control (fails with the reload disabled, so non-vacuous); regress prefix through
`errors` PASS with `restarts=1` (8 divergent lines → 0; prefix pass count 9→10);
`go test ./internal/executor/ ./internal/catalog/ ./internal/initdb/` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35). TPC-DS SF0.5 gate
deliberately skipped — no TPC-DS query uses inheritance, `DROP AGGREGATE`, or
`ALTER TABLE … RENAME` (rationale in the design doc §4).
In-flight: none.
