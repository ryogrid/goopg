(idle — nothing in flight)

Last loop (#5 of this run, 2026-07-30 15:35–16:20): **M0125-0003 §D8's TPC-DS arm
is MEASURED — the SF0.5 gate at `GOOPG_RELSIZE_FALLBACK=2` is complete and it
recommends the flip.** The predecessor loop had been cut off after chunks 1–3;
this loop ran chunk 4 (Q73–99), merged, probed Q72, and wrote up the verdict.

Files: `analysis/m0125-0003-sf05-relsize-20260730/` (README verdict, merged
`sweep-COMPLETE-20260730-155432.txt`, 4 chunks + drivers, `q72-probe/`),
`scripts/tpcds-sf05-regression.sh` (`# planner-flags:` header),
`docs/design/0125-0003-…md` (§I16–I19 + status line), `docs/design/README.md`,
`.ralph/fix_plan.md` (banner, -0003, -0005, -0026), `.ralph/deferral_ledger.md` (3 rows).

## Facts the next loop should NOT re-derive

- **Flag ON `PASS=82 (50 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=13 SKIP=4`
  vs OFF `PASS=79 … TIMEOUT=16`.** Timeout class −4/16. Every one of the 78
  common PASSes agrees on rows AND value checksum. Common-PASS wall time
  2273 s → 1845 s (−18.8 %); 27 of 28 moved queries faster, one slower (Q21 1.74×).
- Rescues: **Q10 40 s, Q69 17 s, Q67 157 s, Q47 277 s** (Q47 also closes
  M0125-0013's runtime half at SF0.5).
- **Q72 is the one cost: 1.13× slower (900 s probe — off `PASS 270 s`, on
  `PASS 305 s`, 100 rows both).** Its `PASS→TIMEOUT` on the board is a BUDGET
  CROSSING, not a hang. Unexplained; ledger row; must be named in -0005's commit.
- **Both §D8 predictions refuted: Q72 was already passing; Q35 — -0003's own
  acceptance query — still TIMEOUTs with the flag on.** The fallback is not what
  Q35 was waiting for; its RC-8 re-scan class needs a task filed off M0125-0026.
- The off arm was **reused, not re-run** — `git diff e29faca9..HEAD -- '*.go'`
  empty + identical D4a `engine-id` empty-diff digest in both reports. Sweeps now
  print `# planner-flags:` so the arm is in the artefact.
- `M0125-0026`'s capture list is now **13** queries (Q5 Q8 Q14 Q30 Q31 Q35 Q54
  Q64 Q65 Q71 **Q72** Q78 Q81), not 16.

## NEXT (banner order, already updated to match)

1. **`M0125-0005`** — the default flip. Both benchmark families are in and both
   recommend it. Its own remaining work: `tpch-spotcheck.sh` re-measured for wall
   clock **and peak RSS** in both states, the written decision, design
   `docs/design/0125-0005-relsize-fallback-default-flip.md`. Commit must name
   Q72's 1.13×; must NOT fold in stage 3 (§I8 shadowing).
2. `M0125-0002` commit 2 (`cloneExprShiftIdx`), `M0125-0003` stage 3.
3. `M0125-0026` (host-independent) when the host is busy; `M0125-0027` still open.

Gates run: `make ralph-state-guard` OK; `bash -n scripts/tpcds-sf05-regression.sh`;
the SF0.5 gate itself (chunk 4 rc=0, 99/99 merged, zero correctness failures);
2 Q72 probe sweeps; pgbench smoke via the commit hook. NOT run: units /
plan-diff / tpch-spotcheck — **no Go code changed this loop** (measurement +
one shell header + docs).

In-flight: none.
