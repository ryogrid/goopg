(idle — nothing in flight)

Last loop: **M0125-0012 (TPC-DS Q8) COMPLETE and committed.** fix_plan box TICKED.
Design `docs/design/0125-0012-q8-subquery-scope-index-remap.md` (§0 + §R carry the
correction), README index row updated to `accepted`, ledger row appended.

Nightly triage: `ci/logs/action-items.md` unchanged since 2026-07-25 (mtime Jul 25
03:20), all 5 `AI-` subjects already filed — no-op for the sixth loop running.

## NEXT (banner order — `.ralph/fix_plan.md` "Current Priority" wins)

Banner items 1–3 are now all done (M0125-0003 stage 1, M0124-0002, M0125-0012), so:

1. **The full 99-query SF0.5 gate**, once (banner item 4) — six loops of debt now.
   Needs a quiet host + ~1 h+. Last attempt reached Q53/99 in 57 min before the
   session cap (ledger 2026-07-29); resume with `QUERIES="$(seq 54 99)"`.
2. **`M0125-0014`/`-0015`** (Q49/Q51 SF=1 re-measure) — quiet host.
3. **`M0124-0004`** (Q35 row count) — the last open M0124 item; quiet host.
   Check `ci/batch/run-nightly.sh` is not running first (harness guards refuse).

## Facts the next loop should NOT re-derive

- **M0125-0012's fix_plan "do not re-diagnose" paragraph is REFUTED** and the box is
  ticked with the correction inline. The defect was `applyJoinTreePosMap`
  (`bushy.go`) descending into a FROM-subquery `Project` and applying the OUTER
  posMap outside its domain — NOT an unrepaired `Filter` ref. 57 was an
  **MHJ-order** offset, not a global FROM-order index.
- **Q8 is now in the TIMEOUT class, not the ERROR class.** Post-fix it exceeds a
  1500 s budget at SF0.5 (elapsed 1633 s) where it used to error at ~11 s. Ledger
  row has the resume point (`pushdown.go` — push `d_qoy`/`d_year` onto `date_dim`).
  This is pre-existing plan quality, not a regression; plan-diff proves shape is
  unchanged. **Do not re-open it as a regression.**
- **Fast Q8 iteration recipe**: the doll-house replica in
  `internal/executor/q8_subquery_scope_remap_test.go` reproduces the whole shape in
  <10 ms. Use it instead of the 11 s SF0.5 query.
- Throwaway servers: `./bin/goopg init -D <dir>` (subcommand is `init`, NOT
  `initdb`); real PG for oracle checks via `initdb`/`pg_ctl` from
  `postgres/local_install` on a /tmp datadir — ~15 s, avoids touching bench clusters.
- `pgrep -f '<pattern>'` **self-matches the invoking Bash shell** — it reported a
  live `query8` process that did not exist. Use `pgrep -x` / `ps -C`.
- `make plan-diff` needs a server on 65433: `bench/tpch/setup_goopg.sh` first
  (`tpch-spotcheck.sh` stops its own server on exit).

Gates run: units PASS (`RALPH_PRECOMMIT_SCOPE=units`); `tpch-spotcheck.sh` PASS
(Q12 rows=2, Q13 rows=35 — canonical); `make plan-diff LABEL=tpcds-round2-head`
**22/22 MATCH, 0 mismatch**; both new Q8 tests PASS post-fix and **proved to FAIL
pre-fix**; pgbench smoke via the commit hook; `make ralph-state-guard` OK.

In-flight: none.
