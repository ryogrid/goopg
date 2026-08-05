# 09 — Verification and Acceptance

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| inherits | the M0126 acceptance discipline (symmetric timeouts, per-class attribution, "a documented no-go is a successful completion; an unmeasured outcome is the only failure") and the standing repo gates (units, pgbench-smoke hook, spotcheck, plan-gate, SF0.5, race-gate for concurrency-adjacent stages) |

## 1. Correctness floor (every stage, non-negotiable)

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` green; the
  pre-commit pgbench smoke on every commit (never `--no-verify`).
- `scripts/tpch-spotcheck.sh` (Q12/Q13 canonical row counts, fresh capped
  server) on every planner/executor/codec commit.
- TPC-DS SF0.5 gate (`scripts/tpcds-sf05-regression.sh sweep`): **zero**
  row-count deltas and **zero** checksum deltas vs the git-tracked oracle.
  This is the gate that caught fusion (Q14 100 vs 200) — it is the primary
  correctness instrument for every executor stage, especially E1 (the seam
  rewrite) and S3 (spill), and runs per stage, not just at the end.
- Full regress-port suite after E1, E4, S3, S4 (codec/format-adjacent
  changes; the M0106 six-silent-regressions precedent).
- Race gate (`make race-gate`) for stages touching shared state (E3's
  build-path changes interact with `parallel_hash_build.go`; S3's temp-file
  registry is per-query but Close paths run under cancellation).
- Sibling-path audits, explicitly listed per stage in code review: E4
  (planner keys ↔ executor key encode), §2.1 of
  [06](06-hash-spill-and-memory.md) (planner nbatch ↔ executor nbatch),
  E5 (compiled ↔ interpreted evaluators).

## 2. Stage gates (advancement criteria)

Recorded per stage in [08](08-migration-and-removal.md) §2's table; the
binding numeric ones:

| gate | bar | evidence file convention |
|---|---|---|
| S1 exit | Q3, Q10, Q18, Q7 each ≤ 1.2× their R0 times (8.46 / 6.04 / 27.58 / 25.13 s; R0 = integer+MHJ pinned baseline, total 493.31 s); no other query > 1.2× vs pre-S1 HEAD | `analysis/leftdeep-joins/<date>-s1-ab.txt` |
| S3 exit | Q21 completes at SF1 under the standard cgroup cap with `work_mem` at default; a forced-spill run (`work_mem` lowered until nbatch ≥ 4 on Q3) returns byte-identical results to the no-spill run | `…-s3-spill.txt` |
| S5 exit | the full acceptance bar, §3 | `…-s5-acceptance.txt` |

## 3. The S5 acceptance bar (successor to M0126-0012's)

TPC-H SF1, fresh capped server per arm, symmetric 600 s timeouts, server age
held constant across arms (sweep-tail discipline):

1. **22/22 complete** — zero hang / OOM / timeout / row-count mismatch. **As
   amended by §3.1 and made executable by §3.3: plus VALUE-level equality
   against the flag-OFF arm, evidenced by `tpch-runner -diff` reporting
   `VERDICT: PASS`. Row counts alone do not discharge this clause.**
2. **Total wall time ≤ 1.2×** the better of pinned R0 (493.31 s) and a
   contemporaneous integer-arm run at the same HEAD.
3. **No single query > 2× its R0 time** — Q9 explicitly: ≤ 170.9 s
   (2 × R0's 85.46 s; the integer default arm's 58.83 s from
   `stage3-order-ab.txt` is the aspirational target beyond the bar).
4. TPC-DS SF0.5: zero deltas (as §1).
5. **No `MultiHashJoin` in any emitted plan; fusion never triggers**
   (assert via EXPLAIN sweep over both suites).
6. **Bushy-plan capability (PG-identical search):** on every searched query
   where PG 18.3's EXPLAIN shows a bushy join spine (composite ⋈ composite),
   goopg must be able to produce the same bushy tree shape — the same
   composite⋈composite pairing over the same relset partition — verified
   through the §4 parity gate's spine diff. Alternative shapes chosen on
   cost-constant or stats-fidelity grounds stay admitted under the ratchet;
   a bushy shape PG can produce that the goopg search *cannot express* is a
   hard failure (the [02](02-plan-shape-contract.md) contract is
   PG-identical shape, not a trade).

A documented no-go with attribution (§6) is an acceptable S5 outcome — the
flag then stays OFF and the bundle's planner half returns to design. The
executor stages S0–S4 stand on their own gates regardless.

### 3.1 The run, and why clause 1 could not have caught it (P5.9, 2026-08-05)

**Run 1 of this bar is a documented NO-GO.** `GOOPG_PGSHAPED_DP` stays OFF.
Evidence: `analysis/leftdeep-joins/2026-08-05-p59-s5-acceptance.txt`
(HEAD `1bb24984`, TPC-H SF1, one binary, two arms differing only in the flag).
Clause 1 fails on four counts — Q2 returns 0 rows against 455, Q7/Q8/Q9 raise
`42883`, Q17 hangs past 3300 s where flag-OFF takes 20.93 s — and clause 3 on
Q5 (4.15×) and Q10 (3.83×). Clause 5 is the one clause that passed: zero
`MultiHashJoin`, zero fusion, in both arms across the EXPLAIN sweep. The
flag-OFF arm completed 22/22 in 380.1 s, inside clause 2's allowance, so the
bar is not failing for want of a contemporaneous baseline.

All of clause 1's failures are **one defect**, attribution class (c): the search
boundary publishes a coordinate map that is a **rotation** of the correct
permutation. The discriminator across the sweep is `boundaryMapIsIdentity`: a
winner that is already left-deep in binding order takes the early return and is
correct; a winner that reorders the leaves gets the rotation.

*(Run 1's write-up attributed the rotation to the `outputLayout` that
`createPlanNode` returns for the winning join arm. That attribution is
**refuted** — see §3.2. The layout and the map built from it are correct; the
map is rewritten afterwards.)*

Two things this bar has to learn from it, both binding on the re-run:

- **A rotation satisfies `boundaryMap`'s tripwire.** §10 of
  [03](03-join-search-pg-dp.md) installed that check against the M0097-0058
  class, and it tests exactly three things — HOLE, OUT OF RANGE, DUPLICATE —
  each of which is a way of *not* being a permutation of `[0,width)`. A
  rotation is a permutation, so the check passes on the one instance of the
  class the search actually produced. A permutation test is not a correctness
  test for a map whose contract is *which* permutation. §3.2 adds the reason
  that observation, though true, is not where the fix goes.
- **Clause 1's row-count comparison cannot see this defect.** The reproducer
  returns the right number of rows with every column value shifted one position
  from its name. Five queries in the ON arm "matched" on count and were not
  compared on value. Clause 1 is therefore amended: **22/22 complete plus
  VALUE-level equality against the flag-OFF arm**, not row counts. The three
  `42883` errors are not three bugs — `extract(year from …)` is the only TPC-H
  construct that type-checks its argument at run time, so Q7/Q8/Q9 are simply
  the only three places the silent corruption becomes loud. A bar that relies
  on a query being loud is measuring the query, not the engine.

Clauses 4 and 6, the §4 parity gate and the §5 estimate audit all score plan
QUALITY, and were deliberately not run: they compare plans from a build whose
plans compute the wrong answer. They are the first instruments to re-run once
the rotation is fixed, and they remain part of the bar P5.9 must clear.

### 3.2 The rotation, attributed (P5.9-c, 2026-08-05)

The producer was innocent. Reproduced in-process against a two-relation
catalog — `select * from customer, orders where o_custkey = c_custkey and
o_orderkey = 1`, which puts the cheap side (`orders`, restricted to one row)
OUTER while the bindings are `customer ++ orders`, so the boundary must emit
its Project — the tree that leaves `createPlanAtSearchRoot` is **correct**:
the join publishes `orders ++ customer`, the layout says so, `boundaryMap`
inverts it, and `projectToBindingOrder` emits `[4 5 6 0 1 2 3]` with each
target naming the column it addresses.

The rotation is applied **after** the boundary, by `remapTopProjection`
(bushy.go). That function finds the join tree to derive its posMap from by
walking DOWN past `*Project` / `*Sort` / `*Limit` / `*LockRows` wrappers — and
the search boundary IS a `*Project`. So the descent stepped over the search
root, handed `buildBindingsPosMap` a node *inside* the searched subtree, and
`collect`'s searched-subtree guard (P5.5-f-ii-a) never fired, because it was
never asked about the root. The map that came back was the search's own
binding→plan-position permutation, and it was then applied to every wrapper
above — including the boundary Project's own target list, which is not a
reference into the map but the map itself. Two permutations composed:
`[4 5 6 0 1 2 3]` became `[1 2 3 4 5 6 0]`, i.e. every column's value one
relation-block from its name. Exactly the reported symptom.

This is the fifth member of 08 §3's skip list, missed at P5.9-b because the
four that were added (`pushPredicatesIntoCrossJoins`, `rewriteJoinsToNLI`,
`rewriteMultiWayChain`, `rewriteScanInputsWithSingleTablePredicates`) all
REWRITE a join tree, while this one only renumbers wrappers — and the
`collect`-side guard made it look already covered. It also latently covers a
second shape: an elided boundary whose root is a `*Sort` was stepped over the
same way.

**What the bar takes from this, and it is not "strengthen `boundaryMap`".**
Run 1's write-up proposed promoting `boundaryMap` from a permutation test to a
per-leaf identity test against each leaf's recorded `baseOffset`/width. That
check would have been silent here: it runs at the producer, and the producer
was right. The invariant that catches a *post hoc* permutation is a
consumer-side one, and `projectToBindingOrder` already makes it hold by
construction —

> target `i` is a bare `ColumnRef` naming the very column it addresses
> (`child.Output()[target[i].Index]`), and the node's own `schema[i]` is that
> same column.

— because a permutation moves the indices and leaves the names behind.
`assertSearchedBoundariesIntact` (createplanroot.go) checks it over the
FINISHED tree at the tail of `Plan()`, after every rewriter, gated on
`GOOPG_PGSHAPED_DP` so the default arm pays one boolean. Verified
non-vacuous both ways: it fires on the unfixed `remapTopProjection`, and
`TestAssertBoundaryProjectionIntactCatchesARotation` rotates a real boundary
node by one and requires the panic.

Regression cover: `internal/planner/joinsearchboundary_test.go`. It asserts on
the column each `select *` target RESOLVES TO rather than on indices (an index
expectation would encode the search's chosen order and then fail for cost-model
changes), and a companion test asserts the fixture still produces a
non-binding-order winner — otherwise the boundary would elide its Project and
the regression test would be green for the wrong reason.

Still open before the bar can be re-run: P5.9-d (the harness compares row
counts, not values) and P5.9-e (Q17, to be re-measured on top of this fix).

### 3.3 Clause 1's instrument, built and calibrated (P5.9-d, 2026-08-05)

§3.1 amended clause 1 to demand VALUE-level equality and left the harness
unable to supply it. `cmd/tpch-runner` now computes three digests per result
set and `-diff` compares two arms on them. Evidence:
`analysis/leftdeep-joins/2026-08-05-p59d-digest-selfdiff.txt`.

| digest | what it is | what it answers |
|---|---|---|
| `colsig` | FNV-1a/64 over the column NAMES in order | did the output header move? |
| `ordered` | FNV-1a/64 chained over the rows in scan order | same tuples, same sequence? |
| `unordered` | the wrapping SUM of the per-row hashes | same MULTISET of tuples? |

Three choices in there are load-bearing, and each is pinned by a test in
`cmd/tpch-runner/digest_test.go`:

- **Sum, not XOR, for the order-independent digest.** XOR cancels an identical
  pair, so a query that emitted a row twice would digest like a query that
  emitted it zero times. Sum is commutative *and* duplicate-sensitive, which is
  what "multiset" requires.
- **Length-prefixed field encoding, not delimiters.** A TPC-H text column may
  contain any byte, so a delimiter is forgeable: `("a","b")` and `("ab","")`
  would collide. NULL gets its own marker byte so it cannot hash as `''`.
- **`rows=N` stays the LAST token on the line.** `scripts/tpch-spotcheck.sh`,
  `ci/batch/stages/stage-tpch.sh` and `scripts/tpch-relsize-arm.sh` all extract
  the row count with an end-of-line-anchored regex. Appending digests after the
  count would have made all three silently extract the empty string the first
  time anyone ran a gate with `-digest` — a new instrument that disarms three
  existing ones. The digests go before the count instead, so `-digest` composes.

The verdicts are deliberately unequal in strength. `VALUE-DIFF` is decisive.
`ORDER-DIFF` (multisets agree, scan order does not) is a **question**: for a
query whose `ORDER BY` is a total order it is a defect, and for one with ties —
Q3, Q10, Q18 — two correct arms may legitimately differ. The differ has no
model of which query is which, so it reports the distinction rather than
absolving it. `NO-DIGEST` **fails**: it is how a run made without `-digest` is
stopped from reading as "everything matched", which is precisely how run 1's
five silently-corrupt queries passed. `BOTH-ERROR` fails too — a query neither
arm answered was not compared.

**Calibration — the flag-OFF arm against itself: 24/24 MATCH.** Two arms,
identical by construction, differing only in the server process and the wall
clock. All four large tie-prone results (Q3 11521 rows, Q10 20501, Q16 18213,
Q15a 10000) matched on the *ordered* digest too, so at a fixed plan scan order
is reproducible and a clean run yields no spurious `ORDER-DIFF`. Repeated across
four server processes and two independently built engine images. Cost: 389 s vs
run 1's digest-less 380.1 s, ~2 % over ~61k scanned rows — inside arm-to-arm
noise. `-digest` still defaults OFF so an R0-comparable timing needs no
argument.

**The bar itself takes two amendments from this.**

1. Clause 1 is now *executable*: the re-run must produce `VERDICT: PASS` from
   `tpch-runner -diff <off-arm> <on-arm>`, with every `ORDER-DIFF` — if any —
   individually adjudicated against that query's `ORDER BY` in the write-up.
   A row-count table is no longer sufficient evidence for clause 1.
2. Both arms of the re-run must be driven by **`scripts/tpch-acceptance-arm.sh`**,
   promoted into the repo this task. Run 1 was driven by an untracked `tmp/`
   script, so the protocol §3.1 documents could not be re-executed from a clean
   checkout — and §3.1 ends by requiring exactly that re-execution.

What this does NOT establish, and the re-run must not read into it: both arms
are goopg. The diff certifies that two goopg arms AGREE, not that either is
right. A value wrong in both arms is invisible to it; only the PG oracle can
see that, and wiring one in is a ledger row (2026-08-05, M0127-P5.9-d), not
part of this instrument.

## 4. The PG plan-shape parity gate (new instrument)

Once the P-PG shape contract holds ([02](02-plan-shape-contract.md) §1),
goopg's join spines are structurally comparable to PG's for the first time.
Add `scripts/pg-plan-shape-diff.sh --strict` (the existing
script is report-only): normalise both EXPLAIN outputs to a join-spine
skeleton — node type, join type, build/probe side (PG: which child is under
`Hash`; goopg: `BuildLeft`), base-rel leaf names — and diff.

- Scope: TPC-H 22 + TPC-DS 99 against PG 18.3 with matched stats
  (both sides ANALYZEd; goopg per-session ANALYZE caveat applies).
- Bar at S5: **report-mode with a pinned mismatch budget** — the count of
  mismatching spines is recorded in the evidence file and must not grow
  across subsequent commits (ratchet). A hard match-all bar is wrong while
  cost constants and stats fidelity still differ; the ratchet makes drift
  visible without blocking on estimator parity.
- **There is no `expected-bushy` category.** goopg implements PG's full
  three-phase search, bushy phase included
  ([03](03-join-search-pg-dp.md) §4.3), so a bushy spine PG chose and goopg
  cannot produce is a genuine divergence, not an accepted trade — it is
  classed per §6 (usually (b) plan shape) and fixed. Spine mismatches driven
  by cost-constant or stats fidelity stay under the ratchet as usual, and
  are re-reviewed at each ratchet update.

### 4.1 The ratchet is per-joinrel PARITY, not an absolute factor (P5.6-g-iii, landed 2026-08-05)

§5's absolute tripwire answers "is this estimate good?". The question this
milestone has to answer is "**is this estimate worse than PG's?**". On TPC-H
the two questions disagree on every joinrel the absolute bar flags — measured,
`analysis/leftdeep-joins/2026-08-05-p56giii-parity-README.md` §1:

| joinrel | goopg | PG 18.3 | absolute bar | parity bar |
|---|---|---|---|---|
| Q18 final | 42 837× over | **5 387× over** | violation | PG trips the 10³ tripwire too |
| Q21 final | 4 003× under | **4 178× under** | violation | excess **1.0×** — parity |
| Q19 final | 131× under | 1.0× | *silent* (<10³) | **violation, 126.5× worse** |

The absolute bar flagged the one joinrel where goopg matches PG exactly and
stayed silent on the one where goopg is two orders of magnitude worse. So the
bar P5.9 certifies is:

- **Unit of comparison: the joinrel, identified by its base-relation SET**
  (upstream's `RelOptInfo.relids`), reconstructed from the printed plan. Two
  engines that reach `{customer,orders}` by different join orders still built
  the same joinrel, and its ACTUAL row count is a property of the query and the
  data, not of the plan — so the misestimate factors are directly comparable.
- **Two conditions, both required** (`estimateaudit.ParityBar`): goopg's factor
  exceeds the reference's by more than `Slack` (default 10×, because this §
  already declines a match-all bar while cost constants and stats fidelity
  differ) **and** goopg's own factor exceeds `Floor` (default 100×, so a
  joinrel PG nails and goopg gets within 20× does not enter the ratchet).
- **A joinrel only one engine built is a SHAPE divergence**, counted separately
  and classed per §6 — there is nothing to compare it against. This is the
  spine-mismatch budget above, now countable per joinrel rather than per query.
- The absolute tripwire of §5 **stays**, as a coarse tripwire and as the home
  of the per-query bars (Q9's ≤10², Q21's PG-parity 5 000×).

**Baseline pinned 2026-08-05** (TPC-H 22, LEGACY planner, goopg plans replayed
from the committed P5.6-g capture, PG 18.3 reference captured live on 65432):
`parity_violations=1 shape_mismatches=67`, 21 joinrels matched, 3 ambiguous.
The single violation is Q19 `{lineitem,part}`. **`shape_mismatches` is an upper
bound**: goopg's EXPLAIN does not deduplicate repeated relation names the way
`select_rtable_names` (ruleutils.c) does (`lineitem_1`, `n1`/`n2`), so Q8, Q17
and Q18 lose their final-joinrel comparison to a rendering gap rather than a
planning difference (deferral-ledger row, 2026-08-05).

## 5. Estimate audit (class-(a) regression tripwire)

Automate the order-attribution methodology: for each TPC-H query, EXPLAIN
(without ANALYZE) at each join level vs actuals from a one-off instrumented
run; flag any joinrel whose estimate is > 10³× off. Q9's chain must show the
[04](04-cost-and-cardinality.md) §3 improvement (≤ 10²× at the final
joinrel). Runs at S5 and on any later selectivity change; output committed
under `analysis/leftdeep-joins/`.

### 5.1 The instrument (P5.6-e-i, landed 2026-08-04)

`cmd/estimate-audit` + `internal/estimateaudit`. One `EXPLAIN ANALYZE` per
query supplies BOTH sides of the comparison (the `rows=` of the cost
parenthetical and the `actual rows=` of the instrumentation), so the "one-off
instrumented run" above is not a separate build:

```
go build -o /tmp/estimate-audit ./cmd/estimate-audit
bench/tpch/setup_goopg.sh                 # cluster on 65433
/tmp/estimate-audit --label <YYYY-MM-DD>-<slug>   # all 22, ~13 min
bench/tpch/stop_goopg.sh
```

It writes `analysis/leftdeep-joins/<label>.txt` (the audit) and
`<label>.plans.txt` (the raw plans, so a later reader can re-derive any row),
and exits 1 when a joinrel is over threshold — instrument and tripwire in one
binary. The unit of audit is the JOINREL: a badly misestimated *scan* is not
a §5 violation, because §5's criterion is stated at join levels.

Three measurement conditions are forced by goopg-specific behaviour, and each
would silently corrupt the audit if left alone:

- **`--serial` (default).** goopg does not propagate worker instrumentation
  out of a `Gather` (upstream merges it in `execParallel.c`
  `ExecParallelRetrieveInstrumentation`), so in a parallel plan every node
  *below* the Gather reports estimates only. Q9 — the query this section
  states its acceptance criterion on — plans entirely below a Gather, so it
  is **unmeasurable in parallel**. The first run of the instrument recorded
  exactly that and is kept as evidence:
  `analysis/leftdeep-joins/2026-08-04-p56e-parallel-uninstrumented.txt`.
  The flag sets `max_parallel_workers_per_gather = 0`; the join tree under
  audit is the same one, minus the Gather.
- **`--warm-stats` (default).** goopg's ANALYZE statistics are
  per-connection and bare `ANALYZE;` is a no-op, so the run holds one
  stats-warmed session (explicit `ANALYZE <table>` per table) for every
  query. Without it the audit measures the no-stats planner.
- **Cumulative, not per-loop, actuals.** goopg's `actual rows=` is a raw
  cumulative counter (`instrumentedOp.stats.rowsOut`), where upstream prints
  the per-loop average. The tool consumes the printed value as-is; a reader
  who assumes PG semantics and multiplies by `loops` inflates every
  nested-loop inner node by exactly the loop count. Ledgered.

Two modes were added by P5.6-g-iii (2026-08-05) and are what make §4.1
runnable:

```
# capture the reference in the same run (PG 18.3 on 65432, bench/tpch/setup_pg.sh)
/tmp/estimate-audit --label <label> --ref-port 65432

