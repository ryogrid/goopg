# 2026-08-05 estimate audit — P5.6-g-iii (the acceptance instrument, not the estimator)

Instrument: `cmd/estimate-audit --label 2026-08-05-p56giii-parity
--from-plans analysis/leftdeep-joins/2026-08-05-p56g.plans.txt --ref-port 65432
--timeout 150s`.

**No estimator code changed this loop.** The goopg side is the *committed*
P5.6-g capture replayed offline (`--from-plans`), so every goopg number here is
bit-identical to `2026-08-05-p56g.txt`; the only new measurement is the PG 18.3
reference, captured live from the TPC-H reference cluster (port 65432, db
`tpch`, `bench/tpch/setup_pg.sh`) with the same queries, the same
`max_parallel_workers_per_gather = 0`, and an ANALYZE of every table first.

Files: `2026-08-05-p56giii-parity.txt` (audit + parity column),
`2026-08-05-p56giii-parity.pg.plans.txt` (the raw PG plans, so any row below can
be re-derived).

**The two clusters carry independent HammerDB loads** (goopg `lineitem` =
5 997 241 rows, PG = 5 998 835), so actuals differ by ~0.03 % and, where a
predicate cuts near a threshold, by a few rows (Q18's final joinrel: 70 vs 84).
That is why the unit of comparison is each engine's own **misestimate factor**
and not its row count: a 0.03 % data difference cannot move a 126× excess.

## 1. Why the absolute tripwire had to stop being the bar

09 §5's tripwire asks "is this estimate good?". The question P5.9 has to answer
is "is this estimate worse than PG's?". On TPC-H the two questions disagree on
**every** joinrel the old instrument flagged:

| query | goopg | PG 18.3 | old instrument | new instrument |
|---|---|---|---|---|
| Q18 final | est 2 998 620 vs actual 70 → **42 837×** | est 452 478 vs actual 84 → **5 387×** | VIOLATION | PG is 5 387× off too — over §5's own 1 000× tripwire |
| Q21 final | est 1 vs actual 4 003 → **4 003×** | est 1 vs actual 4 178 → **4 178×** | VIOLATION | **excess 1.0×** — measured parity, goopg is marginally the more accurate |
| Q19 final | est 1 vs actual 131 → **131×** | est 116 vs actual 112 → **1.0×** | *silent* (131× < 1 000×) | **PARITY-VIOLATION, 126.5× worse than the reference** |

The old bar flagged the one joinrel where goopg matches PG exactly (Q21) and
stayed silent on the one where goopg is two orders of magnitude worse than PG
(Q19). That is the instrument being wrong, not the estimator.

## 2. What landed

- **A per-query bar for Q21 beside Q9's** (`estimateaudit.Q21AntiJoinMax`,
  5 000×), carrying its justification into the rendered artifact rather than
  being a bare number in a map. It is a *bar*, not a mute: PG and goopg both
  measure 4 003–4 178× here, and a genuine regression of the same joinrel still
  trips it (unit-tested). Absolute violations on TPC-H: **2 → 1** (Q18 stays).
- **The 09 §4 ratchet restated as per-joinrel parity** (`internal/estimateaudit/parity.go`).
  A joinrel is identified the way upstream identifies it — by the **set of base
  relations underneath it** (`RelOptInfo.relids`), reconstructed from the
  printed plan — so goopg's `{customer,orders}` and PG's `{customer,orders}` are
  the same estimation problem *even when the two engines reached it by different
  join orders*. A joinrel only one engine built is a **shape divergence**,
  counted separately and classed per §6, because there is nothing to compare.
- Two conditions, both required, before a row counts (`ParityBar`): goopg's
  factor is more than **10×** the reference's (`Slack` — §4 declines a
  match-all bar while cost constants and stats fidelity still differ) **and**
  goopg's own factor exceeds **100×** (`Floor` — a joinrel PG happens to nail
  and goopg gets within 20× says nothing about join order at that size).
