(idle — nothing in flight)

Last loop: M-NIGHTLY `testport/TestPort_PublicationSurvivesRestart`
(AI-20260810-011258-005). COMPLETE, committed, pushed. Also closed
AI-20260810-011258-004 (`TestPort_IsolationMergeUpdate`) as CONFIRMED STALE —
it passes at HEAD in 4.77 s, no code change.

The discovery: publications silently vanished across EVERY restart, and neither
the heap nor the reload was at fault. `reloadUserPublicationsFromHeap` found and
decoded the rows correctly (instrumented: `baseRows=1`, name + `puballtables`
right) but stamped `Publication.DBOid` with the raw `cat.DBOID()` — the STORAGE
db oid `detectCatalogDBOID` reads out of the pg_database heap, `PostgresDBOid`
= 5. Every live path keys on the NAMESPACE oid instead: `resolveDBOid` defaults
to `DefaultDBOid` = 1, and dispatch.go's pg_publication lister queries
`PublicationsForDBOid(NamespaceDBOid(ectx.CurrentDatabaseOid))`, which folds
5 → 1. Since `d14af1e6` made DBOid half the registry key, reloaded rows landed
in a namespace no `postgres` connection ever reads. Third catalog to hit this
storage-vs-namespace mismatch (`f1e73ce0` fixed pg_ts_config the same way).

Fix: stamp `catalog.NamespaceDBOid(cat.DBOID())` —
`internal/initdb/catalog_heap_reload.go`. Design: addendum on the B3.3 entry in
`docs/design/wal-pg-identical-stream/IMPLEMENTATION-TODO.md`. Ledger: 1 row —
domain (~1382) / range (~1492) / enum (~1583) reloads stamp `cat.DBOID()` the
same way, unobservable today only because `LookupDomain`/`LookupEnum` fall back
to a name scan when no dbOid is threaded.

Gates run: repro confirmed at HEAD and out-of-test on a manual capped cluster
(port 5533) BEFORE the fix; `TestPort_PublicationSurvivesRestart` PASS after;
`go test -run SurvivesRestart ./internal/testport/` PASS (whole restart-
durability family, 45 s); units precommit PASS (`internal/initdb` re-ran cold,
59 s); pgbench commit hook PASS; `make ralph-state-guard` OK (auto-repaired the
previous loop's clean-exit marker).

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
M0130 all `[x]`, so M-NIGHTLY stays the top selectable milestone. 3 items remain
unchecked, all from batch 20260810-011258: AI-...-002
`TestE2E_FailoverGoopgToPG` (subtest `sync_remote_apply`), AI-...-003
`TestE2E_PGStandbyFullCycle`, AI-...-006 pgbench/nightly. -002 and -003 are the
M0130-S10 standby harness and likely share one root cause — re-run both at HEAD
first and diagnose together. AI-...-006 stays the highest-value engine item:
79 aborted clients whose ORIGINATING error is not in the log, and the run still
prints `0 failed`.

In-flight: none.
