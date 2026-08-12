(idle — nothing in flight)

M0131-S28 is now COMPLETE and checked off in fix_plan (all variants green).

Landed this loop: `internal/testutil/pgcluster/session.go` (`Cluster.OpenSession`
→ `Session{Exec,QueryScalar,Close}`, a pinned `sql.Conn`) + the last S28
assertion in `internal/testport/e2e_goopg_crashstart_on_pgdata_test.go`:
`BEGIN; INSERT 100 rows` held open across `KillHard`, and those rows must be
absent through goopg after recovery while a committed marker row survives.

Worth carrying:
- **goopg NOW REPLAYS a real-PG crash tail end to end.** The `XLOG_NEXTOID`
  self-arming skip in `TestE2E_GoopgCrashStartOnPGDataDir` no longer fires, so
  all 14 captured answers are compared against goopg for real (2.8 s). Verified
  by corrupting one expected answer and seeing goopg's own value returned. The
  skip stays as a tripwire only. Implication: S21's remaining opcode gaps are
  narrower than the S28 text assumed — re-probe before sizing S21.
- **Non-vacuity is the whole difficulty of an uncommitted-rows assertion.**
  "Recovery discarded them" and "they were never written" are the same
  observation. Three cheap discriminators: rows visible inside their own txn;
  `pg_stat_activity.state = 'idle in transaction'` matched by
  `application_name`; a committed marker AFTER them (a COMMIT flushes the single
  WAL stream up to its own LSN, so the uncommitted records are pinned on disk —
  SIGKILL loses WAL buffers, not the log). Plus guard 13 in the bytes.
- `pg_waldump` pads columns (`tx:        779,`) — match on
  `strings.Join(strings.Fields(line), " ")`, never the raw line.
- `pg_waldump -p dir <firstseg>` reads THROUGH to the end of WAL, so
  `dumpCrashTail` covers late records too.
- pgcluster sessions must pin `sql.Conn`, not `sql.DB`: a pool would run the
  INSERT and the COMMIT on different backends.

Gates: both S28 variants + `_Concurrent` skip green (4.5 s);
`internal/testutil/pgcluster` green; guard 13's three failure branches each
proven to bite (throwaway probe, deleted); `go vet` + `gofmt -l` clean;
`RALPH_PRECOMMIT_SCOPE=units` PASS (cached); pgbench smoke via commit hook. No
planner/executor change → tpch-spotcheck not required.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): unchecked M0131 in file
order — S9 (LARGE, sub-sliced; S9.1 SRF-only views highest-value), S21 (LARGE —
re-probe its real remaining gap first, see above), S24 (LARGE/RISKY).

In-flight: none.
