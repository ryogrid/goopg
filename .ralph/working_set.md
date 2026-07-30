(idle — nothing in flight)

Last loop (#6 of this run, 2026-07-30 16:13–17:0x): **M0125-0005 is DONE — the
`GOOPG_RELSIZE_FALLBACK` default IS FLIPPED to stage 2**, with its owed
spotcheck measurement, written decision, design doc and re-pinned plan baseline.

Files: `internal/planner/relsize.go` (`defaultRelSizeFallbackStage`, inverted
parser arms), `internal/planner/relsize_fallback_test.go` (parse contract),
`scripts/tpch-spotcheck.sh` (planner-flags line + wall clock + cgroup
`memory.peak`), `docs/design/0125-0005-…md` (Execution section appended to the
2026-07-28 stub), `docs/design/README.md`, `.ralph/fix_plan.md` (banner + item),
`.ralph/deferral_ledger.md` (2 new rows + RC-5 resume point),
`analysis/m0125-0005-spotcheck-20260730/`,
`plan_snapshots/m0125-0005-relsize-default-stage2.txt`.

## Facts the next loop should NOT re-derive

- Spotcheck, 2 alternating runs/arm + 1 post-flip: **off 75.0 s → on 30.9 s
  (2.43×)**, Q12 62.38 → 19.61 s, Q13 unmoved, **Q12=2 / Q13=35 in all five**.
  Peak RSS **indistinguishable** (off arm's own spread 1125 MB > any arm gap;
  on arm reproducible to 3 MB).
- **§D5.3's regression prediction is REFUTED** — none of round 4's five
  pre-registered queries regressed. That is why it flipped.
- Carried costs that must survive every future citation: **Q72 1.13× slower
  (270→305 s), crosses the 300 s cap, UNEXPLAINED**; **Q35 still TIMEOUTs** —
  the flip is NOT what Q35 was waiting for, and is NOT "no regressions".
- **Any `analysis/` number predating 2026-07-30 is a different planner
  regime.** The flip moves 22/22 TPC-H plans (16 estimate-only, 6 structural:
  Q7 Q9 Q10 Q11 Q12 Q21). Q12's only structural hunk is
  `Hash Join (INNER)` → `(INNER, build=left)` — the mechanism of its 3.18×.
- Plan baseline re-pinned; `make plan-gate` = **22/22 MATCH rc=0**. `plan-gate`
  picks the NEWEST snapshot by mtime — that is why re-pinning was mandatory.
- SF0.5 gate deliberately NOT re-run: criterion is
  `MISMATCH+CKMISMATCH+ERROR==0`, the stage-2 arm scored 0/0/0 over 99 queries,
  and unset-vs-`=2` differs only by a unit-tested parser branch.
- Parser failure direction INVERTED: unparseable and `on`/`true`/`yes` now land
  on the default (2), not off. `=0` is the explicit opt-out.

## NEXT (banner order, already updated to match)

1. **`M0125-0002` commit 2** (`cloneExprShiftIdx`) — walker conversion, 1 of 8
   commits already in.
2. `M0125-0003` stage 3 (needs its own arms; §I8 shadowing).
3. `M0125-0026` (host-independent — take it when the host is busy).

Gates run: units precommit PASS; `go build ./...` OK; spotcheck ×5 PASS;
`make plan-gate` 22/22 MATCH; `make plan-diff` 22/22 diverged (the flip);
`make ralph-state-guard` OK (auto-repaired a stale completed marker);
pgbench smoke via the commit hook. NOT run: TPC-DS SF0.5 (reasoned, see above).

In-flight: none.
