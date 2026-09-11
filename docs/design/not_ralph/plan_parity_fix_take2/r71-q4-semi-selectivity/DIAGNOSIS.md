# R71 DIAGNOSIS — Q4 rows vs election rule: rows dead, election program owns Q4 (2026-09-11)

SCOPE rev 2 (`SCOPE.md`, reviewed APPROVE-WITH-NOTES, notes applied).
No implementation shipped (tree: temp reverted, grep-proof, see §4).

## 1. Crossover probe (SCOPE §1 exit 1)

Temp env-gated row override at `estimateNLIndexJoin`
(`cardinality.go:239-241` — site CONFIRMED: Q4's EXPLAIN rows moved
to each forced value), victim predicate outer⊇{orders} ∧
inner⊇{lineitem} (all firings same victim+value: 14/14/14 hits across
the three runs = estimation passes, idempotent — recorded against
the exactly-one wording per the SCOPE rev-3 one-liner below),
Q4-only runs, probe-off control byte-identical to HEAD
(`q4-base.txt`). SCOPE rev-3 rule applied here: N idempotent firings
with a single victim+value satisfy the predicate (verified by
marker sort -u); >1 DISTINCT victims would have STOPped the round.

| forced rows | NLI rows | grouping election | ordered election | top costs |
|---|---|---|---|---|
| 57066 (HEAD) | 57066 | HashAggregate | Sort (top) | 1283.98 / 1426.77 |
| 30000 | 30000 | HashAggregate | Sort (top) | 675.00 / 750.12 |
| 13490 (PG) | 13490 | HashAggregate | Sort (top) | 303.53 / 337.37 |
| 3439 (PG-parallel) | 3439 | HashAggregate | Sort (top) | 77.38 / 86.10 |

NO election change at any point — rows reprice only. Rows theory
DEAD (third exit), as B1's arithmetic predicted. Binaries:
`goopg-r71probe` (temp) / `goopg-r71base` (clean HEAD); evidence
`/tmp/pp2/r71/` (`q4-base|p13490|p30000|p3439.txt`,
`server-{base,p13490,p30000,p3439}.log` with `R71HIT` markers).

## 2. Oracle read (SCOPE §1 exit 2)

PG's rule for this shape IS `eqjoinsel_semi`, non-MCV path
(`postgres/.../utils/adt/selfuncs.c:2810-2826`; nd2 clamps at
`:2680-2700`; inputs filed in `/tmp/pp2/r71/pg-stats-q4.txt`:
`o_orderkey` nullfrac 0 / n_distinct −1 (unique, no MCV),
`l_orderkey` nullfrac 0 / n_distinct 347537 (no MCV)):

- `nd1` (outer distinct) = 1,500,000 — o_orderkey UNIQUE ⇒ full
  reltuples, NOT scaled by the date restriction (inversion-proof:
  347537/1500000 = 0.2316913; ×58222 = 13489.53 ≈ 13490 rounded;
  had nd1 been 58222, nd1 ≤ nd2 would give 1.0 — not observed).
- `nd2` (inner distinct) = 347537, clamped to inner restricted rows
  (~2M: `l_commitdate<l_receiptdate` estimates 1999686/6001215 ≈ 1/3
  on `:65432`) — clamp does NOT bind (347537 < 2M).
- nd1 > nd2 ⇒ `selec = (nd2/nd1)(1−nullfrac)` = 0.2317 (nullfrac 0
  both sides).

Consequences: (a) the inner qual does NOT drive PG's number — the
nd2 clamp is slack (inner estimate 1999686 in the filed EXPLAIN;
actual 3792312 — both above 347537, so the clamp binds in neither
world); B2's inner-qual suspicion is corrected with mechanism. (b)
goopg HAS the formula (`eqJoinSelectivitySemi`,
`joinselectivity.go:764`, header `:742`); Q4's legacy path never
invokes it (returns outer rows = 1.0 implicit). (c) goopg could NOT
reproduce 0.23 anyway: its filed ndistincts
(`/tmp/pp2/r71/goopg-stats-q4.txt`) are o_orderkey −1 (unique) and
l_orderkey −0.1956 (≈1.17M of 6M — closer to truth 1.5M than PG's
347k!) ⇒ transplant yields ≈0.78 (≈44511 rows), not 0.23. Different
stats ⇒ different selectivity is CORRECT behavior on both sides.

## 3. R47 confrontation (SCOPE §1 exit 3)

For Q4's Group Key line to read `GroupAggregate`, the election (not
the rows) must change: `costAgg` prices sorted/hash CPU identically
and sorted loses by `sortRun(n)` at every n (B1, verified); R47
proved PG itself picks hashed at grouping on pure cost (sort-off
flip) with the firing micro-rule UNIDENTIFIED. The probe (§1) shows
rows can't elect; the oracle (§2) shows the rows wouldn't even match.
The micro-rule program (upper-planner ordering requirements —
K12(B)/K24 line) is the Q4-closing path. A legacy-driver rows
transplant would reprice display + upper-rel sizing toward ≈0.78
(not 0.23), flipping no pp-flagged election — bounded hygiene, not a
close; it needs its own full gates if ever scoped (upper-rel flips
via K59-thresholds/memoize would have to be adjudicated, and the
ndistinct gap means it converges to neither PG's rows nor PG's plan).

## 4. Gates (SCOPE §4)

0. This document (crossover table §1 + oracle cite §2 +
   reconciliation §3). Spotcheck branch: `:65433` peer-held →
   deferral-carries (standing rationale; diagnosis round, nothing
   shipped — no values channel exists to gate).
1. Suites green as executed this round (optimizer + executor ran
   green; vet clean; logs unfiled — stated, not implied); temp
   reverted (`R71DBG` zero hits in `internal/`) + byte-identity
   re-verified post-revert (Q4 EXPLAIN == `q4-base.txt`).
2. Agent review (APPROVE* to close).
3. Commit (explicit pathspec: DIAGNOSIS + TODO — SCOPE committed)
   with `-n` + push. No planner/executor/costing change ships.

## 5. Follow-ups (ordered by this diagnosis)

1. **Election/firing-rule program** (Q4-closing path): what makes a
   sorted aggregate win where pure cost says hashed — K12(B)/K24
   line. Needs its own scope; R47's unidentified micro-rule is the
   chartered unknown.
2. **Legacy semi-rows hygiene** (optional, bounded promise): transplant
   semi selectivity into the legacy NLI driver for display + upper-rel
   sizing (≈0.78, not PG's 0.23 — state both numbers or do not scope
   it). Full values/sweep/pp gates REQUIRED (upper-rel flips possible;
   and the ≈44511 point lies outside the probed interval, so its
   no-flip status would rest on B1 extrapolation, not the probe).
3. **Stats divergence note** (no action): goopg l_orderkey ndistinct
   1.17M vs PG 347k (both off truth 1.5M opposite ways) — estimators
   differ; convergence is not obviously desirable. Ledgered, not owned.
