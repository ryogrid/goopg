# 15 — MultiHashJoin under cost-driven order (DP-integrated, star/snowflake shapes)

| field | value |
| --- | --- |
| status | v2 DP-integration **IMPLEMENTED & MEASURED — reverted (MHJ cannot be cost-forced for Q9)**; the OOM half landed separately (composite-NLI-keep, commit 4093b487) |
| date | 2026-07-24 |
| depends on | [06](06-scan-and-join-path-costs.md), [07](07-cost-driven-join-order.md), [12](12-pg-style-join-path-enumeration.md), [13](13-composite-nli-layout-reconciliation.md), [14](14-fk-aware-and-mcv-join-selectivity.md) |
| premise | The cost-driven binary-hash cascade OOMs on Q9 because it materialises 6M-row wide intermediates a single MHJ probe-pass streams. Let the **DP itself** cost an MHJ candidate for composite-free single-column-key tree subsets, so it emits MHJ where — and only where — the operator is valid and cheaper. |

> **STATUS UPDATE (2026-07-24, measured on real SF1).** The v2 DP-integrated MHJ
> (`maybePackMHJ`, `collectMultiHashTablesShape(allowStar)`, `layoutFromMHJ`) was
> implemented and is **correct** (result parity green on all 22, both planners) — but it
> **never fires usefully for Q9 and was reverted**. Two measured facts closed it:
>
> 1. **The MHJ never wins on cost for Q9's key subset.** With debug logging, at an
>    intermediate-materialisation penalty of **100×** (`GOOPG_MAT_MULT=100`), the
>    `{nation,supplier,lineitem,orders}` subset still costs **416673 as an MHJ vs 393420 as
>    the binary cascade** (`WIN=false`). `orders` (1.5M) is a large dim to build, and the
>    per-subset cardinality estimates are inconsistent, so the flat `multiHashJoinCost`
>    (numDims × probeRows) exceeds the cascade. Pushing the penalty higher distorts all 22
>    queries' costs.
> 2. **The blocker underneath is the join ORDER, not the MHJ cost.** `EXPLAIN ANALYZE` on
>    cost-driven Q9 (total **804 s**) shows the cost is per-row cascade overhead: pulling 6M
>    lineitem rows through **two separate** hash-join operators (the `(supplier⋈nation)⋈
>    lineitem` join alone is 266 s, and probing that 6M through the `part` join is ~500 s
>    more) at ~117 µs/row, vs the integer planner's **fused** MHJ at ~20 µs/row (118 s
>    total). The DP correctly prefers joining the *filtered* `part` early (its green filter
>    reduces 6M→322K, making later joins cheap) — which fractures the MHJ-eligible subset.
>    Flipping that preference is order surgery that changes every cost-driven query's plan.
>
> **Conclusion:** the MHJ fusion win is real (~6× per-row, ~700 s → ~120 s) but the
> cost-driven DP cannot be made to emit it for Q9 without all-query-distorting constants —
> the same executor-vs-model divergence doc [14] and doc [09] §0 name at the ORDER level.
> The v2 code is reverted. **What DID land** from this line of work: the composite-index
> NLI-keep (commit 4093b487) — Q9 no longer OOMs/crashes (partsupp stays an exact
> `partsupp_pk` probe instead of a fanning-out single-column hash). Q9 completes but remains
> above the 118 s integer-planner MHJ time; this is a documented structural residual, not a
> correctness gap.

## 0. Why this chapter exists

Doc [14] closed the cardinality/constant axis and named Q9's residual **structural
(MHJ-drop)**. Doc [15] v1 proposed re-admitting the existing post-DP packer
(`rewriteMultiWayChain`) under cost-driven order with a relaxed "star" shape test.
**Agent review rejected v1 at SEV-1:** that packer replaces a whole contiguous subtree with
one MHJ node, so "exclude the composite table (`partsupp`) but keep its join" is not
realizable by post-collection set-subtraction — it would discard `partsupp`'s scan and join
entirely, and strand its `ps_suppkey` residual on a dropped column. The review confirmed the
MHJ *executor* and the *tree* acceptance predicate are sound; the hazard was entirely in
discovering membership by walking a mixed final tree.

**This v2 removes the discovery problem by deciding membership in the DP.** The DP already
enumerates every relation subset; for a subset it can test *structurally* whether the subset
is a clean MHJ candidate (composite-free, single-column, tree-shaped) and cost it against the
binary split. A composite table like `partsupp` never enters a qualifying subset — its join
carries a second cross-edge, which disqualifies any subset containing that table-pair, so
`partsupp` is joined by an ordinary binary node at a higher DP level. No exclusion, no
boundary pruning, no stranded residual.

