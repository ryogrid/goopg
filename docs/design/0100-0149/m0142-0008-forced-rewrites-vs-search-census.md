# M0142-0008 — recon: how much of goopg's plan shape is chosen by forced rewrites rather than by the search?

Status: accepted (recon closed 2026-09-16, no code change)

## Task

`.ralph/fix_plan.md`'s M0142-0008 line. The milestone's goal requires every
plan to be reached by **the same planning logic** PG uses, never by forcing
shapes. Several deferral-ledger rows (`take2-P6-03`, `take2-P6-04`,
`take3-C-20c-blocked`, `-20d-calibrated`, `-20f-blocked`, `-20g-blocked`,
`take2-P2-10`, `take3-C-09-declined`, `take2-P3-01`) claim goopg still
depends on goopg-only forced rewrites the DP search cannot yet reproduce:
`rewriteScanInputsWithSingleTablePredicates`, `rewriteJoinsToNLI`, plus the
PG-absent knobs `GOOPG_NLI_COSTGATE`, `GOOPG_INDEXKEY_HARVEST`,
`GOOPG_INDEX_PROBE_MULT`, `enable_nestloop_index`. A 2026-09-15 survey
overstated the case (claimed SEMI/ANTI "never enter the DP search" — false,
`parser.JoinSemi` IS handled in `joinpaths.go`/`joinsearchlevel.go`); the
task's own framing required re-measuring at HEAD rather than scoping from
that dated claim. Deliverable: a per-mechanism/per-query census of which
plan nodes come from the search vs. from a forced rewrite, and a verdict on
whether a dedicated milestone is warranted.

## Method

Static + git-archaeology read of all six mechanisms (no runtime
instrumentation needed — the classification question turned out to be
answerable directly from the code's own control flow and comments, unlike
prior M0142 recons that needed a live trace). Read
`internal/optimizer/scan_input_rewrite.go`, `nl_index_join.go`,
`joinpathsnli.go`, `unnest.go`, `cost_funcs.go`, `plannersettings.go`, and
the call sites in `planner.go` (`:1580-1649`) that decide whether a
statement's FROM-tree reaches `tryJoinSearch` at all. Cross-checked against
`git log --oneline -S<symbol>` for provenance, and against two named
witnesses (TPC-H Q4, Q20) the ledger rows already cite.

## Result — the six mechanisms

| mechanism | file:line | class | why |
|---|---|---|---|
| `rewriteScanInputsWithSingleTablePredicates` | `scan_input_rewrite.go:50`, called `planner.go:1634` | **(a) forced, structural, no cost comparison** | Unconditionally swaps `SeqScan`→`IndexScan` on a matching single-table predicate; the only gate is `enable_indexscan`/ambiguity, never `addPath`. Explicitly skips any subtree the DP search already touched (`isSearchedTree(n)` early-return, line 68-70) — it only fires on the **legacy** (non-searched) portion of the tree. |
| `rewriteJoinsToNLI` | `nl_index_join.go:89`, called `planner.go:1641` | **(a) forced within its scope, with an internal heuristic gate, still no cost comparison against alternatives** | Same `isSearchedTree` skip (`nl_index_join.go:119-121`), with an explicit comment: *"the PG-shaped search already chose the join METHOD... re-deciding it here would override that choice."* Within the legacy scope, rewrites `Join{Hash}`→`NestedLoopIndexJoin` whenever `nliCostGateAccepts` (a row-count heuristic, line 1399) passes — never an `addPath` Total-cost comparison. |
| `GOOPG_NLI_COSTGATE` | `nl_index_join.go:55,1402` | **(b) cosmetic to the search** | Only switches which heuristic mechanism-2's *internal* gate uses (legacy stats-blind threshold vs. default matchSet/fan-out-aware gate). Never touches the DP search. |
| `GOOPG_INDEXKEY_HARVEST` | `unnest.go:38,809-816` | **(a) forced, pre-search, shape-determining** | Gates a pass that harvests index-key equijoins out of a correlated subquery **before join-order search runs** (own comment, `unnest.go:39-40`) — it changes which shape the DP search is even given to price. Measured non-plan-neutral for Q4/Q22 (`unnest.go:786-791`: Q4 2.27s ON vs 1.46s OFF at SF1). |
| `GOOPG_INDEX_PROBE_MULT` | `cost_funcs.go:1053` | **(c) legitimate — a search-consumed cost input, not a rewrite** | Scales `indexProbeCost`, read inside the DP search's own `addNLIPaths` (`joinpathsnli.go:341`), the same cost comparison every other candidate goes through. Changing it moves which DP-costed candidate wins; it deletes no rewrite. |
| `enable_nestloop_index` / `PlannerSettings.EnableNestLoopIndex` | `plannersettings.go:109`, checked `nl_index_join.go:94` | **(b) clean kill-switch on mechanism-2 only** | `if !ps.EnableNestLoopIndex { return n }` hard-disables `rewriteJoinsToNLI` entirely, but does **not** gate `addNLIPaths` in the DP search (that path uses a separate PG-style `cp.enableNestLoop` flag for soft `DisabledNodes` marking, not a hard skip). Turning this GUC off removes the forced rewrite while leaving the search's own native NLI-path generation untouched — architecturally clean evidence the two mechanisms are genuinely distinct, not two names for one thing. |

