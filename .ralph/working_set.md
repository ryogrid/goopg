(idle — nothing in flight)

Last loop (#2 of this run, 2026-07-30 12:38–13:55): **M0125-0003 stage 2's TIMED
C-arms are MEASURED — the six-loop-old measurement debt is discharged.**

Task: the banner's quiet-host item. Host verified quiet (no nightly, load ~0.3,
only PG:65438 up). Built §D7's missing harness, then ran the arms.

Files: `scripts/tpch-relsize-arm.sh` (NEW — the per-query-isolated harness),
`scripts/lib/bench-engine-id.sh` (NEW — D4a helpers extracted so TPC-H and
TPC-DS report the same fields), `bench/tpcds/env_tpcds.sh` (now sources it),
`analysis/tpch-relsize-fallback-20260730.md` + `…-20260730/` (report + raw TSVs),
`docs/design/0125-0003-*.md` (§I11–I15), `docs/design/README.md`,
`.ralph/fix_plan.md` (banner + M0125-0003 + M0125-0005), `.ralph/deferral_ledger.md` (3 rows).

## Facts the next loop should NOT re-derive

- **C1 (flag off) → C2 (`GOOPG_RELSIZE_FALLBACK=2`), 21 comparable TPC-H queries:
  693.8 s → 494.0 s (−28.8 %, 1.40×), four wins (Q9 3.29×, Q12 3.43×, Q10 2.58×,
  Q7 1.32×), ZERO regressions, identical row counts, one binary
  (`5b87cf4b53780639`) across all 45 executions.** §D5.3's risk statement is
  refuted for stage 2 **on TPC-H**; stage 3 is no longer shadow-blocked.
- **The pre-registration was wrong both ways:** none of round 4's five regressed;
  Q12 (its 4.4× *loss*) is the 2nd-largest win; Q5, the named expected win, did
  not move — M0077 already fixed it (66.7 s cold here, not 415 s). Do not cite
  round-4 seconds as a live baseline again.
- **W1/W2 are unconstructible, measured** (`scripts/tpch-relsize-arm.sh
  probe-analyze`): `ANALYZE lineitem` in db `tpch` → *relation does not exist*,
  and stats are per-connection while `cmd/tpch-runner` opens one conn per query.
  §D3's W1 = W2 invariant is UNMEASURED. Harness refuses `w1`/`w2` (W_ARM_OK=1 overrides).
- **TPC-H Q21 TIMEOUTs in BOTH arms** (300 s and 600 s caps; 14.2–14.8 GB VmHWM)
  and **does not honour cancellation** (672 s wall vs a 300 s budget, SIGKILL to
  stop) — round-5 §6's defect on the DEFAULT planner. Pre-existing; not the flag.
- Harness gotcha already fixed: a SIGKILLed predecessor made the NEXT start fail
  (that is how c2/Q22 was lost); `start_server` now waits for the port and retries once.
- Absolute seconds carry `MEM_HIGH=13G` < working set (2 GB buffers + 12 GiB heap
  ≈ 14 GB), so 9 queries ran partly in the throttle band. A/B unaffected; cross-report diffs are not.

## NEXT (banner order, now updated to match)

1. **§D8's SF0.5 gate at `GOOPG_RELSIZE_FALLBACK=2`** (~1 h, goopg-only) — the
   TPC-DS timeout class is M0125's acceptance criterion and a TPC-H win is not a
   verdict on it. This is what unblocks `M0125-0005`'s default flip.
2. `M0125-0002` commit 2 (`cloneExprShiftIdx`), `M0125-0003` stage 3, `M0125-0005`.
3. `M0125-0026` when the host is busy; `M0125-0027` (SF=1 harness reports a dead
   server as `OK`) is still open and host-independent.

Gates run: units suite PASS (warm cache; **no Go code changed** this loop);
`bash -n` on all 4 touched shell files; D4a fields re-rendered through
`env_tpcds.sh` after the extraction (engine-id/bin-sha/running all resolve);
4 successful harness runs + a post-SIGKILL cluster sanity read (Q1 ok, 4 rows);
`make ralph-state-guard` OK (auto-repaired the previous loop's completed marker);
pgbench smoke via the commit hook. NOT run: plan-diff, tpch-spotcheck (no
planner/executor code touched — this loop is measurement + shell + docs).

In-flight: none.
