# R65 SCOPE — Q11 EXPLAIN-rendering round: Sort-key OUTER_VAR expansion + InitPlan value `.col1` (closes Q11 → 6/14/0/2)

Follows HEAD `e2a50de40` (SF0.25 migration, pushed). R64 REPORT §4
sequencing is discharged; this round opens the post-migration
plan-parity work.

## 0. Re-triage (live evidence, round-faithful protocol)

The pre-scope triage first produced a scare — a raw-psql capture pair
(`/tmp/pp2/r65-tpch-{pg,goopg}.plans.txt` via `r2-instrument/capture-tpch.sh`)
scored **1/20/1/0** against R62's 5/15/0/2 — then exonerated it as pure
methodology artefact. Rounds capture with `estimate-audit -plan-only`
(`cmd/estimate-audit/main.go:capture`): single session,
`SET max_parallel_workers_per_gather = 0` on BOTH sides, per-session
`ANALYZE` warmup (goopg stats are per-connection), Q15 as standalone
view-body. Re-captured that way at HEAD (bin `/tmp/pp2/r65/goopg-r65`,
fresh `cp -a` clone `/tmp/pp2/clone-tpch-r65` :5533, PG ref :65432):

- `python3 scripts/pg-plan-parity-diff.py r65.plans.txt bench/tpch/plans-pg`
  → **5/15/0/2**, per-query categories IDENTICAL to R62's `pp62.txt`
  except Q13 (the R64 fix delta: `[join-order,join-method,scan-type,
  parameterisation,rendering]` → `[join-order,aggregation-strategy,
  sort-strategy,rendering]`, NL-Right+Memoize → Hash Right Join).
- Same 5/15/0/2 against the FRESH live PG capture (`r65.pg.plans.txt`)
  instead of the fixtures: the reference has not moved.
- R64's fix holds live (Q13 Hash Right Join, PG's shape).

Queue at R64-close (R64 SCOPE §6): (a) Materialize (insufficient-alone —
R63: Q5 differs underneath in join shape + agg strategy, both
cost-driven; a perfect Materialize cut leaves Q5 SHAPE-DIFF), #6
(large-group stats, cost-model scope, 2× deferred), R61 #4 (CTE-body
tagging, fail-closed-correct why-question), (b) Q4 (cost-model framing,
3× deferred — re-verified this round: goopg Sort→HashAgg over a
56860-row NL-semi vs PG GroupAgg over Sort over 13915 rows; the join-row
gap is semi-join selectivity/stats, stays deferred), R63-#1
(partial-skew, no live case), R63-#2 (soleBaseScan IOS arms,
conservative estimator, closes no query), R61 #5 + R62-#1 (watches).
Q22 examined and rejected: HashAgg+Sort vs GroupAgg-over-Sort plus
anti-join filter placement — real planner shape work, multi-category,
not a close. **Flagship: Q11** — structurally identical trees, 2
categories, both display-side (verbose divergences: `/Q11/Sort
rendering: Sort Key text differs`, `/Q11/Sort/c0/HashAggregate
parameterisation: Filter signature differs`). A renderer round that
CLOSES A QUERY is exactly what R63 ordered before (a) Materialize.

## 1. Mechanism chain (both gaps read off the live pair)

Q11 both sides: `Sort → HashAggregate(Group Key ps_partkey, Filter
sum > initplan) → NL(nation → bitmap supplier) → IndexScan partsupp`,
plus `InitPlan 1 → Aggregate → NL(nation_1 → supplier_1) → partsupp_1`.
Node-for-node identical; the tool's aux-reassign already meets the two
InitPlans (no "present only on" divergence) and the bodies compare
clean. What remains is TEXT:

1. **Sort Key reference-vs-compute.** goopg `Sort Key: sum DESC`; PG
   `Sort Key: (sum((partsupp.ps_supplycost *
   partsupp.ps_availqty))) DESC`. The Sort key is a ColumnRef to the
   child HashAggregate's output `sum`; PG's ruleutils `get_variable`
   expands the OUTER_VAR through the child targetlist and prints the
   underlying aggregate expression (with the S18 force-parens wrap for
   a non-Var referent — `operators_explain.go:706-714` already wraps,
   it just wraps the bare name). Site: `*optimizer.Sort` arm
   (`operators_explain.go:703-729`) — `formatExprQual(k.Expr, …)`
   renders the ColumnRef as `sum` and stops.