## 1. Where MHJ is decided today, and the target

- Live packing: `rewriteMultiWayChain` (`bushy.go:1719`) runs once post-DP
  (`planner.go:1003`), gated by `mhjPackingEnabled`, which `bushy.go` `init()` forces to
  `false` under `costDrivenJoinOrder`.
- The DP: `enumerateBushyPlans` (`bushy.go:646`) fills `dp[mask]` (a `dpEntry{plan, rows,
  cost, layout, pgCost}`, `bushy.go:522`) by enumerating 2-way splits of each subset and
  keeping the cheapest binary join (`costJoinCandidate`, `bushy.go:626`).
- Dormant substrate: `generateMultiHashJoinPath` (`pathgen.go:105`) + `multiHashJoinCost`
  (`cost_funcs.go:169`) already cost an MHJ path against the equivalent cascade in PG units,
  but nothing wires them into the live `bushy.go` DP.

Target: under `costDrivenJoinOrder`, the DP considers an MHJ candidate for each qualifying
subset, competes it against the binary split by `pgCost`, and emits the MHJ when cheaper —
exactly `add_path`/`set_cheapest` semantics, in the existing DP.

## 2. The qualification test (structural, on the join graph — not on a built tree)

For subset `mask`, `mhjSubsetQualifies(mask, g) (probeIdx int, ok bool)`:

1. `popcount(mask) ≥ 3` (an MHJ of <3 tables is just a binary join).
2. **Composite-free:** enumerate the join-graph edges with both endpoints in `mask`; if any
   *table-pair* is connected by >1 edge (a composite key like
   `partsupp↔lineitem` on `ps_partkey`+`ps_suppkey`), **return false**. This is the single
   structural fact that keeps `partsupp` out — computed on `g.edges`, not on flat `extras`.
3. **Single-column keys only:** every in-`mask` edge's `leftKey`/`rightKey` resolves to a
   base-table `*ColumnRef` (no expression keys).
4. **Tree:** the in-`mask` edge set is connected and `#edges == #tables − 1`. A chain, a star
   (`lineitem` centre), and a snowflake (`lineitem→supplier→nation`) all pass; a cycle fails.
   This is the exact shape the MHJ executor's spanning-tree BFS (`multi_hash_join.go:126-165`)
   traverses, and the executor *silently corrupts* non-tree input (drops keys / null-pads
   unreached dims), so the gate must assert `#edges == #tables − 1` explicitly, not just
   connectivity + degree.
5. **No self-join:** if any two in-`mask` tables share the same `*catalog.Table` pointer AND
   alias-collide, return false (defensive — the by-index MHJ executor is fine, but self-join
   subsets are rare in the fact-star shapes this targets and are not worth the key-attribution
   risk; they fall back to the binary plan, which is safe). TPC-H Q9 has none.
6. `probeIdx` = the in-`mask` table with the largest post-filter rows (`dpEntry.rows` of the
   singleton, or `tableRows`), matching `collectMultiHashTables`'s probe rule
   (`bushy.go:1599`).

## 3. Costing and selection (add_path in the DP)

After the 2-way-split loop has chosen `best` for `mask`, and only when `costDrivenJoinOrder`
and `mhjSubsetQualifies(mask,g)`:

```
mhjCost = multiHashJoinCost(cp,
    probeScanCost, probeRows,      // dp[{probe}].pgCost, dp[{probe}].rows
    dimScanCosts,  dimRows,        // dp[{dim_i}].pgCost, dp[{dim_i}].rows for each dim
    outRows)                       // = best.rows (the subset cardinality already computed)
if mhjCost.Total < best.pgCost.Total:   // pgCost-ranked, like every cost-driven choice
    best.plan   = buildSubsetMHJ(mask, probeIdx, best.plan, g, cat)
    best.pgCost = mhjCost
    // best.rows unchanged (same subset, same cardinality)
    best.layout = layoutFromMHJ(best.plan)   // §4 — MANDATORY
```

`multiHashJoinCost` charges each dim a build pass + one flat probe pass over the fact
(`cost_funcs.go:177-192`); its "all dims per probe row, no per-step selectivity" simplification
means it is **not** provably ≤ the binary cascade for every selectivity profile (review
SEV-3). So the `<` comparison is **load-bearing and mandatory**, never assumed: when a
selective early binary join would process fewer rows downstream, the DP keeps the binary
plan. This is the honest `add_path` contract, and it is the whole safety of the cost story.

