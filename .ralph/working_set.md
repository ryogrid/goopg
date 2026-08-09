Task: M-NIGHTLY AI-20260810-011258-003 (TestE2E_PGStandbyFullCycle) — blocker #6
FIXED and committed. The fix_plan item STAYS OPEN — blocker #7 now gates Phase D.

Landed:
- `internal/wal/writer.go`: `detectWritePos` records the on-disk TLI per segment
  (`segTLIs`, from `ParseXLogFileName`) and threads it into `scanLastSegmentEnd`
  (new `tli` param, `FormatSegmentNameTLI`; 0 → 1) and the `gap`/`non-final`
  diagnostics. Highest TLI wins on collision (mirrors upstream
  `XLogFileReadAnyTLI`'s newest-first `expectedTLEs` walk). KEY FACT: discovery
  never lost the segments — `parseSegmentName` DISCARDS the TLI, and every
  filename recomposition assumed TLI=1, so goopg did `os.ReadFile` on
  `000000010000000000000002` in a dir holding only `00000002…` files. Segment
  ordinals unchanged (`ParseXLogFileName(name, 0)`, same as before).
- New guards `internal/wal/writer_detect_tli_test.go`:
  `TestDetectWritePos_PromotedTimelineSegments` (real records via the writer,
  segments renamed to TLI 2, content-derived assert) +
  `TestDetectWritePos_PrefersHighestTimeline` (same segNo on both timelines,
  TLI-1 copy zeroed to identical length). Both mutation-verified; the first
  reproduces the production error string verbatim.

Result: the reverse standby STARTS on the promoted PG's base backup for the
first time — opens the data dir, binds, connects its walreceiver on slot
`s10_reverse`, begins continuous replay.

NEXT (blocker #7, fix_plan item 7): the harness's verification query
`SELECT count(*) FROM pg_stat_replication WHERE application_name='s10_reverse'
AND state='streaming'` runs against the PROMOTED PG and gets
`ERROR: cannot open relation "pg_stat_replication" / DETAIL: This operation is
not supported for views` — a real PG on a goopg-built catalog cannot evaluate
ANY view (empty relcache `rd_rules`; pre-existing ledgered ruleless-init gap).
Resume: probe the underlying SRF `pg_stat_get_wal_senders()` instead of the view
in `internal/testport/e2e_pg183_standby_full_cycle_test.go:~250`. The
`walreceiver WALData start mismatch` INFO line before it is NOT a blocker —
`m.StartLSN < expectedStart` already trims. Repro:
`go test -v -run '^TestE2E_PGStandbyFullCycle$' ./internal/testport/` (~6 s).

Gates run: internal/wal full package PASS (5.5 s), detectWritePos suite PASS +
both new tests mutation-verified, `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS, TLI-1 non-regression
`TestE2E_FailoverGoopgToPG` / `TestKillKillRecovery` /
`TestPort_PublicationSurvivesRestart` PASS (7.4 s), `make ralph-state-guard` OK
(auto-repaired the stale completed marker), commit-hook pgbench smoke PASS.
TestE2E_PGStandbyFullCycle still FAILS at blocker #7 (expected).

Ledger: 2 new rows (blocker #6 landed + the history-driven TLI resolution still
missing; blocker #7 diagnosis).

In-flight: none.
