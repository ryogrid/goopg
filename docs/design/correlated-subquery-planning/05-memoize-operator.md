# 05 — Memoize-Style Parameterized Result Cache

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| part of | [correlated-subquery-planning bundle](README.md) |
| owns phase | S7 Memoize operator (see [08-roadmap-and-milestones.md](08-roadmap-and-milestones.md)) |
| depends on | D4.2 rescan contract ([04](04-subplan-execution-engine.md)), D6.2 NLI semi/anti ([06](06-cost-model-touchpoints.md)) |

## 1. Purpose and honest scoping

This chapter designs a **Memoize operator**: a plan-level cache that sits on the
inner side of a parameterized nested-loop join and returns previously computed
inner-side result sets when the same parameter values recur, instead of
re-scanning the inner side.

Two scoping statements up front:

1. **Memoize is a *join* mechanism, not a SubPlan mechanism.** In PostgreSQL,
   Memoize caches the inner side of a parameterized nested loop
   (`postgres/src/backend/optimizer/path/joinpath.c:675`,
   `get_memoize_path`); it never wraps a `SubPlan`. The SubPlan-side caching
   story for goopg is chapter [04](04-subplan-execution-engine.md) (D4.3/D4.4).
   This chapter deliberately preserves that separation (decision D5.2).
2. **Memoize is the *last* phase of this bundle (S7), not a TPC-H unlock.**
   In the SF1 plan comparison
   (analysis/tpch/goopg-pg-tpch-plan-compare-260718/ — on origin/master,
   commit be4f0291; not on branch wal-pg-nodetree), **PostgreSQL 18.3 itself
   memoizes none of the 22 TPC-H queries**: the §4 plan table shows Gather,
   parallel/plain hash joins, index nested loops, bitmap and index-only scans,
   but no `Memoize` node anywhere. TPC-H's uniform key distributions give few
   repeated inner parameters, so the cache does not appear — whether because
   costing rejects it or simply because the chosen plans (hash joins, no
   parameterized nested loops on those inners) never offer an insertion
   point is not distinguishable from the plan text alone. The measured
   HEAD gap on the correlated queries (≈30–40× — Q22 1.80 s, Q4 7.41 s at SF1,
   [measured-at-HEAD e4a43ba6],
   [evidence/unnest-probes-e4a43ba6.txt](evidence/unnest-probes-e4a43ba6.txt))
   is therefore *not* addressable by Memoize; it is addressed by S1–S6.
   Memoize's value is architecture completeness and non-TPCH workloads: skewed
   foreign keys, OLTP-ish repeated lookups, LATERAL-style patterns, and —
   only where the join is provably `inner_unique` (see §2) — the NLI
   semi/anti joins introduced by D6.2.

## 2. Semantics (PG-faithful contract)

Oracle: `postgres/src/backend/executor/nodeMemoize.c` and
`postgres/src/backend/optimizer/path/joinpath.c`.

