Task: M0142-0004b — recon: why does a 3-way CTE `UNION ALL` land at `est=3`
against actuals up to 1557x higher. **DONE and COMMITTED** this loop (banner
item 4, M0142 sub-group), recon closed with NO production code change
(instrumentation added and fully reverted — `git diff` on `cardinality.go`
was empty before commit).

Files: `docs/design/0100-0149/m0142-0004b-cte-union-branch-collapse-is-estimatejoin-recompute-gap.md`
(new, full writeup), `docs/design/README.md` (indexed), `.ralph/fix_plan.md`
(0004b `[x]` + new **M0142-0011** filed with both candidate mechanisms and
resume points).

Key symbols: `estimateJoin` (cardinality.go:724, SEMI/ANTI branch ~738-748,
measured-branch Algo gate ~792 `if j.Algo == JoinAlgoHash || j.Algo ==
JoinAlgoMerge`), `EstimateRows` (cardinality.go:45, `case *Join: return
estimateJoin(x)` at ~95-96 — never consults `PlanCost`), `semiJoinMatchFraction`/
`semiPairMatchFraction` (cardinality.go:868/908 — verified CORRECT, not the
defect), `legacyDisplayCostOf` (plancost.go:164, the "prefer cached PlanRows"
pattern already used by `distinctpaths.go`/`groupingpaths.go`/
`partialaggpaths.go`/`partialsortpaths.go`/`windowsetoppaths.go` but NOT by
`EstimateRows(*Join)`), `operators_explain.go:3024-3028` (EXPLAIN's `rows=`
renderer, reads `PlanCost.PlanRows` — the value that DISAGREES with
`EstimateRows`'s fresh recompute for the same node).

Findings: instrumented Q33's standalone `ss` CTE body (temporary
`GOOPG_EA0004B_TRACE`-gated prints, SF0.25 cluster port 65437) and confirmed
`est=3` is exactly `1+1+1` — each CTE branch's `HashAggregate`/`Hash Semi
Join` independently collapses to rows=1. Two hypotheses from M0142-0004b's
own filing (CTE-specific gap, heavy-filter selectivity) were BOTH ruled out:
the real mechanism is the SEMI join's OUTER input `l = EstimateRows(j.Left)`
returning 1 for a 4-relation join subtree the SAME EXPLAIN renders with
rows=32/91/99 at every level — `semiJoinMatchFraction` itself computes a
correct ~0.998 match fraction (M0142-0006's own code, not implicated).
Traced further: every node in that subtree is a plain `*Join{Algo:
JoinAlgoNestedLoop}` (NOT `*NestedLoopIndexJoin`), and `estimateJoin`'s
measured-selectivity branch (pairNDistinct/MCV/superkey) is gated on
`Algo==Hash||Merge` only — NestedLoop-algo joins fall to the crude
`l*r*0.005` fallback regardless of whether an equi-key is resolvable
(Mechanism A). Separately, `EstimateRows(*Join)` never checks the node's own
`PlanCost.CostSet`/`PlanRows` the way `legacyDisplayCostOf` does for other
node kinds, so it recomputes fresh even when an accurate costed value already
sits on the node (Mechanism B). NOT disambiguated which one is the true fix,
or whether both are needed, or whether either is safe to apply during DP
search (before PlanCost exists) — this intersects the exact "costing order"
territory the banner's item 1 (M0141-S2a/M0139-0007) just resolved, so
M0142-0011 (filed) must re-read that resolution before assuming either fix
is search-safe. Filed as its own slice rather than fixed blind because either
mechanism's fix touches `EstimateRows`/`estimateJoin` broadly enough to need
the full floor-measurement suite (TPC-H/TPC-DS plan-parity, `make
ea-ratchet`, SF0.25 sweep) before landing — matching the M0142-0006/0009
precedent, not the lighter bar pure recon uses.

In-flight: none. sf025 goopg server (started for this probe) stopped via
`bench/tpcds/server.sh stop sf025` before commit; verified via `ps aux` no
goopg process remains except the pre-existing TPC-H bench peer on :65433
(not touched by this loop). No server/gate process left running.

Next step: per the banner, item 4 (M0141/M0142 group) is still open.
M0142-0004b is now closed; M0142-0011 (the fix this recon filed) is the
freshest, most-specific open item, but is NOT yet scoped as smaller than
M0142-0005 — the next loop should treat 0011 and 0005 as comparable in size
(both "sized like an executor slice, scope into sub-tasks before
implementing") and pick whichever it judges more tractable to decompose
first, OR pick a smaller still-open recon (0003c level-6 enumeration-order
parity, 0007 corr=0 fallback re-measure, 0008 forced-rewrite-vs-search
census) if it wants another bounded-recon loop before tackling either
executor-sized slice. Also open: M0141 S2b/S3-S7 (S7 Incremental Sort blocks
14 TPC-DS queries).

Gates run: `go build ./...` clean (no production diff this loop — recon +
docs only); `git status`/`git diff` on `internal/optimizer/cardinality.go`
confirmed empty (clean revert of the temporary trace); `make
ralph-state-guard` — one self-repair (same recurring benign
stale-clean-exit-marker pattern several prior loops have noted), clean after
repair. Full tpch-spotcheck/ea-ratchet/SF025-sweep NOT run this loop —
correctly skipped per the recon-only precedent (M0142-0003a/0003b/0010 also
required only build-clean + empty-diff, not the full floor suite, since no
production code changed).
