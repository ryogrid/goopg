# R57 SCOPE — join-rows N-lead audit: units error, no estimator gap (2026-09-11)

Follows R56 LANDED (`f17f76e81`, REPORT.md): prediction 149461.93
inside [149350,149550]; Q7 winner immobile (gathered 149268.72);
residual ~193 re-owned in REPORT §6 to the "join-rows N-lead
(5874 vs PG 2520 — estimator territory)". This round audits that
ownership claim. Read-only probes (captured plans + DPPATH traces
+ oracle source; no code, no servers).

## 1. Probe D verdicts

### (i) The N-lead is per-loop vs total: NO-MECHANISM for an estimator gap

Both engines divide row estimates by the same parallel divisor —
goopg's `getParallelDivisor` (`cost_funcs.go:175`) is a faithful
port of the oracle's `get_parallel_divisor`
(`postgres/src/backend/optimizer/path/costsize.c:6474`): workers
plus the leader fraction `1.0 − 0.3·workers` when positive.
Partial paths file per-worker rows ("For partial paths, scale
row estimate — one divisor, applied here and undone by
cost_gather's computeGatherRows — not two",
`joinpathsparallel.go:188-191`, merge twin `:415-418`; scan arm
`costParallelSeqscan` returns `clamp_row_est(rows / d)`,
`cost_funcs.go:210`, filed by `considerparallel.go:702`).

The two Q7 captures (`/tmp/pp2/r56/pp-r56.txt` Q7,
`pp-pg-live.txt` Q7, same GUCs incl. mpwg=4) therefore report
different UNITS at the top join, and the live PG plan proves its
own accounting internally:

- PG: Workers Planned 2 → d = 2 + (1.0 − 0.6) = 2.4. Top Hash
  Join rows=2520 per loop → total 2520 × 2.4 = 6048; the
  GroupAggregate ABOVE the Gather Merge shows rows=6047. The
  capture is self-consistent to rounding: 2520 was never a
  total.
- goopg: Workers Planned 4 → d = 4.0 (leader fraction
  1.0 − 1.2 < 0). Top Parallel Hash Join rows=1468 per worker
  (1468.5 unrounded — R56 SCOPE §3 already records 5874/4);
  Gather total 5874 via `computeGatherRows`
  (`cost_funcs.go:857`).

Normalized: **5874 vs 6048, 3% apart**. The "2.33× N-lead"
(5874/2520) compared a total against a per-loop figure. There
is no estimator gap at the top join.

### (ii) Level-by-level chain agreement (both engines textbook)

Every Q7 join level, goopg total vs PG total (goopg join
figures per-worker × 4; PG figures per-loop × 2.4) — with one
display asymmetry stated up front, since this scope's entire
thesis is units discipline: goopg's EXPLAIN mixes units (its
Parallel Seq Scan shows the TOTAL 1837006 while its parallel
joins show per-worker; the trace partial at `:3078` is the
divided 459252), whereas PG shows per-loop uniformly below
the boundary (its Parallel Seq Scan on customer shows 62500,
and 62500 × 2.4 = 150000 exact — an independent divisor
confirmation in its own right):

| level | goopg | PG | Δ |
|---|---|---|---|
| lineitem shipdate filter | 1837006 total (30.6% of 6M) | 0.303 implied (145294 / (120000 × 4.0 lineitems/order)) | ~1% |
| customer ⋈ nation | 11990 (≈6000 × 2 nations, −10 rounding) | 12000 (5000 × 2.4) | 0.1% |
| orders ⋈ customer-side | 119904 | 120000 (50000 × 2.4) | 0.1% |
| lineitem ⋈ orders-side | 146844 (36711 × 4) | 145294 (60539 × 2.4) | 1% |
| top ⋈ supplier | 5874 | 6048 (2520 × 2.4) | 3% |

Mechanisms, all textbook defaults on both sides (no stats
heroics needed, none claimed):

- Partial accounting verified in-trace, not assumed:
  `pp-start-r56.log:3078` files the lineitem partial scan at
  459252 = 1837006/4; `:3130` files the supplier⋈lineitem
  candidate partial hash join (`relids={0,1}`, outer={1}) at
  459252 (output = outer: 459252 × 10000 × (1/10000) ✓, the
  s_suppkey default); `:3126` files the serial JOIN twin at
  1837006 (direction flipped, outer={0} — the serial SCAN
  twin is `:3073`, prebuilt `relids={1}`). One divisor,
  applied once. The winner pair kills it on the actual chain
  too: `:3716` files the serial top join at 5874 vs `:3717`
  the partial at 1468 (total=355248.75, byte-matching
  `pp-r56.txt:154`) — and the region is littered with
  single-division partials (e.g. `:3154`, dozens at
  rows=459252). The double-division theory is dead.
