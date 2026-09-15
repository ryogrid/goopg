Task: M0142-0011 — disambiguate Mechanism A vs B for the `EstimateRows(*Join)`
recompute gap M0142-0004b found. **DONE and about to be committed** this loop
(banner item 4, M0142 sub-group). Recon closed with NO production code
change (candidate Algo-gate broadening + temporary trace both added and
fully reverted — `git diff` on `internal/optimizer/cardinality.go` empty
before commit).

Files: `docs/design/0100-0149/m0142-0011-disambiguate-estimatejoin-recompute-gap.md`
(new, full writeup), `docs/design/README.md` (indexed), `.ralph/fix_plan.md`
(0011 `[x]`, new 0012 filed), `.ralph/deferral_ledger.md` (new row).

Key symbols: `estimateJoin`/`joinEquiPairs` (`internal/optimizer/cardinality.go:722,1129`),
`estimateNLIndexJoin` (`cardinality.go:240`, already correct, M0142-0006's
SEMI/ANTI fix included), `createNestLoopIndexJoinPlan`/
`createNestLoopIndexJoinPlanFused` (`createplannl.go:107,208,265`) — the
R25 decomposition point.

Findings: tested Mechanism A directly (broadened `estimateJoin`'s
`j.Algo == Hash||Merge` gate, rebuilt, re-ran Q33's CTE-branch witness on a
private SF0.25 clone) — plan byte-identical, A alone does NOT fix it. Traced
the collapsing nodes: generic `*optimizer.Join` with `Predicate == nil`,
zero equi-pairs regardless of the gate. Read `createplannl.go` instead of
tracing further: since R25 (plan-parity-fix-take2), an unmemoized
index-probe nested loop is built as `Join{Algo:NestedLoop, Lateral:true,
Right:*IndexScan}` whose equi-key lives on the `*IndexScan` child's own
`Key`/`Keys` (an `OuterColumnRef` nestloop param), NOT in `Predicate`.
`estimateJoin` has no `Lateral` arm — `joinEquiPairs` never looks at
`j.Right`'s own bound key — so EVERY unmemoized index-probe nested loop in
both corpora (the common case; M0142-0005/B6 already established goopg
rarely gets a Memoize on the NL probe path) falls to the crude
`l*r*0.005`/`max(l,r)`-capped fallback, even though `estimateNLIndexJoin`
already has the correct per-probe logic for the OLD fused
`*NestedLoopIndexJoin` type — reachable only through
`createNestLoopIndexJoinPlanFused` (Memoize-wrapped minority). Neither
Mechanism A nor B; a third, more precise mechanism (Mechanism C),
structurally DP-search-safe (construction-time fields only, no `PlanCost`
consultation — same argument that makes `estimateNLIndexJoin` itself safe
today). Filed the fix as **M0142-0012**: teach `EstimateRows`/`estimateJoin`
the `Lateral`+bound-`*IndexScan` shape by generalizing `estimateNLIndexJoin`'s
logic. Flagged likely LARGE corpus-wide blast radius (common shape) — 0012
needs the full floor-measurement suite (TPC-H plan-parity, TPC-DS
plan-parity, `make ea-ratchet`, SF0.25 sweep) before landing, not the
lighter recon bar.

In-flight: none. sf025 goopg server (private binary `tmp/goopg-m0142-0011-bin`,
port 65437) stopped via `bench/tpcds/server.sh stop sf025` and the binary
removed before this write-up. Verified via `ps aux` no stray goopg process
remains except the pre-existing TPC-H bench peer on :65433 (another loop's
live server, never touched this loop).

Next step: per the banner, item 4 (M0141/M0142 group) is still open. Pick
per judgement among: **M0142-0012** (the fix just filed — executor-sized,
needs full floor-measurement, likely the highest-value single item in the
group given its probable blast radius across both corpora), **M0142-0003c**
(level-6 enumeration-order recon, bounded), **M0142-0008** (forced-rewrite-
vs-search census, bounded), **M0142-0005** (Memoize/probe-multiplier
interlock, now carries M0142-0010's extra corpus evidence), or **M0141-S7**
(Incremental Sort — 14 TPC-DS queries, likely the single highest-leverage
item in the whole group by query count, but a bigger executor+planner slice
to scope). A 0142-0012 sub-scoping recon (measure how many corpus nodes are
actually the `Lateral`+`*IndexScan` shape before implementing, mirroring
this milestone's own measure-first discipline) is a reasonable first cut if
the next loop wants to size 0012 rather than implement it blind.

Gates run: `go build ./...` clean (no production diff this loop — recon +
docs only); `go test ./internal/optimizer/...` PASS (including
`TestFallbackCapFiresForNonHashAlgoDespiteStats`, confirming the reverted
Algo-gate experiment left no trace); `git status`/`git diff` on
`internal/optimizer/cardinality.go` confirmed empty. Full
tpch-spotcheck/ea-ratchet/SF025-sweep NOT run this loop — correctly skipped
per the recon-only precedent (M0142-0003a/0003b/0004b/0007/0010 also
required only build-clean + empty-diff, not the full floor suite, since no
production code changed). `make ralph-state-guard` to be run immediately
before the status block.