# or apply a NEW instrument to OLD committed evidence — no 13-minute rerun
/tmp/estimate-audit --label <label> \
    --from-plans analysis/leftdeep-joins/<earlier>.plans.txt \
    --reference  analysis/leftdeep-joins/<earlier>.pg.plans.txt
```

`--from-plans` replays a committed `.plans.txt` instead of connecting: the
estimator is not consulted, so the replayed audit is bit-identical to the
original run's. A freshly captured reference is written to
`<label>.pg.plans.txt` so the comparison stays re-derivable. The reference is
captured through the same code path as goopg (same queries, same `--serial`,
ANALYZE first) — a reference measured under a different protocol would compare
two protocols rather than two planners.

### 5.2 Baseline, 2026-08-04 (pre-flip, `GOOPG_PGSHAPED_DP` OFF)

`analysis/leftdeep-joins/2026-08-04-p56e-baseline.txt`, all 22 queries, TPC-H
SF=1. Five joinrels over the 10³ tripwire, all but one an OVER-estimate:

| query | joinrel | est | actual | factor |
|---|---|---|---|---|
| Q18 | final (SEMI) | 1 756 987 324 | 70 | 2.5 × 10⁷ over |
| Q19 | final | 43 060 427 | 131 | 3.3 × 10⁵ over |
| Q3 | final | 91 875 163 | 30 401 | 3.0 × 10³ over |
| Q20 | inner (SEMI) | 6 772 315 | 2 568 | 2.6 × 10³ over |
| Q7 | inner (build=left) | 126 | 150 000 | 1.2 × 10³ **under** |

**Q9's final joinrel is 124.7× over (est 39 447 200 vs actual 316 264)** —
just outside this section's ≤ 10² bar, and the number P5.9 re-measures once
[04](04-cost-and-cardinality.md) §3's sizing is on the live path. The shape
of the miss is the compounding §3 exists to end: Q9's three outermost
joinrels all carry the SAME estimate (39 447 200) while the actual collapses
from 5 997 241 to 316 264 across them — two joins that cost nothing in the
estimate.

The violations split into two class-(a) causes, both filed as P5.6-e-ii and
neither fixed here — §6 forbids a constant moving without its class
diagnosis, and these need the diagnosis first:

- **A SEMI/ANTI joinrel is priced at its outer input verbatim.** Q18's final
  SEMI carries the identical estimate to the join beneath it
  (1 756 987 324), against 70 actual rows: the match fraction is not applied
  at all, where `calc_joinrel_size_estimate`'s JOIN_SEMI arm (costsize.c)
  scales the outer's rows by the semi-join selectivity. Q20's inner SEMI
  (2.6 × 10³ over) and Q22's ANTI final (643×, under the tripwire) share the
  shape. Note that Q18's outer is *itself* 293× over — the
  `lineitem ⋈ orders` FK equality priced at 1.76 × 10⁹ against 5 997 241
  actual, which is the eqjoinsel/FK-superkey miss P5.6-a…-c reproduce
  upstream's answer for; the SEMI defect stacks on top of it rather than
  causing it.
- **A joinrel's non-equi restriction contributes no selectivity.** Q19's
  final joinrel is a plain INNER over two *unfiltered* scans (5 997 241 ×
  200 000) whose entire WHERE is one three-branch OR over `part` and
  `lineitem` columns; the plan shows only the `Hash Cond`, and the estimate
  (4.3 × 10⁷) credits the OR nothing, against 131 actual rows. Q3's final
  (3.0 × 10³ over) is the same omission over the three-conjunct `Filter:`
  the plan re-applies at the join.

Two instrumentation gaps the run surfaced, both ledgered and neither fixed
here: the Gather gap above, and Q11's `InitPlan`/`SubPlan` joins, which
report no `actual rows=` even in serial mode.

### 5.3 Both causes closed, 2026-08-04 (P5.6-e-ii)

`analysis/leftdeep-joins/2026-08-04-p56eii.txt`, same instrument and same run
conditions, LEGACY planner still (`GOOPG_PGSHAPED_DP` OFF) — only
`estimateJoin` changed. Provenance and the full before/after table:
`2026-08-04-p56eii-README.md`.

What landed, both in `internal/planner/cardinality.go`:

- **SEMI/ANTI are sized from the OUTER.** `estimateJoin` gained the arms
  `calc_joinrel_size_estimate` has: `outer_rows · jselec` for JOIN_SEMI and
  `outer_rows · (1 − jselec)` for JOIN_ANTI, with the match fraction from
  `eqjoinsel_semi`'s no-MCV branch — `nd1 ≤ nd2 → 1.0`, else `nd2/nd1`, and
  0.5 when either side is a default. `nd2` carries upstream's asymmetric
  clamp to the inner relation's row count (the only pathway by which an
  inner-side restriction reaches a semi/anti estimate; clamping `nd1` too
  would double-count the outer's own restrictions).
- **The non-equi restriction is priced.** The conjuncts of `Predicate` that
  `HashKeys` does not already answer are run through `clauseSelectivity`,
  which required `columnStatsForChild` to resolve a column THROUGH a join
  (`Predicate` is written in the merged left‖right space) and to remap
  through a `Project`'s target list, as its ndistinct twin already did.
  Only conjuncts referencing BOTH sides count: a single-sided conjunct is a
  baserestrictinfo upstream and is already priced into the component rel's
  size, even though goopg also leaves a copy on the join for the executor.

| query | joinrel | §5.2 baseline | now |
|---|---|---|---|
| Q19 | final | 328 705× over | 13.1× under |
| Q20 | final (SEMI) | 891× over | 9.5× under |
| Q21 | final (ANTI) | 499× over | 9.7× under |
| Q22 | final (ANTI) | 643× over | 1.8× over |
| Q4 | final (SEMI) | 485× over | 7.3× over |
| Q18 | final (SEMI) | 2.5 × 10⁷ over | 1.26 × 10⁷ over |
| Q9 | final | 124.7× over | 124.7× over |

Five joinrels remain over threshold, one fewer than the baseline and with no
new ones. Q18, Q3 and Q20's inner SEMI all still fail for a cause §5.2 named
but did not own: their OUTER input is 293× / 5.8× / 86× over on its own.

**Why the third cause is NOT fixed here.** The first cut also corrected the
join-key ndistinct lookup — `RightKey.Index` is a MERGED index and was being
resolved against the right child's own schema, so the right side of an
equi-join never entered `max(nd)` — and let both column lookups resolve
through a join, which is what `examine_variable` does. Measured
(`2026-08-04-p56eii-postfix.txt`), every joinrel it touched got more accurate
and the queries above them got far worse: **Q9's final 124.7× → 176 424×
over, Q8's final 1.9× under → 2 171× over**, with Q9's two deepest joins
landing exactly on their actuals. The missing `nd` was cancelling two
pre-existing defects — ANALYZE storing a SAMPLE distinct count with no
Haas–Stokes scale-up (a 1.5 M-row unique key reads as ≈ 30 000), and the
M0126-0010 `max(|l|,|r|)` cap firing only on the nd-unavailable path, so
supplying `nd` also removes the bound. Per §6 that is a class-(a) diagnosis
with its own mechanism, filed as **P5.6-e-iii**: de-saturate ANALYZE, then
land the coordinate correction with it. The rejected run is committed
because the conclusion is only defensible with its numbers present.

### 5.4 The third cause closed, 2026-08-04 (P5.6-e-iii)

`analysis/leftdeep-joins/2026-08-04-p56eiii.txt`, same instrument and same run
conditions, LEGACY planner still (`GOOPG_PGSHAPED_DP` OFF).

What landed:

- **ANALYZE de-saturated** (`internal/executor/operators_analyze.go`,
  `ndistinctEstimate`). goopg stored the SAMPLE's distinct count as the
  table's ndistinct, so with the default statistics target a 1.5 M-row unique
  key read as ≈ 30 000. `ndistinctEstimate` mirrors `compute_scalar_stats`'s
  ndistinct block (analyze.c:2588-2648) branch for branch: the
  `nmultiple == 0` unique-column arm, the `nmultiple == ndistinct`
  whole-value-set arm, and Haas–Stokes Duj1 `n·d/(n − f1 + f1·n/N)` clamped to
  `[d, N]`. `ColumnStats.NDistinct` and `NDistinctFrac` are now two renderings
  of that one estimate, and `StaDistinct()` picks between them with upstream's
  own 10 %-of-rows rule instead of always preferring the fraction.
- **The join keys resolve in the merged coordinate space**
  (`internal/planner/cardinality.go`). `estimateJoin`'s equi arm reads the
  right key through `rightKeyNDistinct`, the same left-width shift the
  SEMI/ANTI path has used since §5.3, and `columnNDistinctForChild` gained the
  `*Join` arm its `columnStatsForChild` twin already had. The two column
  lookups no longer diverge; the divergence tripwire test is retired.
- **The M0126-0010 cap was re-examined and deliberately left alone** — it
  still fires only on the nd-unavailable fallback path. It is a non-PG
  heuristic standing in for upstream's FK-driven `fkselec`
  (`get_foreign_key_join_selectivity`), and a genuine many-to-many join
  legitimately exceeds `max(|l|,|r|)`. What made it look load-bearing in §5.3
  was the saturated `nd` it was compensating for.

Violations: **5 → 2.** Q3, Q7 and Q20's inner SEMI are closed outright; Q18's
final SEMI improved by 293× and still violates.

| query | joinrel | §5.3 | now |
|---|---|---|---|
| Q3 | final | 2 967× over | 10.4× over |
| Q5 | d4 | 447.7× over | 1.5× over |
| Q7 | d4 | 1 190× under | 1.4× over |
| Q8 | d3 | 20.7× over | 1.3× over |
| Q17 | final | 7.5× over | 1.0× under |
| Q18 | final (SEMI) | 1.26 × 10⁷ over | 42 837× over |
| Q20 | inner SEMI | 1 311× over | 129× over |
| Q16 | final (ANTI) | 16.0× under | 85.1× under |
| Q19 | final | 13.1× under | 131× under |
| Q21 | final (ANTI) | 9.7× under | 4 003× under |

Two regressions came out of it, both filed rather than papered over:

- **SEMI/ANTI collapse to `est=1`** (Q21, Q19, Q16, and Q21's inner SEMI). A
  truthful `nd` makes `eqjoinsel_semi`'s `nd1 ≤ nd2` test succeed, the match
  fraction becomes exactly 1.0, and JOIN_ANTI's `outer · (1 − jselec)` floors
  at `clamp_row_est`'s 1. Upstream reaches 1.0 far less often because it takes
  the MCV branch of `eqjoinsel_semi` first, which goopg has no join-level MCV
  list for.
- **Q9 is UNMEASURED** — it exceeded the audit's 150 s timeout where §5.3
  measured it at 93.9 s. Attributed per §6 before anything landed: the
  ANALYZE half is the whole cause (reverting only the planner half reproduces
  the identical plan shape, `rows=160406045` vs `159924827`). The mechanism is
  a class-(a) defect this change UNMASKED rather than introduced — Q9's
  `l_suppkey = ps_suppkey AND l_partkey = ps_partkey` is a TWO-pair equi-join
  that `estimateJoin` prices on ONE pair while excluding BOTH from the
  residual, so it reads `6 M · 800 k / 10 000 = 481 M` and the DP puts it
  under the `part` filter instead of above it. Pricing every pair the way
  `clauselist_selectivity` does swings it the other way (≈ 2 rows) without
  upstream's FK selectivity to bound it, so the fix is
  `get_foreign_key_join_selectivity`, not a constant. **P5.9 cannot certify
  Q9's ≤ 10² bar until this lands.**

Q9's deepest joins are now exact (`5 997 241` on both), which is the evidence
that the ndistinct itself is right and the remaining error is the multi-key
pricing above it.

### 5.5 The multi-key cause closed, 2026-08-04 (P5.6-f)

`analysis/leftdeep-joins/2026-08-04-p56f.txt`, LEGACY planner
(`GOOPG_PGSHAPED_DP` OFF) as before — but **on a different schema from every
audit before it**, so it is diffed against a re-baseline rather than against
§5.4.

**Step 0: the baseline moved.** M0127-P5.6-f-pre proved goopg's `tpch` database
carried 0 user indexes against the PG 18.3 reference's 16, and its fix is
forward-only. Since half 2 of P5.6-f reads a UNIQUE index, the eight UNIQUE
indexes the reference declares were re-created before anything was measured
(`partsupp_pk (ps_partkey, ps_suppkey)` is the one Q9 turns on), and confirmed
to survive a restart — the first end-to-end validation of the P5.6-f-pre fix on
a real cluster. The eight NON-unique FK indexes were deliberately left out:
they carry no uniqueness evidence and would have moved plan SHAPE inside a
cardinality measurement, which §6 forbids.
`2026-08-04-p56f-baseline-idx.txt` is that cluster with the OLD planner, and it
reports the identical two violations and the identical UNMEASURED Q9 as §5.4 —
so the index creation contributed nothing to the delta below.

What landed (`internal/planner/joinkeyproof.go`, `cardinality.go`):

- **Every equi-pair is priced.** `estimateJoin` charged ONE pair while
  `Join.Residual()` excluded them all, so Q9's second equated column vanished
  from the estimate entirely. The pair list is `joinEquiPairs`, and the same
  list is now what `joinResidualSelectivity` excludes — the two can no longer
  disagree. It is derived from `Predicate` when `Join.HashKeys` is empty, which
  is the state EVERY estimate taken during join-order search sees
  (`fillJoinHashKeys` is one late pass at the tail of `Plan()`).
- **`get_foreign_key_join_selectivity` (costsize.c:5651) for the legacy
  estimator.** The same algorithm as `superkeyJoinSelectivity`
  (joinrelsize.go), arm for arm, over `*Join` nodes instead of `RelOptInfo`s:
  the covered pairs are removed and ONE `1/raw_ntuples` substituted for the key
  as a whole, largest divisor first, whole key or nothing. Half 1 alone prices
  Q9's composite key as `1/(200 000 · 10 000)` — 2 400 rows against 5 997 241
  actual, a bigger error than the defect and in the other direction. This is
  why the item always said the halves must land together.
- **The evidence reaches a catalog-free estimator by being stamped.** A table's
  indexes live only in the catalog, and `estimateJoin` takes a bare `Node`
  (EXPLAIN in the executor calls it too). `SeqScan.UniqueKeys` /
  `IndexScan.UniqueKeys` are stamped at the sites that already stamp
  `SmallDim`, through the planner's own `cat` — which also settles the dbOid
  hazard of cost-model/14 §2 that a bare `InMemory` lookup would reintroduce.
- **The proof resolves each end independently.** Requiring BOTH ends of a pair
  to reach a base relation was the mechanism's first shape and was measured
  wrong: Q20's `partsupp ⋈ (SELECT … GROUP BY …)` has a HashAggregate on one
  side that no resolver sees through, the proof went unmade, and the joinrel
  read 283 against 236 624. The uniqueness argument only ever concerns the KEY
  side. Only the declared-FK arm needs the far end, because it has to name the
  referenced parent.

Violations: **2 → 2** — Q18's final SEMI (42 837× over) and Q21's final ANTI
(4 003× under), both owned by P5.6-g and both untouched. **No joinrel got
worse.**

| query | joinrel | re-baseline | now |
|---|---|---|---|
| Q9 | `lineitem ⋈ partsupp` | 479 779 280 (80× over) | **5 997 241 — exact** |
| Q20 | d3 INNER (`partsupp ⋈ agg`) | 12.2× over | 3.1× over |
| Q20 | d2 SEMI | 125.0× over | 31.7× over |
| Q5 | d6 INNER | 5 996 041 | 5 997 241 — exact |

Everything else moved by under 2 %, which is the residual-accounting half.

**Q9 is measurable again — at 291.8 s, not within the audit's 150 s.** The
sequence is 93.9 s (§5.3) → unmeasured (§5.4) → 291.8 s. Its cardinality defect
is closed and the remaining error is class (b), plan shape: all three hash
joins carry the full 5 997 241 rows because the `part` filter (5.3 % selective)
is applied ABOVE them, where PG filters `part` first and index-scans lineitem
through `lineitem_part_supp_fkidx`.

**Why an exact estimate did not move the shape, and what owns that.** The
legacy planner does not size its join-order search with `estimateJoin` at all.
`estimateJoinCost` (bushy.go:1257) has its own cardinality arm, and its
PRODUCTION branch — the integer DP, `costDrivenJoinOrder` OFF — computes `ndv`
as the maximum NDistinct over *every column of the edge's two tables*, ignoring
the join key. The multi-edge enumeration and superkey probe that do exist there
(`crossEdgesBetween` + `uniqueNoFanoutRawCount`, whose FK arm additionally
divides by the CHILD's count where upstream divides by the parent's) sit in the
`costDrivenJoinOrder` branch that M0126 closed as a no-go and left OFF. So
P5.6-f reaches every printed estimate and every post-search decision, and the
search itself not at all. Filed as **P5.6-f-ii**; P5.9 still cannot certify
Q9's ≤ 10² runtime bar, but it can now certify its cardinality.

### 5.6 The search reached, 2026-08-05 (P5.6-f-ii)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56fii{,-halfway}.txt`,
`-README.md`. Same cluster and schema as §5.5, so the diff carries no schema
variable.