- **Cache key = the parameter values** the inner side is parameterized by. In
  PG the keys are the `param_exprs` collected by
  `paraminfo_get_equal_hashops` (joinpath.c:439); in goopg they are the outer
  key columns an NLI join feeds into the inner index probe
  (`NestedLoopIndexJoin`'s outer key set — `internal/planner/nl_index_join.go`).
- **Complete entries only.** A cache entry may be served only if the inner
  scan for that key ran to exhaustion and every row was stored. If the entry
  overflows the memory budget mid-build, it is abandoned and the scan is
  passed through uncached (PG behaves the same way; entries are marked
  `complete` at inner-scan end — nodeMemoize.c:800, :882 — or immediately
  after the first tuple in `singlerow` mode, :832).
- **`singlerow` mode.** When the planner proves the inner side yields at most
  one row per key (unique index probe), the cache stores single tuples and can
  mark entries complete after the first row (PG: `singlerow`,
  nodeMemoize.c:37, :832, :1058). goopg's NLI inner side is a single-column
  B-tree probe, so this is the common mode.
- **LRU eviction under a memory budget.** Entries are evicted
  least-recently-used when the cache exceeds its budget; a key whose entry was
  evicted is simply recomputed (PG sizes the cache from `est_entries`
  computed at plan time and enforces `hash_mem` at run time). goopg budget: a
  fraction of `Context.WorkMem` (see §5 and D6.4 in
  [06](06-cost-model-touchpoints.md)).
- **Correctness preconditions** (all must hold before the planner inserts the
  node — PG checks these in `get_memoize_path`, joinpath.c:675):
  1. The inner side is **rescannable without side effects** and yields
     identical results for identical parameter values within one snapshot
     (no volatile functions in the inner quals/targets).
  2. Every parameter the inner side depends on is part of the cache key
     (**cache-key completeness** — PG rejects lateral vars it cannot key,
     joinpath.c:675ff). A missed dependency silently serves wrong rows; this
     is the highest-severity correctness risk of the operator.
  3. Each key column's type supports the equality semantics the cache uses
     (PG requires hashable equality operators via
     `paraminfo_get_equal_hashops`).
  4. **Semi/Anti joins only when `inner_unique`.** `get_memoize_path` refuses
     SEMI/ANTI joins outright unless the join is marked `inner_unique`
     (joinpath.c:721-728): a nested-loop semi/anti probe stops early instead
     of scanning the inner to completion, so the cache entry can never be
     marked complete. This directly constrains D6.2 — the NLI-semi early-out
     design in [06 §3.2](06-cost-model-touchpoints.md) precludes Memoize
     under those joins except in the unique-inner case.

## 3. D5.1 — Planner insertion rule

**Decision: insert Memoize inside `rewriteJoinsToNLI`
(`internal/planner/nl_index_join.go:72`), as a conditional wrapper on the NLI
inner side, gated on estimated duplicate outer keys.**

Rationale: `rewriteJoinsToNLI` is the single place goopg creates
parameterized inner scans, and it already runs after unnesting and after
`tryBushyDP`, so the outer/inner roles and key columns are final. This mirrors
PG, where `get_memoize_path` is consulted exactly where nested-loop paths are
formed (joinpath.c:1977, :2193).

Insertion algorithm (planner side):

1. `tryBuildNLI` (nl_index_join.go:284) succeeds → we have
   `outer`, `innerScan`, `idx`, and the outer key column(s).
2. Compute `outerRows = EstimateRows(outer)`
   (`internal/planner/cardinality.go:38`) and `ndistinct(outer key)` from
   ANALYZE statistics (per-column ndistinct; the same statistics the DPccp
   path requires — `tryBushyDP` refuses to run without ANALYZE stats, and
   Memoize adopts the same rule: **no stats → no Memoize**).
3. Expected cache hit fraction `h ≈ 1 − ndistinct/outerRows` (clamped to
   [0,1]). Insert Memoize only when
   `h ≥ memoizeMinHitFraction` (initial constant 0.5) **and**
   `outerRows ≥ memoizeMinOuterRows` (initial constant 1000), i.e. at least
   half the probes are expected to be repeats and the join is big enough for
   the bookkeeping to pay off.
4. Estimate `est_entries = min(ndistinct, budget / est_entry_bytes)` and
   store it on the plan node for the executor's initial hash sizing —
   the goopg analog of the `est_entries` computed by
   `cost_memoize_rescan` (`postgres/src/backend/optimizer/path/costsize.c:2541`).
   Full cost integration (charging cached vs uncached rescan cost into a join
   cost comparison) is deferred with the rest of the cost model to the 0077
   line; the S7 gate is the heuristic in step 3, matching the repo's current
   heuristic NLI gate `nliCostGateAccepts` (nl_index_join.go:918), which
   accepts on `outerRows ≤ 100000` and is optimistic when stats are absent —
   Memoize is deliberately **stricter** (stats mandatory) because a wrong
   Memoize is pure overhead plus correctness risk, while a wrong NLI is only
   slow.
5. Volatility check: reject when the inner scan's residual filter contains
   volatile functions (precondition §2 item 1). The allowlist-based
   volatility/side-effect classifier (stable/immutable builtins pass;
   unknown → reject) is **built in S2** as part of chapter 04's D4.2
   cacheability gate; D5.1 reuses it here unchanged.

New plan node: `Memoize{Child Node, KeyCols []ColumnRef, SingleRow bool,
EstEntries int64}` in `internal/planner/plan.go`, printed by
`internal/executor/operators_explain.go` like any other node.

## 4. D5.2 — Scope decision and shared cache library

**Decision: Memoize is a join-side plan operator only (PG-faithful); it does
not replace or wrap the chapter-04 SubPlan caches. Both consume one shared
internal cache library.**

The alternative — one "universal result cache" that serves both SubPlan
memoization (D4.4) and join memoization — was considered and rejected: the two
have different keys (SubPlan: correlation-param projection; Memoize: join key
columns), different lifecycles (SubPlan caches live on `Context`,
`internal/executor/context.go:99-123`; Memoize state lives in the operator),
and PG keeps them separate for the same reason (nodeSubplan.c hashed SubPlan
vs nodeMemoize.c). Merging them couples chapter 04's S2/S3 delivery to S7.

What *is* shared: a small internal package (working name
`internal/executor/kvcache`) providing datum-tuple hashing, logical datum
equality, LRU tracking, and byte-accounted memory budgeting. Consumers:
D4.3 hashed SubPlan, D4.4 projected-key SubPlan cache, and this operator.

## 5. Executor design

Operator `memoizeOp` (new file `internal/executor/operators_memoize.go`),
state machine modeled on `ExecMemoize` (nodeMemoize.c:697):

| state | meaning |
| --- | --- |
| `lookup` | first `Next()` after a `Rescan(params)`: probe the cache with the current key (prepared like `prepare_probe_slot`, nodeMemoize.c:302) |
| `serveCached` | key found complete → emit stored rows one by one |
| `fillCache` | key absent → pull from child, tee each row into the entry, emit it; mark complete at child EOF (or after first row when `SingleRow`) |
| `passThrough` | entry abandoned (budget overflow) → stream child rows uncached |

Key points:

- **Rescan integration.** Memoize slots into the NLI driver's *existing*
  slot-based inner protocol (D5.3 below): per outer row the driver calls
  `BindOuter`/`Rescan(slot, width)` on its inner side; when that inner is a
  `memoizeOp`, the rescan extracts the key datums from the bound slot and
  either serves from cache or forwards the rescan to the child. Child
  forwarding is where chapter 04's D4.2 rescan work is consumed (a cache
  miss must re-drive the child cheaply), which is why S7 follows S2 in the
  roadmap — but note the NLI seam and the `subPlanHandle` protocol are
  distinct seams ([04 §4.1](04-subplan-execution-engine.md)).
