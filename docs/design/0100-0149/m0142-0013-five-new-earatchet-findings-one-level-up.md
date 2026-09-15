# M0142-0013 — recon: the 5 NEW `make ea-ratchet` findings M0142-0012 unmasked (Q23, Q84, Q95)

Status: accepted (recon closed 2026-09-15, no code change)

## Task

`.ralph/fix_plan.md`'s M0142-0013 line, filed by M0142-0012's post-fix `make
ea-ratchet` run: 5 new findings appeared once M0142-0012 fixed 22 of the 112
pinned estimate findings. Resume point: instrument each witness the same
env-gated-trace-then-revert way M0142-0010 did, on a private throwaway SF0.25
clone, and determine whether these are a NEW mechanism or another symptom of
an already-filed gap — **M0142-0005's missing-Memoize-on-NL-probe gap named
as the first thing to rule in/out**, since M0142-0012 changes exactly the
estimator that gap's workaround depends on.

The 5 findings, read directly from `tmp/c20a/ea-findings.json` (the capture
M0142-0012-verify already produced and committed evidence from — no fresh
10-minute capture needed):

| q | relset key | node | est | actual | qerr | pg_est | pg_qerr |
|---|---|---|---|---|---|---|---|
| 23 | `catalog_sales+cte:frequent_ss_items+customer+date_dim` | Hash Semi Join | 76 | 0 | 76.0 | — | — |
| 23 | `cte:frequent_ss_items+customer+date_dim+web_sales` | Hash Semi Join | 38 | 0 | 38.0 | — | — |
| 84 | `customer+customer_address+customer_demographics+household_demographics+income_band+store_returns` | Limit | 100 | 4 | 25.0 | 10 | 2.5 |
| 95 | `cte:ws_wh+customer_address+date_dim+web_returns+web_site+ws1` | Sort | 21392 | 12 | 1782.7 | — | — |
| 95 | `cte:ws_wh+customer_address+date_dim+web_site+ws1` | Hash Semi Join | 29988 | 22 | 1363.1 | — | — |

Three of five carry `pg_est: None` — `scripts/estimate-parity/parity.py`
could not even match a corresponding PG node, meaning PG's plan shape diverges
too far from goopg's for its automatic correspondence to fire. This recon's
own job includes filling that gap by hand for the un-matched three.

## Method

Cloned `bench/tpcds/runtime_goopg/data-sf025` to a private port (5533,
`tmp/m0142-0013/`, per `goopg_shared_bench_cluster_collisions` — removed
before commit) and ran `EXPLAIN (ANALYZE, TIMING OFF)` for each witness query
against both the private goopg clone and the real PG 18.3 oracle
(`:65438`/`tpcds025`). For Q95 specifically, added a temporary
`GOOPG_M0142_0013_TRACE=1`-gated `fmt.Fprintf(os.Stderr, …)` inside
`estimateJoin`'s SEMI/ANTI branch (`internal/optimizer/cardinality.go:880`),
printing the `l` value (`EstimateRows(j.Left)`) and `j.Left`'s own
`PlanCostInfo()` when set. Reverted after the recon — `git diff
internal/optimizer/cardinality.go` is empty; this task lands no production
diff. Starting the private server as a plain foreground Bash call silently
lost the process after ~8 minutes (the systemd scope stopped with no
corresponding kill-log entry, cause not diagnosed — likely the harness
reaping a synchronous long-running Bash call); switching to
`run_in_background: true` for the `scripts/goopg-test-run.sh ... start`
invocation fixed it reliably. Worth carrying forward as a footnote to
`goopg_manual_server_test_workflow`.

## Finding 1 (Q23, both) — CTE `HAVING count(*) > 4` selectivity, verified PG-formula-identical

Both Q23 witnesses bottom out in `frequent_ss_items`'s own
`HashAggregate … Filter: (count(*) > 4)`. At SF0.25 the true answer is **zero
items** ever sell more than 4 units on the exact same calendar day (grouped by
`substr(item_desc,1,30), item_sk, exact date`, an unusually fine-grained
key), confirmed by running the CTE's body standalone (`count(*) = 0`).

goopg's own `HashAggregate` estimates `rows=4561` for this CTE. **Real PG
18.3, on the identical SF0.25 data, estimates `rows=4573` for the same node —
and also measures `actual rows=0`.** The two estimates agree to within
sampling noise (4561 vs 4573) and the qualitative error (order-of-magnitude
over-estimate of a `HAVING count(*) > k` filter) is PG's own well-known
selectivity weak spot, not a goopg defect — `estimate_num_groups` has no
count-distribution model to draw on, so both engines apply the same kind of
crude default.

