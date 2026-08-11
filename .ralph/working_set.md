(idle — nothing in flight)

Loop #128 landed **M0131-S20.1 + S20.2**. S20 stays UNCHECKED — S20.3/.4/.5 remain.

Carry-forward:

- **Next pick per the banner:** finish M0131-S20 (S20.3 unconditional pre-replay
  `pg_internal.init` sweep, S20.4 `multixact.NewStoreAt` seeding from
  `pg_control.nextMulti` — the seam exists and still has no caller, S20.5 write
  the `minRecoveryPoint` policy down). Then S29.
- **New seam:** `internal/initdb/recovery_state.go` — `beginRecovery(dataDir)`
  returns `startupRecoveryDecision{crashRecovery, redoLSN, prevState}` and is the
  ONLY reader of `pg_control.State` in the tree. Anything else that needs to know
  "is this start a recovery?" asks it. `endOfRecoveryCheckpoint` next to it.
  On the WAL side: `wal.ReplayRecordsFrom` / `ReplayFromDirWithMgrAt` /
  `replayStartAt` take an explicit redo anchor; `redo == 0` = "use the scan".
- **Plan corrections recorded in the design doc, not silently applied:**
  `DB_SHUTDOWNED_IN_RECOVERY` is NOT clean (upstream's test is a single
  `!= DB_SHUTDOWNED`), and the "teach `isCheckpointRecord` about
  `XLOG_CHECKPOINT_REDO`" subtask is dropped as a mis-specification.
- **⚠ Biggest finding of the loop — filed as M0131-S30, wrong-answer class:**
  goopg crash recovery LOSES and DUPLICATES heap rows. Scale-5 pgbench + SIGKILL
  + restart: 500000 → 499949 rows, 218 missing `aid`s against 64 net missing
  (~154 duplicated). **Reproduced identically on a worktree build of the parent
  commit `15e73de3`** (500000 → 499936) — pre-existing, not an S20 regression.
  Prime suspect is the already-ledgered non-atomic non-HOT update
  (`HeapDelete` + `HeapInsert` as two records). Confirm with `pg_waldump` around
  a duplicated `aid` BEFORE changing code.
- 3 ledger rows: the archive-recovery (`recovery.signal`) InRecovery arm is still
  unimplemented in `beginRecovery`; goopg never VALIDATES that a checkpoint record
  actually lives at the redo address (upstream's "could not locate a valid
  checkpoint record"); plus the S30 discovery.

Technique reused (seventh loop): every guard proven fail-when-broken by scripted
revert over /tmp backups — 6 break directions here, each caught by a different
assertion.

Traps: port 5533 was held by an ORPHANED `goopg-sub` from an earlier session —
used 5534/5535 instead; always `ss -ltnp` first and grep the server log for
"address already in use". The in-situ crash smoke is only meaningful with a
same-shape baseline run (worktree build of the parent commit) — that comparison
is what turned "S20 broke durability" into "pre-existing, file it".

Gates run this loop: 6 new guards PASS + each proven failing without the fix,
`internal/wal` + `internal/control` + `internal/initdb` PASS (74 s),
`TestE2E_PGColdStart*`/`PGStandbyFullCycle`/`Failover*` PASS (68 s), in-situ
SIGKILL crash-restart smoke (both HEAD and parent-commit builds), UNITS PASS,
`scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35), pgbench smoke via the
commit hook.

In-flight: none.