- **Key normalization: logical datum comparison, not byte-image comparison.**
  The cache compares keys with the same logical equality the join predicate
  uses (via the shared `kvcache` datum equality). PG additionally supports
  `binary_mode` (nodeMemoize.c:169, :1065), which it enables in two cases:
  when the join operator is **not hashable** — hash equality may then be
  coarser than the operator itself (the `-0.0`/`+0.0` float example,
  joinpath.c:509-520), so bit-by-bit comparison is required — and **always
  for lateral-Var keys**, where PG has no visibility into how the value is
  used (joinpath.c:561-570). goopg **skips `binary_mode` initially**, which
  is defensible against that criterion: the D5.1 insertion rule only creates
  Memoize under NLI joins whose key equality *is* the B-tree/hash equality
  of the allowlisted scalar key types (ints, dates, text with the default
  collation), i.e. exactly the case where PG itself stays in logical mode;
  and goopg has no LATERAL yet, so the mandatory-binary case cannot arise.
  Revisit when either non-hashable join operators or LATERAL keys become
  possible.
- **Memory accounting.** Every stored row is byte-accounted against the
  operator budget `min(EstEntries * est_entry_bytes, ctx.WorkMem / 4)`;
  `Context.WorkMem` is the same budget the hash-join build side spills
  against (`internal/executor/operators_join_agg.go:418-420`, default
  512 MiB). Memoize never spills — overflow evicts LRU entries, and if the
  *current* entry cannot fit it is abandoned (`passThrough`), exactly PG's
  behavior.
- **EXPLAIN.** The node prints as `Memoize` with `Cache Key: <cols>`; under
  `EXPLAIN ANALYZE` it reports `Hits: n  Misses: n  Evictions: n  Overflows:
  n` counters like PG. These counters ride the S0 instrumentation channel
  defined in [07-verification-and-measurement.md](07-verification-and-measurement.md)
  §6.

### D5.3 — NLI inner contract change (prerequisite executor/plan work)