2. **Scalar-subquery value reference.** goopg Filter `(sum >
   (InitPlan 1))`; PG `(sum(…) > (InitPlan 1).col1)`. PG plans the
   scalar subquery as PARAM_EXEC and deparses the reference via
   `get_parameter` → `(plan_name).colN`; a scalar subquery returns one
   column, so always `.col1`. Site: `formatExprQual`
   `*optimizer.SubqueryExpr` arm (`operators_explain.go:1425-1428`),
   which renders `"(" + subPlanName(…) + ")"` unconditionally — the
   comment's "upstream decorates scalar subplan references with
   nothing but parentheses" is TRUE for testexpr position (`EXISTS
   (SubPlan 1)`, `= ANY (SubPlan 1)`) and FALSE for value position,
   as the live Q11 pair proves.

The `sum` vs `sum(…)` half of gap 2's Filter signature is gap 1's fix
applied inside the Filter expression (same ColumnRef-through-agg-output
expansion). Both verbose divergences fall to these two renderer arms.

## 2. The cut (renderer-only, two arms, PG-faithful)

- **Arm A — Sort-key (and upper-qual) OUTER_VAR expansion.** When a
  rendered ColumnRef names a child-plan output that the child itself
  computed (aggregate / FuncCall over its own input), render the
  underlying expression, qualified exactly as the child would print it
  (`partsupp.ps_supplycost`, not bare). Lookup is explicit, not
  hand-wavy: resolve the Sort key ColumnRef against the child
  `*optimizer.Aggregate`'s `Aggs` vs `GroupExprs`/`Passthrough` via
  `schema` (`internal/optimizer/plan.go:1288-1294`); only an `Aggs`
  hit expands. Plain pass-through columns (e.g. Q4's `o_orderpriority`
  — goopg prints it bare, PG prints `orders.o_orderpriority`
  qualified; the qualifier gap is N4-forgiven, not this round) keep
  today's rendering. The computed-only guard is load-bearing for the
  existing pin `BareVarKeysUnchanged`
  (`explain_sortgroup_paren_test.go:80-93`, `Sort Key: a` over a scan
  child with a ColumnRef binding — must still render `a`). The S18
  sibling pair (Sort arm :703 + Aggregate group-key arm :925) moves
  together per the M0134-0001 comment — Q11's Group Key
  (`partsupp.ps_partkey`, qualified and identical on both sides, pairs
  today) is not touched by this arm.
- **Arm B — InitPlan value `.col1`.** Non-correlated scalar
  `SubqueryExpr` (today's `InitPlan` kind branch of `subPlanName`)
  in VALUE position renders `(InitPlan N).col1`. Testexpr positions
  (`ExistsExpr`, `InExpr` ANY, array subquery) keep bare parens.
  Arm B keys off the InitPlan branch ONLY — the existing pin
  `TestExplainSubPlanCorrelatedScalar`
  (`explain_subplan_test.go:134-145`, pins bare `(SubPlan 1)`) must
  keep passing unmodified.

Deliberate non-mirrors (not drift): widths (side-column only, never a
verdict); `on nation_1` vs `on nation nation_1` (already forgiven by
N4 alias/suffix canonicalisation — the P0-04 suffix remainder is a
separate queued item, NOT pulled in); Q11 rows 10666 vs 10667
(display rounding off the same 32000 search-exact input — stats, not
rendering); Q4/Q22 (planner shape, §0).

## 3. Falsifiable predictions

| # | Claim | Mechanism (§1–2) | Verdict on miss |
|---|---|---|---|
| P0 | Q11 → MATCH (both verbose divergences gone) | Arms A+B | surviving divergence → STOP, re-audit (third gap) |
| P1 | pp roll-up 6/14/0/2; `rendering` category count drops (Q3 and Q5 Sort Keys shed `rendering`, Q10 partial; the rest keep non-rendering categories — Q13 key 2 needs transitive expansion through the outer agg's Group Key, out of Arm A's immediate-child scope, and Q16's keys are pass-through aliases) | Arm A corpus-wide | rendering count rises anywhere, or Q16 moves at all → STOP |
| P2 | values md5 MATCH pre-round binary on ALL 22 TPC-H queries (renderer-only: zero executor/planner change) | display-only cut | ANY values move → STOP, re-audit (render path shares state with exec) |
| P3 | `tpch-runner -explain` normalised diff = text-only lines (Sort Key / Filter), zero node-kind/child moves | §2 | any structural move → STOP |
| P4 | DS SF0.25 sweep: PASS=96 MISMATCH=0 SKIP=3 (2026-09-11 oracle) | renderer cannot move row counts | any mismatch → STOP |

Re-audit rule (R59–R64): any P-miss triggers DPTRACE A/B against the
pre-round binary, not celebration.

## 4. Sibling audit (why the cut is two arms)

- `ExistsExpr` / `InExpr` / `ArraySubqueryExpr` arms: testexpr bare
  parens are PG-faithful (`explain_subplan_test.go` pins) — untouched.
- Plan-name line (`InitPlan 1` header, `subPlanName` :1306): PG prints
  it bare — untouched. Only the VALUE-reference site changes.
- Group Key arm (:925): untouched. Narrower claim than "no Group
  Key divergence anywhere": the verbose dump DOES contain Group Key
  rendering divergences (Q13, Q7, Q9 — e.g. Q13's `Group Key: c_count`
  vs PG's `Group Key: count(orders.o_orderkey)`); what is true is that
  Q11's Group Key already pairs, and the computed-Group-Key shape is
  deliberately out of Arm A's immediate-child scope (see §6).
- `formatExprPG` scan-qual path (`varprefix=false`): no separate
  change needed — a scalar subquery inside a scan-level qual is still
  an InitPlan whose PARAM_EXEC reference PG deparses as
  `(InitPlan 1).col1`, and since Arm B lives in the shared
  `SubqueryExpr` arm, scan quals get the PG-faithful text
  automatically.
- Correlated `SubPlan` (non-InitPlan kind): value-position shape
  unprobed by the corpus (no live case) — Arm B covers the InitPlan
  branch only; the SubPlan branch keeps bare parens pending a live
  PG pair (ledgered in §6 as a deliberate PG-unfaithful remainder).

## 5. Gates (implementation round, all FOREGROUND)

1. `go test ./internal/executor/ ./internal/optimizer/` green (new
   renderer pins included, no `-count=1`); `go vet` clean.
2. `scripts/tpch-spotcheck.sh` PASS (practice-card must-gate).
3. TPC-H values A/B vs pre-round binary on identical data: 24/24 MATCH
   (digest + `-diff`; renderer-only so P2 is the whole point).
4. `tpch-runner -explain` A/B, normalised: text-only moves (P3).
5. pp: 6/14/0/2 with Q11 MATCH (P0–P1).
6. DS SF0.25 sweep foreground, private lane
   (`GOOPG_BIN=tmp/goopg-sf025-bin scripts/tpcds-sf025-regression.sh
   sweep`): PASS=96 SKIP=3 (P4). Triage the sweep's plans-channel
   drift as signal, not celebration (Sort Key/Filter text moves on
   TPC-DS queries with agg-output sorts / InitPlan refs are expected).
7. `make plan-gate`: structural mode strips costs, NOT Sort Key /
   Filter text, so it WILL report DIFFER on Q11/Q3/Q5/Q10 post-cut —
   discharge by re-pinning (`plan-snapshot-capture LABEL=…`) or record
   an explicit opt-out with rationale. A red plan-gate with no mention
   reads as an un-triaged regression.
8. REPORT.md, then review, then commit (explicit pathspec; round
   process per TODO.md uses `commit -n`) + push.

## 6. Ledgered follow-ups (not this round)

- Unchanged: (a) Materialize (still insufficient-alone for Q5 — but
  this round IS the query-closing round R63 ordered before it, so (a)
  is re-triageable next), #6, R61 #4, (b) Q4, R63-#1, R63-#2, R61 #5,
  R62-#1, P0-04 suffix remainder, NLI staleness comment.
- Road-not-taken record: corpus-wide OUTER_VAR expansion as a general
  deparse rewrite (done narrowly at the Sort/upper-qual sites with
  live PG pairs instead — Q3/Q5 adjudicate the generality); transitive
  expansion through outer agg Group Keys (Q13 key 2) stays out.
- Deliberate PG-unfaithful remainder: correlated-`SubPlan` value
  `.col1` (PG prints `(SubPlan N).col1` in value position too) —
  no live corpus case, deferred to a round with one.

Evidence tmp-only `/tmp/pp2/r65/` (`r65.plans.txt`, `r65.pg.plans.txt`,
`r65.txt`, `pp65-cats.txt`, `estimate-audit` + `goopg-r65` bins);
clone `/tmp/pp2/clone-tpch-r65` (server DOWN, kept for the A/B).
Pre-round binary: build from HEAD before the cut; same clone, same
seed.