§5.5 named one cause — the integer DP's `ndv` being a table-wide maximum. It was
real and it was not sufficient. Instrumenting the DP (rather than reading it)
surfaced two more, both of which had been latent for as long as the accurate
path existed and neither of which any unit test could have caught, because both
concern how a coordinate is *interpreted* rather than what is computed from it:

1. **A `joinEdge`'s key expressions are in GLOBAL FROM-list coordinates.**
   `accurateKeyDistinct` indexed `Stats.Columns` with `ColumnRef.Index`, so on
   Q5 it read out of range for *every* join key (`c_nationkey` is `Index: 16`
   against 8 columns) and returned 0 — silently handing the search back to the
   table-wide maximum it was supposed to replace. On `nation`, `Index: 3` was in
   range and answered with `n_comment`'s distinct count for `n_nationkey`.
   `edgeColName`'s `cr.Name` fallback masked the whole thing by still returning
   the right *name*. Third instance of the P5.6-e-ii `RightKey` class.
2. **`accurateKeyDistinct` bypassed `StaDistinct()`**, multiplying
   `NDistinctFrac × RowCount` unconditionally — the branch P5.6-e-iii created
   `StaDistinct()` to arbitrate (`get_variable_numdistinct`, selfuncs.c).

**The half-fix is recorded because it is the argument for the whole one.**
Landing only the superkey proof made Q5's `lineitem ⋈ supplier` truthful
(39 981 → 5 997 241) while its rival `customer ⋈ supplier` kept reading 10 000
against a real 60 000 000; the DP took the cartesian product and Q5 went 65.9 s →
over the 150 s timeout. A search selects on *comparisons*, so a partially
truthful estimator is not a partial fix — it is a new class-(b) defect. This is
the §6 protocol's own logic applied to the estimator: the class was (b) both
before and after, and only a uniform divisor closes it.

