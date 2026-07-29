(idle — nothing in flight)

Last loop: **M0124-0002 COMPLETE — DISCHARGED**, no regression attributable to
`9740fce9`. Report `analysis/tpch-tpcds-round2-retro-20260729.md`. fix_plan box
TICKED. `plan_snapshots/tpcds-round2-head.txt` is committed and is now the live
baseline (captured LAST, so `plan-gate`'s newest-by-mtime pick lands on it).

Nightly triage: `ci/logs/action-items.md` unchanged since 2026-07-25
(mtime Jul 25 03:20) — filing a no-op for the fifth loop running.

## NEXT (banner order — `.ralph/fix_plan.md` "Current Priority" wins)

The user-set M0125 list (commit `d69fd834`) had items 1 and 2 as M0125-0003
stage 1 and M0124-0002; **both are now done**, so the next items are:

1. **`M0125-0012` (Q8)** — the only unresolved member of round 1's nine
   goopg-only errors; reproduces at SF0.5 in 12 s (`query8.sql:10`).
2. **SF0.5 back half** — Q54–Q99 debt (ledger 2026-07-29); resume with
   `QUERIES="$(seq 54 99)"`, do NOT re-run from Q1.
3. **`M0125-0014`/`-0015`** (Q49/Q51 SF=1 re-measure) — need a quiet host.
4. **`M0124-0004`** (Q35 row count) — the last open M0124 item; quiet host.

**M0125-0002 / -0004 / -0005 and M0125-0003 stage 2 are now UNBLOCKED** — the
snapshot they diff against exists. Use `make plan-diff LABEL=tpcds-round2-head`,
never `plan-gate`, when a *specific* baseline matters.

## Facts the next loop should NOT re-derive

- **A TPC-H 22-query stream costs ~15 min** at S-cold, `GOGC=100`/12 GiB — NOT
  the ~5.5 min the old "1086 → 325 s" note implies. Budget two streams per loop,
  no more. Longest: Q21 216 s, Q9 175–202 s.
- **Q9 and Q22 have ~22 % intra-arm run-to-run spread.** Any A/B move under that
  on those two queries is noise; do not open an investigation on it.
- **Arm rebuild recipe**: `git show 9740fce9 -- internal/planner/bushy.go` and
  `git apply -R` in a worktree (patch cached at `tmp/armA-revert.patch`); it
  reverse-applies CLEAN at HEAD. Binaries still at `tmp/goopg-arm{A,B}`.
  `internal/executor/expr.go` must stay untouched or the Q8 crash confounds it.
- **Harnesses** (reusable, parameterised): `analysis/m0124-0002/run-stream.sh`
  (`TPCH_RETRO_QUERIES` narrows the stream) and `with-server.sh` (arbitrary
  command against one arm's server). `run-arm.sh` in the same dir is an earlier
  loop's abandoned harness — kept for its partial data, not the one to use.
- **Plan targets**: `tpch@tpch` on 65433 (Makefile defaults). The
  `postgres@postgres` advice is stale folklore and never fired.
- `go test ./internal/...` HANGS in `internal/testport`. Use
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`.
- **Never `pkill -f`** — self-matches, kills the invoking shell (exit 144).

Gates run: units PASS (all cached — docs/analysis-only change, no Go edit);
TPC-H 22-query A/B PASS both arms (22/22 complete, 12/12 row anchors exact);
`make plan-diff` 22/22 MATCH; pgbench smoke via the commit hook;
`make ralph-state-guard` OK.

In-flight: none.