The insertion point in D5.1 is not a drop-in wrapper today, because the NLI
join's inner side is **contractually a concrete index scan**, not a generic
child:

- `NestedLoopIndexJoin.Inner` is typed `*IndexScan`
  (`internal/planner/plan.go:633-640`), and the executor builds a concrete
  `*indexScanOp` ("Inner is always an *IndexScan by plan-node contract",
  `internal/executor/executor.go:149-155`).
- The driver's inner protocol is concrete and slot-based: `openPrep(ctx)`
  once (`operators_nljoin.go:120`), then per outer row
  `BindOuter(o.outerMS, w)` + `Rescan(o.outerMS, w)`
  (`operators_nljoin.go:239-241`; signature
  `Rescan(outerSlot SlotView, outerWidth int)`,
  `internal/executor/operators_index.go:345`).

**Decision D5.3:** S7 therefore includes, as explicit scope:

1. widening `NestedLoopIndexJoin.Inner` to a `Node` (or a small interface)
   so the planner can interpose `Memoize{Child: *IndexScan}`, with the
   executor keeping a fast path when the inner is a bare index scan;
2. `memoizeOp` implementing the `openPrep`/`BindOuter`/`Rescan(slot, width)`
   protocol, extracting the cache-key datums from the bound outer slot
   (the same columns the index probe consumes), and forwarding
   `openPrep`/rescans to its child `indexScanOp`;
3. the shared `kvcache` library (D5.2) is **built in S2 by chapter 04**
   (consumers D4.3/D4.4 land first); S7 consumes it — this sequencing is
   part of the S2 deliverable list, not an S7 afterthought.

This is deliberately *not* the `subPlanHandle` protocol from
[04 §4.1](04-subplan-execution-engine.md): the NLI seam predates this bundle
and stays slot-based; any future unification happens via the interface
promotion criteria in 04 §10.1.

## 6. Applicability evidence (why S7 is last)

- TPC-H SF1: PG 18.3 memoizes **zero** of 22 queries under the comparison
  GUCs (analysis/tpch/goopg-pg-tpch-plan-compare-260718/, §4 plan table — on
  origin/master, commit be4f0291). goopg should not expect TPC-H wins either;
  the S7 acceptance gate is therefore a microbenchmark (skewed-FK join,
  §[07](07-verification-and-measurement.md)) plus plan-stability on TPC-H
  (Memoize must NOT appear at SF1 — appearing would signal a broken gate).
- Where it will fire: skewed outer keys (Zipfian FK joins), NLI semi/anti
  joins from D6.2 that are provably `inner_unique` **and** whose outer side
  repeats keys (Q4-shaped `orders → lineitem` probes share `o_orderkey`?
  No — orderkey is unique per outer row; but `customer → nation`-shaped
  lookups repeat heavily), and future LATERAL support (which will also force
  `binary_mode` — §5).
- Rollout guard: a session GUC `enable_memoize` (default `on`, mirroring PG's
  GUC of the same name) so the operator can be disabled without a rebuild;
  registered per the repo's GUC discipline
  (`internal/config/defaults.go` + `postgresql.conf.sample` sync test).

## 7. Open questions

1. **Is S7 worth scheduling before LATERAL exists?** Without LATERAL, the
   only Memoize consumers are NLI inner probes; with unique-key probes the
   hit rate is near zero except for FK-shaped keys. The roadmap keeps S7 last
   partly for this reason; re-evaluate after S6 lands with real
   NLI-semi/anti plan shapes.
2. **Budget sharing.** Should Memoize's budget come out of the same
   `ctx.WorkMem` pool the hash joins draw on (risking double-counting when a
   Memoize sits under a hash-join build) or a separate accounting bucket?
   Initial answer: same pool, `WorkMem/4` cap per operator, revisit with
   D6.4's budget audit.
3. **ndistinct trustworthiness.** goopg's ANALYZE ndistinct on FK columns at
   SF1 is untested for this purpose; S0's instrumentation should record
   predicted-vs-actual hit fractions so the S7 gate constants
   (`memoizeMinHitFraction`, `memoizeMinOuterRows`) can be tuned from data.
4. **Multi-column keys.** `tryBuildNLI` currently matches single-column
   B-tree indexes; if composite-key NLI lands later, the cache key and
   `paraminfo_get_equal_hashops`-style per-column checks must generalize.