Landed together: one divisor for both search modes (`graphJoinKeyDivisor`, the
graph-space twin of P5.6-f's `superkeyJoinEstimate`, plus the §4 per-clause
product for the unproven remainder); name-based column resolution;
`StaDistinct()` rendering. `uniqueNoFanoutRawCount` is deleted — its FK arm
divided by the child's raw count where costsize.c:5847 divides by the parent's.

Result: violations **2 → 2** (Q18, Q21 — both P5.6-g's), **no joinrel worse**,
and **Q9 measured for the first time**: UNMEASURED (>150 s) → 6.3× over, inside
its `≤100×` override. Runtime, which is what class (b) is judged on: **zero
regressions across 22 queries**, Q5 65.9 s → 17.1 s, Q7 38.9 → 27.2, Q21 125.1 →
90.5, common-measured total 546.8 s → 445.1 s (**0.81×**), and Q9 291.8 s →
16.6 s off-instrument. Plan-gate diverged 19/22 against the old baseline — the
intended outcome — and is 22/22 MATCH against the re-pinned
`plan_snapshots/m0127-p56fii.txt`.

**P5.9 can now certify Q9's ≤ 10² runtime bar as well as its cardinality.**

### 5.7 The semi-join arms completed, and what the two violations actually are, 2026-08-05 (P5.6-g)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56g.txt`, `-README.md`. Same
cluster and schema as §5.5/§5.6.

Landed in `internal/planner/cardinality.go` (+ `joinkeyproof.go` publishing the
whole statistics row): `eqjoinsel_semi`'s **MCV arm** — match the two MCV lists,
take the matched frequency mass as exact, and run the nd heuristic only on the
uncertain remainder with both distinct counts discounted by the match count —
and the **`(1 - nullfrac1)` factor** on every branch including the 0.5 punt.
`CLAMP_PROBABILITY` came with them. 13 tests.

**Result: a measured no-op on TPC-H.** Violations 2 → 2, both bit-identical;
every other joinrel moved under 5 %, in both directions, on INNER joins this
change cannot reach — ANALYZE's sampling noise between runs, which is now the
documented noise floor for reading this instrument.

**A no-op is indistinguishable from a broken wire until you separate them.**
Both halves were probed on a throwaway cluster with the same binary, varying
only the data: a semi-join whose inner has an MCV list estimates **5 010 against
an actual 5 010**, and the identical join whose inner ANALYZE gave no MCV list
(uniform data — upstream discards a list whose values are not more common than
average) estimates **20**. A 25 %-NULL outer key estimates **750 against an
actual 750** where it previously said 1 000. The mechanism works; TPC-H's
near-uniform surrogate keys and NOT NULL join columns cannot exercise it.

**The finding that reframes the milestone: neither violation belongs to this
item, and one of them is not a defect.** Both were re-measured against the PG
18.3 reference (port 65432, same dataset), which nobody had done:

- **Q21's ANTI — PG 18.3 estimates `rows=1` too**, against the same actual of
  4 003. `neqjoinsel` (selfuncs.c) does not price a `<>` clause through
  `eqjoinsel` for `JOIN_SEMI`/`JOIN_ANTI` at all; it returns `1 - nullfrac` by
  documented design. The eq clause is a self-join on `lineitem.l_orderkey`, so
  `nd1 = nd2` and every branch — including the new MCV one, whose
  `uncertainfrac` is then exactly 1.0 — returns 1.0, and `outer · (1 - jselec)`
  floors at one row in both engines. **Closing this would mean diverging from
  PG.** It is an audit-override, not an estimator task.
- **Q18's SEMI is a real divergence of a different mechanism.** PG never plans
  it as a semi-join: `GROUP BY l_orderkey` makes the subquery unique on the
  join key, so PG dedups to a plain inner join and estimates 117 159 (1 674×
  over). goopg keeps the SEMI and lands on the **0.5 punt** — `5 997 241 × 0.5`
  exactly — because `resolveBaseColumn` has no `*HashAggregate` arm and the
  inner's 1 210 559-row estimate is far above `defaultNumDistinct`, so the
  clamp never rescues `nd2`. Neither new arm participates.

**Consequence for §4 and P5.9: at 1 674× PG itself trips this audit's 1 000×
tripwire on Q18.** An absolute factor is the wrong bar for a PG-parity
milestone; the ratchet P5.9 certifies should be per-joinrel parity against the
reference, with the absolute tripwire kept only as a coarse tripwire. Filed as
**P5.6-g-ii** (the `*HashAggregate` arm and Q18's dedup-to-inner shape) and
**P5.6-g-iii** (the Q21 override + the parity bar).

DS05 could not run: the gate self-refuses while the nightly CI batch holds the
host (`FATAL: the nightly CI batch is running`), and the batch was mid-run with
a wedged testport stage for this loop's whole duration. Carried, with the exact
command, in `.ralph/working_set.md`.

### 5.8 The instrument corrected, 2026-08-05 (P5.6-g-iii)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56giii-parity.txt` (+
`.pg.plans.txt`, `-README.md`). **No estimator code changed**: the goopg side is
the committed P5.6-g capture replayed with `--from-plans`, so every goopg number
is bit-identical to §5.7's. The only new measurement is the PG 18.3 reference.

Landed: Q21's per-query bar beside Q9's (`estimateaudit.Q21AntiJoinMax`, 5 000×,
with its justification rendered into the artifact rather than left as a bare
number), and §4.1's per-joinrel parity gate (`internal/estimateaudit/parity.go`).
Absolute violations on TPC-H **2 → 1**: Q18 stays, Q21 is measured parity
(excess 1.0× against PG's own 4 178×).

Two findings the parity column produced on its first run:

- **Q19 `{lineitem,part}` is the only estimator defect TPC-H can prove**:
  goopg est 1 vs actual 131, PG est 116 vs actual 112 — 126.5× worse than the
  reference, and *invisible* to the absolute tripwire at 131× < 10³. Neither
  scan carries a filter, so Q19's three OR'd conjunction groups all ride as the
  join's residual and the whole predicate is priced at the join level, landing
  on the 1-row clamp. Filed as **P5.6-g-iv**.
- **goopg's EXPLAIN cannot name a repeated relation.** Upstream deduplicates
  printed relation names (`select_rtable_names`, ruleutils.c): a subquery's
  second scan of `lineitem` prints as `lineitem_1`, Q8's two `nation` RTEs as
  `n1`/`n2`. goopg prints the bare name, so two range-table entries are
  indistinguishable in the text and Q8/Q17/Q18 lose their final-joinrel
  comparison to a rendering gap. The gate reports the collision (`~` marker,
  `N ambiguous`) instead of silently picking one; the fix is in the renderer.
  Ledgered 2026-08-05.

Watch list (>10× the reference but under the 100× floor, so not yet ratcheted):
Q16 `{part,partsupp,supplier}` 84.9× vs 2.0×, Q20 `{lineitem,part,partsupp}`
32.1× vs 1.1×, Q14 `{lineitem,part}` 12.4× vs 1.0×.

### 5.9 The Q19 defect closed — a missing preprocessing pass, 2026-08-05 (P5.6-g-iv)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56giv.txt` (+ `.plans.txt`,
`-README.md`). Q12 and Q19 measured; see "why only two queries" below.

§5.8 predicted the defect was in how the residual was priced. It was one level
earlier than that: **goopg never ran PG's `canonicalize_qual`**
(`process_duplicate_ors`, prepqual.c), so the OR was never reduced before the
qual was distributed.

Q19's whole WHERE is `(A ∧ …) ∨ (A ∧ …) ∨ (A ∧ …)` where `A` is the join clause
`p_partkey = l_partkey`, repeated verbatim in every arm. Upstream hoists `A` —
along with `l_shipmode IN (…)`, `l_shipinstruct = '…'` and `p_size >= 1`, which
are also in all three arms — leaving `A ∧ (rest₁ ∨ rest₂ ∨ rest₃)`. goopg did
not, with three consequences that compounded:

1. **The join clause was priced twice.** Once as the equi-join key
   (`l·r/nd` = 1/200 000), and again inside each OR arm, where
   `eqOpSelectivity` sees two columns and no constant and returns
   DEFAULT_EQ_SEL. Three arms at ~5·10⁻⁹ apiece drove the product to ~0.1 rows,
   i.e. the 1-row clamp.
2. **Three real restrictions were priced nowhere.** The single-relation
   conjuncts common to all arms stayed trapped inside the OR, so neither scan
   could be filtered and `joinResidualSelectivity` — which correctly skips
   single-sided conjuncts as "already priced at the scan" — had nothing to skip
   and nothing had been priced.
3. **M0058-0004 had already computed the intersection and thrown it away.**
   `commonEquijoinsAcrossOr` (joinorder.go) extracts exactly `A` so the join
   EDGE exists, which is why goopg emitted a Hash Join at all; the qual itself
   stayed opaque. That workaround is the same computation as
   `process_duplicate_ors`, applied to one consumer instead of to the qual.

Landed: `internal/planner/qual_canonical.go` (`canonicalizeQual`, upstream's
`find_duplicate_ors` over goopg's binary AND/OR tree), applied in `planSelect`
at upstream's own placement — after parse analysis, before the qual is
distributed. The parse tree is **not** mutated; it is shared with the view/rule
deparsers, which must keep rendering the query as written.

The equality test is `strictParserExprKey` (exprkey.go), not `parserExprKey`.
That distinction is load-bearing: `parserExprKey` deliberately drops a
ColumnRef's table qualifier (M0097-0003), under which `a.x = 1` and `b.x = 1`
compare equal, and hoisting one of them out of an OR rewrites a qual that admits
rows from either table into one that demands both. Pinned by
`TestCanonicalizeQualDoesNotHoistAcrossTableQualifiers`.

Result — Q19 `{lineitem, part}`:

| | est | actual | ratio | PG 18.3 | excess |
|---|---|---|---|---|---|
| before (§5.8) | 1 | 131 | 131.0× under | 1.0× | **126.5×** |
| after | 309 | 131 | 2.4× over | 1.0× | **2.3×** |

`RATCHET parity_violations=0 shape_mismatches=0`. The plan now shows PG's own
Q19 shape: `Filter: (l_shipmode = ANY …) AND (l_shipinstruct = …)` on the
lineitem scan, `Filter: (p_size >= 1)` on part, the reduced OR at the join.

**Why only Q12 and Q19 were measured.** This pass can only change a query whose
WHERE contains an OR; on every other input `canonicalizeQual` returns its
argument unchanged. Exactly three of the 22 TPC-H texts contain `or`, and Q15's
is `CREATE OR REPLACE VIEW`. Q12 is therefore the control, and it is
bit-identical to §5.7's baseline (1.5× / excess 1.3×; est 45 793 → 46 222 is
ANALYZE sampling noise between sessions) — its OR is a two-arm disjunction of
bare equalities with no common conjunct, so it correctly finds no winners. The
19 OR-free queries were not re-run and are not claimed to have moved; the claim
is that the pass is a structural no-op on them.

**What is deliberately not reproduced.** `find_duplicate_ors` also drops
constant TRUE/FALSE/NULL inputs as it recurses, with different rules for WHERE
quals and CHECK constraints. goopg folds constants in `FoldConstants`, and
duplicating that logic here would give two passes an opportunity to disagree
about three-valued logic. The pass is also applied to SELECT only, not to
UPDATE/DELETE quals (planner.go:9167ff). Both ledgered 2026-08-05.

**Not yet discharged:** the TPC-DS SF0.5 gate. TPC-DS has far more OR-bearing
queries than TPC-H, so it — not TPC-H — is where this pass's plan-shape blast
radius actually gets measured. It self-refuses while the nightly CI batch holds
the host, and is carried on `M0127-P5.6-g-i` together with P5.6-g's own
undischarged sweep. **Discharged 2026-08-05 — see §5.10.**

### 5.10 The DS05 gate for three commits, and which one actually moved the corpus, 2026-08-05 (P5.6-g-i)

Evidence `analysis/leftdeep-joins/2026-08-05-p56gi-*` (README, the two sweep
reports, four whole-corpus plan captures, the capture script).

`scripts/tpcds-sf05-regression.sh sweep` had last run at `ce027cee` (P5.6-f).
Between that report and HEAD sit **three** estimator commits, not the two this
item was filed for — `4b820ab8` (P5.6-f-ii) landed after the baseline sweep and
was never gated either. Arms: **A** `ce027cee` (P5.6-f) → **B** `4b820ab8`
(+f-ii) → **C** `8ce056ff` (+g; g-iii is instrument-only) → **D** `f8338a09`
(+g-iv).

**The gate.** At D the summary is `PASS=94 (57 ck-verified, 37 ck=n/a)
MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4` — identical to the A baseline
line for line, including the 57 value checksums and the single TIMEOUT (Q47).
Not one query changed its row count or its checksum. Total sweep seconds
1828 → 1788, but that comparison is **not** claimed: the A sweep ran
01:23→01:59 and the nightly batch fired at 01:43, so its back half carries load
this run did not.

**The blast radius.** `EXPLAIN` (no ANALYZE) over all 99 queries at each arm,
S-cold, one binary at a time. Noise floor measured first — the same binary
captured twice gives byte-identical plans for all 99, so M0125-0047's
plan-snapshot nondeterminism does not contaminate the attribution.

| step | plans changed |
|---|---|
| A→B **P5.6-f-ii** | **74** of 99 |
| B→C **P5.6-g** | **1** (Q83) |
| C→D **P5.6-g-iv** | **4** (Q13, Q41, Q48, Q85) |
| A→D net | 75 (nothing changed and changed back) |

**§5.7's hypothesis is measured false.** The reason this sweep was raised in
priority — TPC-DS has nullable join keys, so `(1 - nullfrac1)` and the MCV arm
should move plan shape where TPC-H showed nothing — does not survive
measurement: P5.6-g moves **one** plan in the corpus. Its estimates move; the
search's *choice* almost never does. The 74-query churn is **P5.6-f-ii**, the
commit that taught the join-order search to read the join key at all (§5.6):
before it the integer DP priced an edge by the maximum NDistinct over every
column of the two tables, so nearly every multi-way join in the corpus was
ordered on a number unrelated to the key being joined on. 74 plans moved, zero
rows moved. That is the strongest available evidence that §5.6's change is a
re-ordering and not a semantic one — and it is evidence P5.6-f-ii shipped
without.

P5.6-g-iv's four are `canonicalizeQual` behaving as specified. Q41 is the pure
case: `(A AND X) OR (A AND Y)` → `A AND (X OR Y)` inside one scan's filter,
nothing else moves. Q13 is the load-bearing one — the three join clauses
repeated in all three OR arms plus `ca_country = 'United States'` are hoisted
out, so the planner sees join clauses where it previously saw an opaque OR and
builds hash joins in place of a nested loop carrying the whole disjunction as a
filter, which is PG's own Q13 shape. 27 of the 99 texts contain an OR (against
2 in all of TPC-H); the pass fires on the other 23 without changing what the
search picks.

