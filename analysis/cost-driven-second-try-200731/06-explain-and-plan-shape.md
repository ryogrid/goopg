# 06 — EXPLAIN, EXPLAIN ANALYZE, and the plan-shape gate

## 1. `EXPLAIN` (without ANALYZE): nothing changes, and that is the whole point

Fusion is decided at operator-build time on an unmutated plan tree
([04 §2](04-fusion-site-and-data-structures.md)). `EXPLAIN` renders the **plan**:
`explainNodeLabel` and the child walker live in
`internal/executor/operators_explain.go:1380-1600` and switch on `planner.Node` types.

So with fusion on, a 4-table star still prints four `Hash Join` nodes in a left-deep cascade,
exactly as PostgreSQL would. With fusion off, identical text. **`EXPLAIN` output is invariant
under the fusion switch.** That is a testable property (Stage 1 test
`TestExplainInvariantUnderFusion`) and it is the strongest form the plan-parity claim can take.

Contrast with today: `operators_explain.go:1386-1390` prints
`Multi-Way Hash Join (%d tables)`, a node label PostgreSQL has never emitted, and
`:1562-1572` records that before the child-walker fix, eight TPC-H queries reported
"No scan nodes" because the tree was truncated at that label.

## 2. `EXPLAIN ANALYZE`: PG has no concept of a fused pipeline. Three options were considered.

PG's `EXPLAIN ANALYZE` attributes `actual time` and `rows` to every node via
`InstrStartNode`/`InstrStopNode`. A fused operator executes k plan nodes' worth of work in
one `Next()`; there is no honest per-node wall clock to report.

| option | per-node rows | per-node time | verdict |
| --- | --- | --- | --- |
| **A. Report zeros / the top node only** | wrong or partial | fabricated | **rejected** — fabricating `actual time=0.000..0.000` on a node that did work is a lie that will be believed by the next person debugging a plan, and by the nightly batch's parsers |
| **B. Instrument each level with timestamps** | exact | approximate | **rejected for the first cut** — two `time.Now()` per level per row reintroduces the per-row cost the fusion exists to remove, so the numbers would describe a system nobody runs |
| **C. Disable fusion when timing is on** | exact | exact | **chosen** |

### The chosen rule (contract C11)

- `EXPLAIN ANALYZE` (timing on, the default): `instrumentScope.timing == true` ⇒ Q0 fails ⇒
  **the plan runs unfused**. Every `actual time` and `rows` is a real measurement of a real
  operator. The plan text is identical either way, so the shape being explained is the shape
  that runs; only the execution strategy differs.
- `EXPLAIN (ANALYZE, TIMING OFF)`: fusion stays on; per-level row counts are exact via C12
  (`instrumentScope.table[levels[i].plan]`), and no timing is claimed.
- A diagnostic escape hatch `GOOPG_FUSION_UNDER_ANALYZE=1` forces fusion on under timing, for
  A/B work. When set, the top node gains one extra line
  `Runtime Fusion: N levels (timings are pipeline-attributed)` so nobody mistakes the output
  for PG-shaped text. Default off, never on in a gate.

### Implementation note (finding F8)

The gate must **not** read the package-level `instrumentScope`
(`internal/executor/instrument.go:215`), which `withInstrumentation` (`:225-233`) mutates and
restores without a lock — a concurrent `EXPLAIN ANALYZE` in another session would otherwise
flip fusion for unrelated builds, non-deterministically and racily. The predicate must be a
field on the per-build environment ([04 §1.1](04-fusion-site-and-data-structures.md)) set by
`explainOp.Open`'s build call.

### The honest caveat, stated loudly — and it is worse than the first draft said (finding F6)

`explainOp.Open` calls the **legacy** `Build(o.plan.Child)` under `withInstrumentation`
(`internal/executor/operators_explain.go:57-64`), while the server dispatch loop runs
`BuildFastIterator`. Moreover `buildRec`'s `Join` arm never calls `maybeInstrument`
(`executor.go:535-547`), so per-join row counts only exist on the legacy path at all.

So **`EXPLAIN ANALYZE` already measures a different operator tree than production, today,
before any of this work** — and per [02 §4.1](02-premise-audit.md) the two builders have
materially different per-row allocation profiles. Adding "and fusion is off under ANALYZE"
makes an existing divergence larger, it does not create it. Both facts belong in the caveat.

Under the chosen rule, **`EXPLAIN ANALYZE` timings are an upper bound on production time for
any plan that would have fused.** This must be written into the operator's doc comment and
into `docs/` when the work lands, because a future engineer will otherwise chase a phantom
regression between "EXPLAIN ANALYZE says 800 s" and "the query returns in 120 s".

This is not unprecedented: PG has the same class of caveat (instrumentation overhead, JIT
timing), and the repository already documents an analogous trap — `CLAUDE.md`'s
benchmark-timing hygiene section on server age and GC state.

## 3. Row-count reporting under fusion

