Task: testport/TestPort_IsolationIntraGrantInplace — root-cause DONE
  (commit 268710050): perm-10 divergence explained + ledgered; impl
  fix now UNBLOCKED (HOLD lifted mid-loop by concurrent commit
  caf858301 at 03:21 — :65433 restored, gates run again).

Root cause (full bullets on fix_plan ~L574): perm `b1 drop1 b3 sfu3
  revoke4 c1 r3` — goopg's `lockRowsOp.Open` waits on the deferred
  pg_class drop via `maybeRecordPgClassRowMark`→`waitTablePendingDrop`
  (operators_lockrows.go:853/910, operators_ddl.go:12688) BEFORE any
  child row iteration; post-unblock `drainAndStamp` (:1031) re-evals
  the `oid='x'::regclass` filter → `regclassin` misses
  (reg_identifier.go:286-331) → 42P01 where PG emits `0 rows`.
  PG resolves the cast once before the LockTuple wait. Fix sketch:
  after waitTablePendingDrop unblocks + drop committed, short-circuit
  drainAndStamp to EOF. Also explains r3-before-revoke4 tail ordering.

Also this loop: AI-20260919-000526-001 verified = the ledgered
  instrumentscope race, 4th repro (instrument.go:444 vs
  operators_gather.go:124) — noted ~L454, do not re-file.

Environment — CHANGED THIS LOOP: `data.HOLD` REMOVED (only
  preloss-clone-20260915.HOLD remains); concurrent Devin loop commit
  `caf858301` (03:21) lifted HOLD + committed the six staged S2b-15
  optimizer files + folded my race note. Impl tasks are UNBLOCKED —
  tpch-spotcheck should run again (verify :65433 answers first).
  ci/logs/*, .claude/settings.json, .ralphrc, analysis/, postgres,
  third-party/ carry unstaged foreign mods — never commit those.
  WATCH: another live loop commits to this branch — expect its
  commits mid-flight; stage explicitly, verify before committing.

Gates run: repro `go test -v -run
  '^TestPort_IsolationIntraGrantInplace$' ./internal/testport/` FAIL
  4.86s (expected — diagnosis only); pgbench smoke PASS (hook).
In-flight: none.
Next step: item-8 topmost unblocked impl =
  M-NIGHTLY-instrumentscope-race-fix (~L505, design resolved loop 9:
  force nil scope at acquireSubPlanOp Build sites under
  Context.instrumentScope — bug-for-bug OK). Then
  IsolationIntraGrantInplace's sketched fix (executor, needs
  units+race+tpch-spotcheck), then IsolationStats/LockRowsSort/
  PgDumpConnectionSetup/RegressSuite entries.