Both rewrite functions were introduced together in commit `13fad010a`
(M0054-0006, preceded by `bd248ad8d` M0054-0006a-pre) — motivated by Seq
Scans surviving where indexes existed once join order left the plan-time,
single-table index-selection code (`planIndexScanFromWhere`) behind them.

## Result — two case studies (Q4, Q20)

**Q4 (TPC-H, semi-join NLI, ledger `take2-P6-04`, 12.5x if the rewrite is
deleted).** `addNLIPaths` nominally admits SEMI/ANTI join types and would
file a candidate through `addPath` if reached (`joinpathsnli.go:270,276`) —
the search is **not** categorically code-blind to NLI-over-semi. But per the
already-verified M0139-0005 finding, Q4's `EXISTS` is decorrelated by
`unnestExistsExpr` (`unnest.go:4078`), which builds the physical
`Join{SEMI, Hash}` node **directly on the raw parse tree, before any search
joinrel exists at all** — "no sjinfo → no joinrel → no path → no price",
the same fork METHODOLOGY3 F11/K63 (R73-R77) already named for the sibling
costing symptom. So `rewriteJoinsToNLI` is the **only** producer of Q4's
semi-join NLI shape in practice — genuinely forced, despite the code path
nominally existing, because the search is never invoked on this subtree at
all (not out-costed by it).

**Q20 (TPC-H, single-table index promotion, ledger `take2-P6-03`, 6.5x if
the rewrite is deleted).** The DP search *does* have a native, cost-compared
single-table index-scan mechanism (`planIndexScanFromWhere` / level-1 path
generation) — structurally reachable **when the search runs on that
subtree**. The gap here is scope, not capability: per `planner.go:1590-94`'s
own comment, a filterless INNER/CROSS tree is left on the legacy path today
("a filterless INNER/CROSS tree is left on the legacy path for now...
widening to every filterless join tree moves many long-stable plans at once
and deserves its own gated round") — Q20's nested-subquery shape plausibly
falls in that class. Q20 is therefore "search-capable in principle, this
subtree just doesn't reach the search yet" — a narrower, already-scoped
class of gap than Q4's, and the code's own comment already names the
intended future direction.

## Verdict

**The two forced-rewrite functions are not competing with, and winning
against, a cost-based search that would otherwise choose differently.**
Both carry an explicit, load-bearing guard (`isSearchedTree`) confining them
to portions of the tree the search never reaches at all — reading the
ledger rows' 6.5x/12.5x regression numbers as "the search loses to the
rewrite on cost" is the wrong frame; the correct frame is "the rewrite is
the only access-method/join-method decision-maker for the subtrees the
search's current structural coverage excludes." Of the four knobs, two
(`GOOPG_NLI_COSTGATE`, `GOOPG_INDEX_PROBE_MULT`) are architecturally sound
— they tune inputs the search's own `addPath` comparison consumes and
should not be treated as forcing mechanisms at all; `enable_nestloop_index`
is a clean, already-well-scoped kill switch (ledger `take3-B-17e-blocked`
already has its correct resume condition). Only `GOOPG_INDEXKEY_HARVEST`
joins the two rewrite functions as a genuine pre-search, shape-determining
forced mechanism (already correctly flagged blocked in `take3-C-20c-blocked`).

**A dedicated milestone is warranted, but scoped to closing the search's
structural coverage gaps — not to deleting the rewrites, which would
reproduce the already-measured regressions until the gap is closed.** Two
concrete, independently-sized gaps, corresponding to the two case studies:

1. **SEMI/ANTI decorrelation bypasses the search's joinrel machinery
   entirely** (Q4's mechanism; F11/K63/M0139-0005 lineage). M0139-0005
   itself called wiring this "a materially larger task, out of this recon's
   scope" without sizing it. Filed as **M0142-0008a** below.
2. **Filterless INNER/CROSS trees stay on the legacy path** (Q20's
   mechanism; `planner.go:1590-94`'s own comment already names this as
   future work needing "its own gated round"). Filed as **M0142-0008b**
   below.

Both are filed as **scoping recons first**, per K50 and the M0142-0012a/
M0142-0016a precedent: either could move many long-stable plans at once (the
`planner.go` comment says so explicitly for #2; #1 touches every SEMI/ANTI
query in both corpora), so blast radius must be measured before any
structural change lands.

## Floor measurements (mandatory for M0137–M0143 recon tasks)

No production code changed this loop (pure read/classification, no
instrumentation added or reverted): `git diff --stat -- internal/` empty.
No plan-parity/`ea-ratchet` floor shift is possible from a no-diff loop, so
the mandatory TPC-H `match=8/22` / TPC-DS `match=2/99` / `ea-ratchet`
110-entry baseline pins are unchanged by construction (same precedent as
M0142-0007/0013/0015).

## Follow-up

**M0142-0008a** and **M0142-0008b** filed in `.ralph/fix_plan.md` under this
milestone. Ledger row appended recording the reframe (forced rewrites are
coverage backstops, not search competitors) so a future loop does not
re-propose "delete the rewrites" without re-deriving this finding.
