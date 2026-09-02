# Planner refactor take 2 — Phase 0 report, 2026-09-02

Scope of this report: the work landed between `ec220754b` (the design bundle)
and `b7dd94928`. That is **Phase 0 of seven**, plus one finding from Phase 1's
territory. The refactor is not complete and this is not a completion report.

---

## 1. Headline: no performance change, by design — and that is the exit criterion

Phase 0 builds instruments. Its stated exit condition (TODO.md, 08 §3) is that
**no planner behaviour may change**. It did not.

Verified rather than asserted: the only code that reads the new cost annotation
is the EXPLAIN renderer.

```
$ grep -rn "PlanCostInfo\|DeriveLegacyDisplayCost" internal/ --include=*.go \
    | grep -v _test | grep -v plancost.go
internal/executor/operators_explain.go:1898   if pc, set := c.PlanCostInfo(); set {
internal/executor/operators_explain.go:1902   d := optimizer.DeriveLegacyDisplayCost(n, rows)
```

Nothing in path generation, costing, join search or `create_plan` consults it.
`stampPlanCost` only writes. So the 227.0 s TPC-H / 1173 s TPC-DS baselines in
07 §2 stand unchanged, and no timing sweep was run — running one would have
measured noise against an unchanged planner and cost several hours.

**The reportable result of Phase 0 is therefore not a time. It is that four
measurement instruments were wrong, and one planner input is missing
entirely.**

## 2. What was wrong with the instruments

Every number this project will produce is read out of `EXPLAIN`. Five defects
in that renderer were found and fixed; each was live, reproduced against the
running bench cluster, and each would have corrupted the corpus-wide plan-parity
roll-up Phase 0 exists to produce.

### 2.1 Eighteen node types printed their Go type name (`7677faaed`)

`describePlan`'s type switch falls through to `fmt.Sprintf("%T", n)`. Live,
before the fix:

```
EXPLAIN SELECT * FROM regexp_matches('abc','b','g')  ->  *optimizer.FromRegexpMatches
WITH RECURSIVE t(n) AS (...) SELECT * FROM t         ->  *optimizer.RecursiveUnion
SELECT DISTINCT ON (k) ...                           ->  *optimizer.DistinctOn
```

The 2026-09-02 nightly carries 21 regress-diff lines from three of these types.
Two *covered* arms also returned `%T` — invisible to an arm census, still a Go
type name at runtime.

Impact on the instrument: a census over EXPLAIN text measures its labeller. The
repository has paid this once already — the `BitmapHeapScan` arm's own comment
records that a plan census "read zero even once bitmap scans were being chosen".

### 2.2 Four node types hid their entire subtree (`7677faaed`)

`planChildren` never walked `DistinctOn`, `RecursiveUnion`, `RowsFrom` or
`Copy`. goopg rendered `SELECT DISTINCT ON (k) ...` as **one line** where PG
renders Unique / Sort / Seq Scan.

This is worse than a wrong label: **a truncated plan reads as agreement on
everything it does not show**, so a parity diff scores it a MATCH. Same class as
M0125-0037(i)'s missing `SetOp` arm, which truncated TPC-DS Q5/Q18/Q67 to four
lines.

Fixing the `RecursiveUnion` walk then exposed a doubled `CTE <name>` header: the
recursive self-reference is a `CTEScan` over the working table, so it claimed a
second section. It now renders PG's leaf `WorkTable Scan on <name>`, and goopg's
`WITH RECURSIVE` plan is structurally identical to PG's — the two remaining
differences (`Values` vs `Result` for the anchor, and the `t_1` suffix) are real
divergences, now visible instead of hidden.

### 2.3 Schema qualification followed no mode (`2a63fbe21`)

PG qualifies a relation in VERBOSE only (`explain.c:4409-4411`) and **never**
qualifies an index (`explain_get_index_name`). Measured against :65432:

```
PG    plain:   Index Scan using orders_pk on orders
PG    verbose: Index Scan using orders_pk on public.orders
goopg (both):  Index Scan using public.orders_pk on public.orders
```

