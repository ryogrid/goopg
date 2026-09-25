# R70 STEP-A — decision table: BLOCKED (nominal win, fragile) (2026-09-11)

Method per SCOPE §1 (all measured with real code, nothing inferred):
temp per-offer hash-input logging (`R70HASH`: rows/cols/avgVar per
side + cost; binary `goopg-r70attr`, inode-verified `:5533` clone,
Q9 EXPLAIN; byte-identical plans vs clean-HEAD binary on
Q9/Q6/Q13/Q22 — instrument inert); CURRENT terms from DPPATH; grid
evaluation through real `hashsize.Choose` + `spillPages` (throwaway
unit probes, reverted); memLimit 134217728 (64MB × 2.0 multiplier,
measured, not assumed).

## 1. Current terms (flipped orientation, build 5-way 303093 rows)

Measured inputs (temp `R70HASH`, re-verified post-review): inner
(303093 × 41 cols × 461 varB) → Entry 2453 B, 743MB, NBatch 8; outer
orders (1.5M × 9 × 74) → Entry 530 B. Measured total 543226.39;
spill charge 375608 (startup 90758 + run 284850 @ seqPageCost 1.0);
non-spill base 167618.39 (inputs + width-free linear/cpu/qual terms —
unchanged by any narrowing). Attr-side controls Q6/Q13/Q22 saved
(`ctl-q*-attr.txt`, byte-identical to clean-HEAD captures).

## 2. Projection grid (6-col keep-set floor for Q9's build)

Build must carry at minimum l_orderkey + n_name + 4 amount args = 6
cols (hand-enumerated from the query against the plan; the keep-set
mechanism would additionally have to produce exactly this — asserted
as the floor, not the mechanism):

| rows | avgVar | entry | NBatch | proj total | margin vs 245962.63 |
|---|---|---|---|---|---|
| 303093 | 10–30 | 322–342 | 1 | 167618 | **+78,344** |
| 333402 (+10%) | 10–15 | 322–327 | 1 | 167618 | +78,344 |
| 333402 (+10%) | 20–30 | 332–342 | 2 | ~241–242k¹ | +3.6–4.4k (tie) |
| any | ≥412B (8+ cols) | — | 2 | ~392–420k | LOSES |

¹ Tie rows assume the outer narrows with the build (orders→2 cols;
outer spill 23438 pages → ~240.5k). Build-only narrowing at NBatch 2
is ~387k (loses outright). Either way the bar fails there — the
route does not hinge on which.

## 3. Bar verdict: BLOCKED

Pre-registered bar: nominal win beyond noise AND sustained under
±10% rows. Nominal (+78k at 6 cols / avgVar ≤47 — cliff verified at
avgVar≈48, not 5–10B above av30 as first written) passes; but +10%
rows degrades to NBatch 2 with +4k margin, inside any plausible
estimate-noise band (the band itself was never measured to a number
— conservative fail-closed applies: an unshown bar cannot pass).
Landing a corpus-wide width cut on a cliff-edge flip is what the bar
was written to prevent (K22). Real headroom needs DatumBytes
(`minimize_datum`: 22 B/row → 6.7MB, 20× headroom, cliff-independent)
or projection-pushdown (amount computed below the join → ~3-col
build; no such machinery exists — verified beyond grep by R42's
qual-only scope + the measured un-narrowed build pricing — needs its
own feature round, NOT this scope's cut).

Step-A sign-off (required enforcer): bar FAILS → route = blocked
with numbers. No cut. Dependency statement: Q9's hash-vs-NLI contest
converges when the build footprint gains order-of-magnitude headroom,
i.e. on `minimize_datum` (or a future projection-pushdown round).

Evidence: `/tmp/pp2/r70/` (`R70HASH` lines in `server.log`,
`q9-plan.txt` vs `q9-clean.txt` byte-identical, `ctl/` controls,
`r70hash.txt`); throwaway grid probes reverted (tree clean).
