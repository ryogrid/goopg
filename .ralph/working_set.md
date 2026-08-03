(idle — nothing in flight)

M0127-P3.2 is CLOSED and committed. **S3 continues; P3.3 is next.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P3.3` (per-query temp-file registry on
`Context`; relocate spill files to `<datadir>/base/pgsql_tmp/`; startup
sweep; fix the `spillOp.Close` unlink leak). Bar: UNITS + crash-injection
test.**

Carry-over facts a next loop should not re-derive:

- **`ctx.WorkMem` now really bounds an INNER single-key hash join.**
  `internal/executor/join_batch.go` (`hashBatchState`) + hashed spill frames
  (`spillWriter.WriteRowHashed` / `spillReader.ReadRowHashedInto`). goopg's
  `work_mem` BootVal is **512MB** (not PG's 4MB), so nothing spills in the
  TPC-H/TPC-DS gates today — the mechanism is proved by unit identity tests,
  not by the sweeps.
- **The batch state is installed even at NBatch == 1** — deliberately. Bound
  comes from growth, not the estimate. `nbatch > 1` guards every hot-path
  hash computation.
- **Routing hashes the key's CANONICAL bytes** (`appendCanonicalNumericKey`).
  Never re-introduce "hash the int64 if it is one": `demoteIntHash` can move
  a build to the string lane mid-run, and mismatched routing is a LOST MATCH,
  not an error.
- **Declined shapes (still unbounded): LEFT / Semi / Anti / composite-key /
  shared (parallel) build / FOR-UPDATE ctid.** `joinBatchEligible` is the one
  gate. P3.4 owns opening them — it needs PG's fill-aware skip rule 1.
- **Q21 is an ANTI join**, so S3's Q21 exit evidence needs P3.4 before P3.5.
- **Six P3.2 ledger rows** are P3.3/P3.4/P3.5's worklist (declined shapes,
  composite lane, implicit shared-build decline, un-retired
  `maxPresizeBuckets`, no temp-file registry, no EXPLAIN counters).
- **DS05 post-TIMEOUT restart hazard is still live** — Q72 TIMEOUTs (300 s;
  pre-existing, 315/317 s on earlier commits) and the restart then fails on a
  `systemd-run` scope collision. Recovery that worked this loop:
  `systemctl --user reset-failed`, `bench/tpcds/server.sh start sf05`, then
  `QUERIES="$(seq 73 99)" scripts/tpcds-sf05-regression.sh sweep`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified
  (an edit to its IMPLEMENTATION-TODO was reverted this loop). Tracking =
  `docs/design/0127-pg-shaped-join-search.md` §6 + fix_plan checkbox +
  README index status.

Gates run this loop: UNITS PASS; RACE PASS (`make race-gate`, all packages);
SPOT PASS (Q12=2 / Q13=35, 15.8 s, peak 10,224 MB); DS05 slices 1-50 /
51-72 / 73-99 MISMATCH=0 CKMISMATCH=0 ERROR=0; SMOKE via the commit hook;
`make ralph-state-guard` OK (after auto-repair of the previous loop's
clean-exit marker). PLAN not run — no plan surface touched.

In-flight: none.