- `--from-plans` / `--reference`, so a *new* instrument can be applied to
  *old* committed evidence without re-running a 13-minute power run — which is
  exactly what produced this report.

## 3. The baseline this pins (TPC-H 22, LEGACY planner)

```
RATCHET parity_violations=1 shape_mismatches=67
joinrels compared: 21 matched, 67 shape-divergent, 3 ambiguous
```

**The single parity violation is Q19**, `{lineitem,part}`: goopg estimates 1 row
against an actual 131 while PG estimates 116 against 112. Measured, from the
committed plan: neither scan carries a filter (5 997 241 and 200 000 rows in),
so Q19's three OR'd
`(p_brand = … AND p_container IN … AND l_quantity BETWEEN … AND p_size BETWEEN …)`
groups all ride as the join's residual and the whole predicate is priced at the
join level, where the estimate lands on the 1-row clamp. *Which* step collapses
— the disjunction, the per-group conjunction, or the residual not being priced
at all — is the successor task's first question, not a finding of this loop.
Q19 was dismissed in the P5.6-g write-up as "not a semi-join, so not this
item's" — true, and the parity column now shows it is the **only** estimator
defect the milestone's own TPC-H corpus can prove.

Under the floor but on the watch list — each is >10× the reference and would
count if it grew: Q16 `{part,partsupp,supplier}` 84.9× vs 2.0× (excess 42.1×),
Q20 `{lineitem,part,partsupp}` 32.1× vs 1.1× (excess 30.5×), Q14
`{lineitem,part}` 12.4× vs 1.0× (excess 11.9×).

The 67 shape mismatches are **not** all plan-quality defects; they are three
different things, and P5.9 must not read them as one number:

1. **Genuine join-order divergence** — goopg is still on the LEGACY planner in
   this capture, which is what M0127 exists to replace. Q5, Q7, Q9 each show a
   different spine reaching the same final joinrel (all three finals *match*,
   with excess 1.4–3.7×).
2. **Subquery treatment** — Q2 and Q11's PG plans contain
   `{nation_1,partsupp_1,supplier_1}` joinrels that goopg has no counterpart
   for at all: PG pulled the correlated subquery up into the join, goopg kept
   it as a SubPlan. That is a §6 class-(b) divergence and is not an estimator
   question.
3. **A rendering gap in goopg, not a planning difference** — see §4.

## 4. The discovery: goopg's EXPLAIN cannot name a repeated relation

Upstream deduplicates relation names when it prints a plan
(`select_rtable_names`, ruleutils.c): the subquery's second scan of `lineitem`
prints as **`lineitem_1`**, and Q8's two `nation` RTEs print as `n1`/`n2`.
goopg prints the bare relation name for every RTE that carries no user alias,
so two different range-table entries are **indistinguishable in the text**.

Consequences measured here, all of them instrument artifacts rather than
planner facts:

- Q18's goopg final joinrel keys as `{customer,lineitem,orders}` — the same key
  as the inner join below it (marked `~` in the report, 3 such rows) — while
  PG's keys as `{customer,lineitem,lineitem_1,orders}`. The two never match, so
  the milestone's flagship divergence lands in the shape-mismatch bucket
  instead of the parity column, where its excess would be 42 837 / 5 387 ≈ 8×.
- Q17 and Q8 lose their final-joinrel comparison the same way.

The gate reports the collision (`ParityRow.Ambiguous`, the `~` marker and the
`N ambiguous` line) rather than silently picking one occurrence — but the fix
is in the renderer, and it is filed as a deferral-ledger row dated 2026-08-05.
Until it lands, `shape_mismatches=67` is an **upper bound** on real divergence.

## 5. Bar

UNITS + the audit run that reports the parity column (this file). No planner or
executor code changed, so no SPOT/DS05 re-measurement is implied; the goopg
plans are the committed P5.6-g capture, replayed.