**What this leaves open.** The gate has no plan-shape channel: the four captures
above were built by hand for this loop, and a 74-query plan change passed the
gate in silence because it compares row counts and checksums only. That is the
correct *primary* bar — but a search change of this size being invisible to it
is filed as a follow-up under M0127, not fixed here. Q13's new plan also hashes
all 1 920 800 `customer_demographics` rows: free at SF0.5 (20 s → 21 s), and no
SF=1 sweep has run since.

### 5.11 The DS05 gate gets a plan-shape channel, 2026-08-05 (P5.6-g-i-b)

Evidence `analysis/leftdeep-joins/2026-08-05-p56gib-README.md`. Closes §5.10's
"what this leaves open": the 74-query plan change that passed the gate in
silence is now something the gate itself reports.

**The primary bar is untouched.** Row counts and value checksums still decide
pass/fail, and nothing in this channel can change the exit status — verified by
running a sweep with `PLAN_DIFF` pointed at a nonexistent file: the gate still
exits 0 and the report says so out loud. A plan that moves is *information about
a planner change*, not a failure; only rows and checksums are correctness.

**The channel.** `scripts/tpcds-sf05-regression.sh` gained a `plans`
subcommand and a tail stage on `sweep`:

- one `EXPLAIN`-only pass over all 99 queries on a freshly started server,
  written to `plans-<stamp>.txt` beside `sweep-<stamp>.txt` (same stamp — the
  two artefacts of one run pair by name);
- every statement in a file is `EXPLAIN`-prefixed, so Q14/23/24/39's second
  statement is never executed and no query runs for real;
- `scripts/tpcds-plan-diff.py OLD NEW` diffs it per query against the previous
  capture and appends `=== PLAN-SHAPE: queries=99 same=N changed=N … ===` plus
  the changed query list to the report. `--verbose` prints the unified diff.

Three properties make the output readable as signal:

1. **Noise floor zero**, re-confirmed here — three consecutive captures at the
   same commit, `changed=0` each time. `EXPLAIN` without `ANALYZE` emits no
   timings and no actual rows, which is what makes the file byte-stable.
2. **The capture is always the full corpus**, even under `QUERIES=` (which turns
   the *sweep* into a subset probe). A plan file exists to be diffed against
   every other plan file; a subset would report the other 98 as `removed`. The
   full pass costs **14 s**, so there is nothing to save by narrowing it.
3. **The flags line is stamped into the capture**, not only the sweep report. A
   plan diff between two arms run under different planner flags is meaningless,
   and the file has to be able to say which arm it is on its own face.

**Validation — the instrument reproduces §5.10's table without re-running
anything.** The file format is deliberately identical to the hand-rolled
predecessor (`2026-08-05-p56gi-capture.sh`), so the four committed corpus
captures are valid baselines. A capture taken through the new gate path at
`b2d82285` (whose Go code is `f8338a09` verbatim — the two commits since are
docs and CI logs) diffs against them as:

| baseline | changed | expected |
|---|---|---|
| D `f8338a09` | **0** | 0 — same engine code, different harness and directory |
| C `8ce056ff` | **4** — Q13, Q41, Q48, Q85 | §5.10's P5.6-g-iv set, exactly |
| B `4b820ab8` | **5** — + Q83 | + §5.10's P5.6-g set |
| A `ce027cee` | **75** | §5.10's A→D net |

The D row is the load-bearing one: it is the cross-harness compatibility proof,
and it only passes because of one normalisation. psql stamps errors with the
*path* of the script it was reading (`psql:/tmp/xyz.sql:29: ERROR: …`), and
TPC-DS Q36/Q70/Q86 are dsqgen artefacts whose block is an error message rather
than a plan. Before the fix every capture written to a different directory
reported all three as changed — three permanent false positives in a channel
whose entire value rests on a zero noise floor. `tpcds-plan-diff.py`
canonicalises that prefix to `psql:<script>:<line>:` and keeps the line number,
which moves only when the query file itself does.

**Scope.** This channel is goopg-against-goopg over time: it answers "did this
commit move a plan?", not "is the plan PG's". The second question is §4's
per-joinrel parity instrument, and the two are deliberately separate — one runs
on the SF0.5 cluster in 14 s with no PG instance, the other needs the oracle.

### 5.12 What crosses a grouping node, 2026-08-05 (P5.6-g-ii)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56gii{.txt,.plans.txt,-README.md}`,
`-ds05-sweep.txt`, `-plans-{before,after}.txt`.