One guaranteed rendering divergence on **every scan node of every plan**. A
corpus roll-up taken before this would have measured the renderer and nothing
else. Both modes are now byte-identical to PG on the probe.

### 2.4 `EXPLAIN` and `EXPLAIN ANALYZE` disagreed on `rows=` (`5309bf402`)

The plain walker took the row estimate from the collapsed `Filter` wrapper; the
ANALYZE walker took it from the node beneath. Negative control, defect
reintroduced:

```
plain EXPLAIN   rows=50
EXPLAIN ANALYZE rows=10000
```

A **200x** overstatement of the planner's own estimate, on filtered scans, in
the mode most artefacts are captured in — actual row counts are the point of
ANALYZE, so parity captures use it.

### 2.5 A plan-shaping flag no artefact could name (`f2ac4fdfc`)

`GOOPG_INDEX_PROBE_MULT` multiplies NL-index probe cost — it moves the
hash-vs-NL crossover on every join — and was in neither the provenance table nor
the exemption list. The guard that exists to prevent exactly this missed it: its
detector matched only a literal `os.Getenv("GOOPG_…")`, and this flag is read
through a helper. Adding the row without fixing the detector would have left the
next helper-wrapped flag equally invisible, so both landed together and the
detector is now a `go/ast` string-literal walk.

## 3. EXPLAIN now states what the planner believed (`9cbc7661b`)

Both text walkers hard-coded `cost=0.00..0.00 ... width=0`; FORMAT JSON emitted
no cost keys at all. The search computes a real cost for every candidate and
`add_path` picks the winner by comparing them — and `createPlan` discarded the
winning numbers. So no artefact stated what the planner believed, and none could
attribute a wrong plan to a wrong cost.

`PlanCost` is now embedded in the node, as PostgreSQL carries `startup_cost`,
`total_cost`, `plan_rows`, `plan_width` on `struct Plan`. The side-index design
was tried first and abandoned for three independent reasons recorded in
impl/P0-A §3.

The instrument earned itself on its first use. On a filtered TPC-H aggregate:

```
PG     Parallel Seq Scan on lineitem  (cost=0.00..148092.42 rows=645434 width=2)
goopg  Parallel Seq Scan on lineitem  (cost=0.00..80016.73  rows=2000418 width=550)
```

Three divergences, all previously invisible:

- **`width=550` vs `width=2`.** goopg has no `PathTarget`, so it carries the
  whole tuple where PG projects to the two bytes it needs. That is P4-01's case,
  now measurable rather than argued.
- **No per-worker row scaling** on the parallel node.
- **`rows=2000418`**, which is exactly `6 001 255 / 3`. That one turned out to
  be the session's most important finding.

## 4. The finding that outranks everything else in this report

`rows = relation_rows / 3` is `DEFAULT_INEQ_SEL`. The planner was estimating
that date predicate **blind**. Chasing it produced
[impl/FINDING-histograms-lost-on-restart.md](../../docs/design/not_ralph/planner_refactor_take2/impl/FINDING-histograms-lost-on-restart.md):

| state | goopg `rows=` |
|---|---|
| fresh connection after restart | **2 000 418** (= rows/3) |
| after `ANALYZE lineitem` in session | 2 582 059 (PG: ~2.58 M — good) |
| different connection, same server | 2 582 059 (stats **are** shared) |
| after stop/start | **2 000 418** |

`pg_stats` confirms `histogram_bounds` is 101 / 49 / 101 for
`l_shipdate` / `l_quantity` / `l_comment` after ANALYZE and **NULL for every
column after a restart**. `n_distinct` survives; the relation size survives.
Only the histograms are lost — and for narrow columns too, so this is not the
wide-text TOAST gap P1-11 records.

Why it outranks the cost model: without a histogram every range predicate falls
to `DEFAULT_INEQ_SEL`. Twelve of the 22 TPC-H queries carry one. The error
propagates multiplicatively into exactly the join-order decisions this refactor
is about, and **no cost-model fidelity can recover an input that is not there.**

