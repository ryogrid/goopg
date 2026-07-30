(idle — nothing in flight)

Last loop (#1 of this run, 2026-07-30 10:17–12:35): **M0125-0011's gate-integrity
follow-up landed AND the owed full 99-query SF0.5 gate is DISCHARGED.**

Task: close the ledger row (2026-07-29) whose resume point asked the SF0.5 sweep
to stamp binary identity, then spend the quiet-host window on the gate that had
been owed four times.

Files: `bench/tpcds/env_tpcds.sh` (shared D4a helpers `bench_engine_id` /
`bench_engine_bin_sha` / `bench_running_engine_sha`, `GOOPG_BIN` override),
`scripts/tpcds-sf05-regression.sh` (`sf05_ensure_bin`,
`sf05_engine_binary_line`, `sf05_guard_engine_stable`),
`scripts/tpcds-bench-compare.sh` (delegates to the shared helpers),
`docs/design/0125-0011-*.md` + `0124-0001-*.md` + `0125-0026-*.md` +
`docs/design/README.md`, `analysis/m0125-sf05-fullgate-20260730/` (report +
driver log + README), `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

## Facts the next loop should NOT re-derive

- **Gate result, HEAD `e29faca9`, quiet host, one binary, 10:20→12:26:**
  `PASS=79 (49 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=16 SKIP=4`.
  Five changes vs the 2026-07-29 baseline, no change in the other direction:
  Q16/Q94/Q95 `CKMISMATCH→PASS` (M0125-0007/-0008/-0023), Q75 `ERROR→PASS`
  (M0125-0004, also clears the live `Q75,100,pinned` anchor), Q47
  `MISMATCH→TIMEOUT` (row defect fixed; its runtime is M0125-0013's open half).
- **The timeout class is 16, not 15** — Q47 joined it. M0125-0026's capture list
  is amended in both fix_plan and the design doc. Do not call Q47 unbounded: its
  one completion reading is 142 s at SF=1 against a 300 s cap on half the data.
- **NEW defect filed, NOT fixed: `M0125-0027`** — `tpcds-bench-compare.sh:138`'s
  catch-all `else status="OK"` records a *connection-refused* psql as `OK` with
  the error text's line count as its row count (measured: `goopg Q99 OK 0s 2`
  with nothing on 65436). Also owed there: re-read the published SF=1 board for
  `OK` cells with a tiny row count at ~0 s. M0125-0026's per-class tasks must
  start at **M0125-0028** now.
- The SF0.5 sweep now **always rebuilds** (`SF05_NO_BUILD=1` opts out and says so
  in the report), refuses to clobber the shared `tmp/goopg-bench-bin` while
  ci/batch or the SF=1 harness runs from it (FORCE=1 does NOT waive that), and
  shouts `*** SWEEP VOID ***` into the report if a restart swaps the engine.
  Run a private image with `GOOPG_BIN=tmp/goopg-sf05-bin`.
- The dead chunked attempt in `analysis/m0125-sf05-fullgate-20260730/` is now
  labelled invalid in that dir's README — don't cite its cells or seconds.

## NEXT (banner order — the gate obligation is no longer in the way)

1. `M0125-0002` **commit 2 — `cloneExprShiftIdx`** (`nl_index_join.go:777`): the
   first commit expecting plan hunks; needs the timed 22-query TPC-H run
   (`scripts/goopg-test-run.sh`, `GOGC=100` / `GOMEMLIMIT=12GiB`) on a quiet host
   plus `make plan-diff LABEL=tpcds-round2-head`.
2. `M0125-0003` stage 2's TIMED four-arm study (quiet host), then stage 3.
3. `M0125-0005`; `M0125-0026` is the host-independent option when the host is busy.

Gates run: `go build ./...` clean; `bash -n` on all three touched shell files;
four direct `sf05_guard_engine_stable` branch probes (inactive / source-changed /
image-only / unchanged); two SF0.5 subset probes (Q1, Q98) validating the header;
SF=1 harness header re-rendered after the delegation; **full 99-query SF0.5 gate
PASS**; units suite PASS (warm cache — no Go code changed this loop);
`make ralph-state-guard` OK; pgbench smoke via the commit hook.
NOT run: timed TPC-H, plan-diff (no planner/executor code touched).

In-flight: none.
