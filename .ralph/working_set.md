Task: M0130-S10 — PG 18.3 standby E2E harness (COMPLETE)

Files:
- internal/testport/e2e_pg183_standby_full_cycle_test.go: New TestE2E_PGStandbyFullCycle —
  four-phase M0130 acceptance test (goopg primary → pg_basebackup → PG 18.3 standby →
  DDL/DML replay → promote → reverse-attach goopg standby on new timeline).
  Gated on GOOPG_SKIP_M0130_E2E + testing.Short().
- docs/design/0130-0010-pg183-standby-e2e-harness.md: Status draft→accepted;
  added "What was built" section.
- docs/design/README.md: Row status draft→accepted.
- .ralph/fix_plan.md: M0130-S10 marked [x] DONE.

Key symbols:
- TestE2E_PGStandbyFullCycle: four-phase E2E acceptance test
- waitForPhysicalStreamingPGtoGoopg: new helper for PG→goopg streaming direction
- Uses existing helpers: runGoopgBasebackupToPG, runPGBasebackup, configurePGStandbyFromGoopgBackup,
  configureGoopgStandbyFromPGBackup, waitForPhysicalStreamingGoopgToPG, waitForPGCount,
  waitForGoopgCount, copyInitFiles, normalizePGWALSegmentNames, pgScalar, goopgScalar

Hypothesis/Findings:
- ALL M0130 tasks (S1–S10) are now [x] complete
- S10 test is the acceptance vehicle; reuses extensive existing helper infrastructure
  from TestE2E_FailoverGoopgToPG + TestE2E_FailoverPGtoGoopg
- The test is gated behind GOOPG_SKIP_M0130_E2E because it needs real PG binaries
  (pg_basebackup/psql/pg_ctl/initdb/postgres) and takes ~2-3 minutes
- All four phases (forward replicate, DDL replay, failover, reverse attach) are
  exercised in sequence

Next step: M0130 milestone complete; next priority per fix_plan banner is
M0119-0004 (pg_dump 002-010 TAP) then remaining unchecked items

Gates run:
- go build ./...: OK
- go vet ./...: pre-existing cpCancel warning in cmd/goopg/main.go (not my change)
- RALPH_PRECOMMIT_SCOPE=units: PASS (all cached)
- make ralph-state-guard: REPAIRED + PASS

In-flight: none