The downstream `Hash Semi Join` estimates diverge more (goopg 76/38 vs a
hand-traced PG equivalent of ~42, from PG's differently-shaped `Hash Right
Semi Join` plan — see `postgres/…`-independent oracle capture in this task's
working notes) but both are wrong by comparable orders of magnitude against
the same actual=0, and PG's own plan for this query uses a different overall
join order (`Hash Right Semi Join` outer-in vs goopg's `Hash Semi Join`
inner-in) that the parity tool's node-matcher could not align — hence
`pg_est: None` in the capture. **Verdict: not a goopg-specific defect,
same class as M0142-0010 — closed, no code change, no new mechanism.**

## Finding 2 (Q84) — Limit inherits a compounded correlated-join estimate; plan shapes diverge too far to isolate a single defect

The flagged node is the outermost `Limit` (est=100, capped at the `LIMIT 100`
clause because its child `Gather Merge` itself estimates `rows=214`, and
`min(100, 214) = 100`). Tracing down: `Gather Merge(214)` ← `Hash Join
(household_demographics⋈income_band, rows=208)` ← `Nested Loop
(…⋈household_demographics_pkey, rows=594)` ← `Hash Join
(store_returns⋈customer, rows=615)`. Actual total is 4 rows.

Two things rule out M0142-0005: (1) the one Lateral-index probe in this chain
(`household_demographics_pkey`) is a plain **INNER** join, not SEMI/ANTI, and
its own actual/estimate ratio is exact (594 outer → 29 actual matches the
`Index Scan`'s own `rows=1.00 loops=29` — a correct 1:1 passthrough, not an
amplifier); (2) there is no unmemoized NL-index probe anywhere near the
amplifying step — every inflated node here is a plain `Hash Join`/`Hash
Semi Join`.

PG's own equivalent top-of-tree estimate (`Limit rows=10`, from a `Nested
Loop rows=6` beneath a differently-parallelised `Gather Merge`) is closer to
the actual 4 than goopg's 100 — a genuine, real gap (`pg_qerr=2.5` vs
goopg's `qerr=25.0`, clearing the ea-ratchet bar). But PG's plan for this
query does not share goopg's join order past the first two joins (different
`Parallel Hash Join` nesting, different worker counts), so the two engines'
compounding correlated-selectivity errors (three demographic-dimension
equi-joins stacked on the same `customer` row) do not cancel the same way,
and no single node-to-node comparison isolates one defect to fix. **Verdict:
inconclusive at recon depth — real divergence exists, but it is a
join-shape/compounding-selectivity question (independence-assumption
failure across correlated dimension tables), not an M0142-0005 symptom and
not yet attributable to one function. No follow-up filed beyond noting it as
context for any future correlated-multi-join-selectivity task; not urgent
enough to file its own M0142 slice given the modest absolute qerr (25) and
the shape-divergence confound.**

## Finding 3 (Q95, both) — genuine NEW mechanism: `estimateLateralIndexJoin`'s plain-INNER branch skips its own inner probe's residual filter selectivity

Both Q95 findings trace to the SAME node: the `Hash Semi Join
(ws1_1.ws_order_number = ws_order_number)` whose LEFT/outer input is a
Lateral-index-join chain — `Nested Loop` (`Hash Join(ws1⋈web_site, rows≈
29989)` outer, `Memoize`-wrapped `Index Scan using date_dim_pkey` inner) then
`Nested Loop` (`Index Scan using customer_address_pkey … Filter: (ca_state =
'VA')`, ~4% match rate, `actual rows=0.04 loops=551` in the real
`EXPLAIN ANALYZE`). The planner's own accepted plan costs this chain's
**own** node at `rows=210` (the `ca_state='VA'` filter correctly narrows it
during path costing). But the `Hash Semi Join` sitting on top estimates
`rows=29988` — **142x its own outer input's costed row count**, which
violates the structural invariant that a SEMI join's output cannot exceed its
outer input.

**Confirmed by direct instrumentation** (temporary trace in `estimateJoin`'s
SEMI/ANTI branch, `cardinality.go:880`): `l = EstimateRows(j.Left)` evaluates
to exactly **29988** for this `j.Left` (a `*Project{*Join{Lateral:true}}`
chain) — matching the raw, PRE-filter `Hash Join(ws1⋈web_site)` estimate
almost exactly (29989), not the planner's own post-filter 210. Root cause,
read directly in the source: `estimateLateralIndexJoin`
(`cardinality.go:382-396`) has two branches —

```go
func estimateLateralIndexJoin(j *Join) int64 {
	l := EstimateRows(j.Left)
	if j.Type != JoinTypeSemi && j.Type != JoinTypeAnti {
		return l                                    // <-- no residual selectivity applied
	}
	sel := lateralNLIMatchFraction(j)
	sel *= joinResidualSelectivity(j)               // <-- SEMI/ANTI DOES apply it
	...
}
```

For a plain **INNER** Lateral-index join (this chain has two, nested), the
function returns the outer's row count completely unconditionally — it never
consults `joinResidualSelectivity(j)`, which the three-line-away SEMI/ANTI
branch calls correctly. So any filter carried on the inner probe (here,
`ca_state = 'VA'`) is silently dropped whenever `EstimateRows` recomputes
through this node — which is exactly what happens when a SEMI join sitting
above it (or any other consumer that calls `EstimateRows` rather than reading
the already-costed `PlanCost.PlanRows`) asks "how many rows does your left
input produce". The **fused** sibling, `estimateNLIndexJoin`
(`cardinality.go:241-245`), has the **identical** shape — same unconditional
`return l` on its own INNER branch — so this is not a decomposed-vs-fused
divergence; it is a same-function, same-shape defect present in **both**
twins, a `pattern_sibling_paths_must_agree` case where the SAME function's
own two branches (INNER vs SEMI/ANTI), not two sibling functions, disagree
on whether to apply residual selectivity.

Note this is silent almost everywhere in the corpus today because most
consumers of a Lateral-index INNER join read the winning path's own
`PlanCost.PlanRows` (correctly filtered, per the planner's real cost
computation) rather than re-deriving via `EstimateRows`. It only surfaces
when another join (here, a SEMI join) is estimated on TOP of the chain
during DP search, before a winning path/PlanCost exists to consult —
M0142-0011's "Mechanism C" question (does a consumer read PlanCost or
recompute?) applies here too, one level up from where M0142-0011 first found
it.

**Verdict: NEW mechanism, not M0142-0005. Root-caused and reproduced via
direct instrumentation, not inferred from a plan-node label.** Filed as
**M0142-0016** below (fix, not recon — the mechanism is unambiguous; what's
unmeasured is corpus-wide blast radius and whether tightening it moves any
plan shape, so the task itself opens with that measurement per the milestone's
own DP-search-surgery caution, K50).

## Why this is not "reject and move on" without a resume point

Findings 1 and 2 are closed (Finding 1 verified non-actionable against the
real oracle; Finding 2 is real but unattributable at recon depth and too
small to justify its own slice today — noted for a future correlated-join
task, no ledger row filed since nothing concrete is owned yet). Finding 3 is
a genuine, instrumented, reproducible defect with a two-line fix location
identified in both twins — filed as an owned follow-up task, not just a
ledger row, per the group's "recon completes when measurement + note +
follow-up task all exist" rule.

## Floor measurements (mandatory for M0137–M0143 recon tasks)

No production code changed (temporary trace added and reverted in the same
loop; `git diff internal/optimizer/cardinality.go` empty). `go build
./internal/optimizer/...` clean. Floors unchanged from M0142-0012's own
post-fix pin by construction: TPC-H `match=8/22`, TPC-DS `match=2/99`, `make
ea-ratchet` baseline stays the M0142-0009-pinned 112-entry set (this task
neither fixes nor regresses any pinned finding — the 5 findings analyzed here
were already NEW, unpinned entries from M0142-0012's own post-fix capture,
not part of the 112-entry baseline). Throwaway server/clone (port 5533,
`tmp/m0142-0013/`) stopped and removed before commit.

## Follow-up

**M0142-0016** (filed below in `.ralph/fix_plan.md`): fix
`estimateLateralIndexJoin`'s and `estimateNLIndexJoin`'s plain-INNER branches
to apply `joinResidualSelectivity` the same way their own SEMI/ANTI branches
already do. Resume point: `cardinality.go:382-396` (Lateral/decomposed) and
`cardinality.go:241-245` (fused) — both twins need the same fix, per
`pattern_sibling_paths_must_agree`. Per K50, any DP-search cardinality change
can flip candidates elsewhere in the corpus, so the task must measure blast
radius (how many queries' plans this can move, in either direction) before
landing, the same discipline M0142-0012a used for M0142-0012.
