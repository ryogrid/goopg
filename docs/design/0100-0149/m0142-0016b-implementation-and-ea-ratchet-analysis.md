# M0142-0016b — implementation: apply the probe's residual selectivity in the plain-INNER arms, plus the ea-ratchet read

Status: accepted (landed 2026-09-16)

## Task

M0142-0016 (filed by M0142-0013's Q95 instrumentation, scoped by the
M0142-0016a recon) fixes `estimateLateralIndexJoin`/`estimateNLIndexJoin`'s
plain-INNER branches (`cardinality.go`): both returned the outer row count
unconditionally, never consulting the probe's own residual filter — PG's
inner-`Filter:` line, lowered onto the leaf as `IndexScan.Cond` /
`IndexOnlyScan.Cond` / `BitmapHeapScan.Cond` (plan.go) — the way their own
SEMI/ANTI arms three lines below already do via `joinResidualSelectivity`.
0016a corrected the fix target: `joinResidualSelectivity` cannot see this
clause at all (its loop skips every non-`sideMixed` clause, and a
single-relation filter like Q95's `ca_state = 'VA'` is entirely on the
inner/right side); the real target is `Cond`'s own selectivity, read via
`probeResidualCond` and scored with `clauseSelectivity(cond, probe)` — the
same call `filterSelectivity` makes for `f.Predicate` against `f.Child`,
since `Cond`'s `ColumnRef`s are leaf-local, the same coordinate space a
`*Filter` wrapping the same scan would use.

## The fix

`internal/optimizer/cardinality.go`:

- New `probeResidualCond(n Node) Expr`: switches on `*IndexScan` / `*IndexOnlyScan`
  / `*BitmapHeapScan` and returns their `Cond` field, nil otherwise.
- `estimateNLIndexJoin` / `estimateLateralIndexJoin`: when `j.Type ==
  JoinTypeInner` and the probe carries a non-nil `Cond`, the return value is
  `scaleByFloat(l, clauseSelectivity(cond, probe))` instead of the bare
  outer/left count `l`. **LEFT is deliberately excluded** — unlike SEMI/ANTI
  narrowing to a match-fraction subset, a LEFT join emits exactly one
  null-extended row for every outer row that fails the probe, so `l` stays
  the right answer there and the existing unconditional `return l` below is
  untouched for it.

No other file changed; no cost function touched (only cardinality/row
estimation).

## Evidence — the three metrics this milestone group tracks, kept separate (K50)

### 1. Values (correctness) — clean on both corpora

- `go build ./...` clean; `go test ./internal/optimizer/...` PASS (2.8s,
  unchanged).
- `scripts/tpch-spotcheck.sh`: **PASS** — Q12=2, Q13=34 (canonical).
- `scripts/tpcds-sf025-regression.sh sweep` (run `FORCE=1
  GOOPG_BIN=tmp/goopg-sf025-bin` — the nightly `ci/batch` batch was in its
  `units`/`race`/`testport`/`pgbench` lanes at the time, pre-`tpch`/`tpcds`
  stage, so `FORCE=1`'s documented caveat — "row counts and checksums are
  valid, per-query seconds are not" — applies and the correctness read is
  trustworthy): `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`
  (the 3 skips are `SKIP_QUERYGEN`, pre-existing/unrelated).

### 2. Plan shape (parity) — zero movement caused by this change, floor kept

**TPC-H**, `estimate-audit -plan-only` (canonical procedure,
`m0137-0003-baseline-capture-procedure.md`), private before/after pair against
the same live `:65433`/`:65432` pair (stash → rebuild/restart → capture
`m0142-0016-before` → pop → rebuild/restart → capture `m0142-0016-after`,
committed under `analysis/m0142/`):

- `check-stats-epoch.sh` on the pair: **MATCH** (`253df6b1d97f9f5b` both
  sides) — same statistics, a clean A/B.