**The item was filed as the wrong half of itself, and the oracle is why.**
P5.6-g-ii asked for a `*HashAggregate` arm on `resolveBaseColumn`, and §5.7 had
already measured that the arm alone reads *worse* (Q18 2.99 M → 4.84 M). It
reads worse because upstream does not have it. `examine_simple_variable`
(selfuncs.c), inside a subquery RTE, hits `if (subquery->groupClause)`, sets
`vardata->isunique` when the referenced output is the sole grouping column, and
returns — "cannot go further" — *without* a statistics tuple. What crosses a
grouping node upstream is **uniqueness, never a distribution**: grouping mashes
the underlying column's MCV list and histogram beyond recognition, but it
cannot destroy the fact that one row survives per distinct group value. The
consumer is `get_variable_numdistinct`'s `if (vardata->isunique) stadistinct =
-1.0 * (1.0 - stanullfrac)` — a negative `stadistinct` is a fraction of the
relation's rows, and `stanullfrac` is 0 because there is no statistics tuple,
so the answer is the grouped relation's own row count.

Landed accordingly: `resolvesToGroupUniqueColumn` / `groupUniqueNDistinct`
(joinkeyproof.go), consumed **only** by `columnNDistinctForChild`.
`resolveBaseColumn` still has no grouping arm and `columnStatsForChildBase`
still answers nil through one; a test pins that, because handing the base
column's MCV list up would make `eqjoinsel_semi` take its MCV arm on the wrong
relation's frequencies — the P5.6-e-ii defect class in a new place. Upstream's
`list_length(...) == 1` restriction is kept and is load-bearing: with two
grouping columns the pair is unique but neither column is (Q20's
`GROUP BY ps_partkey, ps_suppkey`). The DISTINCT / DISTINCT ON halves of the
same test are the `*Distinct` / `*DistinctOn` arms.

**Q18: 42 837× → 24 242×** (est 2 998 620 → 1 696 939 against an actual 70).
The old number was `5 997 241 × 0.5` exactly — `eqjoinsel_semi`'s punt, taken
because `defaultNumDistinct` sat far below the inner's rows so the nd2 clamp
never fired. Parity excess against PG's own 5 387× drops 8.0× → 4.5×. It
remains this corpus's one absolute violation, and the residual is now
attributable rather than mysterious: goopg's `l_orderkey` ndistinct (~1 210 559)
is *more* accurate than PG's (~339 000, against a truth of 1 500 000), which is
what makes goopg's post-HAVING inner ~3.6× larger than PG's 113 141. Closing
the rest is a HAVING-selectivity problem, not a join-selectivity one.

**`reduce_unique_semijoins` was measured inert, not skipped.** PG's Q18 plan
confirms the SEMI→INNER conversion fires. At goopg's join order it changes no
number: for an inner unique on the join key, `inner_rows` equals nd2, so
`outer · inner / max(nd1, nd2)` and `outer · min(1, nd2/nd1)` agree term for
term. What it buys upstream is join-order freedom — PG joins `orders ⋈ agg`
first (113 141) where goopg joins `orders ⋈ lineitem` first (5 997 241). Ledger
row; deferred rather than guessed, because a goopg SEMI `*Join`'s `Output()` is
left-only and a node-type swap changes the output width of everything above it.

**The defect the arm exposed: `estimateJoin` had no outer-join arm at all.**
LEFT / RIGHT / FULL took the INNER product — upstream's first line for each of
them — and stopped before the second, "the output must be at least as large as
the non-nullable input" (`calc_joinrel_size_estimate`, costsize.c). It was
unreachable while a LEFT join's key resolved to nothing, because the
`defaultEqSelectivity` fallback caps at `max(l, r)`. With the keys resolvable,
TPC-DS Q77's `store LEFT JOIN (… GROUP BY s_store_sk)` estimated 885 rows for a
join whose outer alone is 8 885. `outerJoinRowFloor` is that clamp, RIGHT
included (goopg keeps JOIN_RIGHT where upstream has already commuted it, so its
non-nullable input is the inner).

**DS05: 12 of 99 plans moved, zero rows moved.** `PASS=94 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4`, identical to the `ce027cee` baseline
line for line. Five plans from the grouping arm (joins between grouped CTEs on
the group key, whose estimate goes from `l` to `min(l, r)`), seven from the
floor; Q77 moved under the arm and moved *back* once the floor landed. Stream
2 116 s → 2 074 s with three real wins, all from the floor: **Q80 41 s → 14 s,
Q40 16 s → 2 s, Q78 29 s → 17 s**. This is the first commit whose plan-shape
channel (§5.11) was read *before* the sweep rather than after — the 20 s
capture scoped the blast radius, and caught the Q77 impossibility early enough
that the floor shipped in the same change.

### 5.13 Q18's residual is not a defect, and the instrument was off by one selectivity, 2026-08-05 (P5.6-g-v)

The item asked one question — is Q18's residual the group estimate or the
HAVING selectivity? — and required it be answered by measurement before
anything was touched. One `EXPLAIN` of the bare subquery on each engine
answers it:

| | group estimate | after `HAVING sum(l_quantity) > 313` | factor |
|---|---|---|---|
| PG 18.3 | 339 423 | 113 141 | ÷3 |
| goopg | 1 150 720 | 383 573 | ÷3 |

**Both engines apply exactly the same HAVING selectivity.** `113 141 =
339 423 / 3` and `383 573 = 1 150 720 / 3` are both DEFAULT_INEQ_SEL over an
aggregate for which neither engine has statistics — upstream `cost_agg`
(costsize.c) scales `output_tuples` by `clauselist_selectivity(quals)` and
goopg's `*Filter` wrapper over the `*Aggregate` does the identical thing.
There is no HAVING defect.

The entire 3.39× gap is the **group estimate**, and it is the direction the
item warned about: goopg's `l_orderkey` ndistinct (1 150 720) is *more*
accurate than PG's (339 423) against a truth of 1 500 000 — PG is 4.4× LOW.
Q18's inner is larger than PG's *because goopg's statistics are better*.
Closing the parity gap here would mean deliberately degrading ndistinct
accuracy, so **P5.6-g-v closes with no estimator change**: Q18's standing
audit violation is inherent to pricing an aggregate with no statistics, a
defect goopg shares with upstream and is only visibly worse at because
upstream's compensating ndistinct error happens to point the other way.

**What the measurement did find.** Reading the two EXPLAINs side by side
exposed a defect in the instrument itself. goopg keeps a qual and the rows it
filters on two different plan nodes: the predicate lives on a `*Filter`
wrapper which `walkPlanFiltered` (operators_explain.go) collapses onto the
child below it, so the rendered node set matches PG's. The collapsed line
kept printing `EstimateRows(child)` — the **PRE-qual** count — beside a
`Filter:` detail the estimator had already applied. Upstream has no
equivalent gap because the two live on one struct:
`set_baserel_size_estimates` stores `rel->rows` already scaled by
`clauselist_selectivity(baserestrictinfo)`, and `cost_agg` sets `path->rows`
only after the HAVING scaling.

The estimator was always right — a *parent* node reads
`EstimateRows(*Filter)` and sees the filtered count, which is why a `Gather`
above a filtered scan reported the correct number while the scan under it did
not. Only the collapsed line lied, by exactly the filter's selectivity:

| | before | after | PG 18.3 |
|---|---|---|---|
| `lineitem WHERE l_shipdate <= '1994-01-01'` | 5 997 241 | 1 689 312 | 1 673 754 |
| `nation WHERE n_regionkey = 1` | 25 | 4 | 5 |
| TPC-DS `date_dim WHERE d_year = 2000` | 73 049 | 365 | 365 |

This is a **P5.6-g-iii-class instrument defect, not a cosmetic one**. Both
acceptance instruments read that field: `internal/estimateaudit` parses it
with `nodeLineRe` (audit.go), and §5.11's DS05 plan-shape channel captures
it. Every filtered base relation in every capture taken before this commit
reports its unfiltered size. Conclusions drawn from plan *text* about how
large goopg believes a filtered relation to be — the M0125-0026 ledger row's
"`date_dim` is costed at 73 049 rows" among them — were reading the renderer,
not the estimator; C2's qual *placement* finding is unaffected (the predicate
genuinely renders above the scan), but the row-count half of that evidence
was an artifact. **↳ NARROWED by §5.14 — that last clause is too broad: the
artifact reading only applies to a line that carries a collapsed `Filter:`,
and C2's cited `date_dim` scans do not. Read §5.14 before re-using this
paragraph.**

**Gates.** UNITS green. Audit: 1 violation (Q18), unchanged from the
`p56gii` baseline — every joinrel diff is sub-1 % ANALYZE sampling noise
(316 634 → 311 456, ratios 10.4×→10.2×, 6.2×→6.4×) and no joinrel moved to a
worse class; join nodes carry no collapsed `*Filter` in TPC-H, which is why
the fix leaves them untouched. DS05 `plans`: **95 of 99 captures changed, and
with `rows=` normalised the diff is 6 lines — a psql column-width header, and
nothing else.** Zero structural movement across the corpus, which is the
proof the change is confined to rendering: it cannot reach plan selection.
3 regression tests (`explain_collapsed_filter_rows_test.go`), each verified
to fail without the fix (1000→100, 10→3).

### 5.14 Which pre-fix plan-text conclusions survive, 2026-08-05 (P5.6-g-vi)

§5.13 corrupted the evidence base retroactively: every capture under
`analysis/` taken before `20e17fa5` reports some node lines at their pre-qual
row count. This section is the re-read — how wide the damage is, and which
closed findings quoted the damaged field. Full working:
`analysis/leftdeep-joins/2026-08-05-p56gvi-README.md`. **No code changed.**

**The blast radius, measured.** The two DS05 corpus captures that bracket the
fix are line-aligned (5 962 lines each), so a positional diff isolates exactly
what moved. Of **3 283** node lines carrying `rows=`, **836 changed (25.5 %)**,
across **96 of 99 queries** — and the split is clean:

| population | count | changed |
|---|---|---|
| node lines carrying a `Filter:` detail | 966 | **836** |
| node lines with **no** `Filter:` detail | 2 317 | **0** |

**So the rule for reading any pre-`20e17fa5` capture is exact: a `rows=` is
trustworthy iff its node line has no `Filter:` detail beneath it.** Nothing
without a `Filter:` moved anywhere in the corpus. (The 130 `Filter:`-carrying
lines that did not move are details that never came from a collapsed
wrapper — a scan's own qual field, `Filter: (true)` on a CTE scan, an index
recheck.) Where the field is wrong it is badly wrong: overstatement **median
9×, p90 18 000×, max 1 920 800×**. And the rule covers **join nodes**, not
only scans — Q1's `Hash Join (INNER) … Filter: (date_dim.d_year = 2000)` went
`rows=716` → `rows=3`. §5.13's "join nodes carry no collapsed `*Filter`" holds
for TPC-H only; TPC-DS is where the join-level qual placement of C2 lives.

**Verdicts.** Every closed finding whose reasoning quotes a scan or aggregate
row count from plan text was checked against the capture it cites:
**M0125-0026 C2** (pervasive form and the Q5 form), **M0125-0038 (C5)**,
**M0125-0040 (C6)**, **M0125-0031**, and the `estimate-audit` joinrel
conclusions of §5.3–§5.12 — **all survive on their own evidence; none needs
re-deriving.** The audit conclusions survive by direct re-measurement (§5.13's
run: 1 violation, unchanged); the rest survive because the lines they quote
are bare. That is not luck: C2, C5 and C6 are *about* relations goopg failed
to filter, so the numbers they quote are the ones the renderer had no filter
to mis-scale.

**One correction owed, and it runs the other way.** §5.13 called the row-count
half of M0125-0026's "`date_dim` is costed at 73 049" an artifact. It is not.
C2's own measurement is that **66 of 68** qualifying `Filter:` lines sit on a
join node, so the `date_dim` scans carry no filter and 73 049 is what the
estimator genuinely used — the row-count claim is faithful for exactly the
reason the placement claim is true. The two corrupted captures are C2's two
named exceptions (the scalar-SubPlan `date_dim` scans in Q14/Q54, printing
`rows=73049` beside a scan-level `Filter:`), and they are cited only as
placement exceptions; no conclusion rests on their row counts.

**C5 corroborates the fix rather than falling to it.** C5 read
`359 432 (web_sales) × 365.25 (date_dim after d_year) = 131 280 738` against a
rendered `rows=131280740`. But 365.25 appears nowhere in that plan text — the
`date_dim` line reads 73 049. It is `73 049 × 0.005` (`DEFAULT_EQ_SEL`), which
C5 recovered by *dividing* the join estimate. C5 therefore observed, without
naming it, precisely what §5.13 later proved: the estimator was carrying the
post-qual number all along and only the rendered line disagreed.

Pre-fix captures are **not** being re-captured — they stay as the historical
record. Apply the rule above when reading one.

### 5.15 The DS05 TIMEOUT did not hop, it was moved, 2026-08-05 (P5.6-f-iii)

Working notes + full evidence: `analysis/m0127-p56fiii/README.md`.

P5.6-f-iii was filed on the reading that the SF0.5 gate's single TIMEOUT
"hopped from Q72 to Q47 (2026-08-04), unattributed", provisionally blamed on
the documented sweep-tail confound. **That reading is refuted.** The move is a
real re-pricing introduced by **`ce027cee` (P5.6-f)**.

**The gate's summary line cannot distinguish the two.** `TIMEOUT=1` is
invariant to *which* query timed out, so a re-pricing that trades one timeout
for another is indistinguishable from noise at the summary level. Across the
boundary the summary was byte-identical (`PASS=94 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=1 SKIP=4`) while four queries moved by 4–17×.

**Evidence, in the order it decides the question:**

1. *Not noise — a step function.* Eight consecutive sweeps hold the old regime
   (Q47 ≈30 s, Q72 timeout), four hold the new (Q47 timeout, Q72 ≈166 s),
   within-regime spread ±3 s. A GC/state confound is unrepeatable; this is not.
2. *The confound is structurally inapplicable to Q47.* Q47 runs at position 47,
   **before** Q72: in the old regime no timeout had yet occurred, and in the new
   regime Q47 is itself the first. Nothing preceded it to thrash the heap. And
   a *fresher* post-restart server cannot explain Q57 getting 5× slower.
3. *Solo runs reproduce the new regime.* Quiet host, fresh S-cold server,
   `TIMEOUT_SEC=900` to recover the true runtime instead of the clipped cap:
   **Q47 523 s**, **Q57 81 s**, both with correct row counts. The confound
   hypothesis predicted Q47 ≈ 31 s.
4. *Bisect on a copy of the cluster*, so the live SF0.5 dir was never at risk
   and code was the only variable: `30293f78` → 31 s, `29daeb72` → 30 s with a
   **byte-identical plan**, HEAD → 523 s. Old binary on *today's* data is fast,
   which exonerates the cluster data as well as `29daeb72`.
5. `29daeb72..ce027cee` is **one commit**, P5.6-f. P5.6-g-i's four corpus
   captures corroborate for free: Q47's degraded top join is already present in
   A=`ce027cee`.

**Why the boundary sweep is mislabelled `29daeb72`:** its header carries
`build: rebuilt from tree [tree DIRTY in Go sources — the binary is not this
commit alone]` and `diff=129e691bd41a`. That binary was `29daeb72` **plus
uncommitted P5.6-f WIP**. The harness printed the warning and a content hash;
the warning was correct and was not read. **When attributing any sweep, read
the `diff=` field before the commit subject.**

**Mechanism.** Q47's outermost join carries five equi-pairs
(`i_category`, `i_brand`, `s_store_name`, `s_company_name`, `rn = rn-1`).
P5.6-f moved pricing from one pair to every pair
(`internal/planner/cardinality.go:457-483`), folding them under
**independence** — `sel /= pairNDistinct(...)` multiplied across all pairs.
Two of the five are strongly correlated (`i_category`↔`i_brand`,
`s_store_name`↔`s_company_name`), so the joinrel is under-estimated by orders
of magnitude, a tiny inner estimate makes a nested loop look free, and the plan
degrades from a 5-pair **Hash Join** to a **Nested Loop with no join
condition** — quadratic on real CTE volume. The same fold pointed the right way
is why Q9/Q72/Q53 improved. This is the **inverse** of the single-key
degeneracy trap: the fix for under-pricing over-corrected into
under-estimation. Only structural facts (join method, `Hash Cond` arity) are
cited here — per §5.14 the `rows=` on `Filter:`-carrying lines is not evidence.

**P5.6-f stays.** It is a net win (+Q72, +Q53, +Q9's exact joinrel) and
correctness never moved.

> **⚠ Corrected by §5.17 (2026-08-05).** This section originally closed by
> attributing the regression to a missing **functional-dependency arm**, on the
> reading that `clauselist_selectivity` (`clausesel.c`) consults
> `dependencies_clauselist_selectivity` / `statext_clauselist_selectivity`
> before multiplying. **That is false for join clauses**:
> `clauselist_selectivity_ext` gates the whole extended-statistics branch on
> `find_single_rel_for_clauses`, which returns `NULL` as soon as any clause has
> two relids. PG multiplies multi-pair join clauses blind, exactly as goopg
> does — and, measured, PG estimates Q47's two correlated 5-pair joins at
> `rows=1` itself. The successor **M0127-P5.6-f-iv** filed from this paragraph
> is refuted in §5.17, which names the divergence that *is* real. Everything
> above this box (the attribution to `ce027cee`, the bisect, the plan
> degradation) stands unchanged.

**Gate lesson (actionable):** the plan-shape channel (§5.11) catches plan
drift, but a *named-victim* timeout comparison would have caught this on the
night it landed. The sweep report should diff the TIMEOUT **set**, not its
cardinality. Landed as §5.16.

### 5.16 The DS05 gate diffs the TIMEOUT set, 2026-08-05 (P5.6-f-v)

Discharges §5.15's gate lesson. The counts in `=== SUMMARY: … TIMEOUT=1 … ===`
cannot say *which* query owns them, which is exactly how a 17× re-pricing of
Q47 hid behind four byte-identical summary lines. The sweep now also diffs its
own **per-query status/runtime vector** against the previous report and prints
what moved, by name:

```
TIMEOUT    +Q47  -Q72
SLOWER     Q57 15s->81s (5.4x)  …
```

**The channel.** `scripts/tpcds-sweep-diff.py OLD NEW`, wired into `sweep` as a
tail stage directly under the SUMMARY (before the slower plan pass), plus a
`delta [OLD [NEW]]` subcommand that runs nothing and costs nothing. Like §5.11
it is **non-blocking** — performance never fails this gate, only rows and
checksums do — and like §5.11 its baseline is chosen *before* the new report
exists so it cannot diff a run against itself.

Four deliberate limits, printed in the channel's own header every run so a
quiet delta is never read as a stronger claim than it is:

1. **The input is the report itself**, in the format `cmd_sweep` already
   printf()s. No new artefact, no new format — and therefore every one of the
   ~90 reports already archived under `tpcds-results-sf05/` is a valid
   baseline, retroactively.
2. **Both arms compare the intersection.** A query absent from one side (subset
   probe, or a corpus that grew) has no verdict there; counting it as "left the
   PASS set" made every full-vs-probe pair scream. What is missing is named once
   as `ONLY-OLD` / `ONLY-NEW`.
3. **TIMEOUT readings are excluded from the runtime arm** — a clipped query
   reports the cap, not its runtime (§5.15's `TIMEOUT_SEC=900` probe is the
   whole reason that distinction matters). Verdict changes already name it.
4. **A runtime move needs ≥2× *and* ≥5 s on the larger side.** Reports carry
   integer seconds, so 1 s → 3 s is "3×" and is noise. Both thresholds are CLI
   flags and both appear in the summary line.

The default baseline additionally **skips SUBSET PROBES**: a probe is stamped
"NOT a gate result" and covers a handful of queries, so diffing a full sweep
against one would compare 3 queries and stay silent about the other 96. The
newest *full* report is the last comparable gate run.

**Validation — replay, not a new measurement.** The tool was run over all 87
adjacent pairs of the archived corpus: zero crashes, zero parse failures, and
**17 pairs report a verdict change**. On the pair that motivated it —
`sweep-20260804-214607` → `-232914`, the two reports whose SUMMARY lines are
byte-identical — it prints `TIMEOUT +Q47 -Q72`, `PASS +Q72 -Q47`, and
`SLOWER Q57 15s->81s (5.4x)`: both §5.15 victims, named, from artefacts that
already existed on the night it landed. A full gate sweep at HEAD then ran the
channel live (`sweep-20260805-090258`, PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=1 SKIP=4): `verdict-changes=none`, one runtime move (Q83 7 s → 3 s) —
the correct reading for a harness-only commit.

**What this does not do.** It compares *adjacent* runs, so a regression that
creeps in below 2× per run stays unnamed, and it still cannot *fail* the gate on
a traded timeout — it only makes one impossible to miss in the report. Making a
named-victim regression fatal needs a curated per-query budget file, which is
not filed: the timeout set is still moving under active planner work.

### 5.17 PG has no functional-dependency arm for join clauses, 2026-08-05 (P5.6-f-iv)

Working notes + full evidence: `analysis/m0127-p56fiv/README.md`.

§5.15 attributed Q47's 31 s → 523 s regression correctly to `ce027cee`, then
filed the wrong repair: **M0127-P5.6-f-iv**, "damp correlated equi-pairs the way
`dependencies_clauselist_selectivity` does". Read against the oracle, that
resume point is refuted on two independent grounds.

**1. The upstream citation does not apply to join clauses.**
`clauselist_selectivity_ext` (clausesel.c) gates extended statistics on
`find_single_rel_for_clauses`, and that helper returns `NULL` the moment a
clause carries two relids:

```c
	if (!bms_get_singleton_member(rinfo->clause_relids, &relid))
		return NULL;		/* multiple relations in this clause */