The benchmark lifecycle restarts the server and nothing runs an in-session
ANALYZE. So the recorded goopg figures — the 227.0 s / 9.9x headline among them
— were almost certainly measured on a planner with no histograms. That does not
make them wrong, but it means the gap attributable to *planning logic* cannot be
separated from the gap attributable to *missing statistics* until this is fixed.
It moves to the head of Phase 1.

## 5. Corrections to the design bundle

The bundle was reviewed before it was written against; agent review of the two
implementation designs, plus measurement, corrected the following. Each is
recorded in TODO.md.

| claim | status |
|---|---|
| TPC-DS row anchors are inert (`P0-10`) | **Dropped.** Already fixed by `63056c544`, 2026-07-30 — a month before the bundle was written. A `git log -S` would have refuted it. |
| The nightly silently passes a plan gate that never ran (`P0-08`) | **Wrong.** ci/batch calls `make plan-diff` with an explicit pinned `LABEL`, not `plan-gate`, and reports drift by design. Also `plan-gate`'s `ls -t` selects `warm-stats-base`, not `m0077-final`. The re-pin is three coordinated edits. |
| 83 of 95 oracle rows read `0s` (`P0-09`) | **54**, not 83 — and nothing reads the column. Rescoped to a two-line timer change with no re-capture. |
| `estimate-audit` has no PG side and no shape comparison | **Wrong.** `--reference`/`--ref-port` exist and `spine.go` compares join pairings. "Never measured" was overstated. |
| P0-11 needs a new `addPath` trace | **Rescoped.** A `DPTRACE` channel already exists at join-pair granularity; P0-11 extends it, and emitter and parser must land together or `Malformed` inflates. `addPartialPath` was omitted from the design. |
| P1-11b is the highest-value Phase 1 item | **Rationale wrong in both directions.** ISO date strings sort in date order, so the bucket is found correctly and only the within-bucket fraction defaults — ~0.5% at 100 buckets, not 0.31-vs-0.14. And it is moot on a restarted server. |
| A cost side-index preserves `Plan`'s signature | **Wrong.** No per-statement context exists on the chain; `createPlanNode` returns bottom-up; a pointer-keyed map collapses shared CTE subtrees. |
| `describePlan` has 28 arms; `get_typavgwidth` prefers `stawidth`; `cost_material` is blocking; goopg has a `Materialize` node | All wrong; corrected in impl/P0-A. |

Two measured facts the bundle did not have: the TPC-H clusters hold **different
`lineitem` loads** (+0.040%, HammerDB randomises lines per order), and
`shared_buffers` is **4x apart** on top of the `work_mem` and
`effective_cache_size` gaps P0-12 records.

## 6. State and confounds

- goopg TPC-H :65433 is **up**, running a binary built from `b7dd94928`.
  goopg TPC-DS :65437 is **down**. PG :65432 and :65438 are up.
- **The bench cluster state changed.** Diagnosing §4 ran `ANALYZE lineitem` on
  :65433 several times and restarted it repeatedly. Any A/B timing straddling
  2026-09-02 on that port is confounded and must be re-based.
- `plan_snapshots/` was not re-pinned; the label-changing commits (§2.1-2.3)
  invalidate the goopg-vs-goopg baselines, and re-pinning is part of P0-08,
  still open.

## 7. What is done, and what is next

Closed: P0-01, P0-02, P0-03, P0-04b, P0-04c, P0-04d (new), P0-10 (dropped).
Rescoped: P0-08, P0-09, P0-11.
Open in Phase 0: P0-04 (rtindex-order numbering), P0-05 / P0-06 / P0-07 (the
plan-parity instrument and its baseline), P0-08, P0-09, P0-11, P0-12, P0-13,
P0-04e (JSON wrapper collapse).

Recommended next step, in order:

1. **The histogram-loss fix** (§4). It has a four-step resume point. Everything
   in Phase 1 is measured against a blind planner until it lands.
2. P0-05 / P0-06 / P0-07 — the parity instrument, now that the renderer it reads
   is trustworthy. This was deliberately not built first: a corpus roll-up taken
   before §2.3 would have reported a divergence on every scan node.
3. P0-12 before P2-02b, per the ordering already recorded.