Level *i*'s `rows` must equal what the unfused level-*i* `joinOp` would report: the number of
tuples that passed level *i*'s hash match **and** its residual. In the odometer
([04 §7](04-fusion-site-and-data-structures.md)) that is precisely the number of times the
descent proceeds past level *i*, so it is a single counter increment at the point where the
residual passes. Cheap and exact.

`loops` is 1 for every node (no rescan in a fused cascade), matching the unfused case.

## 4. The new gate this unlocks — and what it costs

Neither existing gate compares plan *shape* to PostgreSQL:

- `make plan-gate` (`Makefile:376-390`) diffs goopg's EXPLAIN against the newest
  `plan_snapshots/*.txt` — a **goopg-vs-goopg** regression detector. It SKIPs (exit 0) when
  there is no baseline or the server is unreachable.
- `scripts/pg-oracle-diff.sh` (`:1-44`) diffs **result text** between goopg and PG 18.3, with
  normalisations for psql timing/whitespace/prompts. It never looks at a plan.

So the proposal's benefit (1) is really: *after MHJ is gone from the plan space, a
goopg-vs-PG structural EXPLAIN gate becomes possible for the first time.*

Sketch of that gate (`scripts/pg-plan-shape-diff.sh`, new work, Stage 4):

1. run `EXPLAIN (COSTS OFF, VERBOSE OFF)` for each TPC-H query on goopg (65433) and on the
   PG 18.3 TPC-H reference (65432, `bench/tpch/README.md`);
2. normalise to a node-label tree: strip costs, strip `Buffers`, strip alias decorations,
   fold `Hash` sub-nodes (PG emits a separate `Hash` node under every `Hash Join`; goopg does
   not — this asymmetry alone is a design decision the gate must settle);
3. diff the label trees; report per-query `SHAPE-MATCH` / `SHAPE-DIFF`.

**Do not undersell the remaining work.** Removing MHJ removes *one* systematic difference.
Others certainly remain: goopg's `Nested Loop (%s)` composite label
(`operators_explain.go:1394-1396`), the absent `Hash` node, `Memoize` placement, `Gather`
degree. The gate should therefore ship in **report mode first** (never failing), and only
become blocking for queries whose shape is already green. Turning it on as a hard gate before
those asymmetries are settled would block every commit.

## 5. Plan snapshots

`plan_snapshots/*.txt` baselines were captured with MHJ on. Flipping `mhjPackingEnabled` to
`false` (Stage 4) changes goopg's EXPLAIN for every packing query, so `make plan-gate` will
report a large diff **by design**. The stage procedure must therefore be:

1. run `make plan-gate` **before** the flip and record it green;
2. flip;
3. run `make plan-gate` and **review every diff by hand**, confirming each is a
   `Multi-Way Hash Join (N tables)` node expanding into N−1 `Hash Join` nodes over the same
   scans and nothing else;
4. capture a new baseline with `make plan-snapshot-capture LABEL=post-mhj-retire`.

Skipping step 3 would let an unrelated plan regression ride in under the noise of an expected
diff. That is exactly how a silent row-count regression gets in.

Note `PLAN_DB`/`PLAN_USER` default to `tpch`/`tpch` in the Makefile (`:343-345`) and the gate
SKIPs silently when the server is not reachable — a SKIP is **not** a pass and must not be
recorded as one.

---

## 6. Two further asymmetries the shape gate must settle first (verified)

Removing `MultiHashJoin` removes *one* systematic difference from goopg's plan text. Two more
were measured on the committed snapshot and both are unconditional — they apply to **every**
plan, not just packing ones. Neither is addressed by anything in this bundle.

**(a) goopg never emits PG's `Hash` node.** PostgreSQL always renders a separate `Hash` node as
the inner child of every `Hash Join`. goopg does not:

```
$ grep -cE '^\s*->\s+Hash\s*(\(|$)' plan_snapshots/m0125-0043-after.txt   -> 0
$ grep -cE '^\s*->\s+Hash Join'      plan_snapshots/m0125-0043-after.txt   -> 40
```

40 `Hash Join` nodes, **zero** `Hash` nodes. A structural label-tree diff against PG would
mismatch on every hash join in every query even with MHJ gone. The gate must either synthesise
the `Hash` node on the goopg side or fold PG's away — a decision, not a detail.

**(b) goopg's costs and widths are placeholders.** Every node prints `cost=0.00..0.00` and
`width=0` (204 of each in the same snapshot; `operators_explain.go:378`, and
`docs/design/cost-model/README.md` says the same). Only `rows=` is real. A PG-parity plan gate
must normalise cost and width away — which means **it can never detect a cost regression**, and
that limitation belongs in its header, not in a footnote.

Together with the bushy-vs-left-deep finding ([02 §10](02-premise-audit.md)), the plan-shape
parity benefit is **three** steps away, not one. Ship the gate in report mode; do not let anyone
plan around it being green.
