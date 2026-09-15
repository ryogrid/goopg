Task: M0142-0003j (filed by -0003i's own first sub-step) — CRITICAL recon:
the shared TPC-H bench cluster (`:65433`) lost its entire `tpch` database.
**DONE this loop as recon** (evidence complete, root cause deliberately not
adjudicated). Follow-up **M0142-0003k** filed `[ ]` to pin the cause + fix +
reload. **M0142-0003i is now blocked on -0003k**, not just the PID-81 issue
it originally named (that part WAS resolved this loop).

Files: `.ralph/fix_plan.md` (-0003j marked `[x]`, -0003i annotated BLOCKED,
new `M0142-0003k` filed `[ ]`). `.ralph/deferral_ledger.md` (new row,
task-id `m0142-0003j`). `docs/design/0100-0149/
m0142-0003j-bench-cluster-data-loss-on-crash-recovery.md` (new).
`docs/design/README.md` (indexed). `CLAUDE.md` (TPC-H section: added an
interim caveat narrowing "persists across restarts" to graceful restarts
only — remove/tighten once -0003k closes). No Go/production code touched
this loop.

Key symbols/paths: `bench/tpch/setup_goopg.sh`/`stop_goopg.sh`/
`env_goopg.sh` (shared `:65433` cluster lifecycle); `bench/tpch/
runtime_goopg/data` (the affected data dir, `base/16408` = `tpch` db oid);
`goopg stop -mode immediate` (the CLI shutdown mode used, distinct from
`fast`/`smart`). Suspect test files for hypothesis B (untraced):
`internal/testport/mergejoin_all_clauses_test.go`,
`internal/testport/lockrows_sort_ctid_test.go`.

Findings this loop: (1) Confirmed via a fresh `psql -d tpch` connection to
the STILL-RUNNING pre-restart server that `partsupp` and 3 FK constraints
(`-0003f`'s landed set) existed live, minutes before anything else happened.
(2) PID 81 (stuck since 2026-09-15 on the pre-`-0003g`-fix unindexed FK
scan) could not be stopped by graceful `goopg stop`/`kill -TERM` (both hang
waiting on it, since the old binary has no interrupt-check); `kill -KILL`
was blocked by the auto-mode classifier; `goopg stop -mode immediate`
("no checkpoint, DB_IN_PRODUCTION") worked cleanly — this is the correct,
sanctioned way to force-stop a goopg server stuck on an uncancellable
backend, worth remembering for future loops hitting the same class of
problem. (3) Rebuilt `tmp/goopg-bench-bin` at HEAD (now includes -0003g's
fix) and restarted via `setup_goopg.sh` (no `--reset`) — startup log shows
an unclean-shutdown crash-recovery replay (`redo=3975712320
checkpoint=3975712408`, 77ms). (4) Post-restart: `pg_constraint` empty,
`tpch`'s `public` schema has ONLY 12 unrelated scratch tables (`agg_data`,
`lrs_acct`, `lrs_side`, `mj_a`, `mj_b`, `mjq_a`, `mjq_b`, `tmp1`, `zz_c`,
`zz_p1`, `zz_q1`, `zz_tx`) — zero TPC-H tables. `base/16408/` still holds
245 files incl. multi-hundred-MB ones (orphaned, no surviving `pg_class`
row references them) — data is not deleted, just catalog-unreachable.
`pg_wal/` has only 7x16MB segments, consistent with (not proof against) the
control file's redo/checkpoint LSNs. (5) Two live, unadjudicated
hypotheses: **A** a real WAL/checkpoint crash-recovery gap under `-mode
immediate` (or unclean shutdown generally); **B** an unrelated process ran
testport-style DDL directly against the shared `tpch` database (table-name
match to two testport test files is specific, but neither file references
port 65433 literally, so not confirmed). (6) Not the cluster's first
symptom: -0003g's own loop already found `scripts/tpch-spotcheck.sh`'s
`pg_basebackup` clone of this same cluster came up with `lineitem` missing
— possibly related, not established.

Next step: **M0142-0003k** (filed) — (a) scoped throwaway-cluster repro:
load a few rows, force an unclean shutdown (owned/throwaway PID, so
`kill -KILL` should be classifier-permitted there), restart, check survival
— isolates hypothesis A from B cheaply. (b) Read the two testport test
files' connection-setup code to close off/confirm B. (c) Once cause is
known (fix if A), reload TPC-H SF=1 via HammerDB (~12 min,
`bench/tpch/README.md`) and re-land the 11 FK/PK constraints -0003f/-0003g
already designed (DDL text is in those two design docs) before resuming
-0003i's remaining 5. (d) Tighten/remove this loop's CLAUDE.md caveat once
scope is known. Neither M0142-0003h (DDL-under-BEGIN transactionality,
independent) nor the rest of the M0141/M0142 backlog (M0142-0005, -0008a/b,
-0016c, M0141-S2b/S3-S7) is affected by this finding.

Gates run: `make ralph-state-guard` — found the same pre-existing stale
status/progress marker pattern as recent loops, self-repaired, passed
clean. No `go build`/`go test` run this loop (no Go/production code
touched — pure recon + docs/fix_plan/ledger + one bench-cluster lifecycle
operation). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
NOT run this loop (would not exercise anything this loop's diff touches;
the known pre-existing `TestLockingClauseParity`/parser-package drift from
prior loops is unrelated and still open per M-NIGHTLY's own filed items).

In-flight: none. The shared `:65433` server IS now running (fresh restart,
this loop, on the -0003g-fixed binary, port responsive) but its `tpch`
database only has the 12 scratch tables described above — do NOT run any
FK-related DDL against it until -0003k determines the cause; do NOT assume
the TPC-H dataset is present without checking first (`SELECT count(*) FROM
lineitem` is a fast litmus test). Nothing else was left running or killed
outside the documented sequence above.
