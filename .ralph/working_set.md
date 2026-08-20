Task: M0134-0042 (lock.sql) — landed a CONTAINED one-line fix, then PARKED
the rest of the case (still `failed` overall).

Files: `internal/executor/operators_ddl.go` (`collectAllViewTransitiveDeps`
BFS `seen` map now seeded with `startName` before recursing — fixes a
circular-view-dependency double-count in the `DROP VIEW ... CASCADE` notice
text), `internal/executor/operators_ddl_view_cascade_cycle_test.go` (new,
`TestCollectAllViewTransitiveDepsExcludesStartOnCycle`), `.ralph/fix_plan.md`
(M0134-0042 entry PARKED with bucket breakdown), `.ralph/deferral_ledger.md`
(2 new rows: pg_locks under-reporting through views, LOCK TABLE ACL gap).

Key symbols: `collectAllViewTransitiveDeps` (operators_ddl.go ~6165) —
one-line fix, `seen[startName.String()] = true` added right after
`seen := map[string]bool{}`. `execDropOneView`'s separate `dropped` map was
already correct (untouched). NOT fixed this loop: `execLockTable` /
`lockRelationTransitively` (operators_ddl.go:25265/25323) — pg_locks
under-reports plain tables reached transitively through a locked view, and
LOCK TABLE has zero ACL enforcement (no `LockTableAclCheck` analog).

Hypothesis/Findings: `lock.sql` sizing (researcher) found 3 unrelated root
causes in 179 diff lines, 0 `^+ERROR` / 4 `^-ERROR`: (A) pg_locks under-
reports transitively-locked plain tables via view recursion — root cause
unpinned between `collectSelectTableRefs` ref-collection missing plain
RangeVars vs. `cat.LookupTable` silently missing, needs a probe test on
`lockRelationTransitively`'s visited set; (B) LOCK TABLE has zero
permission-denied enforcement (PG oracle `postgres/src/backend/commands/
lockcmds.c:104,212-256` `RangeVarCallbackForLockTable`/`LockViewRecurse`);
(C, FIXED) `DROP VIEW ... CASCADE` notice double-counted a view→view cycle
because the BFS never marked its own start node as seen. Landed (C) as the
smallest CONTAINED win; A and B are ledgered, not attempted.

Next step: per fix_plan.md banner (M-NIGHTLY → M0134), select
**M0134-0043 (matview.sql, status `failed`)** — size via
`scripts/pg-regress-runner.sh --verbose matview` next loop (delegate to
`researcher`). Opportunistically, Bucket A of this loop's lock.sql park
(pg_locks view→table under-reporting) is a good quick follow-up if a future
loop wants a small isolated win instead of a fresh file.

Gates run this loop: `go build ./...` PASS; `go test
./internal/executor/...` (package scope, no `-count=1`) PASS, no
pre-existing failures; `scripts/pg-regress-runner.sh --verbose lock` — case
still FAILs overall (expected, Buckets A/B remain) but the cycle-notice diff
hunk is confirmed gone. `make ralph-state-guard` — ran, found a stale
status/progress mismatch (running/executing vs completed) from the previous
loop's clean-exit marker, self-repaired to consistent (running/in_progress).

Delegation: none in flight — researcher (agentId a06e001330ac6e30b) and
implementer (agentId a8e6d429c4f9c6cbd) both completed and reported DONE;
findings folded into fix_plan/ledger rows above. Handoff dir
`tmp/ralph-handoffs/M0134-0042-lockview-notice/` has `brief.md` only (worker
could not write `report.md` in its sandbox — its final-message report is
captured verbatim above/in the commit message instead).

In-flight: none.
