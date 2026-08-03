(idle — nothing in flight)

M0127-P3.3 is CLOSED and committed. **S3 continues; P3.4 is next.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P3.4` (Semi/Anti/LEFT per-batch semantics,
batch-global `antiBuildHasNull`; shared build declines when nbatch > 1).
Bar: UNITS + DS05 + RACE.**

Carry-over facts a next loop should not re-derive:

- **Spill files now live in `<datadir>/base/pgsql_tmp/pgsql_tmp<pid>.*`**
  (`internal/pgtemp`), owned by a per-statement `tempFileRegistry` on
  `Context` (`internal/executor/tempfiles.go`), allocated in `NewContext`
  and shared BY POINTER with worker contexts. Release points:
  `executeOneSimpleStmt` + `dispatch_extended`. Startup sweep in
  `Server.Run` before `close(s.ready)`.
- **A new spill file MUST be created via `newSpillWriter(ctx)`** —
  `newSpillWriterInDir(dir)` is the registry-less form for tests only.
  Eager unlink is `ctx.removeSpillFile(path)` (unlinks AND deregisters).
- **Four P3.3 ledger rows** are future worklist: `temp_tablespaces`,
  `temp_file_limit`/`log_temp_files` byte accounting (P3.5 surfaces
  counters), statement-vs-resource-owner release scope (**P4.3
  `Materialize` MUST revisit this** — a cross-statement spill holder would
  be unlinked underneath), and PG's un-ported `RemovePgTempRelationFiles`.
- **Declined join shapes are still P3.4's:** LEFT / Semi / Anti /
  composite-key / shared (parallel) build / FOR-UPDATE ctid;
  `joinBatchEligible` is the one gate. Q21 is an ANTI join, so S3's Q21
  exit evidence needs P3.4 before P3.5.
- **Nightly triage 2026-08-03:** all 20 `AI-20260803-013955-*` items were
  already filed by loop #35. AI-001/002 (units+race internal/executor)
  re-verified STALE at HEAD this loop (`TestMHJParallelNoDuplicates` ok,
  `-race` clean) — the nightly sha predates the fixes.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — ~70 files show
  as unformatted at HEAD. Never `gofmt -w`; match alignment manually.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified.
  Tracking = `docs/design/0127-pg-shaped-join-search.md` §6 + fix_plan
  checkbox + README index status.

Gates run this loop: UNITS PASS (`RALPH_PRECOMMIT_SCOPE=units`, incl. new
`internal/pgtemp`); RACE PASS (`make race-gate` + `go test -race
./internal/server/`); SPOT PASS (Q12=2 / Q13=35, 16.6 s, peak 11,596 MB);
crash-injection gate PASS (`TestStartupSweepReclaimsCrashedQueryFiles`);
SMOKE via the commit hook; `make ralph-state-guard` OK (after auto-repair
of the previous loop's clean-exit marker). DS05 not run — P3.3's bar is
UNITS + crash-injection, and no join semantics changed. PLAN not run — no
plan surface touched.

In-flight: none.
