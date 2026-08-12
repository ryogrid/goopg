(idle — nothing in flight)

M0131-S27 MAIN VARIANT LANDED (loop #18). The item stays UNCHECKED: the
torn-contrecord variant is still open (ledger 2026-08-12).

Files: `internal/testport/e2e_pg_crashstart_on_goopgdata_test.go` (new, the S27
test), `internal/executor/operators_storage.go` (nil-Key guard),
`internal/executor/update_range_index_scan_test.go` (new guard),
`internal/wal/pg_assembled_emit.go` (XLHP_CLEANUP_LOCK), design 0131-0017 +
README, fix_plan S27 annotation, 3 ledger rows.

Worth carrying:
- Writing an E2E is not "just a test": S27's first two runs found two
  PRODUCTION defects. (1) `UPDATE … WHERE id BETWEEN a AND b` on an indexed
  column panicked the backend — `updateViaIndex` deref'd a nil
  `IndexScan.Key` (range scans use LowKey/HighKey; composite probes use Keys).
  (2) goopg's `xl_heap_prune` omitted `XLHP_CLEANUP_LOCK`, so an
  assert-enabled PG 18.3 TRAPped at `pruneheap.c:1677` and the cluster was
  UNSTARTABLE. Both were invisible to every existing gate.
- Why the standby lane missed the prune flag: it only matters when PG replays
  a prune record it did not write. `TestE2E_PGStandbyFullCycle` passes both
  before and after. Any goopg-emitted PG-format record has the same blind
  spot — check the flags upstream's redo *asserts on*, not just the ones it
  reads.
- Harness gotchas for this lane: `cluster.Options.PSQLPath` must be set
  explicitly (`psql` is not on $PATH under `go test`); an uncommitted
  transaction needs `StartPSQL` + a trailing `SELECT pg_sleep(600)`, since
  `psql -c` exits at EOF and the disconnect rolls back.
- `internal/server` is FLAKY at HEAD under full-package parallel runs
  (different test fails each run; verified with my changes stashed). Do not
  chase it as a regression.

Gates: new S27 E2E PASS; `TestE2E_PGColdStartOnGoopgDataDir`,
`TestE2E_GoopgCrashStartOnPGDataDir`, `TestE2E_GoopgColdStartOnPGDataDir`,
`TestE2E_PGStandbyFullCycle`, `TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart`
PASS; `RALPH_PRECOMMIT_SCOPE=units` PASS; `internal/wal` + `internal/executor`
PASS; `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35); pgbench smoke via
the commit hook; `make ralph-state-guard` OK (auto-repaired the stale completed
marker, as in the last two loops).

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`
(unchanged for 4 loops); all 4 `## AI-` items already filed under M-NIGHTLY.

Next loop (banner = M-NIGHTLY filing, then M0131): remaining unchecked M0131 in
file order — S9 (LARGE), S8b, S21 (LARGE), S24, S26, S27 (torn contrecord only),
S28 (GIN-refusal variant only). S26 (`pd_lsn` completeness audit, ~2 loops) is
the natural next: S27 now gives it a lane where PG replays goopg records over
live pages.

In-flight: none.