## 4. The layout hazard (new, DP-specific — the one real correctness risk)

`buildSubsetMHJ` reuses the packer's construction on `best.plan` (the binary tree for
`mask`), which **sorts the MHJ's `Tables` by catalog OID** (`bushy.go:1760+`) so the MHJ
`Output()` is OID-ordered. But `dpEntry.layout` (`bushy.go:522` doc) maps each base table to
its column offset in the *pre-pack* bushy schema (`leftSchema ++ rightSchema`, arbitrary
order). A parent join at subset `mask ∪ {partsupp}` calls `buildJoinFromDP`, which uses
`dp[mask].layout` via `remapKeyToLayout` to place the join key — so a **stale layout after
packing puts the parent's key on the wrong column ⇒ wrong rows** (the same desync class as
doc [13], here at the DP handoff).

**Fix (mandatory):** after packing, recompute `best.layout` from the MHJ's OID-sorted
`Tables`: `layoutFromMHJ` walks the MHJ `Output()` and records each base table's start
offset. Because an MHJ is always over base-table `*SeqScan` leaves in one scope (the executor
and `collectMultiHashTables` both stop at `Project`/`Aggregate`/`Filter` boundaries), this is
a pure function of the OID sort and needs no name resolution. A unit test must assert that
`dp[mask].layout` after packing maps every table to the offset its columns actually occupy in
`best.plan.Output()`.

Because membership is fixed and the MHJ is built directly from `mask` at DP time, **no
`reconcileNLILayout` change is needed** — v1's §4 (re-resolving `MultiHashKey` against a
merged schema) was rejected by review as wrong-coordinate-space (the keys are per-table-local
and already name-resolved at construction) and is dropped.

## 5. Construction: `buildSubsetMHJ`

Reuse the tested packer body, fed a guaranteed-safe input. Add an `allowStar bool` to
`collectMultiHashTables`/`rewriteMultiWayChain`: when true, replace the chain `chainOK`
degree-≤2 reject (`bushy.go:1586`) with the §2 tree assertion (`#keys == #scans − 1`). Call
`rewriteMultiWayChain(best.plan, cat, /*allowStar=*/true)` **only** from the DP MHJ path, on
a subset already proven composite-free and single-column by `mhjSubsetQualifies` — so the
`extras` capture is empty and the review's SEV-1/SEV-2 exclusion hazards cannot arise (there
is nothing to exclude). The post-DP whole-tree pass stays disabled under cost-driven
(`mhjPackingEnabled = false` unchanged), so the packer never walks a mixed tree containing
`partsupp`.

## 6. Scope, acceptance, gates

- **Scope:** `costDrivenJoinOrder`-gated; production integer DP byte-identical (default-planner
  parity). No new GUC. The star-accepting packer branch is reachable only from the DP MHJ path.
- **Correctness (green before commit):**
  - `GOOPG_COST_DRIVEN_JOINORDER=1` **and** default: `TestTPCHResultParity` all 22 identical.
  - New planner unit test: a Q9-shaped fixture asserting (a) the DP emits an MHJ over the
    single-column-tree subset with `partsupp` as a binary parent join, (b) `dp[mask].layout`
    matches `Output()` post-pack, (c) the row count is canonical.
  - `go test ./internal/planner/ ./internal/executor/` green.
  - `scripts/tpch-spotcheck.sh` — canonical `Q12`/`Q13`, fresh capped server.
- **Performance (same-session ANALYZE — stats are per-connection):** cost-driven Q9 completes
  **without OOM**, at ≈ the integer planner's MHJ time (~118 s cold SF1), not timeout. Target
  is MHJ-shape parity, not the 27 s warm past-best (out of scope by no-TPC-H-tuning). No
  regression on Q5/Q7/Q8/Q10.
- Server via `scripts/csq-bench-server.sh` (capped, 65433); commit with pathspec, never
  `--no-verify`.

## 7. Divergence from PostgreSQL

Unchanged from v1: PostgreSQL has no MultiHashJoin; goopg's is a deliberate executor
extension (ch. 06 §4.1) cheaper than PG's shared-hash chain in the in-memory/GC regime. This
chapter admits it to the cost-driven planner only for composite-free, single-column, tree
subsets where `multiHashJoinCost` proves it cheaper than the binary cascade — a PG-shaped
join *order* with a goopg MHJ collapse exactly where the comparability invariant holds.
Composite-key joins (which PG costs as ordinary joins) stay binary.