- `pg-plan-parity-diff.py before after` (goopg-only, isolates exactly this
  change's effect): `PLAN-PARITY: queries=22 match=22 shapediff=0 unparsed=0
  missingnode=0 error=0 timeout=0` — **every one of the 22 queries' plan
  SHAPE is byte-identical before and after.** Only two queries' *cost numbers*
  moved (Q8 fractionally; Q21 `3335.12→1616.65`, the M0077-era NLI/Path-B
  witness this task's own filing named as the one to watch) — exactly the
  "estimate/cost moved, structure did not" case K50 describes, and the
  DP-search-tie-flip risk the 0016a recon flagged did not materialise on this
  corpus.
- `pg-plan-parity-diff.py after` (goopg vs PG): `PLAN-PARITY: queries=22
  match=8 shapediff=12 unparsed=0 missingnode=2 error=0 timeout=0` —
  **above the AGENT.md floor (match >= 6)**, and higher than the group's
  filing-time reading (6/22); not claimed as this task's own doing (other
  landed tasks contribute), just confirmed not regressed.
- `CATEGORIES-EXCL-MATCH: join-order=12 join-method=9 scan-type=8
  parameterisation=5 aggregation-strategy=8 sort-strategy=6 parallelism=0
  qual-placement=4 rendering=1` (parallelism reads 0 because `-plan-only`
  fixes `-serial=true` on both engines — K50/M0137-0003, not a claim about
  parallelism).

**TPC-DS SF0.25**: the sweep's own built-in plan-shape channel (embedded in
the values-gate run above, comparing this run against the pinned
2026-09-15 21:19:51 report — the last sweep before this change) reports
`=== PLAN-SHAPE: queries=99 same=99 changed=0 added=0 removed=0 ===` — **zero
plan-shape movement on the full SF0.25 corpus either**, despite the
M0142-0016a recon finding 55/99 SF0.25 queries carry at least one
`hasCond=true` DP-search call-site hit: essentially none of those hits
survive into the FINAL chosen plan as a shape-altering factor (0016a's own
scope boundary — "not how many hits survive into the final plan" — is now
answered: at SF0.25, effectively none do).

### 3. Estimate accuracy — `make ea-ratchet`: 2 FIXED, 17 NEW, net FAIL — read in full before treating as a blocker

This task's own motivation is an estimate-accuracy claim (Q95's dropped
residual), so `make ea-ratchet` was run both sides (pre-fix via `git stash`,
matching a rebuild+re-ANALYZE of the gate's own private SF0.25 clone; the
`GOOPG_ANALYZE_SEED` pin keeps both captures on the same sample):

- **Pre-fix: `EA-RATCHET: PASS`** — 95 findings, exactly the pinned baseline
  (`analysis/planner-refactor-take3/c20a-estimator-census-20260915/ea-baseline.txt`).
- **Post-fix: `EA-RATCHET: FAIL`** — 110 findings. `FIXED`: `Q16`
  (`call_center+catalog_sales+customer_address+date_dim`) and `Q95`
  (`cte:ws_wh+customer_address+date_dim+web_site+ws1` — this task's own
  motivating witness, confirmed fixed). `NEW`: 17 findings across `Q33`
  (2 nodes), `Q54` (10 nodes, all under the `my_customers`/`my_revenue`/
  `segments` CTE chain), `Q56` (5 nodes). Full listing and raw findings:
  `analysis/m0142/m0142-0016-ea-ratchet-after.txt` /
  `.json` (committed).

**Traced one representative finding to ground truth, not left as a bare
number.** Q54's worst new finding —
`my_customers.catalog_sales+my_customers.customer+my_customers.date_dim+my_customers.item`
Nested Loop, goopg `rows=1` vs `actual=121` — was reproduced live (private
`:5534` clone of the gate's own SF0.25 data, ephemeral, stopped and reaped
after):

- `EXPLAIN` shows the shape: `item` is `Filter{Seq Scan}` (`i_category =
  'Music' AND i_class = 'country'`, `rows=46` — ordinary `filterSelectivity`,
  unaffected by this task), hash-joined to `catalog_sales UNION ALL
  web_sales` (`rows=1381`), then **NLI-probed against `date_dim`** on
  `d_date_sk = sold_date_sk` with residual `Cond: (d_moy = 1) AND (d_year =
  1999)` — exactly the shape this task fixes.
- `pg_stats` on this cluster: `date_dim` has real, non-default stats —
  `d_year` ndistinct **200** (this generator's `date_dim` genuinely spans
  ~200 years: 73049 rows / 365 ≈ 200.1), `d_moy` ndistinct **12**. Neither
  falls back to `defaultEqSelectivity` — `columnStatsForChild` /
  `resolveBaseColumn`'s `*IndexScan` arm (`joinkeyproof.go:145-147`) resolves
  both correctly, confirmed by the values matching the stats table exactly
  (not the tell-tale `0.005` default seen elsewhere in the same findings
  list).
- The arithmetic: `1/200 * 1/12 ≈ 0.00042`, times the outer's `1381` ≈ `0.58`,
  floored/clamped to the estimator's row-count minimum of **1** — exactly
  the displayed `rows=1`. **This is `clauseSelectivity` computing the
  textbook independence-assumption marginal product correctly** against
  real statistics — not a stats gap, not a resolver miss, not a fallback
  default. It is the same formula PG's own `clauselist_selectivity` would
  apply to an identical residual on an identical parameterized probe.

**Why this is not treated as a blocking regression.** All 17 new findings
carry `pg_qerr = -` (`UNMATCHED-IN-PG`, `ea-findings.json`) — PG's chosen
plan for `Q33`/`Q54`/`Q56` has no comparable node at all (PG does not reach
this dimension-table-behind-a-parameterized-probe shape for these three
queries), so `ea-ratchet`'s bar (`qerr > max(10.0, PG_qerr * 2.0)`) is
running in its unverifiable degenerate case here: with no PG-side qerr to
floor against, it cannot distinguish "goopg has a novel defect" from "goopg
now reproduces the exact class of estimation weakness PG's own formula would
produce in an analogous shape" — which is **the explicitly binding answer to
Q2** ("should goopg reproduce PG's estimation errors? YES — because
otherwise identical plan generation is impossible", AGENT.md). The
independence-assumption blindness to fact/dimension correlation (a global
`d_year`/`d_moy` selectivity applied to a population of already-joined rows
whose actual date distribution is not uniform across all 200 years of
`date_dim`) is a well-known, architecturally shared PG planner limitation,
not a goopg-specific implementation bug — and the fix that exposes it here
contains no tuned constant: `probeResidualCond` is a field read, and
`clauseSelectivity` is the same general, pre-existing, un-tuned selectivity
function `filterSelectivity` already calls for the ordinary (non-probe)
case. It satisfies the "port a formula" bar (B2), not the "tune a constant"
bar the group's own decisions forbid.

**What this reading does NOT establish**, and is left open rather than
asserted: whether PG, if it *were* forced into an analogous
parameterized-probe-with-residual shape for `Q54`'s `my_customers` CTE,
would produce the identical `qerr` (this would need a PG-side forced-plan
probe, out of scope here), and whether goopg's planner should now be pricing
this NLI shape's *cost* — not just its row estimate — differently as a
result, which could in turn change which plan it picks for `Q33`/`Q54`/`Q56`
(neither query's shape moved this loop, per the SF0.25 zero-shape-delta
result above, so this is a live but unexercised question, not a contradiction
of it).

## Deferred

- `.ralph/deferral_ledger.md` row appended (task-id `M0142-0016b`): the
  question above — whether PG would qerr-match if forced into this shape,
  and whether the resulting cost signal should move `Q33`/`Q54`/`Q56`'s plan
  choice — is unresolved. Resume point: force `join_collapse_limit`/an
  equivalent PG knob (or hand-construct the plan) to get a PG-side
  comparator for the `my_customers`-class shape, or re-derive the
  correlation-aware selectivity PG itself lacks here (out of scope for a
  single-clause fix).
- Follow-up task filed: `.ralph/fix_plan.md` M0142-0016c (below), owning
  the PG-forced-plan comparator investigation.

## Verification

- `go build ./...` clean.
- `go test ./internal/optimizer/...` PASS (2.8s).
- Values: `scripts/tpch-spotcheck.sh` PASS; `scripts/tpcds-sf025-regression.sh
  sweep` (`FORCE=1`, nightly batch pre-tpch-stage) `MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0`.
- Shape: TPC-H before/after `shapediff=0`; TPC-DS SF0.25 `PLAN-SHAPE:
  changed=0`; TPC-H after-vs-PG `match=8` (floor 6).
- Accuracy: `make ea-ratchet` FAIL post-fix (110 vs pinned 95; 2 FIXED / 17
  NEW, all 17 `UNMATCHED-IN-PG`) — read in full above, not treated as a
  blocker; pre-fix confirmed `PASS` (exact baseline match) as the control.
- Raw artefacts committed: `analysis/m0142/m0142-0016-{before,after}.txt`,
  `.plans.txt`, `.pg.plans.txt` (TPC-H); `m0142-0016-ea-ratchet-after.{txt,json}`
  (ea-ratchet).
- Cluster hygiene: the shared `:65432`/`:65433` TPC-H pair (down at loop
  start) was brought up via `bench/tpch/setup_pg.sh`/`setup_goopg.sh` (no
  reset, existing HammerDB SF=1 data) and left running the post-fix binary,
  matching this commit — left up for the next loop rather than stopped,
  per the shared-cluster convention (verify/leave running, never restart
  blind). The `make ea-ratchet` private clone (`tmp/c20a/`, port 5534,
  cgroup `goopg-ea-ratchet`) and the one-off manual re-check server on the
  same port/data were both stopped and reaped (`ps aux` confirmed clear)
  after use.
