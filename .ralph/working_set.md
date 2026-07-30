Task: M0125-0031 goal (a) — the WARM TPC-DS SF0.5 gate. DONE and committed this
loop (#12, 2026-07-30). The umbrella item M0125-0031 stays OPEN (fixes owed).

Files:
- `analysis/m0125-0031-warm-sf05-20260730/` — NEW: README report, 5 chunk
  reports, merged `sweep-COMPLETE-20260730-220423.txt` (99/99), `run-chunks.sh`
- `docs/design/0125-0028-warm-stats-programme.md` — §-0031b execution record
- `docs/design/README.md` — index row status + findings
- `.ralph/{fix_plan,deferral_ledger}.md` — -0031 body, banner NEXT SELECTION,
  numbering note, new `M0125-0033`; two ledger rows

## Result (one binary `fdd0c6e199182fbb`, quiet host, 22:04→23:58)

S-cold @relsize=2: PASS=82 **TIMEOUT=13** → WARM @relsize=2: PASS=83
**TIMEOUT=12**. Target was **0**. Zero MISMATCH/CKMISMATCH/ERROR in both arms.

Three findings later loops must honour:
1. **ZERO members were size-starved.** The 12 hard members (Q5 Q8 Q14 Q30 Q31
   Q35 Q54 Q64 Q65 Q71 Q78 Q81) are identical to the baseline's and have now
   failed under all three cardinality regimes → **cardinality work is exhausted
   as a route to goal (a)**; only plan-shape work remains. Q72's 307→308 s
   "rescue" is cap-straddling jitter, not a rescue.
2. **Warm statistics change no answers** — all 82 common-PASS queries agree on
   rows AND value checksum (50 ck-verified).
3. **Q18 regressed 117 s → 251 s (2.1×) under warm stats** (156→117→251 across
   the three regimes); answer unchanged → cost-model fidelity gap, not a stats
   gap. Filed `M0125-0033`. Removing Q18 flips the aggregate from "warm 2.7 %
   slower" to "warm 3.2 % faster".

## NEXT (banner order)

**`M0125-0026`** — its plan capture/classification is now the ONLY path to goal
(a). It is host-independent. Fold into its capture set: the free **warm** arm,
**Q18** (-0033) and TPC-H **Q21** (-0032), so one taxonomy covers both
benchmarks; per-class fix tasks then file from **`M0125-0034`** onward.

Gates run: warm SF0.5 gate 99/99 (5 chunks, one binary, one engine-id; zero
correctness failures); `make ralph-state-guard` OK (auto-repaired a stale
progress marker); pgbench smoke via the commit hook. No Go code changed this
loop (analysis/docs only), so no unit gate.

In-flight: none.