- lineitem ⋈ orders-side: 459252 × 119904 × (1/1500000) =
  36710.77 → 36711 ✓ (PK-FK; the truncated 6.667e-7 would
  falsely give 36713, so the exact fraction is load-bearing).
  orders ⋈ customer: 1500000 × 11990 × (1/150000) ≈ 119904
  ✓ (PK-FK 1/150K; exact fraction gives 119900 — off by 4
  on rounded display inputs, unrounded trace inputs at
  `pp-start-r56.log:3408` file the serial 119904).
  customer ⋈ nation: uniform 6000/nation both engines.
- Top join: goopg 146844 × 10000 × 4.0e-6 = 5873.76 → 5874
  ✓, i.e. l_suppkey 1e-4 × s_nationkey 1/25 (the OR was
  already applied below: goopg's n1×n2 NL shows rows=2 of
  625, sel 2/625 = 0.0032 ✓). PG: 2520/(60539 × 800) implies
  sel ≈ 5.2032e-5 for 6048 total; the 1e-4 × 0.5
  factorization (l_suppkey default × OR Join Filter 0.5, 2 of
  4 nation combos ✓, over the pre-joined 800/loop
  supplier⋈n1 input — per-loop broadcast below the Gather
  Merge, so the 2.4 applies single-sided — 10000 × 2 × 1/25
  ✓) predicts 2422/loop vs the actual 2520, a 4.1% uplift
  INSIDE PG's own number. Stated, not buried: it is stats
  (MCV), not mechanism, and inside the §3 bar — but the
  equation does not balance to exactness, and neither does
  goopg's ±4 on rounded inputs. Same shape, same defaults,
  dust disclosed on both sides.
- R36's defect class (unreliable-selectivity baserels return
  baseRows — STATUS.md, implementation reverted, NOT landed)
  is NOT implicated on this chain: every qual here took the
  reliable path (the numbers above prove it). That ledger
  item stands, untouched.

### (iii) What R56 got right, and the one label that changes

R56's prediction math is UNAFFECTED: the leader really sorts
5874 rows and each worker really sorts 1468 — pricing-level N,
which is what the tournament prices. The margin equation, the
landing, the 9 DS moves, and the Q78 fix all stand.

The single correction: the residual owner. "Join-rows N-lead —
estimator territory" was a units error. The sort-input-N
difference decomposes into (a) STRUCTURE — Sort-over-Gather
vs Sort-under-GatherMerge, fixed by R56 itself — and
(b) WORKER COUNT: PG sizes the query to 2 workers
(`compute_parallel_worker`; PG's mpwg default is 2 and it
chose 2 with mpwg=4 allowed), goopg plans 4. Worker-count
selection is a separate mechanism from row selectivity, with
its own probe surface (how many workers to request) — it is
NOT this round.

## 2. The authorised cut: NONE — close as artifact, re-own residual

No planner change follows from probe D; there is no gap to
fix. The residual ~193 (R56 arm 149461.93 vs gathered
149268.72, while PG ranks the sorted-GroupAgg shape first)
re-owns to two already-ledgered items: worker-count selection
(goopg 4 vs PG 2 — next audit candidate) and the Hash-vs-
GroupAgg strategy preference at ~6K rows / 2 groups
(AGG_MIXED). Either in-bar outcome of those audits is
scope-allowed; neither is authorised here.

Out of scope (hard, ledgered): worker-count sizing audit
(R58 candidate — needs its own scope round first); §3
tie-break calibration; F3 procost; AGG_MIXED proper; Q8's
cost-margin gap (a ~25k COST margin, winner-immobile —
verified NOT the same artifact class: PG Q8's 1002/loop ×
2.4 = 2405 vs goopg's 592 × 4 = 2368 agree at 1.5%).

## 3. Falsifiability

The §1.ii table recomputes from the two cited captures plus
the two divisor functions — any level disagreeing >10% after
normalization FAILS this scope (estimator gap real, re-open
with a number). A landing of "no cut" is the scope-allowed
outcome ONLY with the table green; it is green above
(max Δ 3%).

## 4. Must-holds

- No code, no plans, no estimates change in this round —
  TPC-H digests, DS sweep, and parity-diff are vacuously
  unmoved (nothing to re-run; mechanism stated, not assumed —
  same dispensation as a pure-exoneration probe).
- TODO.md closes the N-lead item as artifact with the
  normalized numbers (5874 vs 6048), and names the worker-count
  audit as the next scope round.

## 5. Status

R57 = this audit (probe D, §1) + review in the next task.
Nothing here authorises a constant, a threshold tweak, a
worker-count rule, or §3 calibration.
