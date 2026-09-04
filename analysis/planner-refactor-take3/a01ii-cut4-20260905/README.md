# A-01(ii) cut 4 — re-pin + spine re-measure

```
label: A-01ii-cut4 | date: 2026-09-05
binary: tmp/goopg-a01iic3b (cuts 1–3 + substitution propagation)
goopg: TPC-H SF=1 port 65433 tpch@tpch | PG 18.3 reference port 65432
instruments: cmd/estimate-audit --plan-only (clause-6 pairing channel),
  plan-only goopg plans + PG-ref paired run
```

## Re-measurement (take3 04 §11; corrects 07 §2.2 attribution)

07 §2.2 recorded `shape_mismatches = 46` as an UPPER BOUND attributed to
absent dedup ("Q8/Q17/Q18 lost their final-joinrel comparison to rendering
rather than to planning"). Dedup has since landed twice over: `_N`
suffixes (`register`, M0125-0039 era) and now statement-global RTIDs with
sublink-body registration (cuts 1–3).

Measured on the paired plans (`analysis/leftdeep-joins/
a01ii-cut3-paired.plans.txt`, 21 planned queries):

- Queries with two same-printed scan labels (the Ambiguous condition):
  **0**. Every scan node carries a distinct printed name — aliases
  (`nation n1/n2`), `_N` suffixes (`nation_1`, `region_1`,
  `lineitem_1`, `partsupp_1`, `supplier_1`), or unique bare names.
- Poster children resolved: Q11 pairs FULLY (`both` on all 4 levels
  incl. `{nation_1+supplier_1} ⋈ {partsupp_1}`); Q17/Q18 show `_1`
  suffixes on both engines' sides enabling comparison; Q2/Q8 sublink
  bodies registered.
- Clause-6 pairing summary: 22 compared; 26 matched, 33 goopg-only,
  31 PG-only; bushy goopg 7 (Q2,Q5,Q7,Q8,Q9,Q10,Q20) vs PG 3
  (Q7,Q8,Q20); 1 clause-6 candidate (Q8). Shape divergence that remains
  is planning (bushy vs left-deep), not rendering.

Attribution corrected: the "upper bound" qualifier on the 46 is
discharged — no joinrel comparison is lost to duplicate printed names
any more. (The absolute 46 count itself belongs to the parity-with-
actuals channel, re-measured at owner acceptance C-21, not here; what
this cut re-measures is exactly the rendering hazard the 46 was blamed
on, and it is gone.)

## Baseline re-pin

No committed goopg-vs-goopg plan baseline is re-pinned in this cut:
the A-05 non-skippable pin (TODO_ALL) owns baseline policy, and the
cut-3 PP diffs (13/22, all label-only in the PG-faithful direction)
are recorded per-query in the gate logs, not as a new snapshot. The
paired PG-ref plans are committed alongside
(`a01ii-cut3-paired.pg.plans.txt`) for the future A-02/A-03 parity
capture to build on.
