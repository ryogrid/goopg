# Working set — inter-loop baton

Task: **M0145-0011 — COMPLETE and `[x]`.** Scope (c)/E2 measured this loop;
(a)+(b) landed loop 39, (d)'s SF1 confirmation loop 40.

## Banner

Item 3 now reads `… → M0145-0011 [x] → M0145-0012 [ ]`, so **M0145-0012 is the
next selectable task** (retire the goopg-only `rows<=1` CTE fallback guard,
`initialRelRows`, `joinsearch.go:520-526`). Re-read the banner anyway.

## Scope (c)/E2 — the relaxation is a TWO-SITE invariant

`GOOPG_PULLUP_CTE_LEAF=on` (default OFF, flag-provenance table, NOT exempt)
admits `*CTEScan` leaves into `flattenPulledBodyTree`'s splice.

```
pull-up census (TPC-DS SF0.25, knob arm, 99 queries, GOOPG_NLI_CENSUS=1)
  any-body-leaf-(*optimizer.CTEScan)  30 -> 0      (all from Q14/Q23/Q95)
  (pulled)                            42 -> 72     every other class unchanged
  PULLUPCLASSIFY refusal              none, both arms

seam census (GOOPG_PGSHAPED_DP_TRACE=1, same three queries)
  Q14/Q23/Q95  leaf-count x3/x4/x1  ->  pulled-leaf-not-scan x5/x4/x1
```

**The decline RELOCATES; no CTE leaf reaches the DP.** `tryPGShapedJoinSearch`
re-checks the leaf kind and its comment names `flattenPulledBodyTree` as its
guarantor — one invariant, two sites. The three plans that move do so via the
documented `pulled`-suppression side effect (the TPC-H Q4 10x mechanism):
estimated cost falls 1.4x-2.4x while measured runtime is FLAT to 11% worse
(13068->14490, 15024->15545, 3004->3230 ms), values byte-identical, semi/anti
counts preserved 3/3, 4/4, 2/2.

**Resume point if the owner files a follow-on**: the seam's pulled-leaf binding
loop in `internal/optimizer/joinsearchseam.go` needs a `rangeBinding` for a
leaf with no `Table`/`Alias` and an `estimateBaseRelInfo`/
`applyRelSizeFallback` arm for a leaf with no catalog statistics; the adjacent
`flat-leaf-not-scan` check is the third site to audit. Ledgered.

## Next step

**M0145-0012** — retire the `rows<=1` CTE fallback. Its criterion (`derived >=
guard effect`) can now lean on M0145-0011's evidence, but note what that
evidence does and does NOT say: the firewall is measured inert-to-harmful at
SF0.25 AND SF1; the `rows<=1` guard was NOT part of either A/B and is still
untouched. Measure it on its own before removing anything.

## Traps carried forward

- A knob-arm capture in the canonical results dir poisons the next default
  sweep's baseline — always redirect `SF025_RESULTS_DIR`.
- Both new flags are read once at process START — an A/B needs two server runs.
- `GOOPG_PULLUP_CTE_LEAF=on` CHANGES PLANS while achieving nothing; a capture
  that does not name it reads as a relaxation that works. It is registered in
  flag provenance for exactly that reason.
- Gate stamps hash the staged index — re-run a gate if code changed after it.
- `..._arm_test.go` is silently excluded (`arm` is a GOARCH).

## Gates run

units PASS; tpch-spotcheck PASS (Q12=2 Q13=33, flag line confirms
`GOOPG_PULLUP_CTE_LEAF=unset(off)`); TPC-DS SF0.25 default arm PASS=96
MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 with plans 99/99 identical and
verdict-changes=none; TPC-H acceptance arm 24 MATCH vs `tmp/arm-on-20260920.txt`;
pre-commit pgbench smoke via the hook.

## In-flight

none