```

A join clause has two relids by construction, so
`statext_clauselist_selectivity` — and `dependencies_clauselist_selectivity`
behind it — **never runs on a join clause list**. Extended statistics are a
*restriction-clause* mechanism upstream. What PG has left for a multi-pair join
is per-pair `eqjoinsel` multiplied blind, minus whatever
`get_foreign_key_join_selectivity` removed — which is exactly the shape P5.6-f
landed. Implementing the filed item would have moved goopg **away** from PG
while citing PG for it.

**2. Measured: PG collapses the same join.** Plain `EXPLAIN` of `query47.sql`
verbatim against the PG 18.3 SF0.5 oracle (:65438, `tpcds05`) gives `rows=1` on
*both* correlated 5-pair joins — `Merge Join … rows=1` over an inner
`Merge Join … rows=1`. The collapse is not the divergence.

**What differs is the size of the join's INPUTS.** Same query, same scale:

| node | PG 18.3 | goopg `096d3949` |
|---|---|---|
| `CTE Scan on v1` | **7 643** | **18** |
| top join estimate | rows=1 | rows=1 |
| top join method | **Merge Join** | **Nested Loop** |

PG refuses the nested loop from an estimate of 1 because rescanning a
7 643-row CTE per outer row is expensive. goopg accepts it because its inner is
18 rows. The plan flip is downstream of a **425× under-estimate of the `v1`
subtree**, and that under-estimate predates P5.6-f (`30293f78` carries the same
18 and still picks the Hash Join) — P5.6-f only tipped an already-mispriced
comparison.

**The 425×, isolated.** goopg's `HashAggregate rows=18` is `child/2` over a
`Hash Join rows=36`, so the error is in that join. Holding the four-table join
fixed on the live SF0.5 cluster and varying only the `date_dim` restriction:

| restriction | `date_dim` scan | join below `store` | join **above** `store` | extra factor |
|---|---|---|---|---|
| *(none)* | 73 049 | 1 439 608 | 1 439 608 | **1.0** |
| `d_year = 2000` | 365 | 7 193 | 35 | **≈ 1/205** |
| `d_dom = 15` | 365 | 7 193 | 35 | **≈ 1/205** |
| Q47's three-branch OR | 368 | 7 252 | 36 | **≈ 1/201** |
| `d_year > 1999` | 36 889 | 726 987 | 367 128 | **≈ 0.505** |

`store.s_store_sk` is unique over a 12-row relation, so that join is
row-preserving by construction and must return its left input unchanged. The
extra factor instead reproduces, per row, **the selectivity the `date_dim` scan
had already applied** (0.505 for the inequality; 365/73 049 = 0.005 for each
equality). With no restriction present it is exactly 1.0. That is a
double-count of a pushed-down baserestrictinfo — the trap
`joinResidualSelectivity`'s own header says it prevents — and PG does not do it
(its `store` join is 2 583 → 2 465).

**Ruled out, so the next loop does not re-walk it.** `joinResidualSelectivity`'s
guard (`exprSide(c, leftWidth) != sideMixed → continue`) is *correct in
isolation*: a throwaway probe gives `sideLeft` for `col = const` and `sideMixed`
for `col = col` across the width, so a one-sided conjunct is skipped there. The
leak is elsewhere in `estimateJoin`'s pair list — most likely
`splitAllEqualitiesForHash` admitting a `col = const` restriction as an
equi-*pair*, though the `d_year > 1999` row (factor 0.505, which is neither
`1/nd` nor `defaultEqSelectivity`) shows that arm cannot explain every row.

> **⚠ That last paragraph is wrong; corrected by §5.18 (2026-08-05).** The leak
> is not in `estimateJoin` at all — instrumenting the live server shows
> `estimateJoin` returning the *correct* 726 987 for the `store` join while
> EXPLAIN printed 367 128. The measurement in the table above stands; only its
> attribution to the pair loop was wrong. §5.18 names the real node.

**Successors.** **M0127-P5.6-f-vi** (the double-charge, with a unit test as its
discriminating instrument — build a join whose left is a `*Filter` over a scan
with the same conjunct still in `Predicate`, assert the estimate equals the
unfiltered one scaled once) and **M0127-P5.6-f-vii** (`estimateAggregate`'s
`child/2` vs upstream `estimate_num_groups`, a second and independent gap on the
same path, *not* load-bearing for Q47 and therefore not to be folded in).
Correctness never moved in any regime: Q47 returns its 100 oracle rows
throughout. 2 ledger rows.

### 5.18 The double-charge is a duplicated qual, not a mispriced join, 2026-08-05 (P5.6-f-vi)

§5.17 measured the defect correctly and located it wrongly. Instrumenting the
running SF0.5 server settles it in one line: for the `store` join of §5.17's
probe under `d_year > 1999`,

```
ESTJOIN l=726987 r=12 sel=0.0833 resid=1 pairs=1 rows=726987 bound=726987
```

`estimateJoin` returns **726 987 — the correct, row-preserving answer** — and
EXPLAIN prints 367 128. Its pair loop, its residual guard and
`splitAllEqualitiesForHash` are all exonerated; the two candidates §5.17 left
open are both dead. (A synthetic unit-scale rebuild of the same three-node tree
also returns the correct number, which is why the prescribed unit test had to be
written against the *plan the planner actually builds*, not against a
hand-assembled join.)

The factor is applied one node higher. The same probe traced through the
`*Filter` arm of `EstimateRows`:

```
ESTFILTER childType=*planner.SeqScan child=73049 sel=0.505 leafLocal=true  pred=&{pos:173 Op:> …}
ESTFILTER childType=*planner.Join    child=726987 sel=0.505 leafLocal=false pred=&{pos:173 Op:> …}
```

**The same source conjunct — same `pos` — is priced by two different `*Filter`
nodes.** `pushSingleSideQualsIntoInnerJoinInputs`
(`internal/planner/inner_join_qual_pushdown.go`, M0125-0004) copies a
single-relation restriction from the residual Filter down onto the relation it
references and **deliberately leaves `f.Predicate` untouched** — its documented
"property 2", which is what keeps the join's own residual evaluation correct.
The estimator then charged both copies, so the restriction's selectivity was
squared and the surplus landed on the join.

Upstream has no such node pair to reconcile: `distribute_restrictinfo_to_rels`
(`optimizer/plan/initsplan.c`) **moves** a single-relation clause into that
baserel's `baserestrictinfo`. `set_baserel_size_estimates` prices it once, and
the joinrel above never sees it — which is exactly the invariant
`calc_joinrel_size_estimate`'s opening comment asserts ("we are not
double-counting them because they were not considered in estimating the sizes of
the component rels").

**The fix.** goopg cannot move the clause, so it records the duplication:
`Filter.PushedBelow` lists the conjuncts a placement pass copied downward, and
`filterSelectivity` (`cardinality.go`) skips them. Splitting the predicate to do
so is not a second formula — `clauseSelectivity`'s `OpAnd` arm is itself
`left × right` under the independence assumption — so an empty `PushedBelow`
reproduces the former number bit for bit. Both duplicating passes are stamped,
per the sibling-paths rule: the binary-join arm (`pushInnerJoinInputQuals`) and
the MHJ arm (`pushResidualQualsIntoMHJTables`), whose header states the identical
"property 2". The two *moving* siblings — `pushOuterQualsIntoLaterals`
(pushdown.go, rewrites `f.Predicate`) and `pushSingleSourceFiltersIntoMHJTables`
(mhj_input_rewrite.go, consumes `mh.Filters`, which no estimator reads) — were
checked and need no stamp.

**Measured after (same probe, same cluster):**

| restriction | `date_dim` scan | join below `store` | join **above** `store` | extra factor |
|---|---|---|---|---|
| *(none)* | 73 049 | 1 439 608 | 1 439 608 | 1.0 |
| `d_year = 2000` | 365 | 7 193 | **7 193** | **1.0** |
| `d_year > 1999` | 36 889 | 726 987 | **726 987** | **1.0** |

Q47's `v1` subtree moves **18 → 3 626 rows** against PG's 7 643 — the same order
at last, and the residual gap is §5.17's separately-filed `estimateAggregate`
`child/2` (P5.6-f-vii), not this.

**Scope of the change, and what it did *not* buy.** This under-sized *every*
join above *every* pushed-down restriction, so it is broad, not Q47-local: the
SF0.5 gate reports **50 of 99 plan shapes changed** where the previous sweep
reported 0. It bought no verdict change — the named TIMEOUT set is still exactly
`{Q47}`, PASS=94 / MISMATCH=0 / CKMISMATCH=0 / ERROR=0, byte-identical
per-query verdicts to the `f05b5329` baseline. Q47's own plan still takes the
nested loop. Stated plainly because §5.17's chain predicted the estimate fix
would be *necessary*, not that it would be *sufficient*, and only the first half
is now evidence.

**The estimate audit is the load-bearing check here, not a formality.** The fix
*removes* a downward correction, so every affected joinrel estimate moves UP —
and §5's TPC-H corpus is dominated by OVER-estimates (Q3's `d2` sat at 10.2×
over). A broad upward shift could therefore have pushed a joinrel through the
10³ tripwire. It did not:
`analysis/leftdeep-joins/2026-08-05-p56fvi-postfix.txt` vs the `p56gv-postfix`
baseline shows per-joinrel moves of **±1–4 %** and no new violation. The corpus
keeps its single standing violation, Q18's final SEMI, which *improved*
25 182× → 23 433× (still the aggregate-result-statistics gap ledgered under
P5.6-g-v, not a joinrel-sizing defect). TPC-H barely moves because its
single-relation restrictions mostly reach their leaves through Slice A
(`attachRelationLocalFilters`), which partitions them out pre-DP and never
leaves a duplicate; `pushInnerJoinInputQuals` is the TPC-DS-shaped path.

### 5.19 `estimate_num_groups`, and the end of the DS05 TIMEOUT set, 2026-08-05 (P5.6-f-vii, closing -f-viii)

`estimateAggregate` answered `child / 2` for any GROUP BY that was not a single
bare ColumnRef, and for the one that was, the column's whole-table NDistinct
with **no clamp at all**. Both halves are now `estimateNumGroups`
(`internal/planner/cardinality.go`), which is `estimate_num_groups`
(`utils/adt/selfuncs.c:3449`): unique variables per grouping expression, the
per-relation product of their distinct counts clamped to that relation's
`tuples` (÷ 10 when more than one variable, never below the largest single
ndistinct), the Yao/Dell'Era correction for a relation the plan RESTRICTED, the
product across relations, and the closing clamp to `input_rows`.

**The item was filed as explicitly NOT load-bearing for Q47.** It is what
closed it. Q47's `v1` body is a 6-key GROUP BY over 7 252 rows; the two numbers
that moved are one line apart in the plan:

| node | before (`child/2`) | after (`estimate_num_groups`) | PG 18.3 |
|---|---|---|---|
| `HashAggregate (6 keys)` (the `v1` body) | 3 626 | **7 252** | 7 643 |
| `CTE Scan on v1` (post `d_year = 2000 AND avg_monthly_sales > 0`) | 6 | **12** | — |
| top block | `Nested Loop rows=1958` over `Hash Join rows=108` | **`Hash Join (INNER, build=left) rows=7252`** | Merge Join |

There is no new formula behind the 7 252: six grouping keys over 7 252 input
rows cannot produce more than 7 252 groups, so the answer is the `input_rows`
clamp, and halving it was arbitrary. Halving it twice — the CTE body is scanned
three times (`v1`, `v1_lag`, `v1_lead`) — is what made the 6-row outer look
like a free rescan driver, which is precisely the resume point -f-viii had
written down ("the `CTE Scan on v1 rows=6` outer is what makes rescanning 3 626
rows look free"). The nested loop is gone and **Q47 completes in 12 s against
its 300 s timeout, 100 rows, matching the PG oracle.**

**The DS05 named TIMEOUT set is now empty** — `TIMEOUT=0`, PASS 94 → **95**,
`MISMATCH=0 CKMISMATCH=0 ERROR=0`, the first sweep since §5.15 with no timing-out
query at all. Per §5.16 the delta is stated by NAME: `PASS +Q47 / TIMEOUT −Q47`,
59 of 99 plan shapes changed, no other verdict move.

**Estimate audit** (`analysis/leftdeep-joins/2026-08-05-p56fvii.txt`, vs the
`p56fvi-postfix` baseline): no new violation. Every TPC-H joinrel moves under
1 % except Q20, which *improves* — its `d2` SEMI 77 462 → 63 875 (30.2× → 24.9×
over) and its `d3` INNER 715 931 → 561 004 (3.0× → 2.4× over), because Q20's
inner aggregate no longer feeds a halved row count into the joins above it.
Q18's standing final-SEMI violation improved again, 23 433× → 23 015×.

**Four upstream refinements are deliberately absent**, each ledgered rather than
faked, because each needs machinery goopg's planner does not have at estimate
time: the equivalence-class de-duplication of step 3 (no EC structure),
`estimate_multivariate_ndistinct` (no extended statistics), the boolean
short-circuit (`exprType` is not available in this package — a boolean *column*
still answers 2 through its own ndistinct, only boolean *expressions* fall to
the default), and the volatile-grouping-expression arm that returns
`input_rows`. `estimateSetOp`'s non-ALL `/2` is left alone for the same reason
§5.18's change was measured alone: the set-op's output columns are not carried
as grouping expressions over each input, and wiring it would put a second
variable into this sweep.

## 6. Attribution protocol for regressions (inherited, binding)

Any per-query regression during S1–S5 gets classed before any fix lands:

- **(a) cardinality** — estimate wrong → fix in [04](04-cost-and-cardinality.md)
  §3 mechanisms only;
- **(b) plan shape** — estimate right, order/method wrong → enumerator or
  cost-function bug, cite the PG analogue;
- **(c) cost-model realism** — plan matches intent, runtime disagrees →
  missing cost term (nbatch, seam) with `costsize.c` citation;
- **(d) executor** — same plan, slower run → seam/allocation regression;
  pprof before patch.

No constant may change without its class diagnosis in the commit message —
the "no unfalsifiable tuning" rule made procedural.

## 7. Microbenchmarks (executor stages)

`go test -bench` fixtures under `internal/executor/`:

- seam benchmark: 3-level cascade, 1M synthetic probe rows — allocs/op must
  be 0 in steady state after E1+E2 (the assertion that kills the pool
  round-trip class);
- build benchmark: single-pass vs two-pass (guards E3 against regression);
- key benchmark: composite int64 pair vs string keys (E4);
- spill benchmark: nbatch ∈ {1, 4, 16} at fixed input size (S3 overhead
  curve; nbatch=1 must be within noise of pre-S3).

Benchmarks are tripwires with recorded baselines in the evidence directory,
not CI gates (WSL2 noise); regressions > 20 % require investigation before
the stage advances.
