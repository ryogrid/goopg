Task: M0131-S30.8 control run — VALIDATED; surfaced and filed **M0131-S32**, a
new highest-severity wrong-answer defect. No engine code changed this loop.

**Read `docs/design/0131-0025-single-row-update-stall.md` before touching S30/S32.**

Result of loop #143, in order:

1. The no-crash CONTROL loop #142 said had never been run correctly is now a
   committed harness (`analysis/atomicity-nocrash-control.sh`). It **PASSES 2/2**:
   `sum(abalance) == sum(delta)` exact, LIVE and after a clean stop+restart, with
   `count(*) from pgbench_history` matching pgbench's own transaction count.
   So the invariant is valid and **S30.8's premise SURVIVES** — `RUNS=2
   crashprobe30` still FAILs 2/2 at the same HEAD (31384 != 44061;
   -157231 != -165901), i.e. the failure really does require a crash.
2. Localised that crash failure on the preserved `/tmp/crashprobe30/run1` using
   `pgbench_history` as per-row ground truth: the 12677 gap decomposes EXACTLY as
   1 account off its own history sum by 568 + **11 accounts with non-zero
   `abalance` and NO history row** (-12109). That is the REVERSE of S30.8's filed
   direction — the accounts UPDATE survived and the history INSERT did not (or an
   in-flight txn's update is visible when it should be aborted, i.e. S30.7's
   `MarkUnknownAsAborted` arm is incomplete). Note goopg has no `xmin` system
   column, so XIDs could not be read back from SQL.
3. While checking whether tellers/branches were usable signals, found they
   diverge on the CLEAN control too (-90351 / -182750 vs -54526). Reduced that to
   **M0131-S32**: ONE session, 300 autocommit `UPDATE t SET v=v+1 WHERE id=1`
   drives `v` to exactly 64 and it then freezes forever, while every remaining
   statement reports `UPDATE 1` and commits. No concurrency, no crash, no error.
   The S31 index-unreachable signature reappears alongside it (`WHERE id=1`
   returns nothing; `WHERE id+0=1` returns the stale row).

Files: `analysis/atomicity-nocrash-control.sh` (NEW control),
`analysis/hotstall.sh` (NEW minimal S32 repro, gate `OVERALL: PASS`),
`docs/design/0131-0025-single-row-update-stall.md`, `docs/design/README.md`,
`.ralph/fix_plan.md` (S32 filed; S30.8 control result appended),
`.ralph/deferral_ledger.md`.

Key symbols (for the NEXT loop, none edited yet): `tryApplyHOTUpdate`
(`internal/executor/operators_storage.go:3555`), its `oldItem.Flags !=
storage.ItemIDNormal` skip return (`:3625`, stale comment), the `!used` fallback
EPQ loop (`:4328`), `isConcurrentlyUpdated` (`:3180`).

Ruled out for S32, do not re-test: all three concurrency guards above — they
cannot fire with one client.

Next step: work **M0131-S32**. Put an unconditional counter on every
`used == false` return of `tryApplyHOTUpdate` and on entry to the `!used`
fallback, run `bash analysis/hotstall.sh`, and see which arm handles updates 65+.
Independently, the statement reports `UPDATE 1` while writing nothing — the row
count comes from the planned row set, not from rows written.

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(cached); `make ralph-state-guard` PASS (auto-repaired the previous loop's
completed marker); commit-hook pgbench smoke PASS; new control PASS 2/2;
`crashprobe30` FAIL 2/2 as expected; `analysis/hotstall.sh` reproduces.

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012) — all 12 already filed under M-NIGHTLY; nothing new to file.

In-flight: none.
